package session

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
)

const GitRiskWords = "The agent can push to any repository with any token it holds, skipping reviews and checks."

func handedWords(name string) string {
	return "The agent can copy " + name + "'s secret, and a copy outlives the grant, so rotate it afterwards."
}

type GrantChoice struct {
	Value string
	Label string
}

var GrantScopes = []GrantChoice{{Value: GrantSession, Label: "This session"}, {Value: GrantProject, Label: "This project"}}

var GrantExpiries = []GrantChoice{{Value: "hour", Label: "1 hour"}, {Value: "day", Label: "1 day"}, {Value: "week", Label: "1 week"}, {Value: "never", Label: "Until taken back"}}

var deliveryWords = map[string]string{DeliveryHanded: "handed over", DeliverySigned: "signed for by Software Factory", DeliveryCopy: "as short-lived copies"}

var grantForWords = map[string]string{"hour": "for 1 hour", "day": "for 1 day", "week": "for 1 week", "never": "until taken back"}

type AccessRisk struct {
	Scope   string
	Expires string
	Level   string
	Label   string
	Says    []string
	Accept  string
	Git     []string
}

var riskLabels = map[string]string{RiskLow: "Low risk", RiskMedium: "Medium risk", RiskHigh: "High risk"}

type accessTarget struct {
	host     string
	port     int
	cred     string
	delivery string
	user     string
	role     string
	bound    []string
	tags     []string
	gits     []string
	gitAddr  string
	lans     []string
}

func (a accessTarget) what() string {
	switch {
	case a.cred != "":
		return a.cred
	case a.port > 0:
		return PortTarget(a.host, a.port)
	}
	return a.host
}

func (g *Grants) hostTarget(host string) accessTarget {
	return accessTarget{host: host, gits: g.gitsOf([]string{host})}
}

func (g *Grants) credentialTarget(c Credential) accessTarget {
	return accessTarget{cred: c.Name, delivery: c.Delivery(), user: c.Secret.User, role: c.Secret.Role, bound: c.Bound, tags: c.Tags, gits: g.gitsOf(c.Bound), lans: lanEntries(c.Bound)}
}

func grantChoice(list []GrantChoice, v string) bool {
	for _, c := range list {
		if c.Value == v {
			return true
		}
	}
	return false
}

func choiceLabel(list []GrantChoice, v string) string {
	for _, c := range list {
		if c.Value == v {
			return c.Label
		}
	}
	return v
}

func scopeWords(scope, project string) string {
	if scope == GrantProject {
		return "every session of " + clip(project, 100)
	}
	return "this session"
}

func hostList(hosts []string) string {
	if len(hosts) <= 1 {
		return strings.Join(hosts, "")
	}
	return strings.Join(hosts[:len(hosts)-1], ", ") + " and " + hosts[len(hosts)-1]
}

