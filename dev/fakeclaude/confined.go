package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

const homeDeny = "Read(~/**)"

type reviewSettings struct {
	DisableAllHooks bool `json:"disableAllHooks"`
	Permissions     struct {
		BlockReadsOutside bool     `json:"blockReadsOutsideWorkingDirectories"`
		Deny              []string `json:"deny"`
	} `json:"permissions"`
}

func confined(args []string) error {
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
	allowed, _ := value(args, "--allowedTools")
	for tool := range strings.SplitSeq(allowed, ",") {
		if slices.Contains([]string{"Read", "Grep", "Glob"}, strings.TrimSpace(tool)) || strings.HasPrefix(strings.TrimSpace(tool), "Read(") {
			return fmt.Errorf("--allowedTools allows %s, which would allow reads outside the working folder", strings.TrimSpace(tool))
		}
	}
	return nil
}
