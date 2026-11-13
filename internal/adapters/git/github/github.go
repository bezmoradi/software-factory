package github

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	apiVersion   = "2022-11-28"
	maxAnswer    = 4 << 20
	maxLogBytes  = 32 << 20
	maxLogLines  = 200
	maxLineBytes = 4096
	maxLoggedRun = 5
	userAgent    = "software-factory"
)

var notGitHub = map[string]bool{
	"gitlab.com": true, "bitbucket.org": true, "codeberg.org": true, "gitea.com": true, "dev.azure.com": true,
	"ssh.dev.azure.com": true, "git.sr.ht": true, "sourceforge.net": true, "gitee.com": true, "huggingface.co": true,
}

type API struct {
	Client *http.Client
}

var _ session.ForgeAPI = API{}

func bare(host string) string {
	return strings.TrimPrefix(host, "https://")
}

func (a API) Supports(host string) bool {
	h := bare(host)
	name, _, _ := strings.Cut(h, ":")
	return strings.HasPrefix(host, "https://") && h != "" && !notGitHub[name] && !strings.HasPrefix(name, "gitlab.") && !strings.HasPrefix(name, "bitbucket.")
}

func Base(host string) string {
	h := bare(host)
	switch {
	case h == "github.com":
		return "https://api.github.com"
	case strings.HasSuffix(h, ".ghe.com") && !strings.HasPrefix(h, "api."):
		return "https://api." + h
	}
	return "https://" + h + "/api/v3"
}

func (a API) client() *http.Client {
	c := http.DefaultClient
	if a.Client != nil {
		c = a.Client
	}
	out := *c
	out.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &out
}

func repoPath(r session.ForgeRepo) string {
	return "/repos/" + url.PathEscape(r.Owner) + "/" + url.PathEscape(r.Name)
}

