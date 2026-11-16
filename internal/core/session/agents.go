package session

import "slices"

type Method string

const (
	MethodPlugin Method = "plugin"
	MethodACP    Method = "acp"
	MethodCodex  Method = "codex"
)

const (
	DevAgentID     = "dev-acp"
	defaultProject = "software-factory"
	signInLater    = "sign in in the session's sign-in panel on the page when the session starts"
)

type AgentSpec struct {
	ID          string
	Name        string
	Method      Method
	Command     []string
	AskingModes []string
	Pass        []string
	Enabled     bool
	Knackpack   bool
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
		{ID: "claude", Name: "Claude Code", Method: MethodPlugin, Command: []string{"claude", "--bg"}, Pass: []string{"CLAUDE_CONFIG_DIR", "DISABLE_AUTOUPDATER"}, Enabled: true, Knackpack: true,
			Startup: "claude", Status: &SignInStatus{Args: []string{"auth", "status"}, Kind: "claude", Step: signInLater}},
		{ID: "codex", Name: "Codex CLI", Method: MethodCodex, Command: []string{"codex", "app-server"}, Pass: []string{"CODEX_HOME"}, Enabled: true,
			Startup: "codex", Status: &SignInStatus{Args: []string{"login", "status"}, Kind: "codex", Step: signInLater}},
	}
}

func DevelopmentAgent(command string, knackpack bool) AgentSpec {
	return AgentSpec{ID: DevAgentID, Name: "Development ACP agent", Method: MethodACP, Command: []string{command}, Enabled: true, Knackpack: knackpack,
		Startup: "claude", Status: &SignInStatus{Args: []string{"auth", "status"}, Kind: "claude", Step: signInLater}}
}

func BoxContainer(box string) string {
	project := defaultProject
	if p := composeProject.Load(); p != nil {
		project = *p
	}
	return project + "-box-" + box
}

func BoxExec(box string) string {
	return "docker exec -it " + BoxContainer(box)
}

func (a AgentSpec) codeSignIn() bool {
	return a.Status != nil && a.Status.Kind == "claude" && len(a.Command) > 0
}

func (a AgentSpec) StepIn(box string) string {
	if !ValidBox(box) {
		if a.Status != nil && a.Status.Step != "" {
			return a.Status.Step
		}
		return signInLater
	}
	switch {
	case a.ID == DevAgentID:
		return "press Sign in in the session's sign-in panel; the development fake answers with what $HOME/.sf-fake-signin says in the session's container: docker exec " + BoxContainer(box) + " sh -c 'echo subscription > $HOME/.sf-fake-signin'"
	case a.Status != nil && a.Status.Kind == "codex":
		return BoxExec(box) + " codex login --device-auth"
	case a.codeSignIn():
		return "press Sign in in the session's sign-in panel, or run " + BoxExec(box) + " claude, then /login"
	}
	return "its own sign-in in the session's container (" + BoxContainer(box) + ")"
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
