package agenthost

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	DefaultRunTimeout = 30 * time.Second
	MaxRunTimeout     = 2 * time.Minute
	MaxRunStdout      = 16 << 20
	MaxRunStderr      = 64 << 10
	termGrace         = 3 * time.Second
	stopWait          = 5 * time.Second
	drainWait         = 2 * time.Second
)

type process struct {
	id      string
	command string
	cwd     string
	pid     int
	started time.Time
	done    chan struct{}
	once    sync.Once
	exit    *int
}

func (s *Server) inside(dir string) bool {
	rel, err := filepath.Rel(s.cfg.Workspace, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel)
}

func lookPath(name string, env []string) (string, bool) {
	path := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, true
		}
	}
	return "", false
}

func clean(s string) bool {
	return !strings.ContainsRune(s, 0)
}

func (s *Server) prepare(ctx context.Context, c commandJSON) (*exec.Cmd, *problem) {
	if len(c.Argv) == 0 || len(c.Argv) > MaxArgs || c.Argv[0] == "" {
		return nil, fail(http.StatusBadRequest, "argv", fmt.Sprintf("A command needs a name and at most %d arguments in all.", MaxArgs))
	}
	for i, a := range c.Argv {
		if len(a) > MaxArg {
			return nil, fail(http.StatusBadRequest, "argv", fmt.Sprintf("Argument %d is %d bytes, over agent-host's limit of %d KiB per argument.", i, len(a), MaxArg>>10))
		}
		if !clean(a) {
			return nil, fail(http.StatusBadRequest, "argv", fmt.Sprintf("Argument %d holds a NUL byte.", i))
		}
	}
	if !filepath.IsAbs(c.Cwd) || filepath.Clean(c.Cwd) != c.Cwd || !s.inside(c.Cwd) {
		return nil, fail(http.StatusBadRequest, "cwd", "The working folder must be inside agent-host's workspace.")
	}
	if info, err := os.Stat(c.Cwd); err != nil || !info.IsDir() {
		return nil, fail(http.StatusBadRequest, "cwd", "The working folder doesn't exist.")
	}
	if len(c.Pass) > MaxNames || len(c.Env) > MaxNames {
		return nil, fail(http.StatusBadRequest, "env", fmt.Sprintf("A command takes at most %d passed and %d set variables.", MaxNames, MaxNames))
	}
	for _, n := range c.Pass {
		if !envName.MatchString(n) {
			return nil, fail(http.StatusBadRequest, "env", "A passed variable's name isn't a valid environment variable name.")
		}
	}
	set := map[string]string{}
	for k, v := range c.Env {
		if !envName.MatchString(k) || len(v) > MaxEnvValue || !clean(v) {
			return nil, fail(http.StatusBadRequest, "env", fmt.Sprintf("A set variable has an invalid name, a NUL byte or a value over %d bytes.", MaxEnvValue))
		}
		set[k] = v
	}
	if s.cfg.PluginSocket != "" {
		set[SocketVariable] = s.cfg.PluginSocket
	}
	if s.cfg.AgentsBin != "" {
		set["PATH"] = PathWith(s.cfg.Environ, s.cfg.AgentsBin)
	}
	env := Environment(s.cfg.Environ, c.Pass, set)
	name := c.Argv[0]
	path := name
	if strings.Contains(name, "/") {
		if !filepath.IsAbs(name) {
			return nil, fail(http.StatusBadRequest, "argv", "A command is a name on PATH or an absolute path.")
		}
	} else {
		found, ok := lookPath(name, env)
		if !ok {
			return nil, fail(http.StatusUnprocessableEntity, "missing", name+" isn't installed in the agent container.")
		}
		path = found
	}
	cmd := exec.CommandContext(ctx, path, c.Argv[1:]...)
	cmd.Args[0] = name
	cmd.Dir = c.Cwd
	cmd.Env = env
	ownGroup(cmd)
	return cmd, nil
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	if !upgrading(r) {
		refuse(w, fail(http.StatusBadRequest, "upgrade", "Starting a process needs Upgrade: "+Upgrade+"."))
		return
	}
	var q commandJSON
	if p := decode(w, r, &q); p != nil {
		refuse(w, p)
		return
	}
	cmd, p := s.prepare(context.Background(), q)
	if p != nil {
		refuse(w, p)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		refuse(w, fail(http.StatusInternalServerError, "hijack", "agent-host couldn't take over the connection for the process's input and output."))
		return
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		refuse(w, fail(http.StatusInternalServerError, "pipe", "agent-host couldn't set up the process's input and output."))
		return
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		refuse(w, fail(http.StatusInternalServerError, "pipe", "agent-host couldn't set up the process's input and output."))
		return
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		for _, f := range []*os.File{inR, inW, outR, outW} {
			_ = f.Close()
		}
		refuse(w, fail(http.StatusInternalServerError, "pipe", "agent-host couldn't set up the process's input and output."))
		return
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, devnull
	cmd.Cancel = nil
	s.mu.Lock()
	if s.running() >= s.cfg.MaxProcesses {
		s.mu.Unlock()
		for _, f := range []*os.File{inR, inW, outR, outW, devnull} {
			_ = f.Close()
		}
		refuse(w, fail(http.StatusTooManyRequests, "full", "agent-host runs as many processes as it allows."))
		return
	}
	startErr := cmd.Start()
	_ = inR.Close()
	_ = outW.Close()
	_ = devnull.Close()
	if startErr != nil {
		s.mu.Unlock()
		_ = inW.Close()
		_ = outR.Close()
		s.log.Warn("agent-host process", "event", "start-failed", "command", filepath.Base(q.Argv[0]))
		refuse(w, fail(http.StatusUnprocessableEntity, "start", "The command couldn't start."))
		return
	}
	proc := &process{id: randomID(), command: filepath.Base(q.Argv[0]), cwd: q.Cwd, pid: cmd.Process.Pid, started: time.Now(), done: make(chan struct{})}
	s.procs[proc.id] = proc
	s.mu.Unlock()
	go s.reap(proc, cmd)
	s.log.Info("agent-host process", "event", "started", "command", proc.command)

	conn, brw, err := hj.Hijack()
	if err != nil {
		_ = inW.Close()
		_ = outR.Close()
		s.terminate(proc)
		return
	}
	_, err = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + Upgrade + "\r\n" + ProcessHeader + ": " + proc.id + "\r\n\r\n")
	if err == nil {
		err = brw.Flush()
	}
	if err != nil {
		_ = conn.Close()
		_ = inW.Close()
		_ = outR.Close()
		s.terminate(proc)
		return
	}
	go s.feed(proc, brw.Reader, inW)
	go s.drain(proc, outR, conn)
}