func describe(status int, data []byte) string {
	var e struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Field   string `json:"field"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(data, &e)
	msg := strings.TrimSpace(e.Message)
	for _, x := range e.Errors {
		switch {
		case x.Message != "":
			msg += "; " + x.Message
		case x.Code != "":
			msg += "; " + x.Field + " " + x.Code
		}
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return session.CleanLine(msg, 300)
}

func fromGitHub(h http.Header) bool {
	return h.Get("X-GitHub-Request-Id") != "" || h.Get("X-GitHub-Enterprise-Version") != "" || h.Get("X-GitHub-Media-Type") != ""
}

func (a API) do(ctx context.Context, r session.ForgeRepo, method, path string, in any, need string, out any) (int, error) {
	status, _, err := a.send(ctx, r, method, path, in, need, out)
	return status, err
}

func (a API) send(ctx context.Context, r session.ForgeRepo, method, path string, in any, need string, out any) (int, http.Header, error) {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, Base(r.Host)+path, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+r.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) && errors.Is(ue.Err, context.DeadlineExceeded) {
			return 0, nil, context.DeadlineExceeded
		}
		return 0, nil, errors.New("the request to " + bare(r.Host) + "'s API failed")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return resp.StatusCode, resp.Header, errors.New("the answer from " + bare(r.Host) + "'s API couldn't be read")
	}
	if len(data) > maxAnswer {
		return resp.StatusCode, resp.Header, errors.New("the answer from " + bare(r.Host) + "'s API is larger than 4 MiB")
	}
	if !fromGitHub(resp.Header) {
		return resp.StatusCode, resp.Header, &session.ForgeError{Status: resp.StatusCode, Message: bare(r.Host) + " doesn't answer like GitHub's API at " + Base(r.Host) + ", so this tool isn't supported for it"}
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return resp.StatusCode, resp.Header, &session.ForgeError{Status: resp.StatusCode, Message: "GitHub refused the saved token (401: " + describe(resp.StatusCode, data) + "); it may have expired or been revoked, so ask the person to save a new one"}
	case resp.StatusCode == http.StatusForbidden && (resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""):
		return resp.StatusCode, resp.Header, &session.ForgeError{Status: resp.StatusCode, Message: "GitHub's rate limit for the token was reached; try again later"}
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
		return resp.StatusCode, resp.Header, &session.ForgeError{Status: resp.StatusCode, Need: need, Message: "GitHub answered " + strconv.Itoa(resp.StatusCode) + " (" + describe(resp.StatusCode, data) + ") for " + r.Slug()}
	case resp.StatusCode >= 300:
		return resp.StatusCode, resp.Header, &session.ForgeError{Status: resp.StatusCode, Message: "GitHub answered " + strconv.Itoa(resp.StatusCode) + ": " + describe(resp.StatusCode, data)}
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		return resp.StatusCode, resp.Header, errors.New("GitHub's answer isn't the JSON this engine expects")
	}
	return resp.StatusCode, resp.Header, nil
}

func (a API) Repository(ctx context.Context, r session.ForgeRepo) error {
	var repo struct {
		HasIssues *bool `json:"has_issues"`
	}
	if _, err := a.do(ctx, r, http.MethodGet, repoPath(r), nil, session.NeedMetadata, &repo); err != nil {
		return err
	}
	if repo.HasIssues != nil && !*repo.HasIssues {
		return &session.ForgeError{Status: http.StatusGone, Message: "issues are switched off for " + r.Slug() + " in its settings on GitHub"}
	}
	return nil
}

func (a API) Branch(ctx context.Context, r session.ForgeRepo, branch string) (string, error) {
	var b struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	status, err := a.do(ctx, r, http.MethodGet, repoPath(r)+"/branches/"+refPath(branch), nil, session.NeedContents, &b)
	if status == http.StatusNotFound {
		if rerr := a.reachable(ctx, r); rerr != nil {
			return "", rerr
		}
		return "", session.ErrNoBranch
	}
	if err != nil {
		return "", err
	}
	if len(b.Commit.SHA) != 40 {
		return "", errors.New("GitHub's answer for the branch has no commit")
	}
	return b.Commit.SHA, nil
}

func (a API) reachable(ctx context.Context, r session.ForgeRepo) error {
	_, err := a.do(ctx, r, http.MethodGet, repoPath(r), nil, session.NeedMetadata+" and "+session.NeedContents, nil)
	return err
}

func refPath(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

type pullJSON struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
}

func (a API) OpenPull(ctx context.Context, r session.ForgeRepo, head string) (session.ForgeLink, bool, error) {
	var list []pullJSON
	q := url.Values{"state": {"open"}, "head": {r.Owner + ":" + head}, "per_page": {"10"}}
	if _, err := a.do(ctx, r, http.MethodGet, repoPath(r)+"/pulls?"+q.Encode(), nil, session.NeedPulls, &list); err != nil {
		return session.ForgeLink{}, false, err
	}
	for _, p := range list {
		if p.Number > 0 {
			return session.ForgeLink{Number: p.Number, URL: session.CleanLine(p.HTMLURL, 300)}, true, nil
		}
	}
	return session.ForgeLink{}, false, nil
}

func linkOf(p pullJSON) (session.ForgeLink, error) {
	if p.Number <= 0 {
		return session.ForgeLink{}, errors.New("GitHub's answer has no number")
	}
	return session.ForgeLink{Number: p.Number, URL: session.CleanLine(p.HTMLURL, 300)}, nil
}

func (a API) CreateIssue(ctx context.Context, r session.ForgeRepo, title, body string) (session.ForgeLink, error) {
	var out pullJSON
	if _, err := a.do(ctx, r, http.MethodPost, repoPath(r)+"/issues", map[string]string{"title": title, "body": body}, session.NeedIssues, &out); err != nil {
		return session.ForgeLink{}, err
	}
	return linkOf(out)
}

func (a API) CreatePull(ctx context.Context, r session.ForgeRepo, title, body, head, base string, draft bool) (session.ForgeLink, bool, error) {
	var out pullJSON
	in := map[string]any{"title": title, "body": body, "head": head, "base": base, "maintainer_can_modify": false}
	if draft {
		in["draft"] = true
	}
	status, err := a.do(ctx, r, http.MethodPost, repoPath(r)+"/pulls", in, session.NeedPulls, &out)
	if draft && status == http.StatusUnprocessableEntity && draftRefused(err) {
		return a.readyPull(ctx, r, title, body, head, base)
	}
	if err != nil {
		return session.ForgeLink{}, false, err
	}
	link, err := linkOf(out)
	return link, draft && out.Draft, err
}

type runJSON struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	App        *struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"app"`
}

