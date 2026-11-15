package state

import (
	"cmp"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	prFollowFile     = "prfollow.json"
	maxFollowProject = 255
	maxFollowURL     = 300
	maxFollowNumber  = 1_000_000_000
	maxFollowStop    = 300
	maxProjectURL    = 2048
)

var followKey = regexp.MustCompile(`^[0-9a-f]{64}$`)

type PRFollows struct {
	Dir string
	mu  *sync.Mutex
}

func NewPRFollows(dir string) PRFollows { return PRFollows{Dir: dir, mu: &sync.Mutex{}} }

var _ session.FollowStore = PRFollows{}

type followJSON struct {
	SID       string    `json:"sid"`
	Project   string    `json:"project"`
	Branch    string    `json:"branch"`
	Issue     int       `json:"issue,omitempty"`
	IssueSent bool      `json:"issueSent,omitempty"`
	Watch     bool      `json:"watch,omitempty"`
	Pushed    time.Time `json:"pushed"`
	Number    int       `json:"number,omitempty"`
	URL       string    `json:"url,omitempty"`
	Opened    bool      `json:"opened,omitempty"`
	Done      string    `json:"done,omitempty"`
	DoneAt    time.Time `json:"doneAt"`
	CheckSent string    `json:"checkSent,omitempty"`
	SeenAt    time.Time `json:"seenAt"`
	SeenURLs  []string  `json:"seenUrls,omitempty"`
	Rounds    int       `json:"rounds,omitempty"`
	AutoStop  string    `json:"autoStop,omitempty"`
}

type followSettingsJSON struct {
	Project string   `json:"project"`
	URL     string   `json:"url"`
	Bots    []string `json:"bots,omitempty"`
	Auto    bool     `json:"auto,omitempty"`
	Assign  bool     `json:"assign,omitempty"`
	Owner   string   `json:"owner,omitempty"`
}

type prFollowsJSON struct {
	Version  int                  `json:"version"`
	Follows  []followJSON         `json:"follows"`
	Settings []followSettingsJSON `json:"settings"`
}

func validProject(id session.ProjectID) bool {
	return id.Name != "" && cleanText(id.Name, maxFollowProject) && id.URL != "" && cleanText(id.URL, maxProjectURL)
}

func followURL(u string) bool {
	return u == "" || len(u) <= maxFollowURL && strings.HasPrefix(u, "https://") && cleanText(u, maxFollowURL)
}

func followOf(j followJSON) (session.SavedFollow, bool) {
	ok := sessionID.MatchString(j.SID) && j.Project != "" && cleanText(j.Project, maxFollowProject) && j.Branch != "" && cleanText(j.Branch, maxForgeBranch) &&
		j.Issue >= 0 && j.Issue <= maxFollowNumber && j.Number >= 0 && j.Number <= maxFollowNumber && followURL(j.URL) &&
		(j.Done == "" || j.Done == session.FollowMerged || j.Done == session.FollowClosed) && (j.CheckSent == "" || followKey.MatchString(j.CheckSent)) &&
		len(j.SeenURLs) <= session.MaxFollowSeen && j.Rounds >= 0 && j.Rounds <= session.MaxFollowRounds && (j.AutoStop == "" || cleanText(j.AutoStop, maxFollowStop))
	for _, u := range j.SeenURLs {
		ok = ok && followURL(u)
	}
	if !ok {
		return session.SavedFollow{}, false
	}
	return session.SavedFollow{SID: j.SID, Project: j.Project, Branch: j.Branch, Issue: j.Issue, IssueSent: j.IssueSent, Watch: j.Watch, Pushed: j.Pushed, Number: j.Number,
		URL: j.URL, Opened: j.Opened, Done: j.Done, DoneAt: j.DoneAt, CheckSent: j.CheckSent, SeenAt: j.SeenAt, SeenURLs: append([]string(nil), j.SeenURLs...),
		Rounds: j.Rounds, AutoStop: j.AutoStop}, true
}

func settingsOf(j followSettingsJSON) (session.FollowSettings, bool) {
	if len(j.Bots) > session.MaxFollowBots {
		return session.FollowSettings{}, false
	}
	for _, b := range j.Bots {
		if !session.BotLogin.MatchString(b) {
			return session.FollowSettings{}, false
		}
	}
	return session.FollowSettings{Bots: append([]string(nil), j.Bots...), Auto: j.Auto, Assign: j.Assign}, true
}

func (s PRFollows) Load() (session.FollowRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := session.FollowRecord{Projects: map[session.ProjectID]session.FollowSettings{}}
	data, err := read(s.Dir, prFollowFile)
	if err != nil || data == nil {
		return out, err
	}
	var f prFollowsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return out, errors.New(prFollowFile + " isn't a record of followed pull requests this engine reads")
	}
	seen := map[string]bool{}
	for _, j := range f.Follows {
		v, ok := followOf(j)
		if !ok || seen[v.SID] || len(out.Follows) >= session.MaxFollowed {
			continue
		}
		seen[v.SID] = true
		out.Follows = append(out.Follows, v)
	}
	for _, j := range f.Settings {
		v, ok := settingsOf(j)
		id := session.ProjectID{Name: j.Project, URL: j.URL}
		if !ok || !validProject(id) || len(out.Projects) >= session.MaxFollowed {
			continue
		}
		out.Projects[id] = v
	}
	return out, nil
}

func (s PRFollows) Save(rec session.FollowRecord) error {
	owner := ownerIn(s.Dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	f := prFollowsJSON{Version: 1, Follows: []followJSON{}, Settings: []followSettingsJSON{}}
	capped := 0
	for _, v := range rec.Follows {
		j := followJSON{SID: v.SID, Project: v.Project, Branch: v.Branch, Issue: v.Issue, IssueSent: v.IssueSent, Watch: v.Watch, Pushed: v.Pushed.UTC(), Number: v.Number,
			URL: v.URL, Opened: v.Opened, Done: v.Done, DoneAt: v.DoneAt.UTC(), CheckSent: v.CheckSent, SeenAt: v.SeenAt.UTC(), SeenURLs: v.SeenURLs, Rounds: v.Rounds, AutoStop: v.AutoStop}
		_, ok := followOf(j)
		if ok && len(f.Follows) >= session.MaxFollowed {
			capped++
		}
		if !ok || len(f.Follows) >= session.MaxFollowed {
			continue
		}
		f.Follows = append(f.Follows, j)
	}
	for id, v := range rec.Projects {
		j := followSettingsJSON{Owner: owner, Project: id.Name, URL: id.URL, Bots: v.Bots, Auto: v.Auto, Assign: v.Assign}
		if _, ok := settingsOf(j); !ok || !validProject(id) || len(f.Settings) >= session.MaxFollowed {
			continue
		}
		f.Settings = append(f.Settings, j)
	}
	slices.SortFunc(f.Settings, func(a, b followSettingsJSON) int {
		return cmp.Or(cmp.Compare(a.Project, b.Project), cmp.Compare(a.URL, b.URL))
	})
	noteDropped(s.Dir, prFollowFile, capped)
	return write(s.Dir, prFollowFile, f)
}
