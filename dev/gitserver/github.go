package main

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	devOwner   = "dev"
	maxBody    = 1 << 20
	perPageMax = 100
)

type apiUser struct {
	Login string `json:"login"`
	ID    int    `json:"id"`
	Type  string `json:"type"`
}

type apiIssue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	User      string    `json:"user"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
	Pull      bool      `json:"pull,omitempty"`
	Head      string    `json:"head,omitempty"`
	HeadSHA   string    `json:"headSha,omitempty"`
	Base      string    `json:"base,omitempty"`
	Draft     bool      `json:"draft,omitempty"`
}

type apiComment struct {
	ID        int       `json:"id"`
	Issue     int       `json:"issue"`
	User      string    `json:"user"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	Path      string    `json:"path,omitempty"`
	Line      int       `json:"line,omitempty"`
	Commit    string    `json:"commit,omitempty"`
	State     string    `json:"state,omitempty"`
}

type apiCheck struct {
	Commit     string `json:"commit"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
}

type apiStatus struct {
	Commit      string `json:"commit"`
	Context     string `json:"context"`
	State       string `json:"state"`
	Description string `json:"description,omitempty"`
}

type apiTriggers struct {
	IssueDelay     int  `json:"issueDelay,omitempty"`
	Issue502       int  `json:"issue502,omitempty"`
	Issue502Kept   bool `json:"issue502Kept,omitempty"`
	PushDelay      int  `json:"pushDelay,omitempty"`
	PushDelayAfter bool `json:"pushDelayAfter,omitempty"`
	Comment403     int  `json:"comment403,omitempty"`
}

type apiState struct {
	DraftsOff      bool         `json:"draftsOff,omitempty"`
	Next           int          `json:"next"`
	NextID         int          `json:"nextId"`
	Issues         []apiIssue   `json:"issues"`
	Comments       []apiComment `json:"comments"`
	ReviewComments []apiComment `json:"reviewComments"`
	Reviews        []apiComment `json:"reviews"`
	Checks         []apiCheck   `json:"checks"`
	Statuses       []apiStatus  `json:"statuses"`
	Triggers       apiTriggers  `json:"triggers"`
}

func statePath(root string) string {
	return env("GITSERVER_API_STATE", filepath.Join(root, "github.json"))
}

func withState(root string, write bool, fn func(*apiState) error) error {
	path := statePath(root)
	lock, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	s := apiState{Next: 1, NextID: 1}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if err := fn(&s); err != nil || !write {
		return err
	}
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func login() string { return env("GITSERVER_LOGIN", "sf-dev-owner") }

func requestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b[:4]) + ":" + hex.EncodeToString(b[4:]))
}

type api struct {
	root  string
	token string
	repo  string
}

func (a api) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-GitHub-Request-Id", requestID())
	w.Header().Set("X-GitHub-Media-Type", "github.v3; format=json")
	auth := r.Header.Get("Authorization")
	if auth != "Bearer "+a.token && auth != "token "+a.token {
		log.Printf("api %s %s refused: no or wrong token", r.Method, r.URL.Path)
		answer(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	log.Printf("api %s %s", r.Method, r.URL.RequestURI())
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v3")
	if path == "/user" && r.Method == http.MethodGet {
		answer(w, http.StatusOK, apiUser{Login: login(), ID: 1, Type: "User"})
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "repos" || parts[1] != devOwner || parts[2] != a.repo {
		answer(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	a.repoRoute(w, r, parts[3:])
}

func (a api) repoRoute(w http.ResponseWriter, r *http.Request, rest []string) {
	get, post := r.Method == http.MethodGet, r.Method == http.MethodPost
	num := func(i int) (int, bool) {
		if len(rest) <= i {
			return 0, false
		}
		n, err := strconv.Atoi(rest[i])
		return n, err == nil && n > 0
	}
	switch {
	case len(rest) == 0 && get:
		answer(w, http.StatusOK, map[string]any{"name": a.repo, "full_name": devOwner + "/" + a.repo, "private": false, "has_issues": true, "default_branch": "main",
			"html_url": "https://" + r.Host + "/" + devOwner + "/" + a.repo})
	case len(rest) >= 2 && rest[0] == "branches" && get:
		name, err := url.PathUnescape(strings.Join(rest[1:], "/"))
		sha := ""
		if err == nil {
			sha = a.head(name)
		}
		if sha == "" {
			answer(w, http.StatusNotFound, map[string]string{"message": "Branch not found"})
			return
		}
		answer(w, http.StatusOK, map[string]any{"name": name, "commit": map[string]string{"sha": sha}, "protected": false})
	case len(rest) == 1 && rest[0] == "pulls" && get:
		a.listPulls(w, r)
	case len(rest) == 1 && rest[0] == "pulls" && post:
		a.createPull(w, r)
	case len(rest) == 1 && rest[0] == "issues" && post:
		a.createIssue(w, r)
	case len(rest) == 1 && rest[0] == "issues" && get:
		a.listIssues(w, r)
	case len(rest) == 3 && rest[0] == "issues" && rest[2] == "comments":
		n, ok := num(1)
		if !ok {
			answer(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
			return
		}
		if post {
			a.comment(w, r, n)
			return
		}
		a.listComments(w, r, n, func(s *apiState) []apiComment { return s.Comments }, a.commentJSON)
	case len(rest) == 3 && rest[0] == "pulls" && rest[2] == "comments" && get:
		n, _ := num(1)
		a.listComments(w, r, n, func(s *apiState) []apiComment { return s.ReviewComments }, a.reviewCommentJSON)
	case len(rest) == 3 && rest[0] == "pulls" && rest[2] == "reviews" && get:
		n, _ := num(1)
		a.listComments(w, r, n, func(s *apiState) []apiComment { return s.Reviews }, a.reviewJSON)
	case len(rest) == 3 && rest[0] == "commits" && rest[2] == "check-runs" && get:
		a.checkRuns(w, rest[1])
	case len(rest) == 3 && rest[0] == "commits" && rest[2] == "status" && get:
		a.statuses(w, rest[1])
	default:
		answer(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	}
}

func answer(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func invalid(w http.ResponseWriter, message string, errs ...map[string]string) {
	if errs == nil {
		errs = []map[string]string{}
	}
	answer(w, http.StatusUnprocessableEntity, map[string]any{"message": message, "errors": errs, "documentation_url": "https://docs.github.com/rest"})
}

func (a api) head(branch string) string {
	if strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") {
		return ""
	}
	sha, err := git(filepath.Join(a.root, a.repo+".git"), "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil || len(sha) != 40 {
		return ""
	}
	return sha
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(v)
}

func (a api) issueURL(host string, i apiIssue) string {
	kind := "issues"
	if i.Pull {
		kind = "pull"
	}
	return "https://" + host + "/" + devOwner + "/" + a.repo + "/" + kind + "/" + strconv.Itoa(i.Number)
}

func (a api) userJSON(name string) map[string]any {
	return map[string]any{"login": name, "type": "User"}
}

func (a api) issueJSON(host string, i apiIssue) map[string]any {
	out := map[string]any{"number": i.Number, "title": i.Title, "body": i.Body, "state": i.State, "user": a.userJSON(i.User),
		"created_at": i.CreatedAt.UTC().Format(time.RFC3339), "html_url": a.issueURL(host, i)}
	if i.Pull {
		out["pull_request"] = map[string]any{"html_url": a.issueURL(host, i)}
	}
	return out
}

func (a api) pullJSON(host string, i apiIssue) map[string]any {
	return map[string]any{"number": i.Number, "title": i.Title, "body": i.Body, "state": i.State, "draft": i.Draft, "user": a.userJSON(i.User),
		"created_at": i.CreatedAt.UTC().Format(time.RFC3339), "html_url": a.issueURL(host, i),
		"head": map[string]any{"ref": i.Head, "sha": i.HeadSHA, "label": devOwner + ":" + i.Head}, "base": map[string]any{"ref": i.Base}}
}

func (a api) listPulls(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = "open"
	}
	head := q.Get("head")
	if owner, branch, ok := strings.Cut(head, ":"); ok {
		if owner != devOwner {
			answer(w, http.StatusOK, []any{})
			return
		}
		head = branch
	}
	var out []map[string]any
	err := withState(a.root, false, func(s *apiState) error {
		for _, i := range slices.Backward(s.Issues) {
			if i.Pull && (state == "all" || i.State == state) && (head == "" || i.Head == head) {
				out = append(out, a.pullJSON(r.Host, i))
			}
		}
		return nil
	})
	if err != nil {
		answer(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
		return
	}
	page(w, r, out)
}

func (a api) createPull(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Draft bool   `json:"draft"`
	}
	if decode(r, &in) != nil || in.Title == "" || in.Head == "" || in.Base == "" {
		invalid(w, "Validation Failed", map[string]string{"resource": "PullRequest", "code": "missing_field"})
		return
	}
	head := in.Head
	if owner, branch, ok := strings.Cut(head, ":"); ok && owner == devOwner {
		head = branch
	}
	sha := a.head(head)
	if sha == "" {
		invalid(w, "Validation Failed", map[string]string{"resource": "PullRequest", "field": "head", "code": "invalid"})
		return
	}
	if a.head(in.Base) == "" {
		invalid(w, "Validation Failed", map[string]string{"resource": "PullRequest", "field": "base", "code": "invalid"})
		return
	}
	var made apiIssue
	refusal := ""
	err := withState(a.root, true, func(s *apiState) error {
		if in.Draft && s.DraftsOff {
			refusal = "Draft pull requests are not supported in this repository."
			return nil
		}
		for _, i := range s.Issues {
			if i.Pull && i.State == "open" && i.Head == head && i.Base == in.Base {
				refusal = "A pull request already exists for " + devOwner + ":" + head + "."
				return nil
			}
		}
		made = apiIssue{Number: s.Next, Title: in.Title, Body: in.Body, User: login(), State: "open", CreatedAt: time.Now().UTC(), Pull: true,
			Head: head, HeadSHA: sha, Base: in.Base, Draft: in.Draft}
		s.Next++
		s.Issues = append(s.Issues, made)
		return nil
	})
	switch {
	case err != nil:
		answer(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
	case refusal != "":
		invalid(w, "Validation Failed", map[string]string{"resource": "PullRequest", "code": "custom", "message": refusal})
	default:
		log.Printf("api opened pull request #%d from %s (draft %v)", made.Number, head, made.Draft)
		answer(w, http.StatusCreated, a.pullJSON(r.Host, made))
	}
}

func (a api) createIssue(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if decode(r, &in) != nil || in.Title == "" {
		invalid(w, "Validation Failed", map[string]string{"resource": "Issue", "field": "title", "code": "missing_field"})
		return
	}
	var made apiIssue
	var t apiTriggers
	fail := false
	err := withState(a.root, true, func(s *apiState) error {
		t = s.Triggers
		if s.Triggers.Issue502 > 0 {
			s.Triggers.Issue502--
			fail = true
			if !s.Triggers.Issue502Kept {
				return nil
			}
		}
		made = apiIssue{Number: s.Next, Title: in.Title, Body: in.Body, User: login(), State: "open", CreatedAt: time.Now().UTC()}
		s.Next++
		s.Issues = append(s.Issues, made)
		return nil
	})
	if err != nil {
		answer(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
		return
	}
	if made.Number > 0 {
		log.Printf("api opened issue #%d", made.Number)
	}
	if t.IssueDelay > 0 {
		log.Printf("api issue answer delayed %d seconds", t.IssueDelay)
		select {
		case <-time.After(time.Duration(t.IssueDelay) * time.Second):
		case <-r.Context().Done():
			log.Printf("api the client left before the issue answer")
			return
		}
	}
	if fail {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "<html><body>502 Bad Gateway</body></html>\n")
		return
	}
	answer(w, http.StatusCreated, a.issueJSON(r.Host, made))
}

func (a api) listIssues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state, creator := q.Get("state"), q.Get("creator")
	if state == "" {
		state = "open"
	}
	var list []apiIssue
	err := withState(a.root, false, func(s *apiState) error {
		for _, i := range s.Issues {
			if (state == "all" || i.State == state) && (creator == "" || strings.EqualFold(i.User, creator)) {
				list = append(list, i)
			}
		}
		return nil
	})
	if err != nil {
		answer(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
		return
	}
	slices.SortStableFunc(list, func(x, y apiIssue) int {
		return cmp.Or(x.CreatedAt.Compare(y.CreatedAt), cmp.Compare(x.Number, y.Number))
	})
	if q.Get("direction") != "asc" {
		slices.Reverse(list)
	}
	out := make([]map[string]any, 0, len(list))
	for _, i := range list {
		out = append(out, a.issueJSON(r.Host, i))
	}
	page(w, r, out)
}

func (a api) comment(w http.ResponseWriter, r *http.Request, n int) {
	var in struct {
		Body string `json:"body"`
	}
	if decode(r, &in) != nil || in.Body == "" {
		invalid(w, "Validation Failed", map[string]string{"resource": "IssueComment", "field": "body", "code": "missing_field"})
		return
	}
	var made apiComment
	found, forbidden := false, false
	err := withState(a.root, true, func(s *apiState) error {
		found = slices.ContainsFunc(s.Issues, func(i apiIssue) bool { return i.Number == n })
		if !found {
			return nil
		}
		if s.Triggers.Comment403 > 0 {
			s.Triggers.Comment403--
			forbidden = true
			return nil
		}
		made = apiComment{ID: s.NextID, Issue: n, User: login(), Body: in.Body, CreatedAt: time.Now().UTC()}
		s.NextID++
		s.Comments = append(s.Comments, made)
		return nil
	})
	switch {
	case err != nil:
		answer(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
	case !found:
		answer(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	case forbidden:
		log.Printf("api comment on #%d refused with 403", n)
		answer(w, http.StatusForbidden, map[string]string{"message": "Resource not accessible by personal access token"})
	default:
		log.Printf("api commented on #%d", n)
		answer(w, http.StatusCreated, a.commentJSON(r.Host, made))
	}
}

func (a api) link(host string, c apiComment, anchor string) string {
	return "https://" + host + "/" + devOwner + "/" + a.repo + "/pull/" + strconv.Itoa(c.Issue) + "#" + anchor + strconv.Itoa(c.ID)
}

func (a api) commentJSON(host string, c apiComment) map[string]any {
	return map[string]any{"id": c.ID, "body": c.Body, "user": a.userJSON(c.User), "created_at": c.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at": c.CreatedAt.UTC().Format(time.RFC3339), "html_url": a.link(host, c, "issuecomment-")}
}

func (a api) reviewCommentJSON(host string, c apiComment) map[string]any {
	out := a.commentJSON(host, c)
	out["html_url"] = a.link(host, c, "discussion_r")
	out["path"], out["commit_id"], out["original_commit_id"], out["original_line"] = c.Path, c.Commit, c.Commit, c.Line
	out["line"] = c.Line
	if c.Line == 0 {
		out["line"] = nil
	}
	return out
}

func (a api) reviewJSON(host string, c apiComment) map[string]any {
	return map[string]any{"id": c.ID, "body": c.Body, "user": a.userJSON(c.User), "state": c.State, "commit_id": c.Commit,
		"submitted_at": c.CreatedAt.UTC().Format(time.RFC3339), "html_url": a.link(host, c, "pullrequestreview-")}
}

func (a api) listComments(w http.ResponseWriter, r *http.Request, n int, from func(*apiState) []apiComment, shape func(string, apiComment) map[string]any) {
	var out []map[string]any
	found := false
	err := withState(a.root, false, func(s *apiState) error {
		found = slices.ContainsFunc(s.Issues, func(i apiIssue) bool { return i.Number == n })
		for _, c := range from(s) {
			if c.Issue == n {
				out = append(out, shape(r.Host, c))
			}
		}
		return nil
	})
	switch {
	case err != nil:
		answer(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
	case !found:
		answer(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	default:
		page(w, r, out)
	}
}

func page(w http.ResponseWriter, r *http.Request, all []map[string]any) {
	q := r.URL.Query()
	per, err := strconv.Atoi(q.Get("per_page"))
	if err != nil || per <= 0 {
		per = 30
	}
	per = min(per, perPageMax)
	n, err := strconv.Atoi(q.Get("page"))
	if err != nil || n <= 0 {
		n = 1
	}
	last := max(1, (len(all)+per-1)/per)
	from, to := min((n-1)*per, len(all)), min(n*per, len(all))
	at := func(p int) string {
		v := r.URL.Query()
		v.Set("page", strconv.Itoa(p))
		v.Set("per_page", strconv.Itoa(per))
		return "<https://" + r.Host + r.URL.Path + "?" + v.Encode() + ">"
	}
	var links []string
	if n < last {
		links = append(links, at(n+1)+`; rel="next"`, at(last)+`; rel="last"`)
	}
	if n > 1 {
		links = append(links, at(1)+`; rel="first"`, at(min(n-1, last))+`; rel="prev"`)
	}
	if len(links) > 0 {
		w.Header().Set("Link", strings.Join(links, ", "))
	}
	out := all[from:to]
	if out == nil {
		out = []map[string]any{}
	}
	answer(w, http.StatusOK, out)
}

func (a api) checkRuns(w http.ResponseWriter, commit string) {
	var runs []map[string]any
	err := withState(a.root, false, func(s *apiState) error {
		for i, c := range s.Checks {
			if c.Commit == commit {
				runs = append(runs, map[string]any{"id": i + 1, "name": c.Name, "status": c.Status, "conclusion": c.Conclusion,
					"html_url": "https://" + devOwner + ".invalid/checks/" + strconv.Itoa(i+1), "app": map[string]string{"slug": "sf-dev-checks", "name": "Dev checks"}})
			}
		}
		return nil
	})
	if err != nil {
		answer(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
		return
	}
	if runs == nil {
		runs = []map[string]any{}
	}
	answer(w, http.StatusOK, map[string]any{"total_count": len(runs), "check_runs": runs})
}

func (a api) statuses(w http.ResponseWriter, commit string) {
	list := []map[string]any{}
	state := "pending"
	err := withState(a.root, false, func(s *apiState) error {
		failed, pending := false, false
		for _, c := range s.Statuses {
			if c.Commit != commit {
				continue
			}
			list = append(list, map[string]any{"context": c.Context, "state": c.State, "description": c.Description, "target_url": ""})
			failed = failed || c.State == "failure" || c.State == "error"
			pending = pending || c.State == "pending"
		}
		switch {
		case failed:
			state = "failure"
		case len(list) > 0 && !pending:
			state = "success"
		}
		return nil
	})
	if err != nil {
		answer(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
		return
	}
	answer(w, http.StatusOK, map[string]any{"state": state, "sha": commit, "total_count": len(list), "statuses": list})
}

func apiCommand(root string, args []string) error {
	need := func(n int, usage string) error {
		if len(args) != n {
			return errors.New("usage: sf-gitserver " + usage)
		}
		return nil
	}
	number := func(s string) (int, error) {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return 0, errors.New(s + " isn't a pull request or issue number")
		}
		return n, nil
	}
	seconds := func(s string) (int, error) {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 3600 {
			return 0, errors.New(s + " isn't a number of seconds from 0 to 3600")
		}
		return n, nil
	}
	add := func(n int, list func(*apiState) *[]apiComment, c apiComment) error {
		return withState(root, true, func(s *apiState) error {
			if !slices.ContainsFunc(s.Issues, func(i apiIssue) bool { return i.Number == n }) {
				return fmt.Errorf("no issue or pull request #%d", n)
			}
			c.ID, c.Issue, c.CreatedAt = s.NextID, n, time.Now().UTC()
			if c.State != "" || c.Path != "" {
				for _, i := range s.Issues {
					if i.Number == n {
						c.Commit = i.HeadSHA
					}
				}
			}
			s.NextID++
			*list(s) = append(*list(s), c)
			fmt.Println(c.ID)
			return nil
		})
	}
	switch args[0] {
	case "comment":
		if err := need(4, "comment <pr> <login> <text>"); err != nil {
			return err
		}
		n, err := number(args[1])
		if err != nil {
			return err
		}
		return add(n, func(s *apiState) *[]apiComment { return &s.Comments }, apiComment{User: args[2], Body: args[3]})
	case "review-comment":
		if err := need(6, "review-comment <pr> <login> <path> <line> <text>"); err != nil {
			return err
		}
		n, err := number(args[1])
		if err != nil {
			return err
		}
		line, err := strconv.Atoi(args[4])
		if err != nil || line < 0 {
			return errors.New(args[4] + " isn't a line number (0 for an outdated comment)")
		}
		return add(n, func(s *apiState) *[]apiComment { return &s.ReviewComments }, apiComment{User: args[2], Path: args[3], Line: line, Body: args[5]})
	case "review":
		if err := need(5, "review <pr> <login> <APPROVED|CHANGES_REQUESTED|COMMENTED> <text>"); err != nil {
			return err
		}
		n, err := number(args[1])
		if err != nil {
			return err
		}
		state := strings.ToUpper(args[3])
		if !slices.Contains([]string{"APPROVED", "CHANGES_REQUESTED", "COMMENTED", "DISMISSED"}, state) {
			return errors.New("the state is APPROVED, CHANGES_REQUESTED, COMMENTED or DISMISSED")
		}
		return add(n, func(s *apiState) *[]apiComment { return &s.Reviews }, apiComment{User: args[2], State: state, Body: args[4]})
	case "drafts":
		if len(args) != 2 || args[1] != "on" && args[1] != "off" {
			return errors.New("usage: sf-gitserver drafts on|off")
		}
		return withState(root, true, func(s *apiState) error { s.DraftsOff = args[1] == "off"; return nil })
	case "show":
		if err := need(2, "show <pr>"); err != nil {
			return err
		}
		n, err := number(args[1])
		if err != nil {
			return err
		}
		return withState(root, false, func(s *apiState) error {
			i := slices.IndexFunc(s.Issues, func(i apiIssue) bool { return i.Number == n })
			if i < 0 {
				return fmt.Errorf("no issue or pull request #%d", n)
			}
			pick := func(list []apiComment) []apiComment {
				out := []apiComment{}
				for _, c := range list {
					if c.Issue == n {
						out = append(out, c)
					}
				}
				return out
			}
			out, _ := json.MarshalIndent(map[string]any{"issue": s.Issues[i], "comments": pick(s.Comments), "reviewComments": pick(s.ReviewComments),
				"reviews": pick(s.Reviews)}, "", "  ")
			fmt.Println(string(out))
			return nil
		})
	case "list":
		return withState(root, false, func(s *apiState) error {
			for _, i := range s.Issues {
				kind := "issue"
				if i.Pull {
					kind = fmt.Sprintf("pull request %s -> %s, draft %v", i.Head, i.Base, i.Draft)
				}
				fmt.Printf("#%d %s %s: %s\n", i.Number, i.State, kind, i.Title)
			}
			return nil
		})
	case "issue-delay":
		if err := need(2, "issue-delay <seconds>"); err != nil {
			return err
		}
		n, err := seconds(args[1])
		if err != nil {
			return err
		}
		return withState(root, true, func(s *apiState) error { s.Triggers.IssueDelay = n; return nil })
	case "issue-502":
		if len(args) != 3 || args[2] != "created" && args[2] != "lost" {
			return errors.New("usage: sf-gitserver issue-502 <count> created|lost")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 || n > 100 {
			return errors.New(args[1] + " isn't a count from 0 to 100")
		}
		return withState(root, true, func(s *apiState) error {
			s.Triggers.Issue502, s.Triggers.Issue502Kept = n, args[2] == "created"
			return nil
		})
	case "comment-403":
		if err := need(2, "comment-403 <count>"); err != nil {
			return err
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 || n > 100 {
			return errors.New(args[1] + " isn't a count from 0 to 100")
		}
		return withState(root, true, func(s *apiState) error { s.Triggers.Comment403 = n; return nil })
	case "push-delay":
		if len(args) != 3 || args[2] != "before" && args[2] != "after" {
			return errors.New("usage: sf-gitserver push-delay <seconds> before|after")
		}
		n, err := seconds(args[1])
		if err != nil {
			return err
		}
		return withState(root, true, func(s *apiState) error {
			s.Triggers.PushDelay, s.Triggers.PushDelayAfter = n, args[2] == "after"
			return nil
		})
	case "check":
		if len(args) != 5 && len(args) != 4 {
			return errors.New("usage: sf-gitserver check <commit> <name> <queued|in_progress|completed> [conclusion]")
		}
		c := apiCheck{Commit: args[1], Name: args[2], Status: args[3]}
		if len(args) == 5 {
			c.Conclusion = args[4]
		}
		return withState(root, true, func(s *apiState) error { s.Checks = append(s.Checks, c); return nil })
	case "status":
		if err := need(4, "status <commit> <context> <pending|success|failure|error>"); err != nil {
			return err
		}
		return withState(root, true, func(s *apiState) error {
			s.Statuses = append(s.Statuses, apiStatus{Commit: args[1], Context: args[2], State: args[3]})
			return nil
		})
	case "reset":
		return withState(root, true, func(s *apiState) error { *s = apiState{Next: 1, NextID: 1}; return nil })
	}
	return errUsage
}

var errUsage = errors.New("usage: sf-gitserver [commit <text> | comment <pr> <login> <text> | review-comment <pr> <login> <path> <line> <text> | review <pr> <login> <state> <text> | drafts on|off | show <pr> | list | issue-delay <seconds> | issue-502 <count> created|lost | comment-403 <count> | push-delay <seconds> before|after | check <commit> <name> <status> [conclusion] | status <commit> <context> <state> | reset]")

func pushDelay(root string) (int, bool) {
	var t apiTriggers
	_ = withState(root, false, func(s *apiState) error { t = s.Triggers; return nil })
	return t.PushDelay, t.PushDelayAfter
}