func (s *Server) running() int {
	n := 0
	for _, p := range s.procs {
		select {
		case <-p.done:
		default:
			n++
		}
	}
	return n
}

func (s *Server) reap(p *process, cmd *exec.Cmd) {
	_ = cmd.Wait()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	_ = signalGroup(p.pid, true)
	s.mu.Lock()
	p.exit = &code
	s.ended = append(s.ended, p.id)
	for len(s.ended) > keptEnded {
		delete(s.procs, s.ended[0])
		s.ended = s.ended[1:]
	}
	s.mu.Unlock()
	p.once.Do(func() { close(p.done) })
	s.log.Info("agent-host process", "event", "ended", "command", p.command, "exit", code)
}

func (s *Server) feed(p *process, from *bufio.Reader, to *os.File) {
	_, _ = io.Copy(to, from)
	_ = to.Close()
	s.terminate(p)
}

func (s *Server) drain(p *process, from *os.File, to io.WriteCloser) {
	_, _ = io.Copy(to, from)
	_ = from.Close()
	select {
	case <-p.done:
	case <-time.After(drainWait):
		s.terminate(p)
		<-p.done
	}
	_ = to.Close()
}

func (s *Server) terminate(p *process) {
	select {
	case <-p.done:
		return
	default:
	}
	_ = signalGroup(p.pid, false)
	select {
	case <-p.done:
	case <-time.After(termGrace):
		_ = signalGroup(p.pid, true)
	}
}

func (s *Server) stop(id string) (bool, *problem) {
	s.mu.Lock()
	p := s.procs[id]
	s.mu.Unlock()
	if p == nil {
		return false, fail(http.StatusNotFound, "process", "")
	}
	go s.terminate(p)
	select {
	case <-p.done:
		return true, nil
	case <-time.After(stopWait):
		return false, nil
	}
}

func (s *Server) list() []processJSON {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]processJSON, 0, len(s.procs))
	for _, p := range s.procs {
		running := p.exit == nil
		out = append(out, processJSON{ID: p.id, Command: p.command, Cwd: p.cwd, PID: p.pid, Running: running, Exit: p.exit, StartedAt: p.started.UnixMilli()})
	}
	slices.SortFunc(out, func(a, b processJSON) int { return int(a.StartedAt - b.StartedAt) })
	return out
}

func (s *Server) Close() {
	s.loginDrop()
	s.mu.Lock()
	var all []*process
	for _, p := range s.procs {
		all = append(all, p)
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range all {
		wg.Go(func() { s.terminate(p) })
	}
	wg.Wait()
}

type capped struct {
	buf   []byte
	limit int
	over  bool
}

func (c *capped) Write(p []byte) (int, error) {
	room := c.limit - len(c.buf)
	if room > 0 {
		c.buf = append(c.buf, p[:min(room, len(p))]...)
	}
	if len(p) > room {
		c.over = true
	}
	return len(p), nil
}

func (s *Server) run(ctx context.Context, q commandJSON) (runReply, *problem) {
	timeout := DefaultRunTimeout
	longest := MaxRunTimeout
	if s.cfg.Review {
		longest = ReviewRunTimeout
	}
	if q.TimeoutMs > 0 {
		timeout = min(time.Duration(q.TimeoutMs)*time.Millisecond, longest)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd, p := s.prepare(ctx, q)
	if p != nil {
		return runReply{}, p
	}
	s.mu.Lock()
	if s.runs >= MaxRuns {
		s.mu.Unlock()
		return runReply{}, fail(http.StatusTooManyRequests, "full", "agent-host runs as many commands as it allows.")
	}
	s.runs++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.runs--
		s.mu.Unlock()
	}()
	stdout := &capped{limit: MaxRunStdout}
	stderr := &capped{limit: MaxRunStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Cancel = func() error { return signalGroup(cmd.Process.Pid, true) }
	cmd.WaitDelay = drainWait
	err := cmd.Run()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) && !errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil {
		return runReply{}, fail(http.StatusUnprocessableEntity, "start", "The command couldn't start.")
	}
	s.log.Info("agent-host run", "command", filepath.Base(q.Argv[0]), "exit", code)
	return runReply{Code: code, Stdout: string(stdout.buf), Stderr: string(stderr.buf), TimedOut: ctx.Err() != nil}, nil
}
