package session

import "strings"

var (
	ReviewTools   = []string{"Read", "Grep", "Glob", "Agent", "ToolSearch"}
	ReviewAllowed = []string{"Agent", "ToolSearch"}
)

const reviewSettings = `{"disableAllHooks":true,"env":{"CLAUDE_CODE_SUBAGENT_MODEL":"inherit","CLAUDE_CODE_SUBAGENT_MODEL_FORCE":"1"},"permissions":{"blockReadsOutsideWorkingDirectories":true,"deny":["Read(~/**)","Agent(Explore)"]}}`

func ReviewArgv(command, plugin, agent, prompt string) []string {
	return ReviewArgvWith(command, plugin, agent, prompt, ModelChoice{})
}

func ReviewArgvWith(command, plugin, agent, prompt string, m ModelChoice) []string {
	argv := []string{
		command, "-p",
		"--setting-sources", "user",
		"--plugin-dir=" + plugin,
		"--agent", agent,
		"--permission-mode", "dontAsk",
		"--permission-prompts", "none",
		"--tools=" + strings.Join(ReviewTools, ","),
		"--allowedTools=" + strings.Join(ReviewAllowed, ","),
		"--settings=" + reviewSettings,
		"--output-format", "stream-json",
		"--verbose",
		"--no-session-persistence",
	}
	if m.Model != "" {
		argv = append(argv, "--model="+m.Model)
	}
	if m.Effort != "" {
		argv = append(argv, "--effort="+m.Effort)
	}
	return append(argv, prompt)
}
