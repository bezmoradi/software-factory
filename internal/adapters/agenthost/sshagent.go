package agenthost

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	SSHKeysRoute       = "/ssh-keys"
	SSHSignRoute       = "/ssh-sign"
	sshAgentConns      = 16
	maxSSHFrame        = 64 << 10
	maxSSHAnswer       = 64 << 10
	sshAskFor          = 15 * time.Second
	sshIdleFor         = 10 * time.Minute
	agentFailure       = 5
	agentListKeys      = 11
	agentSignRequest   = 13
	sshAgentFailureLen = 1
)

var errRefused = errors.New("agent-host: the ssh agent only lists and signs")

func ListenSSHAgent(path string) (net.Listener, error) {
	return listenSocket(path, "ssh-agent socket", sshAgentConns)
}

type EngineAgent struct {
	client *http.Client
	base   *url.URL
	log    shared.Logger
}

var _ agent.ExtendedAgent = (*EngineAgent)(nil)

func SSHAgent(engine string, logger shared.Logger) (*EngineAgent, error) {
	if _, _, err := net.SplitHostPort(engine); err != nil {
		return nil, fmt.Errorf("agent-host: the engine's agent address must be host:port, got %q", engine)
	}
	if logger == nil {
		logger = quiet{}
	}
	return &EngineAgent{
		client: &http.Client{Timeout: sshAskFor, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			ResponseHeaderTimeout: sshAskFor,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       time.Minute,
			DisableCompression:    true,
		}},
		base: &url.URL{Scheme: "http", Host: engine},
		log:  logger,
	}, nil
}

func ServeSSHAgent(ctx context.Context, ln net.Listener, a *EngineAgent) error {
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go a.serve(c)
	}
}

func (a *EngineAgent) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	for {
		_ = c.SetDeadline(time.Now().Add(sshIdleFor))
		var size [4]byte
		if _, err := io.ReadFull(c, size[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(size[:])
		if n == 0 || n > maxSSHFrame {
			a.log.Warn("ssh agent", "event", "refused", "why", "size")
			return
		}
		frame := make([]byte, 4+n)
		copy(frame, size[:])
		if _, err := io.ReadFull(c, frame[4:]); err != nil {
			return
		}
		if t := frame[4]; t != agentListKeys && t != agentSignRequest {
			a.log.Warn("ssh agent", "event", "refused", "why", "request", "type", int(t))
			if _, err := c.Write([]byte{0, 0, 0, sshAgentFailureLen, agentFailure}); err != nil {
				return
			}
			continue
		}
		one := struct {
			io.Reader
			io.Writer
		}{bytes.NewReader(frame), c}
		if err := agent.ServeAgent(a, one); !errors.Is(err, io.EOF) {
			return
		}
	}
}

func (a *EngineAgent) ask(method, route string, body any, out any) error {
	var payload io.Reader = http.NoBody
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), sshAskFor)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, a.base.JoinPath(route).String(), payload)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		a.log.Warn("ssh agent", "event", "engine-unreachable", "why", relayFailure(err))
		return errRefused
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSSHAnswer+1))
	if err != nil || len(data) > maxSSHAnswer {
		return errRefused
	}
	if resp.StatusCode != http.StatusOK {
		var p problemJSON
		_ = json.Unmarshal(data, &p)
		a.log.Info("ssh agent", "event", "engine-refused", "route", route, "status", resp.StatusCode, "why", clipText(p.Error, 40))
		return errRefused
	}
	if json.Unmarshal(data, out) != nil {
		return errRefused
	}
	return nil
}

type sshKeyJSON struct {
	Key     string `json:"key"`
	Comment string `json:"comment"`
}

type sshSignJSON struct {
	Key   string `json:"key"`
	Data  string `json:"data"`
	Flags uint32 `json:"flags"`
}

type sshSignatureJSON struct {
	Format string `json:"format"`
	Blob   string `json:"blob"`
	Rest   string `json:"rest,omitempty"`
}

func (a *EngineAgent) List() ([]*agent.Key, error) {
	var got struct {
		Keys []sshKeyJSON `json:"keys"`
	}
	if err := a.ask(http.MethodGet, SSHKeysRoute, nil, &got); err != nil {
		return nil, err
	}
	out := []*agent.Key{}
	for _, k := range got.Keys {
		blob, err := base64.StdEncoding.DecodeString(k.Key)
		if err != nil || len(out) >= session.MaxSSHKeys {
			continue
		}
		pk, err := ssh.ParsePublicKey(blob)
		if err != nil {
			continue
		}
		out = append(out, &agent.Key{Format: pk.Type(), Blob: blob, Comment: clipText(k.Comment, 64)})
	}
	return out, nil
}

func (a *EngineAgent) Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error) {
	return a.SignWithFlags(key, data, 0)
}

func (a *EngineAgent) SignWithFlags(key ssh.PublicKey, data []byte, flags agent.SignatureFlags) (*ssh.Signature, error) {
	if len(data) > session.MaxSSHSignData {
		return nil, errRefused
	}
	q := sshSignJSON{Key: base64.StdEncoding.EncodeToString(key.Marshal()), Data: base64.StdEncoding.EncodeToString(data), Flags: uint32(flags)}
	var got sshSignatureJSON
	if err := a.ask(http.MethodPost, SSHSignRoute, q, &got); err != nil {
		return nil, err
	}
	blob, err := base64.StdEncoding.DecodeString(got.Blob)
	if err != nil || got.Format == "" {
		return nil, errRefused
	}
	rest, err := base64.StdEncoding.DecodeString(got.Rest)
	if err != nil {
		return nil, errRefused
	}
	if len(rest) == 0 {
		rest = nil
	}
	return &ssh.Signature{Format: got.Format, Blob: blob, Rest: rest}, nil
}

func (a *EngineAgent) Add(agent.AddedKey) error       { return errRefused }
func (a *EngineAgent) Remove(ssh.PublicKey) error     { return errRefused }
func (a *EngineAgent) RemoveAll() error               { return errRefused }
func (a *EngineAgent) Lock([]byte) error              { return errRefused }
func (a *EngineAgent) Unlock([]byte) error            { return errRefused }
func (a *EngineAgent) Signers() ([]ssh.Signer, error) { return nil, errRefused }
func (a *EngineAgent) Extension(string, []byte) ([]byte, error) {
	return nil, agent.ErrExtensionUnsupported
}
