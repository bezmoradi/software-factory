package session

type foldCount struct {
	verb, one, many string
}

var (
	filesRead = foldCount{verb: "read", one: "file", many: "files"}
	searched  = foldCount{verb: "searched", one: "time", many: "times"}
)

var foldList = map[string]foldCount{
	"Read":   filesRead,
	"Grep":   searched,
	"Glob":   searched,
	"read":   filesRead,
	"search": searched,
}

var foldKeys = []string{"fold", "unfold", "line"}

func foldOf(kind string, ev Fields) Fields {
	switch kind {
	case "tool":
		if agentID, _ := ev["agentId"].(string); agentID != "" {
			return nil
		}
		tool, _ := ev["tool"].(string)
		c, ok := foldList[tool]
		if !ok {
			return nil
		}
		return Fields{"fold": map[string]any{"verb": c.verb, "one": c.one, "many": c.many}}
	case "tool_done":
		if ok, _ := ev["ok"].(bool); !ok {
			return Fields{"unfold": true}
		}
	}
	return nil
}

func (r *Registry) worktreeOf(c *conn) string {
	if a := r.admitted[c.sid]; a != nil && a.Cwd != "" {
		return a.Cwd
	}
	return c.cwd
}

func (r *Registry) areaOf(c *conn) string {
	if a := r.admitted[c.sid]; a != nil {
		return SessionArea(a.Cwd, a.Box)
	}
	return ""
}
