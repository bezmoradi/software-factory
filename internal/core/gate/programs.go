package gate

import (
	"regexp"
	"strings"
)

const (
	maxProgramCommands = 2000
	maxProgramText     = 100000
)

var (
	quietPrefixes = set("env", "time", "nohup", "nice", "ionice", "exec", "command", "builtin", "timeout", "stdbuf", "setsid", "unbuffer", "caffeinate", "chronic", "xargs", "watch", "taskset", "chrt")
	shownPrefixes = set("sudo", "doas")
	prefixValues  = map[string]map[string]bool{
		"env":     set("-u", "--unset", "-C", "--chdir"),
		"nice":    set("-n", "--adjustment"),
		"ionice":  set("-c", "-n", "-p", "--class", "--classdata"),
		"timeout": set("-s", "--signal", "-k", "--kill-after"),
		"xargs":   set("-I", "-n", "-P", "-d", "-E", "-L", "-s", "-a", "--max-args", "--max-procs", "--delimiter", "--arg-file", "--replace", "--max-lines", "--max-chars", "--eof"),
		"watch":   set("-n", "--interval"),
		"stdbuf":  set("-i", "-o", "-e"),
		"sudo":    set("-u", "-g", "-C", "-D", "-h", "-p", "-r", "-t", "-U", "--user", "--group", "--close-from", "--chdir", "--host", "--prompt", "--role", "--type", "--other-user"),
		"doas":    set("-u", "-C"),
	}
	prefixOperands = map[string]int{"timeout": 1, "taskset": 1, "chrt": 1}
	splitString    = set("-S", "--split-string")
	noProgram      = set("[[", "for", "select", "case", "function")
	findRuns       = set("-exec", "-execdir", "-ok", "-okdir")
	printers       = set("echo", "printf")
	programChars   = regexp.MustCompile(`^(?:\[|[A-Za-z0-9._+@:-]{1,80})$`)
)

type programScan struct {
	names    []string
	seen     map[string]bool
	commands int
	ok       bool
}

func Programs(command string) ([]string, bool) {
	s := &programScan{seen: map[string]bool{}, ok: true}
	s.scan(command, 0)
	if !s.ok {
		return nil, false
	}
	return s.names, true
}

func (s *programScan) scan(command string, depth int) {
	if !s.ok {
		return
	}
	if depth > shellDepth || len(command) > maxProgramText {
		s.ok = false
		return
	}
	p := &parsed{}
	if _, ok := shellParse(command, 0, 0, p); !ok {
		s.ok = false
		return
	}
	for _, words := range p.cmds {
		s.command(words, depth, p)
	}
}

func (s *programScan) add(name string) {
	if !programChars.MatchString(name) {
		s.ok = false
		return
	}
	if !s.seen[name] {
		s.seen[name] = true
		s.names = append(s.names, name)
	}
}

func (s *programScan) command(words []string, depth int, p *parsed) {
	if s.commands++; !s.ok || s.commands > maxProgramCommands || depth > shellDepth {
		s.ok = false
		return
	}
	i := 0
	for i < len(words) && (shellWords[words[i]] || assignment.MatchString(words[i])) {
		i++
	}
	for i < len(words) {
		word := words[i]
		if unresolved.MatchString(word) {
			s.ok = false
			return
		}
		name := word[strings.LastIndexByte(word, '/')+1:]
		lower := strings.ToLower(name)
		if noProgram[lower] {
			return
		}
		if !quietPrefixes[lower] && !shownPrefixes[lower] {
			s.add(name)
			s.inside(lower, words[i+1:], depth, p)
			return
		}
		if shownPrefixes[lower] {
			s.add(name)
		}
		next, ok := prefixEnd(lower, words, i+1)
		if !ok {
			s.ok = false
			return
		}
		i = next
	}
}

func (s *programScan) inside(name string, args []string, depth int, p *parsed) {
	switch {
	case shells[name]:
		if j, ok := shellScriptAt(args); ok {
			if j < len(args) {
				s.scan(args[j], depth+1)
			}
			return
		}
		if readsInput(args) {
			s.fed(depth, p)
		}
	case name == "eval":
		s.scan(strings.Join(args, " "), depth+1)
	case name == "find":
		for j, a := range args {
			if findRuns[a] && j+1 < len(args) {
				s.command(args[j+1:], depth+1, p)
			}
		}
	}
}

func (s *programScan) fed(depth int, p *parsed) {
	if p.fedHazard {
		return
	}
	p.fedHazard = true
	for _, b := range p.bodies {
		s.scan(b, depth+1)
	}
	for _, w := range p.cmds {
		if len(w) > 1 && printers[nameOf(w[0])] {
			s.scan(printed(w[1:]), depth+1)
		}
	}
}

func prefixEnd(name string, words []string, j int) (int, bool) {
	for j < len(words) {
		a := words[j]
		switch {
		case a == "--":
			j++
		case name == "env" && splitString[a]:
			return 0, false
		case name == "env" && assignment.MatchString(a):
			j++
			continue
		case optionLike.MatchString(a) && len(a) > 1:
			if prefixValues[name][a] {
				j += 2
			} else {
				j++
			}
			continue
		}
		break
	}
	for n := prefixOperands[name]; n > 0 && j < len(words); n-- {
		j++
	}
	return j, true
}
