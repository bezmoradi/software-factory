package session

import (
	"context"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	MaxFlaggedFile  = 1 << 20
	FlagBudget      = 32 << 20
	maxFlagVersions = 20
	maxFlagged      = 200
	maxSpecFlags    = 200
	keptScans       = 16

	maxReviewedTrees = 32
)

type FlagKind string

const (
	FlagName     FlagKind = "name"
	FlagLarge    FlagKind = "large"
	FlagSecret   FlagKind = "secret"
	FlagPassword FlagKind = "password"
	FlagEncoding FlagKind = "encoding"
	FlagUnread   FlagKind = "unreviewed"
	FlagSpec     FlagKind = "specification"
)

type CommitField string

const (
	FieldMessage        CommitField = "message"
	FieldAuthorName     CommitField = "author-name"
	FieldAuthorEmail    CommitField = "author-email"
	FieldCommitterName  CommitField = "committer-name"
	FieldCommitterEmail CommitField = "committer-email"
	FieldHeaders        CommitField = "headers"
)

var FieldWords = map[CommitField]string{
	FieldMessage:        "message",
	FieldAuthorName:     "author name",
	FieldAuthorEmail:    "author email",
	FieldCommitterName:  "committer name",
	FieldCommitterEmail: "committer email",
	FieldHeaders:        "other headers",
}

var (
	flagName = regexp.MustCompile(`(?i)(^|/)([^/]+\.(pem|key|p12|pfx|keystore|jks|tfstate|tfstate\.backup)|id_(rsa|dsa|ecdsa|ed25519)|credentials[^/]*|\.claude/settings\.local\.json)$`)
	flagText = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----|\b(AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{60,}|xox[baprs]-[A-Za-z0-9-]{10,}|sk-ant-[A-Za-z0-9_-]{20,}|sk-(proj-)?[A-Za-z0-9_-]{32,}|glpat-[A-Za-z0-9_-]{20,}|npm_[A-Za-z0-9]{36,})\b|\bAIza[0-9A-Za-z_-]{35}`)
	flagCut  = regexp.MustCompile(`(\b(AKIA[0-9A-Z]*|gh[pousr]_[A-Za-z0-9]*|github_pat_[A-Za-z0-9_]*|xox[baprs]-[A-Za-z0-9-]*|sk-[A-Za-z0-9_-]*|glpat-[A-Za-z0-9_-]*|npm_[A-Za-z0-9]*)|\bAIza[0-9A-Za-z_-]*)$`)
	flagURL  = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s/?#@:]*:[^\s/?#@]+@[^\s/?#@]`)
	flagWhy  = map[FlagKind]string{
		FlagName:     "its name is one that usually holds secrets",
		FlagLarge:    "it is larger than 1 MiB",
		FlagSecret:   "it contains what looks like a private key or an access token",
		FlagPassword: "it contains what looks like a password in a URL",
		FlagEncoding: "it uses a text encoding other than UTF-8, which the engine can't scan for secrets",
		FlagUnread:   "a commit the push sends holds a version of it that neither the branch's head, a reviewed commit nor the main branch holds, so no reviewer read that version",
		FlagSpec:     "it is a Proctor specification change: a commit the push sends adds, changes, deletes or renames this file under proctor/, and Proctor's specifications decide what a pass means, so the person allows each change",
	}
	absentBlob    = strings.Repeat("0", 40)
	secretPathWhy = "its path holds what looks like an access token"
)

type PushedFile struct {
	Path     string
	SHA      string
	Mode     push.Mode
	Size     int64
	Data     []byte
	Reviewed bool
}

type PushedCommit struct {
	SHA            string
	Message        string
	AuthorName     string
	AuthorEmail    string
	CommitterName  string
	CommitterEmail string
	Headers        string
	Encodings      []string
}

type commitText struct {
	field CommitField
	text  string
}

func (c PushedCommit) fields() []commitText {
	return []commitText{{FieldMessage, c.Message}, {FieldAuthorName, c.AuthorName}, {FieldAuthorEmail, c.AuthorEmail},
		{FieldCommitterName, c.CommitterName}, {FieldCommitterEmail, c.CommitterEmail}, {FieldHeaders, c.Headers}}
}

