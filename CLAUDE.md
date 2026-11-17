# Working on Software Factory

Read `ARCHITECTURE.md` for how the product works and why.

## Rules

- **No code reads, stores, logs or passes a model credential:** no token file, keychain entry or key variable, and no call to a vendor's OAuth endpoint. Every process the engine starts gets the key variables cleared (`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_API_KEY`, `CODEX_API_KEY` and the like). Reason: each agent must be the vendor's own unmodified binary, signed in by the person with the vendor's own mechanism; vendors' terms forbid products that reuse or route through those sign-ins. The one credential the engine holds is the git host token the person gives it for pushing. Three exceptions. Two are the owner's decisions of 2026-11-12: the page's Claude Code sign-in passes the one-time code the person pastes from claude.com to Claude Code's own `claude auth login` in a session's container or the reviewer container; and it passes an Anthropic API key the person pastes to that session's container's or the reviewer container's agent-host, which saves it in a file only that container's user can read and points Claude Code's own `apiKeyHelper` setting at it. The third is the owner's decision of 2026-11-13: a company LLM gateway's key or token the person pastes on the page is passed like the Anthropic key, to that session's container's or the reviewer container's agent-host, which saves it in the same file for the same `apiKeyHelper`. The engine keeps, logs and shows back none of them. Whether Anthropic's terms allow the first wasn't checked.
- **The engine never runs a `git` command line.** It reads `.git` files itself and pushes with a Go git library. Reason: a repository's configuration (`core.fsmonitor`, hooks, diff drivers, credential helpers) names programs git runs, and agents can write `.git`.
- **Nothing from the agent network is trusted as a decision, a verdict or a push.** The engine accepts only reports and requests from it, and refuses what the page doesn't offer. Reason: anything in the agent container, Claude Code's plugin included, may be run by an agent acting on planted instructions (`ARCHITECTURE.md` "Trust boundary").
- **After any change to the page server** (`internal/adapters/http/page`, `cmd/server`), **run the page's security checks with curl** (`ARCHITECTURE.md` "Page security checks") against the running server. Reason: they keep other sites and processes from driving the page, and only a real request shows the headers and limits actually sent.
- **No automated tests: don't add `*_test.go` files or `testdata`.** Reason: the owner chose no test strategy. Check a change with the build, `go vet`, `make lint`, `make arch-check`, `make fix-check`, `make vuln`, `make web-check`, `make plugin-check`, `make plugin-validate`, and a live run with `compose.dev.yaml`. A bypass a review finds is fixed and recorded in the change's description, since no test keeps it from coming back.
- **No comments, in code or configuration:** Go, TypeScript, JavaScript, CSS, HTML, shell, YAML (workflows and compose), Dockerfile, Makefile, TOML and the linters' configuration. Two exceptions: the version comment after a pinned commit SHA or image digest (`uses: actions/checkout@<sha> # v7.0.1`), which Dependabot and reviewers read, and tool directives, which the tools treat as instructions rather than comments (`//go:build`, `//go:embed`, `//nolint:…`, `// Code generated … DO NOT EDIT.`, shebangs, `# syntax=` in the Dockerfile). Reason: the owner's choice. Put an explanation in a name, in this file, a rule file in `.claude/rules/` or `ARCHITECTURE.md`, or in the change's description.
- **Commit only when the owner asks. Never push, add a remote or publish an image.** Reason: the owner decides what becomes public and when.
- **Outside libraries are allowed, pinned (`go.sum`, an npm lockfile) and under licenses compatible with MIT.** Reason: Software Factory is MIT-licensed.

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
    sandbox/boxes/   # the box helper: Docker's Engine API over its socket, the sessions' and reviewer containers' recipes, its HTTPS API and the engine's client
    install/npm/  # agent installs from npm and plugins (knackpack) from git, pinned versions and commits
    install/releases/  # the update check's anonymous calls to GitHub's REST API for the newest release
    git/{files,push,remote,state,workspace}/  # reading .git files and writing review snapshots; pushing; cloning and fetching; the repository record, host tokens and review records; the workspace volume's free space and worktree sizes
    git/keep/  # the engine's copy of each session's commits, and Restore branch
    http/page/  # the page server: routes, middleware, health probe
    agenthost/  # agent-host
    egress/  # the egress proxy
    loggers/slog/  # logging adapter (implements the logger port)
