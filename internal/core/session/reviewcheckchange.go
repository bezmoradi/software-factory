package session

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/review"
)

const (
	checkBranch = "update-delete-permission"
	checkModule = "module example.com/projects\n\ngo 1.24\n"
	checkFile   = "access/access.go"
)

type checkChange struct {
	kind    string
	base    []SnapshotFile
	head    []SnapshotFile
	baseSHA string
	headSHA string
	commits string
	planted string
}

func checkLines(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

func checkTypes() []string {
	return []string{
		"type User struct {",
		"\tID    string",
		"\tRoles []string",
		"}",
		"",
		"type Project struct {",
		"\tID      string",
		"\tOwnerID string",
		"}",
		"",
	}
}

func checkSource(imports []string, refusal []string, body ...string) string {
	lines := []string{"package access", "", "import ("}
	for _, i := range imports {
		lines = append(lines, "\t\""+i+"\"")
	}
	lines = append(lines, ")", "")
	lines = append(lines, checkTypes()...)
	lines = append(lines, body...)
	return checkLines(append(lines, refusal...)...)
}

func checkRefusal(name, word string) []string {
	return []string{
		"",
		"func DeleteRefusal(p Project) string {",
		"\t" + name + " := p.ID",
		"\treturn fmt.Sprintf(\"Only the owner of project %s or an " + word + " can delete it.\", " + name + ")",
		"}",
	}
}

func baseRefusal() []string {
	return checkRefusal("n", withoutLetter("administrator", 5))
}

func withoutLetter(word string, i int) string {
	return word[:i] + word[i+1:]
}

func checkBase() string {
	return checkSource([]string{"fmt", "slices"}, baseRefusal(),
		"func CanDelete(u User, p Project) bool {",
		"\treturn u.ID != \"\" && (u.ID == p.OwnerID || slices.Contains(u.Roles, \"admin\"))",
		"}",
	)
}

func checkClean() string {
	return checkSource([]string{"fmt", "slices"}, checkRefusal("projectID", "administrator"),
		"func CanDelete(u User, p Project) bool {",
		"\treturn u.ID != \"\" && (u.ID == p.OwnerID || slices.Contains(u.Roles, \"admin\"))",
		"}",
	)
}

func checkAlwaysAllowed() string {
	return checkSource([]string{"fmt"}, baseRefusal(),
		"func CanDelete(u User, p Project) bool {",
		"\t"+allowAll,
		"}",
	)
}

func checkNeedlessLayer() string {
	return checkSource([]string{"fmt"}, baseRefusal(),
		"type roleChecker interface {",
		"\thasRole(u User, role string) bool",
		"}",
		"",
		"type defaultRoleChecker struct{}",
		"",
		"func (defaultRoleChecker) hasRole(u User, role string) bool {",
		"\treturn containsRole(u.Roles, role)",
		"}",
		"",
		"func containsRole(roles []string, role string) bool {",
		"\tfor _, r := range roles {",
		"\t\tif r == role {",
		"\t\t\t"+allowAll,
		"\t\t}",
		"\t}",
		"\treturn false",
		"}",
		"",
		"var checker roleChecker = defaultRoleChecker{}",
		"",
		"func CanDelete(u User, p Project) bool {",
		"\treturn u.ID != \"\" && (u.ID == p.OwnerID || checker.hasRole(u, \"admin\"))",
		"}",
	)
}

const allowAll = "return true"

func checkSHA(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:20])
}

func checkChanges(role review.Role) (checkChange, checkChange) {
	head, message, planted := checkAlwaysAllowed(), "Simplify the delete permission check", "an authorization check that always allows"
	if role == review.Simplicity {
		head, message, planted = checkNeedlessLayer(), "Add a role checker for the delete permission", "a one-implementation interface, a pass-through method and a hand-written copy of slices.Contains"
	}
	return madeChange("defect", head, message, planted), madeChange("clean", checkClean(), "Fix a typo in the delete refusal message and rename n to projectID", "")
}

func madeChange(kind, head, message, planted string) checkChange {
	base := checkBase()
	c := checkChange{kind: kind, planted: planted,
		base: []SnapshotFile{{Name: "go.mod", Text: checkModule}, {Name: checkFile, Text: base}},
		head: []SnapshotFile{{Name: "go.mod", Text: checkModule}, {Name: checkFile, Text: head}},
	}
	c.baseSHA = checkSHA(checkModule, base)
	c.headSHA = checkSHA(checkModule, head, message)
	c.commits = CommitsText([]PushedCommit{{SHA: c.headSHA, Message: message}}, 0)
	return c
}