type PushedSet struct {
	Files      []PushedFile
	Commits    []PushedCommit
	Attributes map[string][]byte
	Complete   bool
}

type PushContents interface {
	Pushed(ctx context.Context, repo, base, head string, reviewed []string, budget int64) (PushedSet, error)
}

type Flag struct {
	Path     string
	Commit   string
	Field    CommitField
	Kind     FlagKind
	Why      string
	Size     int64
	Versions []string
	Full     bool
	Link     bool
}

func (f Flag) key() string {
	if f.Commit != "" {
		return f.Commit + ":" + string(f.Field)
	}
	return f.Path
}

func (f Flag) Label() string {
	if f.Commit != "" {
		return "the " + FieldWords[f.Field] + " of commit " + shortSHA7(f.Commit)
	}
	return Escape([]byte(f.maskedPath()))
}

func (f Flag) maskedPath() string {
	return maskLines(f.Path, true)
}

type FlagView struct {
	Flag
	Shown   string
	Allowed bool
	Digest  string
	Spec    bool
}

type scanned struct {
	flags    []Flag
	complete bool
	lfs      []string
	specOver []string
	linkOver bool
	failed   bool
}

func secretName(p string) bool {
	base := strings.ToLower(path.Base(p))
	if base == ".env" {
		return true
	}
	if rest, ok := strings.CutPrefix(base, ".env."); ok && rest != "" {
		return rest != "example" && rest != "sample" && rest != "template"
	}
	return flagName.MatchString(p)
}

func cleanLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = CleanLine(l, len(l))
	}
	return strings.Join(lines, "\n")
}

func needsCleaning[T string | []byte](s T) bool {
	for i := range len(s) {
		if c := s[i]; (c < 0x20 && c != '\n') || c >= 0x7f {
			return true
		}
	}
	return false
}

func secretText(s string) bool {
	return flagText.MatchString(s) || needsCleaning(s) && flagText.MatchString(cleanLines(s))
}

func secretData(b []byte) bool {
	return flagText.Match(b) || needsCleaning(b) && flagText.MatchString(cleanLines(string(b)))
}

func plainUTF8(c PushedCommit) bool {
	for _, e := range c.Encodings {
		if !strings.EqualFold(e, "utf-8") && !strings.EqualFold(e, "utf8") {
			return false
		}
	}
	return utf8.ValidString(c.Message) && !strings.ContainsRune(c.Message, 0)
}

func flagOf(f PushedFile) (FlagKind, string, bool) {
	switch {
	case secretText(f.Path):
		return FlagSecret, secretPathWhy, true
	case f.Mode == push.ModeSubmodule && !f.Reviewed:
		return FlagUnread, flagWhy[FlagUnread], true
	case f.Mode == push.ModeSubmodule || f.Mode == push.ModeFolder:
		return "", "", false
	case secretName(f.Path):
		return FlagName, flagWhy[FlagName], true
	case f.Size < 0 || f.Size > MaxFlaggedFile:
		return FlagLarge, flagWhy[FlagLarge], true
	case f.Data != nil && secretData(f.Data):
		return FlagSecret, flagWhy[FlagSecret], true
	case f.Data != nil && flagURL.Match(f.Data):
		return FlagPassword, flagWhy[FlagPassword], true
	case !f.Reviewed:
		return FlagUnread, flagWhy[FlagUnread], true
	}
	return "", "", false
}

func addVersion(fl *Flag, sha string) {
	switch {
	case fl.Full || slices.Contains(fl.Versions, sha):
	case len(fl.Versions) >= maxFlagVersions:
		fl.Full, fl.Versions = true, nil
	default:
		fl.Versions = append(fl.Versions, sha)
	}
}

