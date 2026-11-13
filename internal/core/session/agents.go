package session

import "slices"

type Method string

const (
	MethodPlugin Method = "plugin"
	MethodACP    Method = "acp"
	MethodCodex  Method = "codex"
)

type AgentSpec struct {
	ID          string
	Name        string
	Method      Method
	Command     []string
	AskingModes []string
	Pass        []string
	Enabled     bool
	Off         string
	Startup     string
	Status      *SignInStatus
}

type AgentView struct {
	ID      string
	Name    string
	Method  Method
	Enabled bool
	Off     string
}

func Agents() []AgentSpec {
	return []AgentSpec{
		{ID: "claude", Name: "Claude Code", Method: MethodPlugin, Command: []string{"claude", "--bg"}, Pass: []string{"CLAUDE_CONFIG_DIR", "DISABLE_AUTOUPDATER"}, Enabled: true,
			Startup: "claude", Status: &SignInStatus{Args: []string{"auth", "status"}, Kind: "claude", Step: "while no agent is signed in, press Sign in under Finish setting up on the Welcome page; otherwise run docker compose exec -it agent claude, then /login"}},
		{ID: "codex", Name: "Codex CLI", Method: MethodCodex, Command: []string{"codex", "app-server"}, Pass: []string{"CODEX_HOME"}, Enabled: true,
			Startup: "codex", Status: &SignInStatus{Args: []string{"login", "status"}, Kind: "codex", Step: "docker compose exec -it agent codex login --device-auth"}},
	}
}

func DevelopmentAgent(command string) AgentSpec {
	return AgentSpec{ID: "dev-acp", Name: "Development ACP agent", Method: MethodACP, Command: []string{command}, Enabled: true,
		Startup: "claude", Status: &SignInStatus{Args: []string{"auth", "status"}, Kind: "claude",
			Step: "the development fake answers with what $HOME/.sf-fake-signin says in the agent container: docker compose exec agent sh -c 'echo subscription > $HOME/.sf-fake-signin'"}}
}

func viewOf(a AgentSpec) AgentView {
	return AgentView{ID: a.ID, Name: a.Name, Method: a.Method, Enabled: a.Enabled, Off: a.Off}
}

func findAgent(list []AgentSpec, id string) (AgentSpec, bool) {
	i := slices.IndexFunc(list, func(a AgentSpec) bool { return a.ID == id })
	if i < 0 {
		return AgentSpec{}, false
	}
	return list[i], true
}
