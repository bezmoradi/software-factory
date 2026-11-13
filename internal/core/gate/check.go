package gate

type Advice struct {
	Kind      ToolKind
	Waits     bool
	Pushes    bool
	Protected string
}

func Check(tool string, input map[string]any, top string, paths []string, review bool) Advice {
	k := Kind(tool, input)
	a := Advice{Kind: k, Waits: WaitsForPerson(k), Pushes: PushShaped(tool, input, review)}
	if k != EditTool {
		return a
	}
	for _, p := range paths {
		if reason := ProtectedWrite(top, p); reason != "" {
			a.Protected = reason
			break
		}
	}
	return a
}
