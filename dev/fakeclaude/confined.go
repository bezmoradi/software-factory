package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

const homeDeny = "Read(~/**)"

var (
	reviewerTools   = []string{"Agent", "Glob", "Grep", "Read", "ToolSearch"}
	reviewerAllowed = []string{"Agent", "ToolSearch"}
	reviewerFlags   = map[string]string{"--permission-mode": "dontAsk", "--setting-sources": "user", "--permission-prompts": "none", "--output-format": "stream-json"}
	reviewerArgv    = map[string]bool{
		"-p": false, "--verbose": false, "--no-session-persistence": false,
		"--setting-sources": true, "--agent": true, "--permission-mode": true, "--permission-prompts": true, "--output-format": true,
		"--plugin-dir": true, "--tools": true, "--allowedTools": true, "--settings": true, "--model": true, "--effort": true,
	}
)

func onlyReviewerFlags(args []string) error {
	if len(args) == 0 {
		return errors.New("no prompt")
	}
	for i := 0; i < len(args)-1; i++ {
		name, _, inline := strings.Cut(args[i], "=")
		valued, known := reviewerArgv[name]
		switch {
		case !strings.HasPrefix(args[i], "-"):
			return fmt.Errorf("argument %q isn't a flag the engine's reviewer runs pass", args[i])
		case !known:
			return fmt.Errorf("flag %s isn't one the engine's reviewer runs pass", name)
		case inline && !valued:
			return fmt.Errorf("flag %s takes no value", name)
		case valued && !inline:
			i++
		}
	}
	if strings.HasPrefix(args[len(args)-1], "-") {
		return errors.New("the last argument is a flag, not the prompt")
	}
	return nil
}

type reviewSettings struct {
	DisableAllHooks bool `json:"disableAllHooks"`
	Permissions     struct {
		BlockReadsOutside bool     `json:"blockReadsOutsideWorkingDirectories"`
		Deny              []string `json:"deny"`
	} `json:"permissions"`
}

func confined(args []string) error {
	if err := onlyReviewerFlags(args); err != nil {
		return err
	}
	raw, ok := value(args, "--settings")
	if !ok {
		return errors.New("--settings is missing")
	}
	var s reviewSettings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return errors.New("--settings isn't JSON")
	}
	switch {
	case !s.DisableAllHooks:
		return errors.New("--settings doesn't turn hooks off")
	case !s.Permissions.BlockReadsOutside:
		return errors.New("--settings doesn't block reads outside the working folder")
	case !slices.Contains(s.Permissions.Deny, homeDeny):
		return fmt.Errorf("--settings doesn't deny %s", homeDeny)
	}
	for flag, want := range reviewerFlags {
		v, err := once(args, flag)
		if err != nil {
			return err
		}
		if v != want {
			return fmt.Errorf("%s is %q, not %s as the engine's reviewer runs", flag, v, want)
		}
	}
	allowed, err := toolSet(args, "--allowedTools")
	if err != nil {
		return err
	}
	for _, tool := range allowed {
		if slices.Contains([]string{"Read", "Grep", "Glob"}, tool) || strings.HasPrefix(tool, "Read(") {
			return fmt.Errorf("--allowedTools allows %s, which would allow reads outside the working folder", tool)
		}
	}
	if !slices.Equal(allowed, reviewerAllowed) {
		return fmt.Errorf("--allowedTools is %s, not %s as the engine's reviewer runs", strings.Join(allowed, ","), strings.Join(reviewerAllowed, ","))
	}
	tools, err := toolSet(args, "--tools")
	if err != nil {
		return err
	}
	if !slices.Equal(tools, reviewerTools) {
		return fmt.Errorf("--tools is %s, not %s as the engine's reviewer runs", strings.Join(tools, ","), strings.Join(reviewerTools, ","))
	}
	return nil
}

func once(args []string, flag string) (string, error) {
	var found []string
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			found = append(found, v)
		} else if a == flag && i+1 < len(args) {
			found = append(found, args[i+1])
		}
	}
	if len(found) != 1 {
		return "", fmt.Errorf("%s is given %d times, not once", flag, len(found))
	}
	return found[0], nil
}

func toolSet(args []string, flag string) ([]string, error) {
	raw, err := once(args, flag)
	if err != nil {
		return nil, err
	}
	var out []string
	for tool := range strings.SplitSeq(raw, ",") {
		if t := strings.TrimSpace(tool); t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out, nil
}
