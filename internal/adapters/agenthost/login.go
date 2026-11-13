package agenthost

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	LoginLife     = 10 * time.Minute
	loginLinkWait = 30 * time.Second
	loginCodeWait = 45 * time.Second
	maxLoginLine  = 8 << 10
)

var linkPattern = regexp.MustCompile(`https://[^\s"'<>]+`)

var loginRefused = regexp.MustCompile(`(?i)invalid|error|failed|denied|expired`)

type loginJSON struct {
	commandJSON
	Step string `json:"step"`
	Code string `json:"code,omitempty"`
}

type loginReply struct {
	URL string `json:"url,omitempty"`
}

type login struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	link   chan string
	later  chan string
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once
	exit   int
}

func (l *login) end() {
	l.once.Do(func() {
		l.cancel()
		_ = l.stdin.Close()
	})
}

func (s *Server) loginStep(ctx context.Context, q loginJSON) (loginReply, *problem) {
	switch q.Step {
	case "start":
		return s.loginStart(ctx, q.commandJSON)
	case "code":
		return loginReply{}, s.loginCode(ctx, q.Code)
	case "cancel":
		s.loginDrop()
		return loginReply{}, nil
	}
	return loginReply{}, fail(http.StatusBadRequest, "step", "")
}

func (s *Server) loginDrop() {
	s.mu.Lock()
	l := s.login
	s.login = nil
	s.mu.Unlock()
	if l != nil {
		l.end()
	}
}

func (s *Server) loginStart(ctx context.Context, q commandJSON) (loginReply, *problem) {
	s.loginDrop()
	life, cancel := context.WithTimeout(context.Background(), LoginLife)
	cmd, p := s.prepare(life, q)
	if p != nil {
		cancel()
		return loginReply{}, p
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return loginReply{}, fail(http.StatusUnprocessableEntity, "start", "The login couldn't start.")
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return loginReply{}, fail(http.StatusUnprocessableEntity, "start", "The login couldn't start.")
	}
	cmd.Stderr = cmd.Stdout
	cmd.Cancel = func() error { return signalGroup(cmd.Process.Pid, true) }
	cmd.WaitDelay = drainWait
	if err := cmd.Start(); err != nil {
		cancel()
		return loginReply{}, fail(http.StatusUnprocessableEntity, "start", "The login couldn't start.")
	}
	l := &login{cmd: cmd, stdin: stdin, link: make(chan string, 1), later: make(chan string, 16), done: make(chan struct{}), cancel: cancel, exit: -1}
	go func() {
		r := bufio.NewReaderSize(out, maxLoginLine)
		sent := false
		for {
			line, err := r.ReadSlice('\n')
			if sent {
				select {
				case l.later <- string(line):
				default:
				}
			} else if m := linkPattern.Find(line); m != nil {
				l.link <- string(m)
				sent = true
			}
			if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
				break
			}
		}
		_ = cmd.Wait()
		if cmd.ProcessState != nil {
			l.exit = cmd.ProcessState.ExitCode()
		}
		close(l.done)
		cancel()
	}()
	s.mu.Lock()
	s.login = l
	s.mu.Unlock()
	s.log.Info("agent-host login", "step", "start", "command", filepath.Base(q.Argv[0]))
	wait := time.NewTimer(loginLinkWait)
	defer wait.Stop()
	select {
	case link := <-l.link:
		return loginReply{URL: link}, nil
	case <-l.done:
		s.loginDrop()
		return loginReply{}, fail(http.StatusUnprocessableEntity, "login", "Claude Code's login ended before it showed a sign-in link.")
	case <-wait.C:
	case <-ctx.Done():
	}
	s.loginDrop()
	return loginReply{}, fail(http.StatusGatewayTimeout, "login", "Claude Code's login didn't show a sign-in link in time.")
}

func (s *Server) loginCode(ctx context.Context, code string) *problem {
	if !session.ValidLoginCode(code) {
		return fail(http.StatusBadRequest, "code", "")
	}
	s.mu.Lock()
	l := s.login
	s.mu.Unlock()
	if l == nil {
		return fail(http.StatusConflict, "login", "No login is waiting for a code. Start it again.")
	}
	if _, err := io.WriteString(l.stdin, code+"\n"); err != nil {
		s.loginDrop()
		return fail(http.StatusConflict, "login", "The login already ended. Start it again.")
	}
	for len(l.later) > 0 {
		<-l.later
	}
	wait := time.NewTimer(loginCodeWait)
	defer wait.Stop()
	refused := false
	for waiting := true; waiting; {
		select {
		case <-l.done:
			waiting = false
		case line := <-l.later:
			if loginRefused.MatchString(line) {
				refused = true
				waiting = false
			}
		case <-wait.C:
			waiting = false
		case <-ctx.Done():
			waiting = false
		}
	}
	s.loginDrop()
	<-l.done
	s.log.Info("agent-host login", "step", "code", "exit", l.exit, "refused", refused)
	if refused || l.exit != 0 {
		return fail(http.StatusUnprocessableEntity, "login", "Claude Code didn't accept the code. Start again, sign in, and paste the newest code.")
	}
	return nil
}

var _ session.LoginHost = (*Client)(nil)

func (c *Client) Login(ctx context.Context, cmd session.Command, step, code string) (string, error) {
	var out loginReply
	if err := c.call(ctx, http.MethodPost, "/v1/login", loginJSON{commandJSON: wire(cmd), Step: step, Code: code}, &out); err != nil {
		return "", err
	}
	return out.URL, nil
}
