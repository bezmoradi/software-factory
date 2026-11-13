package agenthost

import (
	"regexp"
	"slices"
	"sort"
	"strings"
)

var baseNames = []string{"PATH", "HOME", "LANG", "TERM", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy"}

var keyNames = map[string]bool{
	"ANTHROPIC_API_KEY": true, "ANTHROPIC_AUTH_TOKEN": true, "CLAUDE_CODE_OAUTH_TOKEN": true,
	"GOOGLE_APPLICATION_CREDENTIALS": true, "CODEX_API_KEY": true, "OPENAI_API_KEY": true,
	"GH_TOKEN": true, "GITHUB_TOKEN": true,
	"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true, "AWS_BEARER_TOKEN_BEDROCK": true,
	"AZURE_OPENAI_API_KEY": true, "NODE_OPTIONS": true, "SF_PUSH_TOKEN": true,
}

var keyLike = regexp.MustCompile(`(?i)(API_?KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|AUTH|PRIVATE_KEY|ALLOW_ALL)`)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

func KeyVariable(name string) bool {
	return keyNames[strings.ToUpper(name)] || keyLike.MatchString(name)
}

func Environment(own []string, pass []string, set map[string]string) []string {
	have := map[string]string{}
	for _, kv := range own {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			have[k] = v
		}
	}
	out := map[string]string{}
	for _, name := range append(slices.Clone(baseNames), pass...) {
		if v, ok := have[name]; ok && envName.MatchString(name) && !KeyVariable(name) {
			out[name] = v
		}
	}
	for k, v := range set {
		if envName.MatchString(k) && !KeyVariable(k) {
			out[k] = v
		}
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+out[k])
	}
	return env
}

func PathWith(own []string, dir string) string {
	path := ""
	for _, kv := range own {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	parts := []string{dir}
	for p := range strings.SplitSeq(path, ":") {
		if p != "" && p != dir {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ":")
}
