# AGENTS.md

Guidance for AI coding agents (and humans) working in **github.com/bezmoradi/software-factory** — an `api` project scaffolded by [Gopamin](https://github.com/bezmoradi/gopamin) using the Clean Architecture (Ports & Adapters) pattern. This file follows the [AGENTS.md](https://agents.md) convention.

## What this project is

The engine of Software Factory, a standalone Go product; `ARCHITECTURE.md` says how it works and why.

## Project layout

```text
cmd/
  main.go  # entry point: constructs adapters, injects them, starts the server
  server/  # HTTP server bootstrap
internal/
  core/
    shared/    # cross-cutting contracts: the logger port
    session/ gate/ review/ push/ workflow/ level/ agent/  # core areas (ARCHITECTURE.md "Code")
  adapters/  # implement the ports; the only place third-party libs live
    engines/{acp,codex}/         # agent protocol clients (Claude Code runs through the plugin and agent-host)
    sandbox/docker/  # the sandbox check and its probe
    install/npm/  # agent installs from npm and plugins (knackpack) from git, pinned versions and commits
    git/{files,push,remote,state}/  # reading .git files and writing review snapshots; pushing; cloning and fetching; the repository record, host tokens and review records
    http/page/  # the page server: routes, middleware, health probe
    agenthost/  # agent-host
    egress/  # the egress proxy
    loggers/slog/  # logging adapter (implements the logger port)
configs/  # loads .env into the process environment
tools/  # small project-root helpers
.env  # configuration — see Environment variables
.go-arch-lint.yml  # architecture-boundary rules (make arch)
.golangci.yml  # golangci-lint config — static analysis (make lint)
```

## The dependency rule

Source-code dependencies point **inward only**:

```
cmd  →  adapters  →  core
```

Inner layers must never import outer ones. Concretely:

- The core is grouped **by area** (`internal/core/session`, `gate`, `review`, `push`, `workflow`, `level`, `agent`): business rules and the ports (interfaces) the outside world implements. **No** framework, library, or transport imports — the standard library and other `internal/core/...` packages only — and **no serialization tags** (`json:"..."`): wire formatting is a transport concern the adapters own.
- `internal/core/shared/` — cross-cutting contracts (the logger) used across areas.
- `internal/adapters/` — the only place third-party libraries live: agent protocol clients, the sandbox, git reading and pushing, the page server, agent-host, the egress proxy, and loggers that implement the ports. An adapter depends on the core, never on another adapter.
- `cmd/` — the composition root: it constructs concrete adapters, injects them into the core, and starts the process.

Never import an `internal/adapters/...` package from `internal/core/...`.

### This rule is enforced

The dependency rule above is checked mechanically by `make arch` ([go-arch-lint](https://github.com/fe3dback/go-arch-lint), config in `.go-arch-lint.yml`). A violation **fails the build**. Fix it by moving the offending code to the correct layer — **never** by widening a `mayDependOn` list or adding an `anyVendorDeps` grant to the core. Loosening `.go-arch-lint.yml` to turn a red build green defeats the purpose of the architecture.

If go-arch-lint ever reports a dependency you can't find in your imports, it's almost certainly `deepScan` tracing a dependency-injection call in `cmd` — leave `deepScan: false` (the config enforces the import graph, not data-flow).
The generated CI pipeline (`.github/workflows/ci.yml`) already runs `make arch-check`, so a boundary violation blocks the build — `arch-check` exits non-zero on a violation and also fails if the linter has been silently disabled. Add it to a pre-push hook too if you want the feedback before you push.

## This project's stack
- HTTP: the standard library's `net/http` — the page server in `internal/adapters/http/page`
- Datastore: none
- Logger: `slog` — adapter in `internal/adapters/loggers/slog`

## Health

The page server answers the health probe outside its middleware chain, so probes stay cheap and unlogged:

| Method | Path | Purpose | Success |
|---|---|---|---|
| `GET` | `/healthz` | liveness — the process is running | `200 {"status":"ok"}` |

The page's own routes (`/api/…`, including `/api/review`, `/api/test-list`, `/api/push`, `/api/push-answer`, `/api/send-findings`, `/api/changes` and `/api/sign-in-expect`) need the page key, the page's `Host` and `Origin`, and JSON; other paths are `404`. The handlers live in `internal/adapters/http/page`.

## Middleware

Requests other than the health probe pass through a middleware chain composed in `internal/adapters/http/page` (`Server.Page`):

- **recover**: a panic becomes a logged `500` with its stack, not a crash. `http.ErrAbortHandler` passes through untouched, and a panic after the response started aborts the connection rather than appending a `500`.
- **request logging**: method, path, status, duration.

The checks every page route makes (the key, `Host`, `Origin`, JSON, body caps of 64 KiB, or 1 MiB on the approval routes, and field names) are in the page's own handler, not the chain: `ARCHITECTURE.md` "Page security checks" lists them. Handlers that wait set their own deadlines (`PollFor`, `pushFor`).

**Security headers** (`nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, a strict `Content-Security-Policy`) wrap the whole server, the health probe included.

## Running

```bash
make up            # start the Docker stack, wait until healthy, open the page (SF_PORT changes the port)
make dev           # make up from this folder's code, rebuilt on every save: the engine alone for a page change, all four for Go (dev/watch)
make down          # stop the Docker stack; volumes and their data stay
make run           # run the app (serves on :7799; override with PORT)
```

## Components

`ARCHITECTURE.md` describes the parts, the trust boundary and the flows, and its Reference section has the details to change them by: the image's stages, paths, users and modes, the Compose settings, and how repositories, agent installs, the sandbox check, knackpack, reviews, pushes, start-up files and sign-in work. Read the part you change before changing it.

## Hand checks with Compose

The page is published on 127.0.0.1:7799 (SF_PORT changes it):

```bash
docker compose up -d --build && docker compose ps            # all healthy
docker compose exec agent curl -sS https://github.com         # refused by the proxy (403)
docker compose exec agent curl -sS -o /dev/null -w '%{http_code}\n' https://api.anthropic.com   # any HTTP status
docker compose exec agent curl -sS --noproxy '*' http://reviewer:8090/healthz   # doesn't resolve: the reviewer isn't on agentnet
docker compose exec reviewer touch /reviews/x                       # Read-only file system
COMPOSE_FILE=compose.yaml:compose.dev.yaml docker compose up -d --build   # project software-factory-dev: the fakes, own volumes
```

In the development overlay, the fake reviewer (`dev/fakeclaude`) refuses a run whose `--settings` doesn't turn hooks off, block reads outside the working folder and deny `Read(~/**)`, or whose `--allowedTools` allows `Read`, `Grep` or `Glob`, and it reads trigger files in the snapshot's `head/`: `BLOCK-REVIEW` blocks; `SLOW-REVIEW` answers after 45 seconds, long enough to press Deny first; `SHALLOW-REVIEW` reports `num_turns` 1 (3 otherwise); `READ-OUTSIDE` reports a denied `Read` of `/home/reviewer/.claude/.credentials.json` in `permission_denials` without reading anything; `SEVERE-PASS` passes with a `### [High]` finding and a `| F1 | High | yes |` findings-table row; `TOKEN-IN-REVIEW` blocks with a token-shaped string it builds when it runs. With none of them it passes. Every reply reports `modelUsage` for `sf-fake-model`; when the prompt names `earlier/<name>.md`, the reply says whether that file is in the folder; and a run without `--agent` whose prompt says "manual test list" answers with a short list (with `TOKEN-IN-REVIEW` in `head/`, a list holding a token-shaped string). The fake ACP agent (`dev/fakeacp`) calls the tool a prompt names (`factory_push`, `factory_issue`, `factory_pull_request`, `factory_checks` or `factory_review_comments`; for an issue or pull request, the rest of that line is the title) and replies with its answer; echoes a note from the engine (a prompt that starts with `[Software Factory]` and a space); and for the prompt `stall` sends one chunk and stays in its turn until it is stopped.

The dev git host serves the seed repository at `https://gitserver.test:8443/dev/demo.git` (and the older `/demo.git`), asks for the development token in `GITSERVER_TOKEN`, and takes a new commit with `docker compose exec gitserver sf-gitserver commit "a new commit"` (with `COMPOSE_FILE=compose.yaml:compose.dev.yaml` exported, as the README says, so plain `docker compose` commands reach the overlay's project, `software-factory-dev`). Under `/api/v3` it fakes the part of GitHub's REST API the engine's GitHub adapter uses, for the repository `dev/demo`, with the same token as a Bearer token: the repository, branches, open pull requests by head, opening pull requests (drafts recorded) and issues, listing issues, issue comments, review comments, reviews, `/user` and a commit's check runs and statuses. Its subcommands: `comment <pr> <login> <text>`, `review-comment <pr> <login> <path> <line> <text>`, `review <pr> <login> <state> <text>`, `drafts on|off` (off: a draft is refused with GitHub's 422), `show <pr>`, `list`, `check <commit> <name> <status> [conclusion]`, `status <commit> <context> <state>`, `reset`, and the triggers `issue-delay <seconds>` (an issue is opened at once and answered after the delay), `issue-502 <count> created|lost` (the next issues answer 502, opened or not), `comment-403 <count>` (the next comments the engine posts answer 403) and `push-delay <seconds> before|after` (a push waits before it is applied, or is applied and its answer waits). Its environment: `GITSERVER_ROOT` (`/srv/git`), `GITSERVER_CA_DIR` (`/srv/ca`, where it writes `ca.pem` for `SF_DEV_GIT_CA`), `GITSERVER_LISTEN` (`:8443`), `GITSERVER_NAMES`, `GITSERVER_TOKEN` (required), `GITSERVER_LOGIN` (the login `/user` answers and new issues and comments are by, default `sf-dev-owner`) and `GITSERVER_API_STATE` (the API fake's state file, default `/srv/git/github.json`).

To see the check fail, restart egress with an override that sets its `EGRESS_ALLOW` to the default list plus `github.com` (the lists then differ), or attach the agent service to the `outside` network, or stop egress; each makes the next start fail with the reason and every running session Held.

## Environment variables

Configuration is loaded from `.env` at the project root (via `godotenv`). Real shell environment variables take precedence over `.env`, so you can override any key inline (e.g. `PORT=9000 make run`).

- `PORT` — HTTP listen port (default `7799`; the image sets 8080 inside the container).
- Engine, Docker edition: `SF_PAGE_HOST`, `SF_PAGE_NAME` (with `SF_PAGE_HOST=0.0.0.0`: the name whose addresses the page accepts besides loopback), `SF_PUBLIC_PORT`, `SF_REVIEWER_HOST` (agent-host in the reviewer container, `host:port` or `off`), `SF_REVIEWS` (the engine's path to the reviews volume), `SF_REVIEWER_REVIEWS` (the reviewer's path to it, the same by default), `SF_REVIEWER_PROXY` (the proxy on the reviewer network, default `egress-review:3128`), `SF_DEV_REVIEWER` (development only: the fake reviewer's path in the reviewer container, run in place of `claude`; the engine logs a warning when it is set), `SF_STATE_DIR`, `SF_WORKSPACE`, `SF_AGENT_ADDRESS`, `SF_AGENT_HOST`, `SF_EGRESS_PROXY`, `SF_EGRESS_STATUS`, `EGRESS_ALLOW`, `EGRESS_DENY`, `SF_SANDBOX_CHECK`, `SF_SANDBOX_OUTSIDE`, `SF_AGENT_WORKSPACE`, `SF_AGENTS_DIR`, `SF_AGENTS_INSTALL`, `SF_NPM_REGISTRY`, `SF_UMASK`, `SF_UPLOADS` (the engine's path to the uploads volume), `SF_AGENT_UPLOADS` (the agents' path to it), `SF_PLUGIN_DIR` (the Software Factory plugin in the agent container), `SF_AGENT_BINARY` (the `software-factory` binary in the agent container, for `mcp` and `sandbox-probe`, or `off`), `SF_DEV_ACP_AGENT`, `SF_DEV_SKIP_SIGNIN` (development only: `1` signs a page in without a code; start the development overlay with `SF_DEV_SKIP_SIGNIN=1 COMPOSE_FILE=compose.yaml:compose.dev.yaml docker compose up -d --build`) and `SF_DEV_GIT_CA` (development only: a PEM file of CAs the engine trusts for clones and fetches besides the system's; the engine logs a warning when it is set).
- agent-host: `SF_AGENT_HOST_MODE` (or `-mode`: `agent`, or `review` in the reviewer container), `SF_AGENT_HOST_KEY_FILE` (a file holding the key every request but `/healthz` must carry; Compose sets it in the reviewer container), `SF_WORKSPACE`, `SF_AGENT_HOST_LISTEN`, `SF_AGENT_HOST_MAX_PROCESSES`, `SF_ENGINE_AGENT_ADDRESS`, `SF_PLUGIN_SOCKET`, `SF_AGENTS_BIN`, `SF_GIT` (the git program for worktrees, default `git`), `SF_UMASK`. Roles in the agent container never read `.env`. agent-host gives each Claude Code session its own `SF_PUSH_TOKEN`, which the engine makes; don't set it.
- egress: `EGRESS_LISTEN`, `EGRESS_REVIEW_LISTEN` (a second proxy for the reviewers, with its own `EGRESS_MAX_CONNS`, so agents can't use up the reviews' connections; Compose sets `egress-review:3128`, and `EGRESS_LISTEN` to `egress-agents:3128`: each name is an alias on one network only, while the service name `egress` resolves on all of them), `EGRESS_STATUS_LISTEN`, `EGRESS_ALLOW`, `EGRESS_DENY`, `EGRESS_MAX_CONNS`, `EGRESS_IDLE_TIMEOUT` (default `5m`), `EGRESS_MAX_DURATION` (default `1h`).

The `.env` file is git-ignored and optional: a clone has none, and `make run` needs none.

## Checking a change

There are no automated tests (the owner's choice). Check a change with:

```bash
go build ./... && go vet ./...
make lint          # golangci-lint
make arch-check    # the layer rules
make fix-check     # Go's modernizers
make vuln          # govulncheck
make web-check     # the page's type check
make plugin-check  # the plugin's type check
make plugin-validate  # Claude Code's own plugin validator, offline (needs Docker)
make cross-build   # build and vet for linux/amd64, linux/arm64 and darwin/arm64
make no-comments   # no comments in code or configuration (CLAUDE.md)
make no-tests      # no automated tests
make files         # no file over 512 KiB, CRLF, conflict marker, case clash, inline script or style in the page, code naming a credential store, reading a model key or running git, or HTML built from strings
make workflows     # the review-workflow check, actionlint and zizmor (needs Docker)
make shell         # shellcheck over the shell scripts (needs Docker)
make markdown      # markdownlint-cli2 over the Markdown files, with .markdownlint-cli2.jsonc (needs Docker)
make links         # lychee, offline: links to files and headings resolve (needs Docker)
make secrets       # gitleaks over the working tree's files (needs Docker)
make spelling      # typos 1.52.0, which you install yourself
```

`make web-check` and `make plugin-check` need `npm ci --prefix web` once; the plugin check uses the page's TypeScript. `make plugin-validate` catches hooks modules Claude Code wouldn't load, which the type checks can't see; it needs a stack that has run once, since it uses the `software-factory:local` image and the Claude Code in the `software-factory_agents` volume (no network, no sign-in). `tools/plugincheck/` holds hand-written declarations of the part of Claude Code's plugin API that `plugin/hooks` uses (written against 2.1.322), so CI can type-check the plugin without Claude Code's own declarations, which Claude Code writes beside a plugin it loads (`plugin/.claude-plugin/types/`, git-ignored) and which aren't ours to publish; `make plugin-check` checks against both when both exist. When the plugin uses something new, declare it there the way the real declarations do. Move the golangci-lint pin with each Go minor release: a linter built for an older Go fails with "export data version … is greater than maximum supported version".

Then run it: stop the real stack (`docker compose down`), `export COMPOSE_FILE=compose.yaml:compose.dev.yaml`, `docker compose up -d --build`, open the page, and try the flow you changed with the development fakes (`dev/`). After a change to the page server, run the page's security checks (`ARCHITECTURE.md` "Page security checks") against the published port.

## CI

The workflows in `.github/workflows/` never push, publish an image or log in to a registry. Each sets `permissions: {}` and gives each job only what it needs, checks out with `persist-credentials: false` and has a `timeout-minutes`. Every action is pinned to a commit with its version beside it, every container to a digest, and every download to a version and a SHA-256.

- **`ci.yml`** (CI), on pull requests, pushes to `main`, Mondays at 06:17 UTC (advisories for pinned modules and actions appear without a change here) and by hand. A new push to a pull request cancels its older run. Its jobs, each a required check by its name:
  - **Go**: `go build`, `go vet`, `make lint`, `make arch-check`, `make fix-check`, `make vuln`.
  - **Cross-builds**: `make cross-build`, so files built only on another platform keep compiling. windows/amd64 is left out until `internal/adapters/git/files` and `git/state` build there (they use `syscall.O_NOFOLLOW` and `O_DIRECTORY`); add it to `CROSS_TARGETS` in the `Makefile` then, and the `!unix` files are compiled too.
  - **TypeScript**: `make web-check` and `make plugin-check`, with the Node the Dockerfile's page stage uses.
  - **Plugin**: `claude plugin validate plugin` with Claude Code 2.1.322, the version `internal/adapters/install/npm/versions.go` pins; the job fails when that file pins another. The binary comes from the release the official installer downloads (`downloads.claude.ai/claude-code-releases/<version>/linux-x64/claude`), checked against the SHA-256 in that release's `manifest.json`. The installer itself isn't used, since it first downloads and runs the latest release. No sign-in is needed.
  - **Image**: the `runtime` target for linux/amd64 and linux/arm64 with Buildx, kept in the builder's cache and never pushed. QEMU (`tonistiigi/binfmt`) and BuildKit are pinned by digest; Buildx is the runner's own.
  - **No comments**: `make no-comments` (`scripts/nocomments`, a Go program in its own module so the layer rules and `./...` don't include it). It reads Go with `go/parser` and the other kinds with small scanners that skip strings, template literals, regular expressions, heredocs and YAML block scalars (it scans the shell in `run:` blocks, Dockerfile `RUN` instructions and Makefile recipes). It allows only the exceptions in `CLAUDE.md`: `//go:`, `//nolint`, `//line` and `// Code generated … DO NOT EDIT.`; `/// <reference>` and `// @ts-…`; a shebang on the first line; `# syntax=`, `# escape=` and `# check=` at the top of a Dockerfile; `# zizmor: ignore[…]` and `# shellcheck` directives; and a `# v…` version comment on a line with a 40-character commit SHA or an `@sha256:` digest.
  - **No tests**: `make no-tests` (`scripts/check-no-tests.sh`) fails on `*_test.go`, `testdata/` and `__tests__/` folders, `*.test.*` and `*.spec.*` scripts, imports of Go's `testing` packages, `httptest` or testify and of `node:test`, and test frameworks in a `package.json`.
  - **Files**: `make files`: nothing over 512 KiB, no CRLF line endings, no conflict markers, no paths that differ only by case, no inline script, style or event handler in `web/index.html`, no Go or TypeScript outside `dev/` that names a credential store, reads a model key variable or runs `git` through `exec.Command`, and no `innerHTML`, `eval` or the like in the page or the plugin.
  - **Shell**: `make shell`: shellcheck at warning level, POSIX `sh`.
  - **Markdown**: `make markdown`: markdownlint-cli2 with every rule on except line length (MD013, one paragraph per line), `$` prompts (MD014), blank lines around headings and lists (MD022, MD032; GitHub renders them the same), fence languages (MD040, for prompts and plain text), table pipe alignment (MD060), and inline HTML other than the README's centered banner (MD033).
  - **Spelling**: typos 1.52.0 with `.typos.toml`, whose two words are correct where used: `ue`, a variable in `internal/adapters/git/remote/remote.go`, and `iy`, a regular expression's flags in `web/components/markdown.ts`.
  - **Workflows**: `make workflows`: `scripts/check-review-workflow.sh`, actionlint (with ShellCheck on every `run:` step) and zizmor with its default persona, given the job's read-only token so it runs its online audits too, such as known-vulnerable actions and commits no tag holds. The pedantic persona isn't used: it requires a comment on each permission, which `CLAUDE.md` forbids. `.github/zizmor.yml` allows `dangerous-triggers` for `claude.yml` and `workflow-guard.yml`, for the reasons below.
  - **Links**: lychee, offline, over the Markdown files.
  - **Secrets**: gitleaks 8.30.1 over every commit, so a secret removed later still fails. `.gitleaks.toml` adds a rule for the token `claude setup-token` prints, which the default rules miss, and allows one thing: the line `GITSERVER_TOKEN: sf-dev-gitserver-token` in `compose.dev.yaml`, the development git server's documented value. Another value there, or that line in another file, is still reported. The allowlist names the `generic-api-key` rule because gitleaks skips every file that an allowlist without `targetRules` names in `paths`, whatever its `condition`.
- **`claude-code-review.yml`** (Claude review, then Claude approval). Claude reviews every push to a pull request against `CLAUDE.md`, which the action restores from the base branch before Claude starts (the pull request's own copy goes to `.claude-pr/`), and `ARCHITECTURE.md`. `AGENTS.md` and `ARCHITECTURE.md` aren't restored, so a change to them is reviewed like any other. Every problem it confirms blocks, however minor: it comments on the lines and returns a verdict, and the job posts one summary comment, edited on each review. The review only reviews: its job has only `contents: read` and `pull-requests: write`, passes `github_token: ${{ github.token }}` (the Claude app's token could push), allows only `gh pr diff`, `gh pr diff --name-only`, `gh pr view` and the inline-comment tool, and denies Edit, Write, MultiEdit and NotebookEdit; `scripts/check-review-workflow.sh` fails if any of that changes or if an option that could commit, push or widen its permissions appears. **Claude approval** fails closed: drafts, forks (which get none of the repository's secrets; push a copy of the branch here instead), and a review that fails, times out or returns no verdict all fail it, and GitHub would count a skipped required check as passing, so it always runs.
- **`claude.yml`** (Claude) answers `@claude` in issues, pull requests and reviews. The action starts Claude only for people with write access, only the owner's comments (`include_comments_by_actor: bezmoradi`) reach its prompt, and on a pull request it restores `CLAUDE.md` and Claude Code's start-up files from the base branch first. It uses the Claude GitHub app's token, which can push branches; the ruleset keeps it off `main`. Its comment triggers run with the repository's secrets, which is acceptable because nothing runs the pull request's code.
- **`pull-requests.yml`** (Linked issue) fails a pull request whose description doesn't close an issue (`Closes #12`); editing the description runs it again. Dependabot's pull requests pass: GitHub sets the author's login, so nobody else can claim `dependabot[bot]`.
- **`workflow-guard.yml`** (Workflow guard) fails a pull request that changes `.github/`, `scripts/`, the `Makefile`, `.golangci.yml`, `.go-arch-lint.yml`, `.gitleaks.toml`, `.typos.toml` or `.markdownlint-cli2.jsonc` until the owner (`bezmoradi`; nobody else's labeling counts) adds the `review-override` label after its latest push. A `pull_request` run uses the pull request's own copy of those files, so such a change could make its own checks pass. The guard runs on `pull_request_target`, the base branch's copy, and only reads the pull request's files, labels, events and runs through the API; it checks out and runs nothing from it. A later push needs the label added again.
- **`.github/dependabot.yml`**: weekly, for updates out at least 7 days, one grouped pull request each for the actions, the Go modules, the page's npm packages and the Dockerfile's base images. Not covered, so update them by hand: the agent versions, integrity hashes and knackpack's commit in `versions.go` (with Claude Code's, update `VERSION` and `SHA256` in the Plugin job); `third_party/node/LICENSE` and setup-node's `node-version`, which must match the Node in the Dockerfile's base image; the gitleaks and typos downloads and the QEMU and BuildKit images in `ci.yml`; and the images and versions in the `Makefile` (actionlint, zizmor, lychee, gitleaks, typos, golangci-lint, go-arch-lint, govulncheck).
- **Vulnerable dependencies** are fixed by how much they put at risk: an advisory `make vuln` reports as reached in our code, or any Critical or High one in an image or the page's packages, within 7 days of it being published, with a release if one is out; any other advisory, including ones govulncheck lists as not reached, within 30 days, usually through the weekly Dependabot pull request. When no fixed version exists yet, record the advisory and what limits it in the pull request that next touches the dependency.
- **`.github/rulesets/main.json`**: no deletion or force push of `main`, changes only through pull requests merged by squash, and the checks above plus Claude approval required, with no bypass actors, so it binds admins too. It's only a file until applied.

What the owner sets on GitHub:

1. The `CLAUDE_CODE_OAUTH_TOKEN` secret, from `claude setup-token`, as an Actions secret and again as a Dependabot secret (runs Dependabot starts get only Dependabot secrets).
2. The Claude GitHub app on the repository, for `claude.yml`.
3. The label: `gh label create review-override --repo bezmoradi/software-factory --description "The owner has read this change to the checks"`.
4. Squash merging allowed in the repository's settings, then the ruleset: `gh api --method POST repos/bezmoradi/software-factory/rulesets --input .github/rulesets/main.json`. To apply a later change to the file, find its id with `gh api repos/bezmoradi/software-factory/rulesets --jq '.[] | select(.name == "main") | .id'` and run `gh api --method PUT repos/bezmoradi/software-factory/rulesets/<id> --input .github/rulesets/main.json`, which also removes any bypass added on GitHub.

## Adding a feature

1. Put the rule in the core area it belongs to, and the port (interface) it needs next to it.
2. Implement the adapter under the matching `internal/adapters/...` package.
3. Wire the concrete adapter into the composition root in `cmd/main.go`.
4. A new package under `internal/core/`, or an adapter folder outside the globbed categories (`engines/*`, `sandbox/*`, `install/*`, `git/*`, `http/*`, `loggers/*`), attaches to no component in `.go-arch-lint.yml` until you add one, so `make arch` fails until you do (deny-by-default).

## Conventions
- Keep everything under `internal/core/**` free of framework, database, and HTTP imports (`make arch` enforces this).
- Adapters translate between the outside world and domain types; they hold no business rules.
- Run `gofmt`, `go vet ./...`, `go fix ./...` (Go 1.27 modernizers; preview with `make fix-check`), `make lint` (golangci-lint), and `make arch` before committing.
- The stylesheets' layout is read by code, so keep it: each theme in `web/styles/themes.css` starts on a line of its own as `[data-theme="<id>"] {` (one selector, not indented) and sets `--label` and `--default`; each text size starts a line as `:root[data-scale="<percent>"]`. The engine finds themes and sizes with those patterns (`internal/adapters/http/page/assets.go`, `themeRule` and `scaleRule`) and refuses to save any other, and `web/lib/prefs.ts` matches the same selectors exactly. A theme written another way, such as in a selector list, silently drops out of the picker.
