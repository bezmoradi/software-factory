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

const (
	PluginUpToDate = "up-to-date"
	PluginUpdated  = "updated"
	PluginKept     = "kept"
	PluginMissing  = "not-installed"
	PluginChecking = "checking"
	PluginCheckOff = "off"
)

type TrackedPlugin struct {
	ID          string
	Name        string
	Source      string
	Branch      string
	Version     string
	Commit      string
	Refused     bool
	RefusedWhy  string
	InstalledAt time.Time
	CheckedAt   time.Time
	CheckResult string
	CheckHead   string
	CheckFrom   string
	Problem     string
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
	Plugins    []TrackedPlugin
	Advisories AdvisoryView
}

type AgentReportPage interface {
	AgentReport() AgentReport
}