func riskFor(a accessTarget, scope, expires, project string) AccessRisk {
	r := AccessRisk{Scope: scope, Expires: expires, Level: RiskLow, Git: slices.Clone(a.gits)}
	who := "The agent"
	if scope == GrantProject {
		who = "The agents of every " + clip(project, 100) + " session, later ones too,"
	}
	hosts := []string{a.what()}
	var accept []string
	switch {
	case a.delivery == DeliverySigned:
		hosts = a.bound
		r.Level = RiskMedium
		r.Says = append(r.Says, who+" can sign in over SSH as "+a.user+" to "+hostList(hosts)+"; the key never leaves Software Factory, so taking the grant back stops it at once.")
	case a.delivery == DeliveryCopy:
		hosts = a.bound
		r.Level = RiskHigh
		rights := a.cred + "'s keys"
		if a.role != "" {
			rights = "the role " + a.role
		}
		r.Says = append(r.Says, who+" can use short-lived copies of "+a.cred+"'s keys and open "+reachWords(hosts)+".")
		r.Says = append(r.Says, "A copy the agent took keeps the full rights of "+rights+" until it expires, up to an hour, even after you take the grant back.")
		accept = append(accept, "a copy the agent took keeps the full rights of "+rights+" until it expires")
	case a.cred != "":
		hosts = a.bound
		r.Level = RiskHigh
		r.Says = append(r.Says, who+" can read "+a.cred+"'s secret and open "+reachWords(hosts)+".")
		r.Says = append(r.Says, handedWords(a.cred))
		accept = append(accept, "the agent can copy "+a.cred+"'s secret")
	case a.port > 0 && len(a.lans) == 0:
		r.Level = RiskMedium
		r.Says = append(r.Says, who+" can open TCP connections to "+a.what()+".")
	case a.port == 0:
		r.Says = append(r.Says, who+" can open HTTPS connections to "+a.host+".")
		if bare, wild := strings.CutPrefix(a.host, "."); wild {
			r.Level = RiskMedium
			r.Says = append(r.Says, a.host+" covers every name under "+bare+", including ones other people may run.")
		}
	}
	for _, e := range a.lans {
		h, n, _ := SplitPortTarget(e)
		r.Level = RiskHigh
		r.Says = append(r.Says, h+" is on your network: "+lowerFirst(who)+" can reach whatever answers on port "+strconv.Itoa(n)+".")
		accept = append(accept, "the agent can reach "+e)
	}
	if len(a.gits) > 0 {
		r.Level = RiskHigh
		if a.gitAddr != "" {
			r.Says = append(r.Says, a.host+" is an address of "+a.gitAddr+", a git host.")
		}
		r.Says = append(r.Says, GitRiskWords)
		accept = append(accept, "the agent can push without reviews or checks")
	}
	for _, t := range a.tags {
		switch t {
		case TagProduction:
			r.Says = append(r.Says, a.cred+" is tagged production: what the agent does with it reaches production.")
			if r.Level != RiskHigh {
				r.Level = RiskHigh
				accept = append(accept, "what the agent does with "+a.cred+" reaches production")
			}
		case TagWrite:
			r.Says = append(r.Says, a.cred+" is tagged write: the agent can change things with it.")
		}
	}
	uploads := slices.ContainsFunc(hosts, func(h string) bool { return UploadHost(targetHost(h)) })
	switch {
	case uploads:
		if r.Level == RiskLow {
			r.Level = RiskMedium
		}
		for _, h := range hosts {
			if UploadHost(targetHost(h)) {
				r.Says = append(r.Says, h+" accepts uploads, so anything the agent reads, your code included, can be sent there.")
			}
		}
	case a.cred == "" && len(a.gits) == 0 && len(a.lans) == 0 && len(r.Says) > 0:
		r.Says[0] += " Anything sent there leaves this computer."
	}
	if scope == GrantProject && expires == "never" {
		r.Level = RiskHigh
		r.Says = append(r.Says, "It stays open for every "+clip(project, 100)+" session until you take it back.")
		accept = append(accept, "every session of "+clip(project, 100)+" can reach "+hostList(hosts)+" until I take it back")
	}
	if len(accept) > 0 {
		r.Accept = "I accept that " + strings.Join(accept, ", and that ")
	}
	r.Label = riskLabels[r.Level]
	return r
}

func unshownGits(gits, shown []string) []string {
	return slices.DeleteFunc(slices.Clone(gits), func(h string) bool { return slices.Contains(shown, h) })
}

func risksFor(a accessTarget, project string) []AccessRisk {
	out := make([]AccessRisk, 0, len(GrantScopes)*len(GrantExpiries))
	for _, s := range GrantScopes {
		for _, e := range GrantExpiries {
			out = append(out, riskFor(a, s.Value, e.Value, project))
		}
	}
	return out
}

func untilWords(t time.Time) string {
	if t.IsZero() {
		return "until taken back"
	}
	return "until " + t.UTC().Format(time.RFC3339)
}

func lastsWords(gr Grant) string {
	for value, ttl := range expiryChoices {
		if ttl > 0 && gr.Expires.Sub(gr.Made) == ttl {
			return grantForWords[value]
		}
	}
	return untilWords(gr.Expires)
}

func grantWords(gr Grant, result string) string {
	what := gr.what()
	if gr.Credential != "" {
		what = "the credential " + gr.Name + ", " + deliveryWords[gr.Delivery] + ", with " + hostList(gr.Bound) + ","
	}
	what += " for " + scopeWords(gr.Scope, gr.Project)
	switch result {
	case ResultGranted:
		what += ", " + lastsWords(gr)
		switch {
		case gr.AskedBy == AskedByPreset:
			what += ", from the preset " + gr.Preset + " when the session started"
		case gr.Preset != "":
			what += ", from the preset " + gr.Preset
		case gr.AskedBy == AskedByAgent && gr.Reason == "":
			what += ", after the agent tried to reach it"
		case gr.AskedBy == AskedByAgent:
			what += ", asked by the agent"
		}
		if gr.Reason != "" {
			what += ", because: " + gr.Reason
		}
	case ResultExpired:
		what += ", which expired"
		switch {
		case gr.Delivery == DeliverySigned:
			what += "; Software Factory stopped signing with it"
		case gr.Credential != "":
			what += "; its handed-over files and variables are " + HandedGoneWords
		}
	case ResultGrantEnded:
		what += ", which ended when the session stopped"
	}
	return what
}

func refusedWords(what string, request bool) string {
	if request {
		return what + ", which the agent asked for"
	}
	return what + ", which the agent tried to reach"
}
