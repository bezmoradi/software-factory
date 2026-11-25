package agenthost

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	handoverRoute   = "/v1/handover"
	maxHandoverBody = 2 << 20
	accessFolder    = ".config/software-factory/access"
	accessEnvName   = "env.sh"
	claudeEnvName   = "claude-env.sh"
	writtenName     = "written.json"
	askpassName     = "askpass"
	maxHandedPorts  = 256
	ClaudeEnvFile   = "CLAUDE_ENV_FILE"
	blockBegin      = "# software-factory access begin"
	blockEnd        = "# software-factory access end"
	sshConfig       = ".ssh/config"
	sshKnownHosts   = ".ssh/software-factory_known_hosts"
	awsCredentials  = ".aws/credentials"
	awsConfig       = ".aws/config"
)

type handedVarJSON struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type handedJSON struct {
	Name         string          `json:"name"`
	Type         string          `json:"type"`
	Bound        []string        `json:"bound"`
	Variable     string          `json:"variable,omitempty"`
	Value        string          `json:"value,omitempty"`
	KeyID        string          `json:"keyId,omitempty"`
	SecretKey    string          `json:"secretKey,omitempty"`
	SessionToken string          `json:"sessionToken,omitempty"`
	Region       string          `json:"region,omitempty"`
	User         string          `json:"user,omitempty"`
	Password     string          `json:"password,omitempty"`
	HostKey      string          `json:"hostKey,omitempty"`
	Path         string          `json:"path,omitempty"`
	Mode         string          `json:"mode,omitempty"`
	Contents     string          `json:"contents,omitempty"`
	Vars         []handedVarJSON `json:"vars,omitempty"`
}

type handoverJSON struct {
	Box   string       `json:"box"`
	Items []handedJSON `json:"items"`
	Ports []string     `json:"ports,omitempty"`
}

type blockJSON struct {
	Path    string `json:"path"`
	Created bool   `json:"created"`
	Mode    uint32 `json:"mode"`
	Mtime   int64  `json:"mtime"`
}

type writtenJSON struct {
	Files  []string    `json:"files"`
	Blocks []blockJSON `json:"blocks"`
}

var errNotOurs = errors.New("a file of that name is already there")

func accessHome() (string, bool) {
	home := os.Getenv("HOME")
	if !filepath.IsAbs(home) || strings.ContainsAny(home, "'\"\\$`\n") {
		return "", false
	}
	return filepath.Clean(home), true
}

func claudeEnvPath(home string) string {
	return filepath.Join(home, accessFolder, claudeEnvName)
}

func claudeEnvScript() []byte {
	env := "\"$HOME/" + accessFolder + "/" + accessEnvName + "\""
	return []byte("if [ -f " + env + " ]; then . " + env + "; fi\n")
}

func (s *Server) prepareAccess() {
	if s.cfg.Review {
		return
	}
	home, ok := accessHome()
	if !ok {
		return
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return
	}
	defer func() { _ = root.Close() }()
	s.handMu.Lock()
	defer s.handMu.Unlock()
	undoWritten(root)
	if err := writeOwn(root, path.Join(accessFolder, claudeEnvName), claudeEnvScript(), 0o600); err != nil {
		s.log.Warn("handover", "event", "env-file-not-written")
	}
}

func (s *Server) accessEnv() (string, bool) {
	if s.cfg.Review {
		return "", false
	}
	home, ok := accessHome()
	if !ok {
		return "", false
	}
	return claudeEnvPath(home), true
}

func mkdirs(root *os.Root, dir string) error {
	if dir == "." || dir == "" {
		return nil
	}
	if err := root.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := root.Lstat(dir)
	if err != nil || !info.IsDir() {
		return errors.New("not a folder")
	}
	return nil
}

func writeOwn(root *os.Root, name string, data []byte, mode os.FileMode) error {
	if err := mkdirs(root, path.Dir(name)); err != nil {
		return err
	}
	if info, err := root.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return errNotOurs
	}
	tmp := path.Join(path.Dir(name), "."+path.Base(name)+".sf-"+randomID())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmp) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return root.Rename(tmp, name)
}