func scanPushed(set PushedSet, specs bool) scanned {
	out := scanned{complete: set.Complete}
	byPath := map[string]int{}
	plain, specCount := 0, 0
	over := map[string]bool{}
	var paths []string
	listed := map[string]bool{}
	version := func(p string, kind FlagKind, why string, size int64, sha string, spec bool) *Flag {
		i, seen := byPath[p]
		if !seen {
			switch {
			case spec && specCount >= maxSpecFlags:
				over[p] = true
				return nil
			case spec:
				specCount++
			case plain >= maxFlagged:
				out.complete = false
				return nil
			default:
				plain++
			}
			byPath[p] = len(out.flags)
			out.flags = append(out.flags, Flag{Path: p, Kind: kind, Why: why, Size: size})
			i = len(out.flags) - 1
		}
		fl := &out.flags[i]
		if fl.Kind == FlagSpec && kind != FlagSpec {
			fl.Kind, fl.Why = kind, why
		}
		if size > fl.Size {
			fl.Size = size
		}
		addVersion(fl, sha)
		return fl
	}
	for _, f := range set.Files {
		linked := f.Mode == push.ModeSubmodule || f.Mode == push.ModeFolder
		spec := specs && gate.SpecPath(f.Path)
		leaves := spec && f.Mode == push.ModeSymlink && (gate.SpecMakefile(f.Path) || f.Data == nil || gate.SpecLinkLeaves(f.Path, string(f.Data)))
		if !linked && !listed[f.Path] {
			listed[f.Path] = true
			paths = append(paths, f.Path)
		}
		if !linked && f.Data == nil && f.Size >= 0 && f.Size <= MaxFlaggedFile && !secretName(f.Path) && !secretText(f.Path) {
			out.complete = false
			if spec {
				if fl := version(f.Path, FlagSpec, specWhy(f.Path), f.Size, f.SHA, true); fl != nil && leaves {
					fl.Link = true
				} else if fl == nil && leaves {
					out.linkOver = true
				}
			}
			continue
		}
		kind, why, flagged := flagOf(f)
		if spec && (!flagged || kind == FlagUnread) {
			kind, why, flagged = FlagSpec, specWhy(f.Path), true
		}
		if !flagged {
			continue
		}
		if fl := version(f.Path, kind, why, f.Size, f.SHA, spec); fl != nil && leaves {
			fl.Link = true
		} else if fl == nil && leaves {
			out.linkOver = true
		}
	}
	out.specOver = slices.Sorted(maps.Keys(over))
	slices.SortFunc(out.flags, func(a, b Flag) int { return strings.Compare(a.Path, b.Path) })
	for _, c := range set.Commits {
		for _, f := range c.fields() {
			kind := FlagSecret
			switch {
			case f.text != "" && secretText(f.text):
			case f.field == FieldMessage && !plainUTF8(c):
				kind = FlagEncoding
			default:
				continue
			}
			if plain >= maxFlagged {
				out.complete = false
				break
			}
			plain++
			out.flags = append(out.flags, Flag{Commit: c.SHA, Field: f.field, Kind: kind, Why: flagWhy[kind], Versions: []string{c.SHA}})
		}
	}
	out.lfs = workflow.LFSPaths(set.Attributes, paths)
	return out
}

func allowedFlag(f Flag, allowed map[string]map[string]bool) bool {
	set := allowed[f.key()]
	if f.Full || f.Link || len(f.Versions) == 0 || set == nil {
		return false
	}
	for _, v := range f.Versions {
		if !set[v] {
			return false
		}
	}
	return true
}

const secretMask = "[hidden: looks like a secret]"

var keyEnd = regexp.MustCompile(`-----END [A-Z ]*PRIVATE KEY-----`)

func maskSecrets(s string) string {
	return maskLines(s, false)
}

func maskLines(s string, urls bool) string {
	if !secretText(s) && (!urls || !flagURL.MatchString(s)) {
		return s
	}
	lines := strings.Split(s, "\n")
	inKey := false
	for i, line := range lines {
		lines[i], inKey = maskLine("", line, inKey, urls)
	}
	return strings.Join(lines, "\n")
}

func maskCut(s string) string {
	s = maskSecrets(s)
	if loc := flagCut.FindStringIndex(s); loc != nil {
		return s[:loc[0]] + secretMask
	}
	start := strings.LastIndexByte(s, '\n') + 1
	if last := s[start:]; needsCleaning(last) && flagCut.MatchString(CleanLine(last, len(last))) {
		return s[:start] + secretMask
	}
	return s
}

