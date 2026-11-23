package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

func labelsJSON(names []string) []map[string]string {
	out := []map[string]string{}
	for _, n := range names {
		out = append(out, map[string]string{"name": n})
	}
	return out
}

func (a api) assigneesJSON(names []string) []map[string]any {
	out := []map[string]any{}
	for _, n := range names {
		out = append(out, a.userJSON(n))
	}
	return out
}

func nodeID(n int) string { return "PR_dev_" + strconv.Itoa(n) }

func (a api) livePull(host string, i apiIssue) map[string]any {
	out := a.pullJSON(host, i)
	head := i.HeadSHA
	if i.State == "open" {
		if sha := a.head(i.Head); sha != "" {
			head = sha
		}
	}
	out["head"] = map[string]any{"ref": i.Head, "sha": head, "label": devOwner + ":" + i.Head}
	out["node_id"] = nodeID(i.Number)
	out["merged"] = i.Merged
	out["merged_at"] = nil
	if i.Merged {
		out["merged_at"] = time.Now().UTC().Format(time.RFC3339)
	}
	out["assignees"] = a.assigneesJSON(i.Assignees)
	switch {
	case i.State != "open":
		out["mergeable"], out["mergeable_state"] = nil, "unknown"
	case i.Conflict:
		out["mergeable"], out["mergeable_state"] = false, "dirty"
	case i.Draft:
		out["mergeable"], out["mergeable_state"] = true, "draft"
	default:
		out["mergeable"], out["mergeable_state"] = true, "clean"
	}
	return out
}

func (a api) find(n int, pull bool) (apiIssue, bool, error) {
	var found apiIssue
	ok := false
	err := withState(a.root, false, func(s *apiState) error {
		for _, i := range s.Issues {
			if i.Number == n && i.Pull == pull {
				found, ok = i, true
			}
		}
		return nil
	})
	return found, ok, err
}