func readWritten(root *os.Root) writtenJSON {
	var w writtenJSON
	data, err := root.ReadFile(path.Join(accessFolder, writtenName))
	if err == nil && len(data) <= maxHandoverBody {
		_ = json.Unmarshal(data, &w)
	}
	return w
}

func safeRel(p string) bool {
	return p != "" && !path.IsAbs(p) && path.Clean(p) == p && !strings.HasPrefix(p, "../") && p != ".."
}

func stripBlock(text string) string {
	start := strings.Index(text, blockBegin+"\n")
	if start < 0 {
		return text
	}
	end := strings.Index(text[start:], blockEnd+"\n")
	if end < 0 {
		return text[:start]
	}
	return text[:start] + text[start+end+len(blockEnd)+1:]
}

func undoWritten(root *os.Root) {
	w := readWritten(root)
	for _, f := range w.Files {
		if safeRel(f) {
			_ = root.Remove(f)
		}
	}
	for _, b := range w.Blocks {
		if !safeRel(b.Path) {
			continue
		}
		info, err := root.Lstat(b.Path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := root.ReadFile(b.Path)
		if err != nil {
			continue
		}
		rest := stripBlock(string(data))
		if b.Created && strings.TrimSpace(rest) == "" {
			_ = root.Remove(b.Path)
			continue
		}
		if rest != string(data) && writeOwn(root, b.Path, []byte(rest), os.FileMode(b.Mode).Perm()) == nil && b.Mtime > 0 {
			at := time.Unix(0, b.Mtime)
			_ = root.Chtimes(b.Path, at, at)
		}
	}
	_ = root.Remove(path.Join(accessFolder, accessEnvName))
	_ = root.Remove(path.Join(accessFolder, writtenName))
}

type handPlan struct {
	env     []string
	files   map[string][]byte
	modes   map[string]os.FileMode
	blocks  map[string][]string
	askpass []string
}

func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

func sshPattern(host string) string {
	if strings.HasPrefix(host, ".") {
		return "*" + host
	}
	return host
}

func (p *handPlan) add(x handedJSON) error {
	if !session.ValidCredentialName(x.Name) || len(x.Bound) == 0 {
		return errors.New("bad item")
	}
	for _, h := range x.Bound {
		if !session.ValidBoundEntry(h) {
			return errors.New("bad host")
		}
	}
	variable := func(name, value string) error {
		if (!session.ValidCredentialVariable(name) && name != session.SSHPassVariable) || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("bad variable")
		}
		p.env = append(p.env, "export "+name+"="+shellQuote(value))
		return nil
	}
	switch x.Type {
	case session.CredentialToken:
		return variable(x.Variable, x.Value)
	case session.CredentialVars:
		for _, v := range x.Vars {
			if err := variable(v.Name, v.Value); err != nil {
				return err
			}
		}
	case session.CredentialAWS:
		if x.KeyID == "" || x.SecretKey == "" || x.SessionToken == "" || x.Region == "" || strings.ContainsAny(x.KeyID+x.SecretKey+x.SessionToken+x.Region, " \r\n[]") {
			return errors.New("bad aws")
		}
		p.blocks[awsCredentials] = append(p.blocks[awsCredentials], "["+x.Name+"]", "aws_access_key_id = "+x.KeyID, "aws_secret_access_key = "+x.SecretKey, "aws_session_token = "+x.SessionToken)
		p.blocks[awsConfig] = append(p.blocks[awsConfig], "[profile "+x.Name+"]", "region = "+x.Region)
	case session.CredentialSSH:
		if strings.ContainsAny(x.HostKey, "\r\n") || !session.ValidSSHUser(x.User) {
			return errors.New("bad ssh")
		}
		for _, h := range x.Bound {
			host, port, isPort := session.SplitPortTarget(h)
			if !isPort {
				host = sshPattern(h)
			}
			lines := []string{"Host " + host, "  User " + x.User}
			if isPort {
				lines = append(lines, "  Port "+strconv.Itoa(port))
			}
			if x.Password != "" {
				lines = append(lines, "  PreferredAuthentications password,keyboard-interactive")
				p.askpass = append(p.askpass, askpassCases(x.User, host)...)
			}
			if x.HostKey != "" {
				known := host
				if isPort && port != 22 {
					known = "[" + host + "]:" + strconv.Itoa(port)
				}
				lines = append(lines, "  UserKnownHostsFile ~/"+sshKnownHosts, "  StrictHostKeyChecking yes")
				p.files[sshKnownHosts] = append(p.files[sshKnownHosts], []byte(known+" "+x.HostKey+"\n")...)
				p.modes[sshKnownHosts] = 0o600
			} else {
				lines = append(lines, "  StrictHostKeyChecking accept-new")
			}
			p.blocks[sshConfig] = append(p.blocks[sshConfig], lines...)
		}
		if x.Password == "" {
			return nil
		}
		return variable(session.SSHPassVariable, x.Password)
	case session.CredentialFile:
		if !session.ValidHomePath(x.Path) {
			return errors.New("bad path")
		}
		mode := os.FileMode(0o600)
		if x.Mode == session.FileModeShared {
			mode = 0o644
		}
		if _, dup := p.files[x.Path]; dup {
			return errors.New("path given twice")
		}
		p.files[x.Path] = []byte(x.Contents)
		p.modes[x.Path] = mode
	default:
		return errors.New("bad type")
	}
	return nil
}

