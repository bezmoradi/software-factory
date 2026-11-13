package session

import (
	"path"
	"regexp"
	"strings"
)

type ClaimKind string

const (
	ClaimTests ClaimKind = "tests"
	ClaimBuild ClaimKind = "build"
	ClaimFile  ClaimKind = "file"
)

const (
	maxClaimQuote   = 200
	maxClaimFiles   = 5
	maxClaimMessage = 20000
	maxClaimLength  = 600
)

type claim struct {
	kind     ClaimKind
	sentence string
	file     string
}

var (
	fencedCode    = regexp.MustCompile("(?s)```.*?(```|$)|~~~.*?(~~~|$)")
	listMarker    = regexp.MustCompile(`^\s*(?:[-*+•]|\d{1,3}[.)])\s+`)
	quotedText    = regexp.MustCompile(`"[^"\n]*"|“[^”\n]*”`)
	emphasis      = regexp.MustCompile(`\*\*|__|[✅✔✓☑]\x{FE0F}?`)
	sentenceBreak = regexp.MustCompile(`[.!?;]\s+`)
	cleanSaid     = regexp.MustCompile(`(?i)\b(?:no|zero|0|without\s+(?:any\s+)?)\s*(?:new\s+)?(?:errors?|warnings?|failures?|failed|issues?|problems?|regressions?)\b`)
	hedged        = regexp.MustCompile(`(?i)\?|n['’]t\b|\b(?:not|no|never|none|nothing|cannot|without|unable|fail\w*|broke\w*|should|would|could|might|may|must|will|shall|going\s+to|gonna|expect\w*|if|once|until|unless|whether|when|whenever|after|before|ensure|make\s+sure|verify|confirm|check|need\w*|try\w*|tried|hope\w*|assum\w*|probably|likely|presumably|seem\w*|appear\w*|earlier|previous\w*|already|last\s+time|ci|pipeline|workflow|actions|let\s+me|let['’]s|i['’]ll|we['’]ll|plan\w*|todo|next|remaining|still\s+need)\b`)
	testsClaim    = regexp.MustCompile(`(?i)\b(?:(?:all\s+(?:of\s+)?(?:the\s+)?)?(?:\d+\s+)?(?:[a-z0-9-]+\s+){0,2}?tests?|test\s+suites?)\s+(?:now\s+|all\s+|both\s+|also\s+)?(?:pass(?:es|ed|ing)?|succeed(?:s|ed)?|(?:are|is|were|was)\s+(?:all\s+)?(?:passing|green|successful))\b|\btests?\s*[:–—-]\s*(?:pass(?:es|ed|ing)?|green|ok)\b`)
	buildClaim    = regexp.MustCompile(`(?i)\b(?:build|builds|compilation)\s+(?:now\s+|also\s+)?(?:pass(?:es|ed)?|succeed(?:s|ed)?|(?:is|was)\s+(?:green|clean|passing|successful|fine|ok)|works|worked|complete[sd]?\s+(?:cleanly|successfully))\b|\b(?:it|everything|the\s+(?:code|project|app|package|crate|module|binary))\s+(?:now\s+|also\s+)?(?:builds|compiles)(?:\s+(?:cleanly|successfully|fine|again|now|locally|correctly))?\s*[.!]?$|\b(?:builds|compiles)\s+(?:cleanly|successfully|fine)\b`)
	fileVerb      = regexp.MustCompile(`(?i)\b(?:updated|edited|modified|changed|rewrote|rewritten|patched|refactored|fixed|adjusted|tweaked|amended)\s+(?:the\s+)?(?:file\s+)?(?:in\s+)?`)
	fileNext      = regexp.MustCompile(`^(?:,\s*(?:and\s+)?|\s+and\s+|\s*&\s*)`)
	backticked    = regexp.MustCompile("^`([^`\\s]{1,300})`")
	bareFile      = regexp.MustCompile(`^((?:\.{0,2}/)?(?:[A-Za-z0-9_@.+-]+/)*[A-Za-z0-9_@+-][A-Za-z0-9_@.+-]*)`)
	fileExtension = regexp.MustCompile(`\.(?:go|mod|sum|ts|tsx|js|jsx|mjs|cjs|py|pyi|rb|rs|java|kt|kts|c|h|cc|cpp|hpp|cs|swift|m|mm|php|md|mdx|rst|txt|json|jsonc|yaml|yml|toml|ini|cfg|conf|xml|html|htm|css|scss|sass|less|vue|svelte|sh|bash|zsh|ps1|sql|proto|graphql|gql|tf|hcl|lock|gradle|ex|exs|erl|dart|scala|lua|r|jl|zig|nix|env|example|sample|tmpl|tpl|ipynb|csv)$`)
	lineSuffix    = regexp.MustCompile(`(?::\d+){1,2}$`)
	extensionless = map[string]bool{"Makefile": true, "Dockerfile": true, "Containerfile": true, "Gemfile": true, "Rakefile": true, "Procfile": true, "Justfile": true, "Taskfile": true, "LICENSE": true, "CODEOWNERS": true, "Jenkinsfile": true, "Vagrantfile": true, "Brewfile": true, "Pipfile": true}
)

