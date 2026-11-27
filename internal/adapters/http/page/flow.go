package page

import "github.com/bezmoradi/software-factory/internal/core/session"

type flowLookJSON struct {
	Icon string `json:"icon"`
	Tone string `json:"tone"`
	Word string `json:"word"`
}

type flowNodeJSON struct {
	Name  string       `json:"name"`
	Phase string       `json:"phase"`
	Look  flowLookJSON `json:"look"`
	Aside bool         `json:"aside"`
	Tags  []saidJSON   `json:"tags"`
}

type flowRouteJSON struct {
	Label   string       `json:"label"`
	Look    flowLookJSON `json:"look"`
	Taken   bool         `json:"taken"`
	Decided bool         `json:"decided"`
	Note    string       `json:"note"`
}

type flowZoneJSON struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Note      string `json:"note"`
	Untrusted bool   `json:"untrusted"`
}

type flowWireJSON struct {
	Label string `json:"label"`
	Phase string `json:"phase"`
	Tone  string `json:"tone"`
}

type flowKeyJSON struct {
	Swatch string `json:"swatch"`
	Words  string `json:"words"`
}

type flowJSON struct {
	Commit        string          `json:"commit"`
	Reviews       []flowNodeJSON  `json:"reviews"`
	Runs          []flowNodeJSON  `json:"runs"`
	Gate          flowNodeJSON    `json:"gate"`
	Routes        []flowRouteJSON `json:"routes"`
	RoutesLabel   string          `json:"routesLabel"`
	Wires         []flowWireJSON  `json:"wires"`
	Zones         []flowZoneJSON  `json:"zones"`
	Key           []flowKeyJSON   `json:"key"`
	Status        flowLookJSON    `json:"status"`
	Detail        string          `json:"detail"`
	Expires       int64           `json:"expires,omitempty"`
	ExpiredNote   string          `json:"expiredNote,omitempty"`
	ExpiredStatus *flowLookJSON   `json:"expiredStatus,omitempty"`
}

type checkedLineJSON struct {
	Icon    string     `json:"icon"`
	Tone    string     `json:"tone"`
	Name    string     `json:"name"`
	Said    string     `json:"said"`
	Details string     `json:"details,omitempty"`
	Tags    []saidJSON `json:"tags"`
}

type pushLookJSON struct {
	Icon  string `json:"icon"`
	Tone  string `json:"tone"`
	Label string `json:"label"`
	Lead  string `json:"lead"`
}

func flowLookOf(l session.FlowLook) flowLookJSON {
	return flowLookJSON{Icon: l.Icon, Tone: l.Tone, Word: l.Word}
}

func saidsOf(list []session.Said) []saidJSON {
	out := make([]saidJSON, 0, len(list))
	for _, s := range list {
		out = append(out, saidJSONOf(s))
	}
	return out
}

func flowNodeOf(n session.FlowNode) flowNodeJSON {
	return flowNodeJSON{Name: n.Name, Phase: n.Phase, Look: flowLookOf(n.Look), Aside: n.Aside, Tags: saidsOf(n.Tags)}
}

func flowOf(m *session.FlowModel) *flowJSON {
	if m == nil {
		return nil
	}
	out := &flowJSON{Commit: m.Commit, Gate: flowNodeOf(m.Gate), RoutesLabel: m.RoutesLabel,
		Status: flowLookOf(m.Status), Detail: m.Detail, Expires: millis(m.Expires), ExpiredNote: m.ExpiredNote,
		Reviews: []flowNodeJSON{}, Runs: []flowNodeJSON{}, Routes: []flowRouteJSON{}, Wires: []flowWireJSON{}, Zones: []flowZoneJSON{}, Key: []flowKeyJSON{}}
	if m.ExpiredStatus.Word != "" {
		late := flowLookOf(m.ExpiredStatus)
		out.ExpiredStatus = &late
	}
	for _, n := range m.Reviews {
		out.Reviews = append(out.Reviews, flowNodeOf(n))
	}
	for _, n := range m.Runs {
		out.Runs = append(out.Runs, flowNodeOf(n))
	}
	for _, r := range m.Routes {
		out.Routes = append(out.Routes, flowRouteJSON{Label: r.Label, Look: flowLookOf(r.Look), Taken: r.Taken, Decided: r.Decided, Note: r.Note})
	}
	for _, w := range m.Wires {
		out.Wires = append(out.Wires, flowWireJSON{Label: w.Label, Phase: w.Phase, Tone: w.Tone})
	}
	for _, z := range m.Zones {
		out.Zones = append(out.Zones, flowZoneJSON{Kind: z.Kind, Name: z.Name, Note: z.Note, Untrusted: z.Untrusted})
	}
	for _, k := range m.Key {
		out.Key = append(out.Key, flowKeyJSON{Swatch: k.Swatch, Words: k.Words})
	}
	return out
}

func checkedLineOf(l session.CheckedLine) checkedLineJSON {
	return checkedLineJSON{Icon: l.Icon, Tone: l.Tone, Name: l.Name, Said: l.Said, Details: l.Details, Tags: saidsOf(l.Tags)}
}
