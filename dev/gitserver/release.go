package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	releaseStateFile = "release.json"
	releaseSeenFile  = "release-seen.log"
	releaseRepo      = "/release-api/repos/bezmoradi/software-factory"
	releaseSlowFor   = 40 * time.Second
	releaseSeenKept  = 50
)

var (
	releaseNotesModes  = []string{"published", "no-release", "pre-release"}
	releaseAnswerModes = []string{"ok", "malformed", "slow", "refused"}
	errReleaseUsage    = errors.New("usage: sf-gitserver release <tag> [" + strings.Join(releaseNotesModes, "|") + "] | release off | release-answer " + strings.Join(releaseAnswerModes, "|") + " | release-seen")
)

type releaseState struct {
	Tag    string `json:"tag"`
	Notes  string `json:"notes"`
	Answer string `json:"answer"`
}

func readReleaseState(root string) releaseState {
	s := releaseState{Notes: "published", Answer: "ok"}
	data, err := os.ReadFile(filepath.Join(root, releaseStateFile))
	if err == nil {
		_ = json.Unmarshal(data, &s)
	}
	return s
}

func writeReleaseState(root string, s releaseState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, releaseStateFile), append(data, '\n'), 0o600)
}

func releaseCommand(root string, args []string) error {
	s := readReleaseState(root)
	switch {
	case len(args) == 2 && args[0] == "release" && args[1] == "off":
		s.Tag = ""
	case (len(args) == 2 || len(args) == 3) && args[0] == "release":
		if args[1] == "" || strings.ContainsAny(args[1], "/ \t\n") || len(args[1]) > 60 {
			return errors.New(args[1] + " isn't a tag name this fake serves")
		}
		s.Tag, s.Notes = args[1], "published"
		if len(args) == 3 {
			if !slices.Contains(releaseNotesModes, args[2]) {
				return errReleaseUsage
			}
			s.Notes = args[2]
		}
	case len(args) == 2 && args[0] == "release-answer" && slices.Contains(releaseAnswerModes, args[1]):
		s.Answer = args[1]
	case len(args) == 1 && args[0] == "release-seen":
		data, err := os.ReadFile(filepath.Join(root, releaseSeenFile))
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("no request yet")
			return nil
		}
		if err == nil {
			_, err = os.Stdout.Write(data)
		}
		return err
	default:
		return errReleaseUsage
	}
	if err := writeReleaseState(root, s); err != nil {
		return err
	}
	fmt.Printf("tag %q, notes %s, answer %s\n", s.Tag, s.Notes, s.Answer)
	return nil
}

func releaseNotes(tag string) string {
	return "Software Factory " + tag + ".\n\n```bash\ncurl -fsSL https://github.com/bezmoradi/software-factory/releases/latest/download/install.sh | sh\n```\n"
}

type releaseFake struct {
	root string
}

func headerSeen(h http.Header, name string) string {
	if _, ok := h[http.CanonicalHeaderKey(name)]; ok {
		return "present"
	}
	return "absent"
}

func (f releaseFake) note(r *http.Request) {
	line := fmt.Sprintf("%s %s authorization=%s cookie=%s user-agent=%q", time.Now().UTC().Format(time.RFC3339), r.URL.Path, headerSeen(r.Header, "Authorization"), headerSeen(r.Header, "Cookie"), r.UserAgent())
	log.Printf("release-api %s", line)
	path := filepath.Join(f.root, releaseSeenFile)
	data, _ := os.ReadFile(path)
	lines := append(strings.Split(strings.TrimRight(string(data), "\n"), "\n"), line)
	if lines[0] == "" {
		lines = lines[1:]
	}
	if len(lines) > releaseSeenKept {
		lines = lines[len(lines)-releaseSeenKept:]
	}
	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func answerJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f releaseFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	f.note(r)
	s := readReleaseState(f.root)
	switch s.Answer {
	case "slow":
		select {
		case <-time.After(releaseSlowFor):
		case <-r.Context().Done():
			log.Printf("release-api: the client left during the slow answer")
			return
		}
	case "refused":
		answerJSON(w, http.StatusForbidden, map[string]string{"message": "API rate limit exceeded (fake)"})
		return
	case "malformed":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"name": "v99.0.0", "this is": [not json`))
		return
	}
	switch {
	case r.URL.Path == releaseRepo+"/tags":
		tags := []map[string]string{}
		if page := r.URL.Query().Get("page"); s.Tag != "" && (page == "" || page == "1") {
			for _, name := range []string{s.Tag, "latest", "v1.2", "v0.0.1"} {
				tags = append(tags, map[string]string{"name": name})
			}
		}
		answerJSON(w, http.StatusOK, tags)
	case strings.HasPrefix(r.URL.Path, releaseRepo+"/releases/tags/"):
		tag := strings.TrimPrefix(r.URL.Path, releaseRepo+"/releases/tags/")
		if s.Tag == "" || tag != s.Tag || s.Notes == "no-release" {
			answerJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
			return
		}
		answerJSON(w, http.StatusOK, map[string]any{"tag_name": tag, "name": "Software Factory " + tag, "draft": false, "prerelease": s.Notes == "pre-release", "body": releaseNotes(tag)})
	default:
		answerJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	}
}
