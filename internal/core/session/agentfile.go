package session

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxAgentFile     = 192 << 10
	MaxAgentFileOut  = MaxAgentFile + 16<<10
	MaxProjectAgents = 4
	MaxAgentFilesAll = 12
	MaxAgentName     = 64
	maxAgentDesc     = 4096
	maxFrontLines    = 200
	UploadedProvider = "page-upload"
	AgentVerdictAsk  = " End your reply with a line of its own that says Verdict: pass or Verdict: block, and put nothing after it."
)

var (
	agentNameRule = regexp.MustCompile(`^[a-z][a-z0-9-]{0,79}$`)
	AgentFileID   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}-[0-9a-f]{12}$`)
	AgentFileName = regexp.MustCompile(`^sf-[a-z][a-z0-9-]{0,63}-[0-9a-f]{12}\.md$`)
	agentModel    = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,63}(\[1m\])?$`)
	frontKey      = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)[ \t]*:(.*)$`)
	frontItem     = regexp.MustCompile(`^[ \t]+-[ \t]+(.*)$`)
)

var agentFileKeys = []string{"name", "description", "model", "tools"}

var refusedAgentKeys = map[string]string{
	"mcpServers":     "it starts MCP servers, programs that would run in a trusted reviewer container",
	"hooks":          "it runs commands in a trusted reviewer container",
	"permissionMode": "it changes what the reviewer may do without asking",
	"skills":         "it loads skills, whose instructions and scripts reviews don't allow",
}

type AgentFile struct {
	Name        string
	Description string
	Model       string
	Tools       []string
	Body        string
}

func normalAgentText(text string) string {
	text = strings.TrimPrefix(text, "\ufeff")
	return strings.ReplaceAll(text, "\r\n", "\n")
}

func AgentID(name, text string) string {
	sum := sha256.Sum256([]byte(normalAgentText(text)))
	return name + "-" + hex.EncodeToString(sum[:])[:12]
}

func AgentFileOf(id string) string {
	return "sf-" + id + ".md"
}

func ParseAgentFile(text string) (AgentFile, []string) {
	return parseAgentFile(text, MaxAgentFile)
}

func CheckAgentFileOut(file string, data []byte) string {
	if !AgentFileName.MatchString(file) {
		return "the file name isn't one the engine writes"
	}
	a, problems := parseAgentFile(string(data), MaxAgentFileOut)
	if len(problems) > 0 {
		return problems[0]
	}
	if AgentFileOf(a.Name) != file {
		return "the agent's name doesn't match its file name"
	}
	return ""
}

func parseAgentFile(text string, limit int) (AgentFile, []string) {
	if len(text) > limit {
		return AgentFile{}, []string{"the file is over " + strconv.Itoa(limit>>10) + " KiB"}
	}
	if !utf8.ValidString(text) {
		return AgentFile{}, []string{"the file isn't UTF-8 text"}
	}
	text = normalAgentText(text)
	for _, c := range text {
		if c != '\n' && c != '\t' && (unicode.IsControl(c) || c == ' ' || c == ' ') {
			return AgentFile{}, []string{"the file holds control characters"}
		}
	}
	lines := strings.Split(text, "\n")
	if lines[0] != "---" {
		return AgentFile{}, []string{"the file must start with a front matter block: a line ---, then name, description and the other keys, then another line ---"}
	}
	end := slices.IndexFunc(lines[1:], func(l string) bool { return strings.TrimRight(l, " \t") == "---" })
	if end < 0 {
		return AgentFile{}, []string{"the front matter has no closing --- line"}
	}
	front := lines[1 : end+1]
	if len(front) > maxFrontLines {
		return AgentFile{}, []string{"the front matter is over " + strconv.Itoa(maxFrontLines) + " lines"}
	}
	a := AgentFile{Body: strings.Join(lines[end+2:], "\n")}
	values, problems := frontValues(front)
	if len(problems) > 0 {
		return AgentFile{}, problems
	}
	for _, k := range agentFileKeys {
		v, ok := values[k]
		if !ok {
			continue
		}
		switch k {
		case "name", "description", "model":
			if len(v.more) > 0 {
				problems = append(problems, "write "+k+" on one line")
				continue
			}
			s, why := frontScalar(v.value)
			if why != "" {
				problems = append(problems, k+": "+why)
				continue
			}
			switch k {
			case "name":
				a.Name = s
			case "description":
				a.Description = s
			default:
				a.Model = s
			}
		case "tools":
			tools, why := frontTools(v)
			if why != "" {
				problems = append(problems, "tools: "+why)
				continue
			}
			a.Tools = tools
		}
	}
	if len(problems) > 0 {
		return AgentFile{}, problems
	}
	switch {
	case a.Name == "":
		problems = append(problems, "name is missing: give the agent a name of lowercase letters, digits and hyphens")
	case !agentNameRule.MatchString(a.Name):
		problems = append(problems, "name must be lowercase letters, digits and hyphens, starting with a letter")
	}
	switch {
	case strings.TrimSpace(a.Description) == "":
		problems = append(problems, "description is missing: say in a sentence what the agent reviews")
	case utf8.RuneCountInString(a.Description) > maxAgentDesc:
		problems = append(problems, "description is over "+strconv.Itoa(maxAgentDesc)+" characters")
	}
	if _, ok := values["model"]; ok && !agentModel.MatchString(a.Model) {
		problems = append(problems, "model must be an alias such as sonnet, opus, haiku, fable or inherit, or a model id such as claude-sonnet-5")
	}
	if strings.TrimSpace(a.Body) == "" {
		problems = append(problems, "the file has no instructions after its front matter")
	}
	if len(problems) > 0 {
		return AgentFile{}, problems
	}
	return a, nil
}

type frontValue struct {
	line  int
	value string
	more  []string
}

func frontValues(front []string) (map[string]frontValue, []string) {
	values := map[string]frontValue{}
	var order []string
	var problems []string
	last := ""
	for i, l := range front {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if l[0] == ' ' || l[0] == '\t' {
			if last == "" {
				problems = append(problems, "line "+strconv.Itoa(i+2)+" is indented, but no key comes before it")
				continue
			}
			v := values[last]
			v.more = append(v.more, l)
			values[last] = v
			continue
		}
		m := frontKey.FindStringSubmatch(l)
		if m == nil {
			problems = append(problems, "line "+strconv.Itoa(i+2)+" isn't a key and its value")
			last = ""
			continue
		}
		key := m[1]
		if _, dup := values[key]; dup {
			problems = append(problems, "the key "+key+" is given twice")
		}
		values[key] = frontValue{line: i + 2, value: strings.TrimSpace(m[2])}
		order = append(order, key)
		last = key
	}
	for _, k := range order {
		if slices.Contains(agentFileKeys, k) {
			continue
		}
		why, named := refusedAgentKeys[k]
		if !named {
			why = "Software Factory allows only name, description, model and tools in a reviewer agent's front matter"
		}
		problems = append(problems, "the key "+clip(k, 40)+" isn't allowed: "+why)
	}
	return values, problems
}

func frontScalar(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	switch raw[0] {
	case '"':
		return quoted(raw, '"')
	case '\'':
		return quoted(raw, '\'')
	case '[', '{', '|', '>', '&', '*', '!', '%', '@', '`':
		return "", "put the value in double quotes on one line"
	}
	if i := strings.Index(raw, " #"); i >= 0 {
		raw = strings.TrimSpace(raw[:i])
	}
	if i := strings.Index(raw, "\t#"); i >= 0 {
		raw = strings.TrimSpace(raw[:i])
	}
	if strings.Contains(raw, ": ") || strings.HasSuffix(raw, ":") {
		return "", "the value holds a colon followed by a space, which YAML reads as another key: put it in double quotes"
	}
	return raw, ""
}

