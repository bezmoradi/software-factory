package workflow

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/review"
)

const (
	PackManifestFile = "software-factory-pack.json"
	PackFormat       = "software-factory-pack/1"
	MaxPackManifest  = 16 << 10
)

var (
	packName      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	packVersion   = regexp.MustCompile(`^[0-9]{1,9}\.[0-9]{1,9}\.[0-9]{1,9}$`)
	packDocName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	packCommand   = regexp.MustCompile(`^/([a-z0-9][a-z0-9-]{0,63}):[a-z0-9][a-z0-9-]{0,63}$`)
	packSteps     = []Step{Define, Plan, Review}
	reservedPacks = []string{"knackpack", "software-factory"}
	gitHostPacks  = []string{"forgejo", "gogs", "azuredevops"}
	engineOwned   = map[string]string{
		"tools":       "the engine sets the reviewers' tools: read-only, the same for every pack",
		"model":       "the person picks each role's model on the page",
		"effort":      "the person picks each role's effort on the page",
		"permissions": "the engine sets what a reviewer may do, and a pack can't widen it",
		"network":     "the engine decides what reaches the network",
		"hosts":       "the engine decides which hosts sessions and reviewers reach",
		"verdict":     "the engine owns the verdict lines it reads",
		"required":    "the engine decides which roles a push needs",
		"rounds":      "the person sets review rounds on the page",
		"hooks":       "a reviewer pack can't run programs in the trusted reviewer container",
		"mcpServers":  "a reviewer pack can't start servers in the trusted reviewer container",
		"env":         "the engine sets the reviewers' environment",
	}
)

type PackDocuments struct {
	Folder       string
	Requirements string
	Plan         string
}

type PackManifest struct {
	Name      string
	Version   string
	Roles     map[review.Role]string
	Steps     map[Step]string
	Documents *PackDocuments
}

func PackNameProblem(name string) string {
	switch {
	case !packName.MatchString(name):
		return "a pack's name is lowercase letters, digits and hyphens, at most 64, starting with a letter or digit"
	case slices.Contains(reservedPacks, name):
		return "the name " + name + " belongs to the engine's own plugins, so a pack can't take it"
	case strings.HasPrefix(name, "git") || slices.Contains(gitHostPacks, name):
		return "a pack's documents folder is ." + name + ", which is git's or a git host's own, so a pack's name can't start with git or be forgejo, gogs or azuredevops"
	}
	return ""
}

func PackVersion(v string) bool {
	return packVersion.MatchString(v)
}

func ParsePackManifest(data []byte) (PackManifest, []string) {
	if len(data) > MaxPackManifest {
		return PackManifest{}, []string{PackManifestFile + " is over " + strconv.Itoa(MaxPackManifest>>10) + " KiB"}
	}
	value, problem := strictJSON(string(data))
	if problem != "" {
		return PackManifest{}, []string{PackManifestFile + " isn't strict JSON: " + problem}
	}
	root, ok := value.(*object)
	if !ok {
		return PackManifest{}, []string{PackManifestFile + " must hold one JSON object"}
	}
	var out []string
	say := func(s string) { out = append(out, s) }
	for _, k := range root.keys {
		if why, owned := engineOwned[k]; owned {
			say("the key \"" + k + "\" isn't a pack's to set: " + why)
			continue
		}
		if !slices.Contains([]string{"format", "name", "version", "roles", "steps", "documents"}, k) {
			say("the key \"" + clip(k, 40) + "\" isn't one a pack manifest has")
		}
	}
	if s, _ := asString(root.values["format"]); s != PackFormat {
		say("\"format\" must be \"" + PackFormat + "\"")
	}
	m := PackManifest{Roles: map[review.Role]string{}, Steps: map[Step]string{}}
	m.Name, _ = asString(root.values["name"])
	if why := PackNameProblem(m.Name); why != "" {
		say("\"name\": " + why)
	}
	m.Version, _ = asString(root.values["version"])
	if !PackVersion(m.Version) {
		say("\"version\" must be of the form x.y.z")
	}
	if v, ok := root.values["roles"]; ok {
		roles, isObj := v.(*object)
		if !isObj {
			say("\"roles\" must be an object from a role to an agent of the pack")
		} else {
			for _, k := range roles.keys {
				role := review.Role(k)
				agent, isStr := asString(roles.values[k])
				switch {
				case !slices.Contains(review.Roles, role):
					say("\"roles\": \"" + clip(k, 40) + "\" isn't a role; the roles are " + roleList())
				case !isStr || !packName.MatchString(agent):
					say("\"roles\": the " + role.Name() + " role names an agent file of the pack, lowercase letters, digits and hyphens")
				default:
					m.Roles[role] = agent
				}
			}
		}
	}
	if v, ok := root.values["steps"]; ok {
		steps, isObj := v.(*object)
		if !isObj {
			say("\"steps\" must be an object from a step to one of the pack's commands")
		} else {
			for _, k := range steps.keys {
				step := Step(k)
				cmd, isStr := asString(steps.values[k])
				match := packCommand.FindStringSubmatch(cmd)
				switch {
				case !slices.Contains(packSteps, step):
					say("\"steps\": \"" + clip(k, 40) + "\" isn't a step a pack names a command for; those are define, plan and review")
				case !isStr || match == nil || match[1] != m.Name:
					say("\"steps\": the " + k + " step must name one of the pack's own commands, /" + m.Name + ":<command>")
				default:
					m.Steps[step] = cmd
				}
			}
		}
	}
	if v, ok := root.values["documents"]; ok {
		m.Documents = documentsOf(v, m.Name, say)
	}
	if len(out) > 0 {
		return PackManifest{}, out
	}
	return m, nil
}

func documentsOf(v any, name string, say func(string)) *PackDocuments {
	docs, isObj := v.(*object)
	if !isObj {
		say("\"documents\" must be an object with folder, requirements and plan")
		return nil
	}
	var bad []string
	known(docs, []string{"folder", "requirements", "plan"}, "\"documents\"", &bad)
	for _, b := range bad {
		say(b)
	}
	d := &PackDocuments{}
	d.Folder, _ = asString(docs.values["folder"])
	d.Requirements, _ = asString(docs.values["requirements"])
	d.Plan, _ = asString(docs.values["plan"])
	if d.Folder != "."+name {
		say("\"documents\": \"folder\" must be \"." + name + "\", the pack's own folder, so its documents can't point at another file")
	}
	for _, f := range []struct{ key, value string }{{"requirements", d.Requirements}, {"plan", d.Plan}} {
		if !packDocName.MatchString(f.value) || strings.Contains(f.value, "..") {
			say("\"documents\": \"" + f.key + "\" must be a file name inside the folder, with no path")
		}
	}
	return d
}

func roleList() string {
	names := make([]string, len(review.Roles))
	for i, r := range review.Roles {
		names[i] = string(r)
	}
	return strings.Join(names, " and ")
}