func (a API) Checks(ctx context.Context, r session.ForgeRepo, commit string) (session.CheckReport, error) {
	var rep session.CheckReport
	var runs struct {
		Total int       `json:"total_count"`
		Runs  []runJSON `json:"check_runs"`
	}
	_, runErr := a.do(ctx, r, http.MethodGet, repoPath(r)+"/commits/"+url.PathEscape(commit)+"/check-runs?per_page=100", nil, session.NeedContents, &runs)
	var status struct {
		State    string `json:"state"`
		Statuses []struct {
			Context     string `json:"context"`
			State       string `json:"state"`
			Description string `json:"description"`
			TargetURL   string `json:"target_url"`
		} `json:"statuses"`
	}
	_, stateErr := a.do(ctx, r, http.MethodGet, repoPath(r)+"/commits/"+url.PathEscape(commit)+"/status?per_page=100", nil, session.NeedStatuses, &status)
	if runErr != nil && stateErr != nil {
		return rep, &session.ForgeError{Message: "check runs: " + why(runErr) + "; commit statuses: " + why(stateErr)}
	}
	if runErr != nil {
		rep.RunsNote = "couldn't be read: " + why(runErr)
	}
	if stateErr != nil {
		rep.StateNote = "couldn't be read: " + why(stateErr)
	}
	rep.MoreRuns = runs.Total > len(runs.Runs)
	logged := 0
	for _, x := range runs.Runs {
		run := session.CheckRun{Name: x.Name, Status: x.Status, Conclusion: x.Conclusion, URL: x.HTMLURL}
		actions := false
		if x.App != nil {
			run.App = x.App.Name
			actions = x.App.Slug == "github-actions"
		}
		if actions && failed(x.Conclusion) && x.ID > 0 {
			if logged >= maxLoggedRun {
				run.LogNote = "not fetched: only the first " + strconv.Itoa(maxLoggedRun) + " failed jobs' logs are included"
			} else {
				logged++
				lines, err := a.jobLog(ctx, r, x.ID)
				if err != nil {
					run.LogNote = "couldn't be read: " + why(err)
				}
				run.Log = lines
			}
		}
		rep.Runs = append(rep.Runs, run)
	}
	rep.State = status.State
	for _, s := range status.Statuses {
		rep.Statuses = append(rep.Statuses, session.CommitStatus{Context: s.Context, State: s.State, Description: s.Description, URL: s.TargetURL})
	}
	return rep, nil
}

func why(err error) string {
	if fe, ok := errors.AsType[*session.ForgeError](err); ok && fe.Need != "" {
		return fe.Message + "; the token needs " + fe.Need
	}
	return err.Error()
}

func failed(conclusion string) bool {
	switch conclusion {
	case "failure", "timed_out", "cancelled", "startup_failure":
		return true
	}
	return false
}

func (a API) jobLog(ctx context.Context, r session.ForgeRepo, job int64) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Base(r.Host)+repoPath(r)+"/actions/jobs/"+strconv.FormatInt(job, 10)+"/logs", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+r.Token)
	resp, err := a.client().Do(req)
	if err != nil {
		return nil, errors.New("the request for the job's log failed")
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusFound, http.StatusTemporaryRedirect, http.StatusSeeOther:
		where, err := resp.Location()
		if err != nil || where.Scheme != "https" || where.User != nil {
			return nil, errors.New("GitHub's answer for the job's log doesn't point to an https address")
		}
		return a.download(ctx, where.String())
	case http.StatusOK:
		return tail(resp.Body)
	case http.StatusForbidden, http.StatusNotFound:
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, &session.ForgeError{Status: resp.StatusCode, Need: session.NeedActions, Message: "GitHub answered " + strconv.Itoa(resp.StatusCode) + " (" + describe(resp.StatusCode, data) + ") for the job's log"}
	case http.StatusGone:
		return nil, errors.New("the job's log has expired or was deleted on GitHub")
	}
	return nil, fmt.Errorf("GitHub answered %d for the job's log", resp.StatusCode)
}

func (a API) download(ctx context.Context, where string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, where, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := a.client().Do(req)
	if err != nil {
		return nil, errors.New("the job's log couldn't be downloaded")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the job's log download answered %d", resp.StatusCode)
	}
	return tail(resp.Body)
}

func tail(body io.Reader) ([]string, error) {
	r := bufio.NewReaderSize(io.LimitReader(body, maxLogBytes), 64<<10)
	ring := make([]string, maxLogLines)
	n := 0
	for {
		line, err := r.ReadSlice('\n')
		text := string(line[:min(len(line), maxLineBytes)])
		for errors.Is(err, bufio.ErrBufferFull) {
			_, err = r.ReadSlice('\n')
		}
		text = strings.TrimRight(text, "\r\n")
		if text != "" || err == nil {
			ring[n%maxLogLines] = text
			n++
		}
		if err != nil {
			out := make([]string, 0, min(n, maxLogLines))
			for i := max(0, n-maxLogLines); i < n; i++ {
				out = append(out, ring[i%maxLogLines])
			}
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, errors.New("the job's log couldn't be read to the end")
		}
	}
}