func findClaims(message string) []claim {
	text := quotedText.ReplaceAllString(fencedCode.ReplaceAllString(message, "\n"), " ")
	var out []claim
	files := 0
	seen := map[string]bool{}
	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ">") {
			continue
		}
		line = emphasis.ReplaceAllString(listMarker.ReplaceAllString(line, ""), "")
		for _, sentence := range splitSentences(line) {
			sentence = strings.TrimSpace(sentence)
			if sentence == "" || len(sentence) > maxClaimLength || hedged.MatchString(cleanSaid.ReplaceAllString(sentence, "")) {
				continue
			}
			if testsClaim.MatchString(sentence) && !seen["tests"] {
				seen["tests"] = true
				out = append(out, claim{kind: ClaimTests, sentence: sentence})
			}
			if buildClaim.MatchString(sentence) && !seen["build"] {
				seen["build"] = true
				out = append(out, claim{kind: ClaimBuild, sentence: sentence})
			}
			for _, f := range claimedFiles(sentence) {
				if files >= maxClaimFiles || seen["file:"+f] {
					continue
				}
				seen["file:"+f] = true
				files++
				out = append(out, claim{kind: ClaimFile, sentence: sentence, file: f})
			}
		}
	}
	return out
}

func splitSentences(line string) []string {
	var out []string
	rest := line
	for {
		loc := sentenceBreak.FindStringIndex(rest)
		if loc == nil {
			return append(out, rest)
		}
		out = append(out, rest[:loc[0]+1])
		rest = rest[loc[1]:]
	}
}

func claimedFiles(sentence string) []string {
	var out []string
	for _, loc := range fileVerb.FindAllStringIndex(sentence, -1) {
		rest := sentence[loc[1]:]
		for {
			name, used := fileToken(rest)
			if name == "" {
				break
			}
			out = append(out, name)
			rest = rest[used:]
			sep := fileNext.FindString(rest)
			if sep == "" {
				break
			}
			rest = rest[len(sep):]
		}
	}
	return out
}

func fileToken(s string) (string, int) {
	if m := backticked.FindStringSubmatch(s); m != nil {
		if name := fileName(m[1], true); name != "" {
			return name, len(m[0])
		}
		return "", 0
	}
	m := bareFile.FindString(s)
	if m == "" {
		return "", 0
	}
	trimmed := strings.TrimRight(m, ".")
	return fileName(trimmed, false), len(m)
}

func fileName(token string, quoted bool) string {
	token = lineSuffix.ReplaceAllString(token, "")
	if token == "" || strings.Contains(token, "://") || strings.ContainsAny(token, "()[]{}<>*?$\\\"'`,;:") || strings.HasPrefix(token, "-") {
		return ""
	}
	base := path.Base(token)
	switch {
	case extensionless[base], fileExtension.MatchString(strings.ToLower(base)):
	case quoted && strings.Contains(token, "/") && strings.Contains(base, "."):
	default:
		return ""
	}
	clean := path.Clean(token)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return ""
	}
	return strings.TrimPrefix(clean, "./")
}

