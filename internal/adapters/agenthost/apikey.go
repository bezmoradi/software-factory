package agenthost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	keyFolder = ".config/software-factory"
	keyName   = "anthropic-api-key"
	maxConfig = 1 << 20
)

type apiKeyJSON struct {
	Step string `json:"step"`
	Key  string `json:"key,omitempty"`
}

func claudeConfigDir(home string) string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(home, ".claude")
}

func keyHelper(path string) string {
	return "cat '" + path + "'"
}

func (s *Server) apiKeyStep(q apiKeyJSON) *problem {
	home := os.Getenv("HOME")
	if !filepath.IsAbs(home) || strings.ContainsAny(home, "'\n") {
		return fail(http.StatusConflict, "home", "The container has no usable home folder for Claude Code's settings.")
	}
	keyPath := filepath.Join(home, keyFolder, keyName)
	settings := filepath.Join(claudeConfigDir(home), "settings.json")
	switch q.Step {
	case "set":
		if !session.ValidAPIKey(q.Key) || q.Key != strings.TrimSpace(q.Key) {
			return fail(http.StatusBadRequest, "key", "")
		}
		next, err := planHelper(settings, keyHelper(keyPath), true)
		if err != nil {
			return fail(http.StatusConflict, "settings", helperMessage(err))
		}
		if err := writePrivate(keyPath, []byte(q.Key+"\n"), 0o700); err != nil {
			return fail(http.StatusConflict, "key-file", "The key couldn't be saved in the container.")
		}
		if next != nil && writePrivate(settings, next, 0o700) != nil {
			return fail(http.StatusConflict, "settings", helperMessage(nil))
		}
		s.log.Info("api key", "step", "set")
		return nil
	case "clear":
		if q.Key != "" {
			return fail(http.StatusBadRequest, "key", "")
		}
		next, err := planHelper(settings, keyHelper(keyPath), false)
		if err == nil && next != nil {
			err = writePrivate(settings, next, 0o700)
		}
		if err != nil {
			return fail(http.StatusConflict, "settings", helperMessage(err))
		}
		if err := os.Remove(keyPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fail(http.StatusConflict, "key-file", "The saved key couldn't be removed from the container.")
		}
		s.log.Info("api key", "step", "clear")
		return nil
	}
	return fail(http.StatusBadRequest, "step", "")
}

var errOtherHelper = errors.New("another apiKeyHelper is set")

func helperMessage(err error) string {
	if errors.Is(err, errOtherHelper) {
		return "Claude Code's settings in the container already name another apiKeyHelper, which Software Factory doesn't replace. Remove it there, then try again."
	}
	return "Claude Code's settings in the container couldn't be updated."
}

func planHelper(path, helper string, on bool) ([]byte, error) {
	all := map[string]json.RawMessage{}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if !on {
			return nil, nil
		}
	case err != nil:
		return nil, err
	case !info.Mode().IsRegular() || !ownedByMe(info) || info.Size() > maxConfig:
		return nil, errors.New("settings file isn't a plain file of this user")
	default:
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(data)) > 0 {
			if err := json.Unmarshal(data, &all); err != nil {
				return nil, err
			}
		}
	}
	var current string
	if raw, ok := all["apiKeyHelper"]; ok {
		if json.Unmarshal(raw, &current) != nil {
			return nil, errOtherHelper
		}
	}
	switch {
	case on && current == helper:
		return nil, nil
	case on && current != "":
		return nil, errOtherHelper
	case on:
		raw, _ := json.Marshal(helper)
		all["apiKeyHelper"] = raw
	case current != helper:
		return nil, nil
	default:
		delete(all, "apiKeyHelper")
	}
	out, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func writePrivate(path string, data []byte, dirMode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || !ownedByMe(info) {
		return errors.New("folder isn't a folder of this user")
	}
	tmp, err := os.CreateTemp(dir, ".sf-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

var _ session.KeyHost = (*Client)(nil)

func (c *Client) APIKey(ctx context.Context, step, key string) error {
	return c.call(ctx, http.MethodPost, "/v1/api-key", apiKeyJSON{Step: step, Key: key}, nil)
}