func maskChanges(cs ChangeSet) ChangeSet {
	commits := make([]push.Commit, len(cs.Commits))
	for i, c := range cs.Commits {
		c.Message = maskLines(c.Message, true)
		commits[i] = c
	}
	cs.Commits = commits
	changed := make([]push.Change, len(cs.Files))
	for i, f := range cs.Files {
		f.Path = maskLines(f.Path, true)
		changed[i] = f
	}
	cs.Files = changed
	skipped := make([]NotDiffed, len(cs.NotDiffed))
	for i, n := range cs.NotDiffed {
		n.Path = maskLines(n.Path, true)
		skipped[i] = n
	}
	cs.NotDiffed = skipped
	cs.Diff = maskDiff(cs.Diff)
	return cs
}

func maskDiff(diff string) string {
	if !secretText(diff) && !flagURL.MatchString(diff) {
		return diff
	}
	lines := strings.Split(diff, "\n")
	var oldKey, newKey, inHunk bool
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			oldKey, newKey, inHunk = false, false, false
			lines[i], _ = maskLine("", line, false, true)
		case strings.HasPrefix(line, "@@ "):
			inHunk = true
			head := line
			if end := strings.Index(line[3:], " @@"); end >= 0 {
				head = line[:3+end+3]
			}
			lines[i], _ = maskLine(head, line[len(head):], oldKey || newKey, true)
		case !inHunk || line == "":
			lines[i], _ = maskLine("", line, false, true)
		case line[0] == '-':
			lines[i], oldKey = maskLine("-", line[1:], oldKey, true)
		case line[0] == '+':
			lines[i], newKey = maskLine("+", line[1:], newKey, true)
		case line[0] == ' ':
			oldText, oldOpen := maskLine(" ", line[1:], oldKey, true)
			newText, newOpen := maskLine(" ", line[1:], newKey, true)
			lines[i] = newText
			if oldKey {
				lines[i] = oldText
			}
			oldKey, newKey = oldOpen, newOpen
		default:
			lines[i], _ = maskLine("", line, false, true)
		}
	}
	return strings.Join(lines, "\n")
}

func maskLine(prefix, s string, inKey, urls bool) (string, bool) {
	out, open := maskMatches(prefix, s, inKey, urls)
	if !needsCleaning(s) {
		return out, open
	}
	if _, cleanOpen := maskMatches("", CleanLine(s, len(s)), inKey, false); cleanOpen {
		open = true
	}
	if rest := out[len(prefix):]; flagText.MatchString(CleanLine(rest, len(rest))) {
		return prefix + secretMask, open
	}
	return out, open
}

func maskMatches(prefix, s string, inKey, urls bool) (string, bool) {
	if inKey {
		end := keyEnd.FindStringIndex(s)
		if end == nil {
			if s == "" {
				return prefix, true
			}
			return prefix + secretMask, true
		}
		prefix, s = prefix+secretMask, s[end[1]:]
	}
	var b strings.Builder
	b.WriteString(prefix)
	pos := 0
	for _, loc := range flagText.FindAllStringIndex(s, -1) {
		if loc[1] <= pos {
			continue
		}
		b.WriteString(maskURLs(s[pos:max(loc[0], pos)], urls))
		b.WriteString(secretMask)
		pos = loc[1]
		if strings.HasPrefix(s[loc[0]:loc[1]], "-----BEGIN") {
			end := keyEnd.FindStringIndex(s[pos:])
			if end == nil {
				return b.String(), true
			}
			pos += end[1]
		}
	}
	b.WriteString(maskURLs(s[pos:], urls))
	return b.String(), false
}

func maskURLs(s string, urls bool) string {
	if !urls {
		return s
	}
	var out strings.Builder
	pos := 0
	for _, loc := range flagURL.FindAllStringIndex(s, -1) {
		match := s[loc[0]:loc[1]]
		user := loc[0] + strings.Index(match, "://") + len("://")
		at := loc[0] + strings.LastIndexByte(match, '@')
		out.WriteString(s[pos:user])
		out.WriteString(secretMask)
		pos = at
	}
	out.WriteString(s[pos:])
	return out.String()
}
