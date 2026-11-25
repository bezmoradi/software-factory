package actions

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	AgentRole     = "action-agent"
	agentKeyFD    = 3
	agentReadyFD  = 4
	agentReady    = 'y'
	agentReadyFor = 10 * time.Second
	agentConns    = 8
	maxAgentSpec  = 64 << 10
	agentDirMode  = 0o700
	agentSockMode = 0o600
)

var errAgentRefused = errors.New("the run's ssh agent only lists and signs SSH logins")

func startAgent(a agentJSON) error {
	self, err := os.Executable()
	if err != nil {
		return errors.New("the runner couldn't find itself to start the run's ssh agent")
	}
	keyR, keyW, err := os.Pipe()
	if err != nil {
		return errors.New("the run's ssh agent couldn't be given its key")
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		_ = keyR.Close()
		_ = keyW.Close()
		return errors.New("the run's ssh agent couldn't be started")
	}
	cmd := exec.Command(self, AgentRole)
	cmd.Env, cmd.Dir = []string{}, "/"
	cmd.ExtraFiles = []*os.File{keyR, readyW}
	err = cmd.Start()
	_ = keyR.Close()
	_ = readyW.Close()
	if err != nil {
		_ = keyW.Close()
		_ = readyR.Close()
		return errors.New("the run's ssh agent couldn't be started")
	}
	werr := json.NewEncoder(keyW).Encode(a)
	_ = keyW.Close()
	_ = readyR.SetReadDeadline(time.Now().Add(agentReadyFor))
	got := make([]byte, 1)
	n, rerr := readyR.Read(got)
	_ = readyR.Close()
	if werr != nil || rerr != nil || n != 1 || got[0] != agentReady {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.New("the run's ssh agent didn't start")
	}
	return cmd.Process.Release()
}

type keyAgent struct {
	keys session.SSHKeys
	key  string
	user string
	blob []byte
	pub  ssh.PublicKey
}

var _ agent.ExtendedAgent = (*keyAgent)(nil)

func Agent(keys session.SSHKeys) error {
	if err := undumpable(); err != nil {
		return err
	}
	in := os.NewFile(agentKeyFD, "key")
	ready := os.NewFile(agentReadyFD, "ready")
	if in == nil || ready == nil {
		return errors.New("the run's ssh agent has no key to read")
	}
	data, err := io.ReadAll(io.LimitReader(in, maxAgentSpec+1))
	_ = in.Close()
	var a agentJSON
	if err != nil || len(data) > maxAgentSpec || json.Unmarshal(data, &a) != nil || a.Key == "" || a.User == "" {
		return errors.New("the run's ssh agent got no key it reads")
	}
	blob, err := keys.PublicKey(a.Key)
	if err != nil {
		return err
	}
	pub, err := ssh.ParsePublicKey(blob)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(session.ActionAgent), agentDirMode); err != nil {
		return err
	}
	ln, err := net.Listen("unix", session.ActionAgent)
	if err != nil {
		return err
	}
	if err := os.Chmod(session.ActionAgent, agentSockMode); err != nil {
		return err
	}
	if _, err := ready.Write([]byte{agentReady}); err != nil {
		return err
	}
	_ = ready.Close()
	k := &keyAgent{keys: keys, key: a.Key, user: a.User, blob: blob, pub: pub}
	slots := make(chan struct{}, agentConns)
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		slots <- struct{}{}
		go func() {
			defer func() {
				_ = c.Close()
				<-slots
			}()
			_ = agent.ServeAgent(k, c)
		}()
	}
}

func (k *keyAgent) List() ([]*agent.Key, error) {
	return []*agent.Key{{Format: k.pub.Type(), Blob: k.blob, Comment: "software-factory action"}}, nil
}

func (k *keyAgent) Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error) {
	return k.SignWithFlags(key, data, 0)
}

func (k *keyAgent) SignWithFlags(key ssh.PublicKey, data []byte, flags agent.SignatureFlags) (*ssh.Signature, error) {
	if !bytes.Equal(key.Marshal(), k.blob) || len(data) > session.MaxSSHSignData || !session.SSHLogin(data, k.user, k.blob) {
		return nil, errAgentRefused
	}
	sig, err := k.keys.Sign(k.key, data, uint32(flags))
	if err != nil {
		return nil, errAgentRefused
	}
	return &ssh.Signature{Format: sig.Format, Blob: sig.Blob, Rest: sig.Rest}, nil
}

func (k *keyAgent) Add(agent.AddedKey) error       { return errAgentRefused }
func (k *keyAgent) Remove(ssh.PublicKey) error     { return errAgentRefused }
func (k *keyAgent) RemoveAll() error               { return errAgentRefused }
func (k *keyAgent) Lock([]byte) error              { return errAgentRefused }
func (k *keyAgent) Unlock([]byte) error            { return errAgentRefused }
func (k *keyAgent) Signers() ([]ssh.Signer, error) { return nil, errAgentRefused }
func (k *keyAgent) Extension(string, []byte) ([]byte, error) {
	return nil, agent.ErrExtensionUnsupported
}
