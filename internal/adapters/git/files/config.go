package files

import (
	"bytes"
	"errors"
	"strings"
)

type configEntry struct {
	section string
	sub     string
	key     string
	value   string
}

var errConfig = errors.New("the repository's config has a line the engine can't read")

func parseConfig(data []byte) ([]configEntry, error) {
	var out []configEntry
	section, sub := "", ""
	for raw := range bytes.SplitSeq(data, []byte("\n")) {
		line := strings.TrimSpace(strings.TrimSuffix(string(raw), "\r"))
		if strings.HasPrefix(line, "[") {
			end, s, b, ok := header(line)
			if !ok {
				return nil, errConfig
			}
			section, sub = s, b
			line = strings.TrimSpace(line[end:])
		}
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		name, rest := line, ""
		if i := strings.IndexAny(line, "=#; \t"); i >= 0 {
			name, rest = line[:i], strings.TrimLeft(line[i:], " \t")
		}
		if !configName(name) || section == "" {
			return nil, errConfig
		}
		value := "true"
		switch {
		case rest == "" || rest[0] == '#' || rest[0] == ';':
		case rest[0] == '=':
			v, ok := configValue(rest[1:])
			if !ok {
				return nil, errConfig
			}
			value = v
		default:
			return nil, errConfig
		}
		out = append(out, configEntry{section: section, sub: sub, key: strings.ToLower(name), value: value})
	}
	return out, nil
}

func header(line string) (int, string, string, bool) {
	i := 1
	for i < len(line) && (isAlnum(line[i]) || line[i] == '-' || line[i] == '.') {
		i++
	}
	name := strings.ToLower(line[1:i])
	if name == "" {
		return 0, "", "", false
	}
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i < len(line) && line[i] == ']' {
		if dot := strings.IndexByte(name, '.'); dot >= 0 {
			return i + 1, name[:dot], name[dot+1:], dot > 0
		}
		return i + 1, name, "", true
	}
	if i >= len(line) || line[i] != '"' || strings.Contains(name, ".") {
		return 0, "", "", false
	}
	var sub strings.Builder
	for i++; i < len(line); i++ {
		c := line[i]
		if c == '\\' && i+1 < len(line) {
			i++
			sub.WriteByte(line[i])
			continue
		}
		if c == '"' {
			if i+1 < len(line) && line[i+1] == ']' {
				return i + 2, name, sub.String(), true
			}
			return 0, "", "", false
		}
		sub.WriteByte(c)
	}
	return 0, "", "", false
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func configName(name string) bool {
	if name == "" || (name[0] < 'a' || name[0] > 'z') && (name[0] < 'A' || name[0] > 'Z') {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isAlnum(name[i]) && name[i] != '-' {
			return false
		}
	}
	return true
}

func configValue(raw string) (string, bool) {
	var out strings.Builder
	quoted := false
	spaces := 0
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !quoted && (c == ' ' || c == '\t') {
			if out.Len() > 0 {
				spaces++
			}
			continue
		}
		if !quoted && (c == '#' || c == ';') {
			break
		}
		out.WriteString(strings.Repeat(" ", spaces))
		spaces = 0
		switch c {
		case '"':
			quoted = !quoted
		case '\\':
			i++
			if i >= len(raw) {
				return "", false
			}
			switch raw[i] {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case 'b':
				out.WriteByte('\b')
			case '\\', '"':
				out.WriteByte(raw[i])
			default:
				return "", false
			}
		default:
			out.WriteByte(c)
		}
	}
	return out.String(), !quoted
}
