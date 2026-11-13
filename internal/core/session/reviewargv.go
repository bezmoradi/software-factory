package session

import "strings"

var (
	ReviewTools   = []string{"Read", "Grep", "Glob", "Agent", "ToolSearch"}
	ReviewAllowed = []string{"Agent", "ToolSearch"}
)

const reviewSettings = `{"disableAllHooks":true,"permissions":{"blockReadsOutsideWorkingDirectories":true,"deny":["Read(~/**)"]}}`

func ReviewArgv(command, plugin, agent, prompt string) []string {
	return []string{
		command, "-p",
		"--setting-sources", "user",
		"--plugin-dir=" + plugin,
		"--agent", agent,
		"--permission-mode", "dontAsk",
		"--permission-prompts", "none",
		"--tools=" + strings.Join(ReviewTools, ","),
		"--allowedTools=" + strings.Join(ReviewAllowed, ","),
		"--settings=" + reviewSettings,
		"--output-format", "json",
		"--no-session-persistence",
		prompt,
	}
}