func quoted(raw string, q byte) (string, string) {
	var b strings.Builder
	i := 1
	for ; i < len(raw); i++ {
		c := raw[i]
		if c == q {
			if q == '\'' && i+1 < len(raw) && raw[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			break
		}
		if q == '"' && c == '\\' {
			if i+1 >= len(raw) {
				return "", "the quoted value ends with a backslash"
			}
			switch raw[i+1] {
			case '"', '\\', '/':
				b.WriteByte(raw[i+1])
				i++
				continue
			}
			return "", "only \\\" , \\\\ and \\/ can be escaped in a quoted value"
		}
		b.WriteByte(c)
	}
	if i >= len(raw) {
		return "", "the quoted value has no closing quote"
	}
	if rest := strings.TrimSpace(raw[i+1:]); rest != "" && !strings.HasPrefix(rest, "#") {
		return "", "nothing but a comment may follow the closing quote"
	}
	return b.String(), ""
}

func frontTools(v frontValue) ([]string, string) {
	var items []string
	switch {
	case v.value == "" && len(v.more) == 0:
		return nil, "list at least one tool, or leave tools out"
	case v.value == "":
		for _, l := range v.more {
			if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
				continue
			}
			m := frontItem.FindStringSubmatch(l)
			if m == nil {
				return nil, "write each tool on its own line as - Name"
			}
			s, why := frontScalar(m[1])
			if why != "" {
				return nil, why
			}
			items = append(items, s)
		}
	case len(v.more) > 0:
		return nil, "give the tools on one line, or as a list below the key"
	case strings.HasPrefix(v.value, "["):
		inner, ok := strings.CutSuffix(v.value, "]")
		if !ok {
			return nil, "the list has no closing ]"
		}
		for part := range strings.SplitSeq(inner[1:], ",") {
			s, why := frontScalar(part)
			if why != "" {
				return nil, why
			}
			items = append(items, s)
		}
	default:
		s, why := frontScalar(v.value)
		if why != "" {
			return nil, why
		}
		items = strings.Split(s, ",")
	}
	var tools []string
	for _, t := range items {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !slices.Contains(ReviewTools, t) {
			return nil, clip(t, 40) + " isn't one of the tools reviews allow (" + strings.Join(ReviewTools, ", ") + ")"
		}
		if !slices.Contains(tools, t) {
			tools = append(tools, t)
		}
	}
	if len(tools) == 0 {
		return nil, "list at least one tool, or leave tools out"
	}
	return tools, ""
}

func (a AgentFile) Render(id string) string {
	var b strings.Builder
	b.WriteString("---\nname: " + id + "\n")
	b.WriteString("description: \"" + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(a.Description) + "\"\n")
	if a.Model != "" {
		b.WriteString("model: " + a.Model + "\n")
	}
	if len(a.Tools) > 0 {
		b.WriteString("tools: " + strings.Join(a.Tools, ", ") + "\n")
	}
	b.WriteString("---\n")
	b.WriteString(a.Body)
	if !strings.HasSuffix(a.Body, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}
