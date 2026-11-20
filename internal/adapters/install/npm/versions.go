package npm

type Package struct {
	Name      string
	Version   string
	Integrity string
}

type Build struct {
	Packages []Package
	Bin      string
}

type Agent struct {
	ID      string
	Name    string
	Version string
	Command string
	Builds  map[string]Build
	Source  Source
}

var Pinned = []Agent{
	{
		ID: "claude", Name: "Claude Code", Version: "2.1.322", Command: "claude",
		Builds: map[string]Build{
			"amd64": {
				Packages: []Package{
					{Name: "@anthropic-ai/claude-code", Version: "2.1.322", Integrity: "sha512-oP7oW4vM5DREWfL1jn4JSGlmLcrJ5QmppgADrVNJBUPiC1mQ5YvRrke0on+YZ/3QhKIaCJtdr6jPvehcAQIHLQ=="},
					{Name: "@anthropic-ai/claude-code-linux-x64", Version: "2.1.322", Integrity: "sha512-0DoaNAXLWCYYIGachWBCkTUsS8dOOgvk4R2fFKdrLejO9XlRTwaWUMD9ONdDmv0csdpwxj4qLUN6zWMaG2Sz6g=="},
				},
				Bin: "node_modules/@anthropic-ai/claude-code-linux-x64/claude",
			},
			"arm64": {
				Packages: []Package{
					{Name: "@anthropic-ai/claude-code", Version: "2.1.322", Integrity: "sha512-oP7oW4vM5DREWfL1jn4JSGlmLcrJ5QmppgADrVNJBUPiC1mQ5YvRrke0on+YZ/3QhKIaCJtdr6jPvehcAQIHLQ=="},
					{Name: "@anthropic-ai/claude-code-linux-arm64", Version: "2.1.322", Integrity: "sha512-YYNp1op87WWcBEE6jC1neOjMDPdn4Iy+kUHyFcI1GsLl4IwH7EjZlwVKcpsZqVbE0IXEFaOldmyOHC1y3j18Ag=="},
				},
				Bin: "node_modules/@anthropic-ai/claude-code-linux-arm64/claude",
			},
		},
	},
}

const MinimumReviewerClaudeCode = ""

type Tracked struct {
	ID      string
	Name    string
	URL     string
	Branch  string
	Floor   string
	Refused []string
}

var RefusedKnackpack = []string{}

var Knackpack = Tracked{ID: "knackpack", Name: "knackpack", URL: "https://github.com/bezmoradi/knackpack.git", Branch: "main", Floor: "0.26.0", Refused: RefusedKnackpack}