configs/  # loads .env into the process environment
tools/  # small project-root helpers
.env  # configuration — see .claude/rules/environment-variables.md
.go-arch-lint.yml  # architecture-boundary rules (make arch)
.golangci.yml  # golangci-lint config — static analysis (make lint)
```

## Running

```bash
make up            # start the Docker stack, wait until healthy, open the page (SF_PORT changes the port)
make dev           # make up from this folder's code, rebuilt on every save: the engine alone for a page change, the engine and egress for Go, and each session's container from its next start and each reviewer container from its start (dev/watch; a change to the box helper needs the image rebuilt)
make down          # stop the Docker stack; volumes and their data stay
make run           # run the app (serves on :7799; override with PORT)
```

## Components

`ARCHITECTURE.md` describes the parts, the trust boundary and the flows, and its Reference section has the details to change them by: the image's stages, paths, users and modes, the Compose settings, and how repositories, agent installs, the sandbox check, knackpack, reviews, pushes, start-up files and sign-in work. Read the part you change before changing it.

## Checking a change

There are no automated tests (the owner's choice). Check a change with:

```bash
go build ./... && go vet ./...
make lint          # golangci-lint
make arch-check    # the layer rules
make fix-check     # Go's modernizers
make vuln          # govulncheck
make pins          # each pinned npm package's build provenance and OSV advisories for the pins, from the npm registry and api.osv.dev
make web-check     # the page's type check and module rules (make web-arch)
make plugin-check  # the plugin's type check
make plugin-validate  # Claude Code's own plugin validator, offline (needs Docker)
make cross-build   # build and vet for linux/amd64, linux/arm64 and darwin/arm64
make no-comments   # no comments in code or configuration (CLAUDE.md)
make no-tests      # no automated tests
make files         # no file over 512 KiB, CRLF, conflict marker, case clash, inline script or style in the page, code naming a credential store, reading a model key or running git, or HTML built from strings
make instructions  # CLAUDE.md links every rule file in .claude/rules/, each rule file's paths: globs are quoted and match files, CLAUDE.md stays in its byte budget, and there's no AGENTS.md
make workflows     # the review-workflow check, actionlint and zizmor (needs Docker)
make shell         # shellcheck over the shell scripts (needs Docker)
make markdown      # markdownlint-cli2 over the Markdown files, with .markdownlint-cli2.jsonc (needs Docker)
make links         # lychee, offline: links to files and headings resolve (needs Docker)
make secrets       # gitleaks over the working tree's files (needs Docker)
make spelling      # typos 1.52.0, which you install yourself
```

`make web-check` and `make plugin-check` need `npm ci --prefix web` once; the plugin check uses the page's TypeScript. `make plugin-validate` catches hooks modules Claude Code wouldn't load, which the type checks can't see; it needs a stack that has run once, since it uses the `software-factory:local` image and the Claude Code in the `software-factory_agents` volume (no network, no sign-in). `tools/plugincheck/` holds hand-written declarations of the part of Claude Code's plugin API that `plugin/hooks` uses (written against 2.1.322), so CI can type-check the plugin without Claude Code's own declarations, which Claude Code writes beside a plugin it loads (`plugin/.claude-plugin/types/`, git-ignored) and which aren't ours to publish; `make plugin-check` checks against both when both exist. When the plugin uses something new, declare it there the way the real declarations do. Move the golangci-lint pin with each Go minor release: a linter built for an older Go fails with "export data version … is greater than maximum supported version".

Then run it: stop the real stack (`docker compose down`), `export COMPOSE_FILE=compose.yaml:compose.dev.yaml`, `docker compose up -d --build --remove-orphans`, open the page, and try the flow you changed with the development fakes (`dev/`). After a change to the page server, run the page's security checks (`ARCHITECTURE.md` "Page security checks") against the published port.

## How the instructions are organized

This file holds only what applies to every change. Claude Code loads it at every start and again after `/compact`. The rules for one part of the repository are in `.claude/rules/`, one file per subject, and Claude Code loads each when it reads or edits a file that the file's `paths:` globs match. Reason: a session working in one area gets that area's rules and doesn't pay for the rest.

| Rule file | Covers |
|---|---|
| [`dependency-rule.md`](.claude/rules/dependency-rule.md) | The layer rules for Go (`cmd → adapters → core`) and for the page's modules, how `make arch-check` and `make web-arch` enforce them, the stack, adding a feature, and the Go conventions |
| [`page-server.md`](.claude/rules/page-server.md) | The health probe, the page's routes and the middleware chain |
| [`web-page.md`](.claude/rules/web-page.md) | Where the page's modules live and how they're named and imported, the stylesheet layout the engine reads, and the page's accessibility baseline (WCAG 2.2 AA) |
| [`performance-budgets.md`](.claude/rules/performance-budgets.md) | The page's three time budgets and how to measure them in the containerized Chromium |
| [`compose-hand-checks.md`](.claude/rules/compose-hand-checks.md) | Hand checks of the Docker stack, Docker-in-Docker, the release's files and check, and what the development fakes do |
| [`gate-hand-checks.md`](.claude/rules/gate-hand-checks.md) | Hand checks of the trust boundary, the gate, reviews and pushes, with their setup |
| [`environment-variables.md`](.claude/rules/environment-variables.md) | Every setting of the engine, agent-host, egress, the box helper and the control channel, and the development switches |
| [`ci.md`](.claude/rules/ci.md) | The workflows and each CI job, the rulesets, what the owner sets on GitHub, and how to release |
| [`moving-a-pin.md`](.claude/rules/moving-a-pin.md) | What to check when a pinned agent, plugin, browser or release tool moves |

**Where does a new rule go?** Write the glob first: list the files a change to which makes the rule apply, then let that list pick the home. Reason: a rule's subject misleads, and its files don't.

- The files of one area: a rule file in `.claude/rules/` whose `paths:` names them. Add it to the file whose globs already name those files, or start a new one.
- Effectively every file: this file.
- A rule that must still hold after `/compact`: this file, whatever its files. Reason: only this file is loaded again after compaction; a rule file loads again only when Claude next reads a file it matches.

**Maintaining these instructions.**

- One rule, one home. Link to it from anywhere else instead of restating it. Reason: two copies drift apart, and Claude then picks one of them at random.
- Write each glob in double quotes, relative to the repository root. Reason: YAML reads an unquoted glob that starts with `*` as an alias and fails.
- Give each new rule file a row in the table above. Reason: it keeps the file findable before any file it matches has been read.
- After a change, check with `/context` that each rule file loads where you expect. Reason: a glob that matches nothing loads nothing, and nothing says so.
- `make instructions` checks the mechanical part: every rule file has a `paths:` list of quoted globs that each match a file, this file links every rule file and stays within `INSTRUCTIONS_MAX_BYTES` in the `Makefile`, and there's no `AGENTS.md` at the root.
- Rules live in this file and in `.claude/rules/` only: never in a `CLAUDE.md` in a subfolder, and never in an `AGENTS.md`. Reason: before Claude reviews a pull request, the review workflow's action restores the root `CLAUDE.md` and `.claude/` from the base branch and puts the pull request's copies under `.claude-pr/`, while every other file, a subfolder's `CLAUDE.md` included, comes from the pull request, which could then rewrite the rules it is reviewed against. An `AGENTS.md` is read by other tools and not by Claude Code while this file exists, so it would split the rules.
