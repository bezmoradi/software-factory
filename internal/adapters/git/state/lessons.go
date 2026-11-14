package state

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const lessonsFile = "lessons.json"

var (
	lessonFingerprint = regexp.MustCompile(`^[0-9a-f]{12}$`)
	lessonLineID      = regexp.MustCompile(`^[0-9a-f]{16}$`)
	lessonStates      = []string{session.LessonOffered, session.LessonApproved, session.LessonDeclined}
)

type Lessons struct {
	Dir string
	mu  *sync.Mutex
}

func NewLessons(dir string) Lessons { return Lessons{Dir: dir, mu: &sync.Mutex{}} }

var _ session.LessonStore = Lessons{}

type lessonFindingJSON struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Path    string    `json:"path,omitempty"`
	Title   string    `json:"title"`
	Excerpt string    `json:"excerpt,omitempty"`
	Commits []string  `json:"commits"`
	Last    time.Time `json:"last"`
}

type lessonLineJSON struct {
	ID      string    `json:"id"`
	Text    string    `json:"text"`
	From    []string  `json:"from"`
	State   string    `json:"state"`
	Session string    `json:"session,omitempty"`
	At      time.Time `json:"at"`
}

type lessonProjectJSON struct {
	Findings []lessonFindingJSON `json:"findings,omitempty"`
	Lines    []lessonLineJSON    `json:"lines,omitempty"`
}

type lessonsJSON struct {
	Version  int                          `json:"version"`
	Projects map[string]lessonProjectJSON `json:"projects"`
}

func (l Lessons) lock() func() {
	if l.mu == nil {
		return func() {}
	}
	l.mu.Lock()
	return l.mu.Unlock
}

func lessonFindingOf(f lessonFindingJSON) (session.LessonFinding, bool) {
	if !lessonFingerprint.MatchString(f.ID) || !workflow.AgentName(f.Agent) || !cleanText(f.Path, 300) || !cleanText(f.Title, 300) || f.Title == "" {
		return session.LessonFinding{}, false
	}
	out := session.LessonFinding{ID: f.ID, Agent: f.Agent, Path: f.Path, Title: f.Title, Last: f.Last}
	if utf8.ValidString(f.Excerpt) && utf8.RuneCountInString(f.Excerpt) <= session.MaxLessonExcerpt+10 && !strings.ContainsRune(f.Excerpt, 0) {
		out.Excerpt = f.Excerpt
	}
	for _, c := range f.Commits {
		if commitID.MatchString(c) && !slices.Contains(out.Commits, c) && len(out.Commits) < session.MaxLessonCommits {
			out.Commits = append(out.Commits, c)
		}
	}
	return out, len(out.Commits) > 0
}

func lessonLineOf(x lessonLineJSON) (session.LessonLine, bool) {
	if !lessonLineID.MatchString(x.ID) || !cleanText(x.Text, session.MaxLessonLine) || x.Text == "" || !slices.Contains(lessonStates, x.State) ||
		(x.Session != "" && !sessionID.MatchString(x.Session)) {
		return session.LessonLine{}, false
	}
	out := session.LessonLine{ID: x.ID, Text: x.Text, State: x.State, Session: x.Session, At: x.At}
	for _, id := range x.From {
		if lessonFingerprint.MatchString(id) && !slices.Contains(out.From, id) && len(out.From) < session.MaxLessonGiven {
			out.From = append(out.From, id)
		}
	}
	return out, len(out.From) > 0
}

func (l Lessons) Load() (map[string]session.LessonProject, error) {
	defer l.lock()()
	out := map[string]session.LessonProject{}
	data, err := read(l.Dir, lessonsFile)
	if err != nil || data == nil {
		return out, err
	}
	var f lessonsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New("lessons.json isn't a record of lessons from reviews this engine reads")
	}
	for name, p := range f.Projects {
		if !repoName.MatchString(name) || len(out) >= session.MaxLessonProjects {
			continue
		}
		var v session.LessonProject
		for _, x := range p.Findings {
			if fd, ok := lessonFindingOf(x); ok && len(v.Findings) < session.MaxLessonFindings && !slices.ContainsFunc(v.Findings, func(y session.LessonFinding) bool { return y.ID == fd.ID }) {
				v.Findings = append(v.Findings, fd)
			}
		}
		for _, x := range p.Lines {
			if ln, ok := lessonLineOf(x); ok && len(v.Lines) < session.MaxLessonLines && !slices.ContainsFunc(v.Lines, func(y session.LessonLine) bool { return y.ID == ln.ID }) {
				v.Lines = append(v.Lines, ln)
			}
		}
		out[name] = v
	}
	return out, nil
}

func (l Lessons) Save(all map[string]session.LessonProject) error {
	defer l.lock()()
	f := lessonsJSON{Version: 1, Projects: map[string]lessonProjectJSON{}}
	for name, p := range all {
		if !repoName.MatchString(name) {
			continue
		}
		var v lessonProjectJSON
		for _, x := range p.Findings {
			v.Findings = append(v.Findings, lessonFindingJSON{ID: x.ID, Agent: x.Agent, Path: x.Path, Title: x.Title, Excerpt: x.Excerpt, Commits: x.Commits, Last: x.Last.UTC()})
		}
		for _, x := range p.Lines {
			v.Lines = append(v.Lines, lessonLineJSON{ID: x.ID, Text: x.Text, From: x.From, State: x.State, Session: x.Session, At: x.At.UTC()})
		}
		f.Projects[name] = v
	}
	dropped := 0
	for {
		data, err := encode(f)
		if err != nil {
			return err
		}
		trimmed := false
		if len(data) > maxFile {
			for name, p := range f.Projects {
				if len(p.Findings) > 1 {
					dropped += len(p.Findings) - len(p.Findings)/2
					p.Findings = p.Findings[:len(p.Findings)/2]
					f.Projects[name] = p
					trimmed = true
				}
			}
		}
		if trimmed {
			continue
		}
		if err := writeBytes(l.Dir, lessonsFile, data); err != nil {
			return err
		}
		noteDropped(l.Dir, lessonsFile, dropped)
		return nil
	}
}

func (l Lessons) Dropped(n int) { noteDropped(l.Dir, lessonsFile, n) }
