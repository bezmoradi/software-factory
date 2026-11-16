package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func mergedPull(root, title, body string) error {
	bare := filepath.Join(root, "demo.git")
	work, err := os.MkdirTemp("", "gitserver-merged")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	if _, err := git(work, "clone", "-q", bare, "."); err != nil {
		return err
	}
	base, err := git(work, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	branch := "bench-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := git(work, "checkout", "-q", "-b", branch); err != nil {
		return err
	}
	name := branch + ".md"
	if err := os.WriteFile(filepath.Join(work, name), []byte("# "+title+"\n\n"+body+"\n"), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"add", name}, {"commit", "-q", "-m", title}} {
		if _, err := git(work, args...); err != nil {
			return err
		}
	}
	head, err := git(work, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	steps := [][]string{{"push", "-q", "origin", "HEAD:refs/heads/" + branch}, {"checkout", "-q", "main"}, {"merge", "-q", "--no-ff", "-m", "Merge " + branch, branch}}
	for _, args := range steps {
		if _, err := git(work, args...); err != nil {
			return err
		}
	}
	merge, err := git(work, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if _, err := git(work, "push", "-q", "origin", "HEAD:main"); err != nil {
		return err
	}
	return withState(root, true, func(st *apiState) error {
		ref := "refs/sf-bench/" + strconv.Itoa(st.Next) + "/base"
		if _, err := git(work, "push", "-q", "origin", base+":"+ref); err != nil {
			return err
		}
		now := time.Now().UTC()
		made := apiIssue{Number: st.Next, Title: title, Body: body, User: login(), State: "closed", CreatedAt: now, Pull: true,
			Head: branch, HeadSHA: head, Base: "main", BaseSHA: base, MergeSHA: merge, Merged: true, MergedAt: now}
		st.Next++
		st.Issues = append(st.Issues, made)
		fmt.Println(made.Number)
		return nil
	})
}
