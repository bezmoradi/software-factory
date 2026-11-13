package npm

type Package struct {
	Name      string
	Version   string
	Folder    string
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
	{
		ID: "codex", Name: "Codex CLI", Version: "0.167.1", Command: "codex",
		Builds: map[string]Build{
			"amd64": {
				Packages: []Package{
					{Name: "@openai/codex", Version: "0.167.1", Integrity: "sha512-39uApTRT9bkhE8tMXTIfZxaZ27oi7hPqWoF2Coyp+iQFAD3NVHgH838IYVUfJaX85leMSP2s4JOmVw0mMSCN5Q=="},
					{Name: "@openai/codex", Version: "0.167.1-linux-x64", Folder: "@openai/codex-linux-x64", Integrity: "sha512-qZteglyvMCj1QVymMyPyKdtw38HaXCv5gvw8WN+N0+JJstu3k/d8xiYj9ciL7OwFLhcujgP8bCGJgaREeqrHkw=="},
				},
				Bin: "node_modules/@openai/codex/bin/codex.js",
			},
			"arm64": {
				Packages: []Package{
					{Name: "@openai/codex", Version: "0.167.1", Integrity: "sha512-39uApTRT9bkhE8tMXTIfZxaZ27oi7hPqWoF2Coyp+iQFAD3NVHgH838IYVUfJaX85leMSP2s4JOmVw0mMSCN5Q=="},
					{Name: "@openai/codex", Version: "0.167.1-linux-arm64", Folder: "@openai/codex-linux-arm64", Integrity: "sha512-z13jj4X4GDk7CTd1kXHkhXC8Gj8zomd1jYsU2//0KjevbLelPxowWLSTqIcR1rarJCtzveBHsS8gR7B9pAWB0g=="},
				},
				Bin: "node_modules/@openai/codex/bin/codex.js",
			},
		},
	},
}

type Plugin struct {
	ID       string
	Name     string
	Version  string
	URL      string
	Commit   string
	Manifest string
}

var PinnedPlugins = []Plugin{
	{ID: "knackpack", Name: "knackpack", Version: "0.24.0", URL: "https://github.com/bezmoradi/knackpack.git", Commit: "08ceae2d62b0198266f5f792347b2ec568e22adc"},
	{ID: "spec-kit", Name: "specify-cli", Version: "1.4.0", URL: "https://github.com/github/spec-kit.git", Commit: "0019fa14d12f45edabc2cc3176c9168a68bd0f97", Manifest: PyProject},
}
