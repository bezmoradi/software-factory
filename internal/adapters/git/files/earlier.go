package files

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

var (
	earlierPath  = regexp.MustCompile(`^earlier/[a-z0-9-]{1,100}\.md$`)
	approvedPath = regexp.MustCompile(`^approved/[a-z0-9-]{1,40}\.md$`)
)

func extraLimit(name string) (string, int) {
	switch {
	case earlierPath.MatchString(name):
		return "earlier", session.MaxEarlierFile
	case approvedPath.MatchString(name):
		return session.ApprovedDir, session.MaxApprovedFile
	case name == session.ClaimsFile:
		return "", session.MaxClaimsFile
	case name == session.ChangeFactsFile:
		return "", session.MaxChangeFacts
	case name == session.PlanReviewFile:
		return "", session.MaxPlanReviewFile
	}
	return "", -1
}

func writeExtra(dir string, extra []session.SnapshotFile) ([]string, error) {
	var folders []string
	for _, f := range extra {
		if rel, ok := rulesPath(f.Name); ok {
			made, err := ensureFolder(dir, session.RulesDir)
			if err != nil {
				return nil, err
			}
			folders = append(folders, made...)
			sub, err := writeRule(dir, rel, f.Text)
			if err != nil {
				return nil, err
			}
			folders = append(folders, sub...)
			continue
		}
		folder, limit := extraLimit(f.Name)
		if limit < 0 || len(f.Text) > limit {
			return nil, errors.New("a file beside the snapshot has a name or size the engine doesn't write")
		}
		if folder != "" {
			made, err := ensureFolder(dir, folder)
			if err != nil {
				return nil, err
			}
			folders = append(folders, made...)
		}
		if err := writeNew(filepath.Join(dir, filepath.FromSlash(f.Name)), f.Text); err != nil {
			return nil, err
		}
	}
	return folders, nil
}

func ensureFolder(dir, folder string) ([]string, error) {
	sub := filepath.Join(dir, folder)
	_, err := os.Lstat(sub)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.Mkdir(sub, 0o755); err != nil {
			return nil, err
		}
		return []string{sub}, nil
	case err != nil:
		return nil, err
	}
	return nil, nil
}
