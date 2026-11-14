package state

import (
	"encoding/json"
	"errors"
	"regexp"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

var fullCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

func importedRepos(d string) ([]repoJSON, error) {
	data, err := read(d, reposFile)
	if err != nil || data == nil {
		return nil, err
	}
	var f reposJSON
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, errors.New("repos.json can't be read")
	}
	return f.Repos, nil
}

func checkImportedRepos(d string) error {
	list, err := importedRepos(d)
	if err != nil {
		return err
	}
	for _, r := range list {
		if !repoName.MatchString(r.Name) {
			return errors.New("a project's name isn't a folder name the engine gives a project")
		}
		if u, err := session.NormalizeURL(r.URL); err != nil || u != r.URL {
			return errors.New("a project's url isn't a plain https address as Add saves it")
		}
		if r.Branch != "" && !push.ValidBranch(r.Branch) {
			return errors.New("a project's branch isn't a branch name the engine accepts")
		}
		if r.Commit != "" && !fullCommit.MatchString(r.Commit) {
			return errors.New("a project's commit isn't a full commit hash")
		}
	}
	return nil
}

func checkImportedSessions(d, workspace string) error {
	data, err := read(d, sessionsFile)
	if err != nil || data == nil {
		return err
	}
	var f sessionsJSON
	if err := json.Unmarshal(data, &f); err != nil {
		return errors.New("sessions.json can't be read")
	}
	if len(f.Sessions) == 0 {
		return nil
	}
	if workspace == "" {
		return errors.New("this engine has no workspace, so a session's repoPath can't be one of its projects' folders")
	}
	repos, err := importedRepos(d)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for _, r := range repos {
		names[r.Name] = true
	}
	for _, r := range f.Sessions {
		if !names[r.Repo] {
			return errors.New("a session's repo isn't a project in the same file")
		}
		if r.RepoPath != session.RepoFolder(workspace, r.Repo) && r.RepoPath != session.OldRepoFolder(workspace, r.Repo) {
			return errors.New("a session's repoPath isn't its project's folder in this engine's workspace")
		}
	}
	return nil
}
