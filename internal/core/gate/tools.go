package gate

import "strings"

type ToolKind string

const (
	ShellTool   ToolKind = "shell"
	EditTool    ToolKind = "edit"
	ReadTool    ToolKind = "read"
	WebTool     ToolKind = "web"
	RemoteTool  ToolKind = "remote"
	MCPTool     ToolKind = "mcp"
	SessionTool ToolKind = "session"
	UnknownTool ToolKind = "unknown"
)

var toolKinds = map[string]ToolKind{
	"Bash": ShellTool, "Monitor": ShellTool, "PowerShell": ShellTool,
	"Edit": EditTool, "Write": EditTool, "MultiEdit": EditTool, "NotebookEdit": EditTool,
	"Read": ReadTool, "Glob": ReadTool, "Grep": ReadTool, "LS": ReadTool, "NotebookRead": ReadTool, "LSP": ReadTool,
	"WebFetch": WebTool, "WebSearch": WebTool, "Projects": WebTool,
	"RemoteTrigger": RemoteTool, "PublishPlugin": RemoteTool, "SendToProject": RemoteTool, "SendFile": RemoteTool,
	"SendToProjectContext": RemoteTool, "SendUserFile": RemoteTool, "SendFeedback": RemoteTool, "DesignSync": RemoteTool,
	"SendMessage": RemoteTool,
	"Agent":       SessionTool, "Task": SessionTool, "TodoWrite": SessionTool, "TaskCreate": SessionTool, "TaskGet": SessionTool,
	"TaskList": SessionTool, "TaskUpdate": SessionTool, "TaskStop": SessionTool, "TaskOutput": SessionTool,
	"BashOutput": SessionTool, "KillShell": SessionTool, "AskUserQuestion": SessionTool, "EnterPlanMode": SessionTool,
	"ExitPlanMode": SessionTool, "EnterWorktree": SessionTool, "ExitWorktree": SessionTool, "Skill": SessionTool,
	"SlashCommand": SessionTool, "ToolSearch": SessionTool, "ListMcpResourcesTool": SessionTool,
	"ReadMcpResourceTool": SessionTool, "CronCreate": SessionTool, "CronDelete": SessionTool, "CronList": SessionTool,

	"Artifact": RemoteTool, "ArtifactCheck": RemoteTool, "ArtifactComments": RemoteTool, "ArtifactData": RemoteTool,
	"AppifactRepl": RemoteTool, "ClaudeDesign": RemoteTool, "ShareOnboardingGuide": RemoteTool,
	"PushNotification": RemoteTool, "memory_write": RemoteTool, "request_computer": RemoteTool,
	"memory_read": WebTool, "memory_list": WebTool, "FetchInboxMessage": WebTool,
	"EndConversation": SessionTool, "GetTask": SessionTool, "ListAgents": SessionTool, "ListConnectors": SessionTool,
	"ListPlugins": SessionTool, "ListSkills": SessionTool, "OfferChromeSetup": SessionTool, "propose_skills": SessionTool,
	"ProposeGoal": SessionTool, "ReadMcpResourceDirTool": SessionTool, "ReadWidgetContext": SessionTool,
	"ReportFindings": SessionTool, "ScheduleWakeup": SessionTool, "SearchMcpRegistry": SessionTool,
	"SearchPlugins": SessionTool, "SearchSkills": SessionTool, "ShowOnboardingRolePicker": SessionTool,
	"SubagentHandback": SessionTool, "SuggestConnectors": SessionTool, "SuggestPluginInstall": SessionTool,
	"SuggestSkills": SessionTool, "WaitForMcpServers": SessionTool, "Workflow": SessionTool, "WrapUp": SessionTool,
}

func Kind(tool string, input map[string]any) ToolKind {
	if strings.HasPrefix(tool, "mcp__") {
		return MCPTool
	}
	switch tool {
	case "Projects":
		if input["method"] == "project_write" {
			return RemoteTool
		}
	case "Agent", "Task":
		if input["isolation"] == "remote" {
			return RemoteTool
		}
	}
	if k, ok := toolKinds[tool]; ok {
		return k
	}
	return UnknownTool
}

func WaitsForPerson(k ToolKind) bool {
	return k == RemoteTool
}