var (
	testWord     = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:tests?|testing|specs?|pytest|py\.test|jest|vitest|mocha|jasmine|karma|ava|rspec|phpunit|ctest|unittest|nextest|cypress|playwright|check|verify|e2e)(?:[^a-z]|$)`)
	buildWord    = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:build|builds|compile|compiles|tsc|vet|typecheck|type-check|lint)(?:[^a-z]|$)`)
	commandSplit = regexp.MustCompile("&&|\\|\\||[;|\n()`]|\\$\\(")
	envAssign    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	runners      = map[string]bool{"make": true, "gmake": true, "just": true, "task": true, "nx": true, "turbo": true, "bazel": true, "bazelisk": true, "rake": true, "mix": true, "tox": true, "nox": true, "meson": true, "earthly": true, "docker": true, "podman": true}
	scriptHosts  = map[string]bool{"python": true, "python3": true, "node": true, "ruby": true, "perl": true, "php": true, "bash": true, "sh": true, "zsh": true, "dash": true}
	packageTools = map[string]bool{"npm": true, "pnpm": true, "yarn": true, "bun": true, "deno": true}
	notScripts   = map[string]bool{"install": true, "i": true, "add": true, "remove": true, "rm": true, "uninstall": true, "ci": true, "init": true, "create": true, "info": true, "view": true, "ls": true, "list": true, "outdated": true, "why": true, "audit": true, "version": true, "--version": true, "-v": true, "config": true, "login": true, "whoami": true, "publish": true, "pack": true, "link": true, "cache": true, "help": true, "--help": true}
	builders     = map[string]bool{"go": true, "cargo": true, "rustc": true, "tsc": true, "gcc": true, "g++": true, "clang": true, "clang++": true, "cc": true, "c++": true, "javac": true, "kotlinc": true, "cmake": true, "ninja": true, "gradle": true, "gradlew": true, "mvn": true, "mvnw": true, "sbt": true, "dotnet": true, "msbuild": true, "xcodebuild": true, "swift": true, "swiftc": true, "webpack": true, "vite": true, "esbuild": true, "rollup": true, "parcel": true, "next": true, "nuxt": true, "astro": true, "zig": true, "stack": true, "cabal": true, "ghc": true, "elm": true, "make": true}
	skipWords    = map[string]bool{"sudo": true, "env": true, "time": true, "nice": true, "nohup": true, "exec": true, "command": true, "builtin": true, "xargs": true}
	shells       = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true}
	pythonTools  = map[string]bool{"pip": true, "pip3": true, "venv": true, "http.server": true, "json.tool": true}
)

func commandRuns(command string) (tests, build bool) {
	flat := strings.NewReplacer("'", " ", "\"", " ").Replace(command)
	for _, segment := range commandSplit.Split(flat, -1) {
		t, b := segmentRuns(strings.Fields(segment))
		tests = tests || t
		build = build || b || t
	}
	return tests, build
}

func segmentRuns(words []string) (tests, build bool) {
	for len(words) > 0 {
		w := words[0]
		switch {
		case envAssign.MatchString(w), skipWords[w]:
			words = words[1:]
			continue
		case w == "timeout" && len(words) > 1:
			words = words[2:]
			continue
		case shells[path.Base(w)] && len(words) > 1 && strings.HasPrefix(words[1], "-"):
			words = words[1:]
			for len(words) > 0 && strings.HasPrefix(words[0], "-") {
				words = words[1:]
			}
			continue
		}
		break
	}
	if len(words) == 0 {
		return false, false
	}
	joined := strings.Join(words, " ")
	if testWord.MatchString(joined) {
		return true, true
	}
	program := path.Base(words[0])
	if program == "npx" || program == "bunx" || program == "pnpx" {
		return segmentRuns(words[1:])
	}
	opaque := runners[program] || scriptFile(words[0])
	if scriptHosts[program] {
		if arg := firstArgument(words[1:]); arg != "" && !pythonTools[arg] {
			opaque = true
		}
	}
	if packageTools[program] && len(words) > 1 && !notScripts[words[1]] {
		opaque = true
	}
	if opaque {
		return true, true
	}
	return false, builders[program] || buildWord.MatchString(joined)
}

func firstArgument(words []string) string {
	for _, w := range words {
		if !strings.HasPrefix(w, "-") {
			return w
		}
	}
	return ""
}

func scriptFile(word string) bool {
	if strings.HasPrefix(word, "./") || strings.HasPrefix(word, "../") {
		return true
	}
	switch path.Ext(word) {
	case ".sh", ".bash", ".py", ".js", ".mjs", ".ts", ".rb", ".pl", ".ps1":
		return true
	}
	return false
}