func askpassCases(user, host string) []string {
	if strings.HasPrefix(host, "*") {
		return []string{`"` + user + `@"*"` + host[1:] + `'s password: "`, `"(` + user + `@"*"` + host[1:] + `) "*`}
	}
	return []string{`"` + user + "@" + host + `'s password: "`, `"(` + user + "@" + host + `) "*`}
}

func askpassScript(cases []string) []byte {
	return []byte("#!/bin/sh\ncase \"$1\" in\n" + strings.Join(cases, "|") + ") printf '%s\\n' \"$" + session.SSHPassVariable + "\" ;;\n*) exit 1 ;;\nesac\n")
}

func (p *handPlan) addPorts(targets []string) error {
	if len(targets) > maxHandedPorts {
		return errors.New("too many ports")
	}
	var hosts []string
	for _, t := range targets {
		host, _, ok := session.SplitPortTarget(t)
		if !ok || !session.ValidPortEntry(t) {
			return errors.New("bad port")
		}
		if !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	if len(hosts) == 0 {
		return nil
	}
	p.blocks[sshConfig] = append(p.blocks[sshConfig], "Host "+strings.Join(hosts, " "), "  ProxyCommand nc -X connect -x "+session.PortProxy+" %h %p", "  ServerAliveInterval 30")
	return nil
}

func addBlock(root *os.Root, name string, lines []string) (blockJSON, error) {
	b := blockJSON{Path: name, Mode: 0o600}
	var text string
	info, err := root.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		b.Created = true
		if err := mkdirs(root, path.Dir(name)); err != nil {
			return b, err
		}
	case err != nil:
		return b, err
	case !info.Mode().IsRegular():
		return b, errNotOurs
	default:
		data, err := root.ReadFile(name)
		if err != nil {
			return b, err
		}
		text = stripBlock(string(data))
		b.Mode, b.Mtime = uint32(info.Mode().Perm()), info.ModTime().UnixNano()
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += blockBegin + "\n" + strings.Join(lines, "\n") + "\n" + blockEnd + "\n"
	return b, writeOwn(root, name, []byte(text), os.FileMode(b.Mode).Perm())
}

func (s *Server) handover(q handoverJSON) *problem {
	if s.cfg.Review {
		return fail(http.StatusNotFound, "route", "")
	}
	if p := s.sameBox(q.Box); p != nil {
		return p
	}
	home, ok := accessHome()
	if !ok {
		return fail(http.StatusConflict, "home", "The container has no usable home folder for handed-over credentials.")
	}
	plan := &handPlan{files: map[string][]byte{}, modes: map[string]os.FileMode{}, blocks: map[string][]string{}}
	for _, x := range q.Items {
		if err := plan.add(x); err != nil {
			return fail(http.StatusBadRequest, "items", "A credential to hand over isn't one agent-host writes.")
		}
	}
	if err := plan.addPorts(q.Ports); err != nil {
		return fail(http.StatusBadRequest, "ports", "A port to forward isn't one agent-host forwards.")
	}
	if len(plan.askpass) > 0 {
		name := path.Join(accessFolder, askpassName)
		plan.files[name] = askpassScript(plan.askpass)
		plan.modes[name] = 0o700
		plan.env = append(plan.env, "export SSH_ASKPASS="+shellQuote(filepath.Join(home, name)), "export SSH_ASKPASS_REQUIRE=force")
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return fail(http.StatusConflict, "home", "agent-host couldn't open the home folder.")
	}
	defer func() { _ = root.Close() }()
	s.handMu.Lock()
	defer s.handMu.Unlock()
	undoWritten(root)
	w := writtenJSON{Files: []string{}, Blocks: []blockJSON{}}
	var failed []string
	names := make([]string, 0, len(plan.files))
	for name := range plan.files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if _, err := root.Lstat(name); err == nil {
			failed = append(failed, "~/"+name)
			continue
		}
		if err := writeOwn(root, name, plan.files[name], plan.modes[name]); err != nil {
			failed = append(failed, "~/"+name)
			continue
		}
		w.Files = append(w.Files, name)
	}
	for _, name := range []string{sshConfig, awsCredentials, awsConfig} {
		lines := plan.blocks[name]
		if len(lines) == 0 {
			continue
		}
		b, err := addBlock(root, name, lines)
		if err != nil {
			failed = append(failed, "~/"+name)
			continue
		}
		w.Blocks = append(w.Blocks, b)
	}
	env := strings.Join(plan.env, "\n")
	if env != "" {
		env += "\n"
	}
	if err := writeOwn(root, path.Join(accessFolder, accessEnvName), []byte(env), 0o600); err != nil {
		failed = append(failed, "~/"+accessFolder+"/"+accessEnvName)
	}
	if data, err := json.Marshal(w); err == nil {
		_ = writeOwn(root, path.Join(accessFolder, writtenName), data, 0o600)
	}
	forwarded := s.fwd.set(q.Ports)
	if len(failed) > 0 {
		s.log.Warn("handover", "event", "not-written", "files", len(failed))
		return fail(http.StatusConflict, "files", "agent-host couldn't write "+strings.Join(failed, ", ")+" in the container: a file or link of that name is already there, which agent-host doesn't replace.")
	}
	s.log.Info("handover", "event", "set", "items", len(q.Items), "ports", len(q.Ports), "forwards", forwarded)
	return nil
}

var _ session.HandoverHost = (*Client)(nil)

func (c *Client) Handover(ctx context.Context, box string, hand session.HandedAccess) error {
	list := hand.Credentials
	q := handoverJSON{Box: box, Items: make([]handedJSON, 0, len(list)), Ports: hand.Ports}
	for _, h := range list {
		x := h.Secret
		item := handedJSON{Name: h.Name, Type: h.Type, Bound: h.Bound, Variable: x.Variable, Value: x.Value, KeyID: x.KeyID, SecretKey: x.SecretKey, SessionToken: h.Token, Region: x.Region,
			User: x.User, Password: x.Password, HostKey: x.HostKey, Path: x.Path, Mode: x.Mode, Contents: x.Contents}
		for _, v := range x.Vars {
			item.Vars = append(item.Vars, handedVarJSON{Name: v.Name, Value: v.Value})
		}
		q.Items = append(q.Items, item)
	}
	return c.call(ctx, http.MethodPost, handoverRoute, q, nil)
}
