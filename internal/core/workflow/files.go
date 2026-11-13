package workflow

import "regexp"

type fileReviewer struct {
	agent string
	re    *regexp.Regexp
}

var fileReviewers = []fileReviewer{
	{"knackpack:go-code-reviewer", regexp.MustCompile(`(^|/)go\.mod$|\.go$`)},
	{"knackpack:python-code-reviewer", regexp.MustCompile(`(^|/)(pyproject\.toml|setup\.(py|cfg)|Pipfile(\.lock)?|poetry\.lock|uv\.lock|pdm\.lock|pylock(\.[^/]+)?\.toml|pixi\.(toml|lock)|tox\.ini|\.python-version|MANIFEST\.in|(\.?ruff|pyrightconfig|mypy|pytest)\.(toml|ini|json)|\.flake8|[^/]*(requirements|constraints)[^/]*\.(txt|in|lock))$|(^|/)requirements/[^/]*\.(txt|in)$|\.(py[iwx]?|pxd|ipynb)$`)},
	{"knackpack:docker-reviewer", regexp.MustCompile(`(?i)(^|/)(Dockerfile|Containerfile)(\.[^/]*)?$|\.Dockerfile$|(^|/)docker-bake\.hcl$|(^|/)(docker-)?compose[^/]*\.ya?ml$`)},
	{"knackpack:kubernetes-reviewer", regexp.MustCompile(`(^|/)(Chart\.yaml|kustomization\.ya?ml)$`)},
	{"knackpack:react-code-reviewer", regexp.MustCompile(`\.(jsx|tsx)$`)},
	{"knackpack:terraform-reviewer", regexp.MustCompile(`\.(tf|tofu|tfvars)$|\.tftest\.hcl$|(^|/)terragrunt\.hcl$`)},
	{"knackpack:database-reviewer", regexp.MustCompile(`(^|/)(migrations?|migrate|alembic)/|\.sql$|(^|/)schema\.(prisma|rb)$`)},
	{"knackpack:typescript-code-reviewer", regexp.MustCompile(`\.(ts|tsx|mts|cts)$|(^|/)tsconfig[^/]*\.json$`)},
	{"knackpack:css-code-reviewer", regexp.MustCompile(`\.(css|scss|sass|less)$`)},
	{"knackpack:ui-code-reviewer", regexp.MustCompile(`\.(html?|css|scss|sass|less|jsx|tsx|vue|svelte|tmpl|jinja2?|erb)$`)},
}

var (
	docFile         = regexp.MustCompile(`(?i)\.(md|markdown|rst|adoc)$|(^|/)(LICENSE|NOTICE|AUTHORS|CONTRIBUTORS|COPYING|README|CHANGELOG|CHANGES|HISTORY)(\.(md|markdown|txt|rst|adoc))?$`)
	instructionFile = regexp.MustCompile(`(?i)(^|/)(\.claude|\.codex|\.agents?|agents|skills|commands|rules|prompts)/|(^|/)(CLAUDE|AGENTS)(\.[^/]*)?\.md$`)
)

func DocFile(path string) bool {
	if !docFile.MatchString(path) || instructionFile.MatchString(path) {
		return false
	}
	for _, r := range fileReviewers {
		if r.re.MatchString(path) {
			return false
		}
	}
	return true
}