func (a api) getPull(w http.ResponseWriter, r *http.Request, n int) {
	i, ok, err := a.find(n, true)
	switch {
	case err != nil:
		answer(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
	case !ok:
		answer(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	default:
		answer(w, http.StatusOK, a.livePull(r.Host, i))
	}
}

func (a api) getIssue(w http.ResponseWriter, r *http.Request, n int) {
	var found apiIssue
	ok := false
	err := withState(a.root, false, func(s *apiState) error {
		for _, i := range s.Issues {
			if i.Number == n {
				found, ok = i, true
			}
		}
		return nil
	})
	switch {
	case err != nil:
		answer(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
	case !ok:
		answer(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	default:
		answer(w, http.StatusOK, a.issueJSON(r.Host, found))
	}
}

func (a api) assign(w http.ResponseWriter, r *http.Request, n int) {
	var in struct {
		Assignees []string `json:"assignees"`
	}
	if decode(r, &in) != nil || len(in.Assignees) == 0 {
		invalid(w, "Validation Failed", map[string]string{"resource": "Issue", "field": "assignees", "code": "missing_field"})
		return
	}
	var made apiIssue
	found := false
	err := withState(a.root, true, func(s *apiState) error {
		for k, i := range s.Issues {
			if i.Number != n {
				continue
			}
			found = true
			for _, who := range in.Assignees {
				if strings.EqualFold(who, login()) && !slices.Contains(i.Assignees, login()) {
					s.Issues[k].Assignees = append(s.Issues[k].Assignees, login())
				}
			}
			made = s.Issues[k]
		}
		return nil
	})
	switch {
	case err != nil:
		answer(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
	case !found:
		answer(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	default:
		log.Printf("api assigned #%d to %v", n, made.Assignees)
		answer(w, http.StatusCreated, a.issueJSON(r.Host, made))
	}
}

type graphql struct {
	root  string
	token string
}

func (g graphql) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-GitHub-Request-Id", requestID())
	w.Header().Set("X-GitHub-Media-Type", "github.v4; format=json")
	if auth := r.Header.Get("Authorization"); auth != "Bearer "+g.token && auth != "token "+g.token {
		log.Printf("graphql refused: no or wrong token")
		answer(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	if r.Method != http.MethodPost {
		answer(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	var in struct {
		Query     string            `json:"query"`
		Variables map[string]string `json:"variables"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&in); err != nil {
		answer(w, http.StatusOK, map[string]any{"errors": []map[string]string{{"message": "Problems parsing JSON"}}})
		return
	}
	if !strings.Contains(in.Query, "markPullRequestReadyForReview") {
		answer(w, http.StatusOK, map[string]any{"errors": []map[string]string{{"message": "This development server answers only markPullRequestReadyForReview"}}})
		return
	}
	id := in.Variables["id"]
	n, err := strconv.Atoi(strings.TrimPrefix(id, "PR_dev_"))
	var done apiIssue
	found := false
	if err == nil && strings.HasPrefix(id, "PR_dev_") {
		err = withState(g.root, true, func(s *apiState) error {
			for k, i := range s.Issues {
				if i.Pull && i.Number == n && i.State == "open" {
					s.Issues[k].Draft = false
					done, found = s.Issues[k], true
				}
			}
			return nil
		})
	}
	if err != nil || !found {
		answer(w, http.StatusOK, map[string]any{"data": map[string]any{"markPullRequestReadyForReview": nil},
			"errors": []map[string]any{{"type": "NOT_FOUND", "message": "Could not resolve to a node with the global id of '" + id + "'"}}})
		return
	}
	log.Printf("graphql marked pull request #%d ready for review", done.Number)
	answer(w, http.StatusOK, map[string]any{"data": map[string]any{"markPullRequestReadyForReview": map[string]any{"pullRequest": map[string]any{"isDraft": false, "number": done.Number}}}})
}

func fakeToken() string {
	return "gh" + "p_" + strings.Repeat("Fake0", 8)
}

func pullCommand(root string, args []string) error {
	number := func(s string) (int, error) {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return 0, errors.New(s + " isn't a pull request or issue number")
		}
		return n, nil
	}
	pull := func(s string, fn func(*apiIssue) error) error {
		n, err := number(s)
		if err != nil {
			return err
		}
		return withState(root, true, func(st *apiState) error {
			for k := range st.Issues {
				if st.Issues[k].Pull && st.Issues[k].Number == n {
					return fn(&st.Issues[k])
				}
			}
			return fmt.Errorf("no pull request #%d", n)
		})
	}
	issue := func(title, body string, labels []string) error {
		return withState(root, true, func(st *apiState) error {
			made := apiIssue{Number: st.Next, Title: title, Body: body, User: login(), State: "open", CreatedAt: time.Now().UTC(), Labels: labels}
			st.Next++
			st.Issues = append(st.Issues, made)
			fmt.Println(made.Number)
			return nil
		})
	}
	split := func(s string) []string {
		var out []string
		for l := range strings.SplitSeq(s, ",") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		return out
	}
	switch args[0] {
	case "pr-check":
		if len(args) != 4 && len(args) != 5 {
			return errors.New("usage: sf-gitserver pr-check <pr> <name> <queued|in_progress|completed> [conclusion]")
		}
		var head string
		if err := pull(args[1], func(i *apiIssue) error { head = i.Head; return nil }); err != nil {
			return err
		}
		sha := api{root: root, repo: "demo"}.head(head)
		if sha == "" {
			return errors.New("the pull request's branch " + head + " isn't on this server")
		}
		c := apiCheck{Commit: sha, Name: args[2], Status: args[3]}
		if len(args) == 5 {
			c.Conclusion = args[4]
		}
		fmt.Println(sha)
		return withState(root, true, func(s *apiState) error { s.Checks = append(s.Checks, c); return nil })
	case "conflict":
		if len(args) != 3 || args[2] != "on" && args[2] != "off" {
			return errors.New("usage: sf-gitserver conflict <pr> on|off")
		}
		return pull(args[1], func(i *apiIssue) error { i.Conflict = args[2] == "on"; return nil })
	case "merge":
		if len(args) != 2 {
			return errors.New("usage: sf-gitserver merge <pr>")
		}
		return pull(args[1], func(i *apiIssue) error {
			if i.State != "open" {
				return fmt.Errorf("pull request #%d isn't open", i.Number)
			}
			i.State, i.Merged = "closed", true
			return nil
		})
	case "close":
		if len(args) != 2 {
			return errors.New("usage: sf-gitserver close <pr>")
		}
		return pull(args[1], func(i *apiIssue) error { i.State = "closed"; return nil })
	case "ready":
		if len(args) != 2 {
			return errors.New("usage: sf-gitserver ready <pr>")
		}
		return pull(args[1], func(i *apiIssue) error { i.Draft = false; return nil })
	case "issue":
		if len(args) != 3 && len(args) != 4 {
			return errors.New("usage: sf-gitserver issue <title> <body> [label,label]")
		}
		var labels []string
		if len(args) == 4 {
			labels = split(args[3])
		}
		return issue(args[1], args[2], labels)
	case "issue-edit":
		if len(args) != 3 {
			return errors.New("usage: sf-gitserver issue-edit <issue> <body>")
		}
		n, err := number(args[1])
		if err != nil {
			return err
		}
		return withState(root, true, func(st *apiState) error {
			for k := range st.Issues {
				if !st.Issues[k].Pull && st.Issues[k].Number == n {
					st.Issues[k].Body = args[2]
					return nil
				}
			}
			return fmt.Errorf("no issue #%d", n)
		})
	case "issue-token":
		if len(args) != 2 && len(args) != 3 {
			return errors.New("usage: sf-gitserver issue-token <title> [label,label]")
		}
		var labels []string
		if len(args) == 3 {
			labels = split(args[2])
		}
		return issue(args[1], "Set up the deploy.\n\nThe deploy token is "+fakeToken()+" for now.", labels)
	}
	return errUsage
}
