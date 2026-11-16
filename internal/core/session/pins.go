package session

import "time"

type PinProvenance struct {
	Agent      string
	Name       string
	Package    string
	Version    string
	State      string
	Repository string
	Workflow   string
	Ref        string
	Commit     string
	LoggedAt   time.Time
	Note       string
}

type PinnedPlugin struct {
	ID      string
	Name    string
	Version string
	Commit  string
}

type Advisory struct {
	Label   string
	ID      string
	Summary string
	Aliases []string
	Link    string
}

type AdvisoryView struct {
	Checked time.Time
	Off     bool
	Problem string
	Found   []Advisory
	More    int
	Sent    []string
}

type AgentReport struct {
	Home       HomeReport
	Pins       []PinProvenance
	Plugins    []PinnedPlugin
	Advisories AdvisoryView
}

type AgentReportPage interface {
	AgentReport() AgentReport
}
