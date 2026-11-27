<h1 align="center">⚙️ Engineered, Not Vibe Coded</h1>

<p align="center">
  <img src="https://img.shields.io/badge/Engineered-not%20vibe%20coded-blue?style=for-the-badge" alt="Engineered, not vibe coded">
</p>

## #1 STOP VIBE CODING <img src="https://img.shields.io/badge/Merged-8957e5?style=flat-square&logo=git&logoColor=white" alt="Merged">

```diff
- vibe coded: shipped because it compiled
+ engineered: designed, reviewed, documented
```

<sub>✅ Approved by human reviewers · All checks passed</sub>

# Software Factory

Software Factory runs Claude Code in sandboxed containers on your own computer, one container per session, and lets it change code freely, while you follow and steer every session from one page in your browser. The agents can't reach your git host. Only the engine pushes their work, to a branch of its own, and only after independent reviewers have passed that exact commit. It runs in Docker as four containers plus one for each running session, and each session's agent uses your own sign-in with its vendor.

- Every agent session runs in a container of its own, with its own home, its own clone of the repository, and its own git worktree and branch, `sf/<id>`, in a Docker volume. A session's agent can't see another session's files, home or network. Nothing on your computer's disk is mounted into a session's container.
- A small helper container makes each session's container when the session starts and removes it when the session stops, always from the same fixed recipe. It is the only part that holds Docker's socket, and it takes requests only from the engine.
- The agents reach the internet only through a proxy that allows their model APIs and always refuses git hosts. The engine checks this before every start and every 5 minutes.
- Each reviewer runs in a short-lived container of its own that the agents can't reach, which holds only a read-only copy of the commit under review and is removed when that reviewer finishes, with [knackpack](https://github.com/bezmoradi/knackpack)'s bug hunter and simplicity checker, plus reviewers for the kinds of files the change touches.
- A push goes only to the session's own branch, never forced and never to the main branch, and every push request is written to an audit file.
- Each agent's permission requests, questions and plans wait on the page as cards for your answer.
- Software Factory never reads, stores or passes on an agent's sign-in. You sign each session's agent in yourself, with the vendor's own login, when the session starts.

## Table of Contents

- [What you need](#what-you-need)
- [Configuration](#configuration)
- [Run it with Docker](#run-it-with-docker)
- [Optional overlays](#optional-overlays)
- [Get started](#get-started)
- [Projects](#projects)
- [Sessions](#sessions)
- [Access and credentials](#access-and-credentials)
- [Reviews and pushes](#reviews-and-pushes)
- [Safety and cost](#safety-and-cost)
- [Maintenance](#maintenance)
- [Privacy](#privacy)
- [Development](#development)
- [Author](#author)
- [License](#license)

## What you need

- **Docker** on an `amd64` or `arm64` machine: Docker Desktop on macOS or Windows (on Windows, run the commands in a WSL terminal), or Docker Engine with the Compose plugin on Linux.
  - Docker Engine 28 or later (`docker version`). Older Engines let other machines on your network reach ports published on `127.0.0.1`.
  - Compose 2.24.4 or later (`docker compose version`), but not 5.1.4, which misreads the Compose files.
- **`curl`**, and `sha256sum` or `shasum`, for the install.
- **Memory:** each running session and each running reviewer may use 2 GiB and 2 CPUs; the engine 1 GiB, the proxy 256 MiB and the helper 64 MiB. At most 8 reviewers run at once; only `SF_MAX_BOXES` limits sessions. With 8 GiB for Docker, one session and three reviewers fit only below their limits. When memory runs out, Docker stops a session or a reviewer before the engine or the proxy.
- **Enhanced Container Isolation** in Docker Desktop, if your organization turns it on, blocks the helper unless an administrator allows it. Then no session starts.
- **A browser on the same computer.** The page is only on `127.0.0.1`. There is no server deployment.
- **A Claude subscription or an Anthropic API key.**
- **A repository reachable over `https://`.** Private repositories, and every push, need a token (see [Git host tokens](#git-host-tokens)).
- **Internet at first start**, for npm and GitHub. At every start the engine checks GitHub for a newer knackpack.

## Configuration

Most settings are on the page. These environment variables are read when the stack starts:

| Variable | Default | Purpose |
|---|---|---|
| `SF_PORT` | `7799` | The page's port on `127.0.0.1` |
| `SF_PREVIEW_PORT` | `7800` | The [preview](#preview)'s port on `127.0.0.1` |
| `SF_ALLOW` | `api.anthropic.com,claude.ai,claude.com,platform.claude.com` | Hosts agents and reviewers may reach, over HTTPS. Replaces the whole list |
| `SF_EXTRA_ALLOW` | empty | Hosts added to that list, such as `gateway.example.com` |
| `SF_EXTRA_DENY` | empty | Git hosts to refuse besides GitHub, GitLab and Bitbucket. If an allowed host covers a project's host, the sandbox check fails until that host is here |
| `SF_OSV` | empty | `off` stops the OSV lookups (see [Privacy](#privacy)) |
| `SF_NO_UPDATE_CHECK` | empty | `1` stops the daily release check (see [Update to Software Factory](#update-to-software-factory)) |
| `SF_BOX_MEMORY`, `SF_BOX_CPUS` | `2g`, `2` | Each session's memory (swap included), `256m` to `256g`, and CPUs, `0.1` to `256`. A session whose container runs out of memory stops and shows **stopped: out of memory**; Wake it, and run fewer programs in it at once or raise the memory |
| `SF_REVIEWER_MEMORY`, `SF_REVIEWER_CPUS` | `2g`, `2` | The same for each reviewer container |
| `SF_MAX_BOXES` | empty | The most sessions running or starting at once, `1` to `256`; empty is no limit |
| `SF_BOX_SUBNETS` | `10.213.0.0/20` | IPv4 networks for the sessions, one `/28` each, separated by commas. The helper won't start if they overlap a Docker network or Docker's default pools. Two stacks can share the range |
| `SF_BOX_RUNTIME` | empty | `runsc` runs sessions and reviewers under gVisor (see [Run the agents under gVisor](#run-the-agents-under-gvisor)) |
| `SF_VERIFY_MEMORY`, `SF_VERIFY_CPUS` | `4g`, `2` | Each verification run's memory, `256m` to `256g`, and CPUs, `0.1` to `256` |
| `SF_VERIFY_DISK` | `2g` | A verification run's file space, `64m` to `256g`, at most `SF_VERIFY_MEMORY` |
| `SF_VERIFY_MINUTES` | `15` | A verification run's time limit, `1` to `120` minutes |
| `SF_DOCKER_SOCKET` | `/var/run/docker.sock` | Docker's socket, which the helper mounts. Rootless Docker has another one |
| `SF_DOCKER_GID` | `0` | The socket's group: `stat -c %g /var/run/docker.sock` on Linux, `0` on Docker Desktop. `make up` fills it in |
| `SF_HOME` | `~/software-factory` | The install script's folder (read by the script only) |

Set a variable before the command (`SF_PORT=8080 docker compose up -d --remove-orphans`), or `export` it. With the install script, put it after the pipe: `curl -fsSL … | SF_PORT=8080 sh`. Give the variables on every `up`, or they go back to the defaults; the install script saves `SF_DOCKER_SOCKET` and `SF_DOCKER_GID` for you.

**Changing a variable recreates the containers that read it, and stops the sessions until you wake them.**

`SF_ALLOW` and `SF_EXTRA_ALLOW` are only starting values. Once you save [Network access](#network-access), its list (`egress.json`) wins. To go back, move `egress.json` out of the `engine-state` volume and restart the engine.

### Team file

`software-factory.json` at the root of the main branch holds team rules. Only main's version counts. It counts only after you approve it with **Review the team file** in the Execution path panel, and again after each change. It's strict JSON with `"version": 1` and these optional keys:

| Key | Effect |
|---|---|
| `adapter` | `"knackpack"`, the only workflow |
| `workTypes` | Extra work types: `id` (not `change`, `incident` or `explore`), `name`, `steps` (`define`, `plan`, `implement`, `review`, `push`), `edits` (`"any"`), `push` (true or false), `roles` (`bug-hunter`, `simplicity`) |
| `defaultType` | The work type new sessions start as |
| `agents` | Up to 20 installed reviewers every push needs |
| `fileRules` | Up to 20 rules: a path `pattern` with an `agent` that must review it, an `instruction` for its reviewers (one line, up to 300 characters), or both. A file whose instruction looks like a secret is refused |
| `protect` | Up to 50 path patterns. Every Claude Code write to one asks on a card, with no "don't ask again". It never holds a push |
| `rounds` | The review round limit, 1 to 20. The lower of this and the project's setting applies |
| `verify` | `["check"]`, `["browser"]` or both: every push waits for a passing run (see [Verification runs](#verification-runs) and [Browser checks](#browser-checks)), unless you allow it with a reason |

Patterns use `*`, `**` (any number of folders), `?` and `[...]` from the repository's root:

```json
{
  "version": 1,
  "fileRules": [
    { "pattern": "pricing/**", "instruction": "Every price shown to a customer must be the all-in price, taxes and fees included." },
    { "pattern": "migrations/**", "agent": "knackpack:database-reviewer" }
  ],
  "protect": ["pricing/rates.json", ".github/workflows/**"]
}
```

`roles` and `checks` are refused: only the project's pack or your own agent files fill the reviewer roles, and a team file can't name commands to run.

## Run it with Docker

### Install a release

**This command runs the install script without checking it: whoever can change this repository's releases decides what runs.** To check it first, use [Check what you install](#check-what-you-install).

```bash
curl -fsSL https://github.com/bezmoradi/software-factory/releases/latest/download/install.sh | sh
```

The script checks Docker and Compose, installs into `~/software-factory` (or `SF_HOME`, which can't be a git clone), downloads the release's `compose.yaml` and `compose.release.yaml` and checks their SHA-256, writes `software-factory-socket.yaml`, and runs `docker compose -p software-factory -f compose.yaml -f compose.release.yaml -f software-factory-socket.yaml up -d --wait --remove-orphans`. It changes nothing if a check fails, keeps the volumes, and prints the page's address.

It starts `engine` (the page and the gate), `helper` (makes the containers) and `egress` (the proxy). Each session runs in `software-factory-box-<box>`, where `<box>` is the id in the page's commands.

**To upgrade, run the command again** with the same variables. For one version: `curl -fsSL https://github.com/bezmoradi/software-factory/releases/download/v<version>/install.sh | sh`.

**After a release install, add `-p software-factory` after `docker compose`** in these docs' commands. `up` and `run` also need `-f compose.yaml -f compose.release.yaml -f software-factory-socket.yaml`, in the install folder. The page's own commands work from any folder.

**`software-factory-socket.yaml` gives the helper control of Docker, which is close to control of the computer.** It holds only the socket's mount (`SF_DOCKER_SOCKET`) and group (`SF_DOCKER_GID`). The script refuses an `SF_DOCKER_GID` that isn't digits, and an `SF_DOCKER_SOCKET` that isn't an absolute path of letters, digits, `.`, `_`, `/` and `-`. The helper takes requests only from the engine. With the wrong group the helper stays unhealthy and no session starts; on Docker Desktop for Linux or WSL, set `SF_DOCKER_GID` to the group containers see and run the script again.

### Check what you install

With a signed-in [`gh`](https://cli.github.com/), check that this repository's release workflow built the script for that tag, then run it:

```bash
curl -fsSLO https://github.com/bezmoradi/software-factory/releases/download/v<version>/install.sh
gh attestation verify install.sh -R bezmoradi/software-factory --signer-workflow bezmoradi/software-factory/.github/workflows/release.yml --source-ref refs/tags/v<version>
sh install.sh
```

Without `--source-ref`, an older release's script passes too. Put variables before `sh`: `SF_HOME=/srv/sf sh install.sh`. Release notes and the **Update to** panel give these lines filled in.

To check the image (optional), take the digest from the `image:` line of your `compose.release.yaml`, and use [cosign](https://docs.sigstore.dev/cosign/system_config/installation/):

```bash
cosign verify ghcr.io/bezmoradi/software-factory@sha256:<image digest> --certificate-identity https://github.com/bezmoradi/software-factory/.github/workflows/release.yml@refs/tags/v<version> --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify oci://ghcr.io/bezmoradi/software-factory@sha256:<image digest> -R bezmoradi/software-factory
gh attestation verify oci://ghcr.io/bezmoradi/software-factory@sha256:<image digest> -R bezmoradi/software-factory --predicate-type https://spdx.dev/Document/v2.3
```

These check the signature, the build provenance and the SBOMs. They cover the image, not the agents, which the engine checks against pinned hashes.

### Build from source

To develop, or to run code you've read:

```bash
git clone https://github.com/bezmoradi/software-factory.git
cd software-factory
make up
```

Run later commands in that folder. **`compose.yaml` mounts Docker's socket into the helper too.** `make up` fills in `SF_DOCKER_GID`, waits until the stack is healthy and opens the page. Without `make`, on Linux: `export SF_DOCKER_GID=$(stat -c %g /var/run/docker.sock)`, then `docker compose up -d --build --wait --remove-orphans`.

### First start

1. **Wait a few minutes for the agents.** The engine downloads Claude Code 2.1.322 from npm and knackpack from GitHub, each checked against a hash. If a download fails, see **Install the agents again** in [Other tasks](#other-tasks).
2. **Open the page:** <http://127.0.0.1:7799>, or your `SF_PORT`.
3. **Sign the reviewers in** (see [Sign Claude Code in for the reviewers](#sign-claude-code-in-for-the-reviewers)).
4. **Set the Git name and email** (see [Git name and email](#git-name-and-email)).
5. **Add a repository and start a session** (see [Usage](#usage)).

**knackpack isn't pinned: whoever can push to its `main` decides what your reviewers are told.** The owner accepted this risk. At every start the engine installs `main`'s newest head, unless it is older than 0.40.0 or could run programs in the reviewer container; then it keeps the commit it has and says why on the page. A session keeps its knackpack across Stop and Wake. **Agent versions** in [Diagnostics](#diagnostics) shows the result.

### Sign Claude Code in for the reviewers

Open it with **Reviewers’ sign-in** in the command palette.

1. Press **Sign in**, then **Open claude.com to sign in**, and sign in.
2. Paste the code claude.com shows and press **Complete sign-in**.

Claude Code's own `claude auth login` runs in a temporary container; Software Factory doesn't keep the code. The login is in the `software-factory-reviewer-home` volume, shared by all reviewers, so you sign in once. While all 8 reviewer containers are busy, the dialog shows the last check.

### Sign Claude Code in for this session

Each new session signs in once. The dialog opens at **Start**, or when a wake finds no sign-in. Choose **Subscription** (a claude.com code) or **API key**. Or, in a terminal: `docker exec -it software-factory-box-<box> claude`, then `/login`; the dialog notices within 30 seconds. **Cancel** stops the start. The login is kept in `software-factory-home-<box>`, through Stop and Wake.

## Optional overlays

### Run the agents under gVisor

On Linux, `compose.gvisor.yaml` runs sessions and reviewers under [gVisor](https://gvisor.dev) (`runsc`), so a kernel exploit meets gVisor first. The engine, helper and proxy stay on Docker's runtime. Not for Docker Desktop. Without `runsc`, starts are refused; it never falls back.

1. Install gVisor (Linux 5.6+), from gVisor's guide:

   ```bash
   (
     set -e
     ARCH=$(uname -m)
     URL=https://storage.googleapis.com/gvisor/releases/release/latest/${ARCH}
     wget ${URL}/gvisor.tar.zstd ${URL}/gvisor.tar.zstd.sha512
     sha512sum -c gvisor.tar.zstd.sha512
     sudo tar --zstd -xf gvisor.tar.zstd -C /usr/local/bin
     rm -f gvisor.tar.zstd gvisor.tar.zstd.sha512
   )
   ```

2. Register it and test it:

   ```bash
   sudo /usr/local/bin/runsc install
   sudo systemctl reload docker
   docker run --rm --runtime=runsc hello-world
   ```

3. **Starting the overlay recreates the helper, which stops running sessions; Wake brings them back.**

   ```bash
   export COMPOSE_FILE=compose.yaml:compose.gvisor.yaml
   docker compose up -d --build --wait --remove-orphans
   ```

   To go back, unset `COMPOSE_FILE` and run `make up`.

With a release, give `SF_BOX_RUNTIME=runsc` to the install script and on every `up`.

Limits: only the fixed names in `/etc/hosts` resolve (no DNS); git, installs and builds are slower; a release, a real session and a review under gVisor aren't checked yet.

### A headless browser for screenshots

`compose.browser.yaml` adds Chromium to the sessions' image, so an agent can screenshot the UI it builds. It builds from source only. **It recreates the helper, which stops running sessions.**

```bash
export COMPOSE_FILE=compose.yaml:compose.browser.yaml
docker compose up -d --build --wait --remove-orphans
```

With gVisor: `COMPOSE_FILE=compose.yaml:compose.gvisor.yaml:compose.browser.yaml`. With the development fakes: set `SF_BROWSER_STAGE=dev-browser` and list `compose.dev.yaml` before `compose.browser.yaml`.

- Chromium 156.0.8078.79 and its fonts come from snapshot.debian.org as of 2026-11-03 01:35:55 UTC. To update, move the date and versions in the `Dockerfile`'s `browser` stage together. The image is about 700 MB larger.
- Chromium's own sandbox is off; the container's hardening isolates it.
- Example: `chromium --headless --window-size=1280,800 --screenshot=<file> http://localhost:3000`.
- Agents save screenshots in `/workspace/s/<box>/screenshots/<session id>/`, outside the worktree, so they are never committed or pushed. They're deleted with the session.
- **Screenshots** above the conversation lists the newest 50 and updates itself.
- The engine shows only PNG, JPEG or GIF files up to 10 MiB and 8,192 pixels a side, re-encoded as PNG. It's not a live preview.

## Get started

### Usage

1. **Open the page** at <http://127.0.0.1:7799>. Welcome lists the sessions; search and filters are kept in the address.
2. **Sign the reviewers in** (see [Sign Claude Code in for the reviewers](#sign-claude-code-in-for-the-reviewers)).
3. **Set the Git name and email** (see [Git name and email](#git-name-and-email)). No session starts until you do.
4. **Add a project:** under **Projects**, save a token (see [Git host tokens](#git-host-tokens)), enter the `https://` address and press **Add repository**.
5. **Start a session:** **New session** on the project's row, then **Start** (see [New session](#new-session)).
6. **Sign it in and trust the repository** when asked (see [Sign Claude Code in for this session](#sign-claude-code-in-for-this-session) and [Trusting a repository](#trusting-a-repository)).
7. **Write the first message and pick a work type** (see [Execution path (with knackpack)](#execution-path-with-knackpack)). Answer the agent's cards.
8. **Review and push:** **Review**, then **Push** (see [Reviews and the push gate](#reviews-and-the-push-gate)).
9. **Get the work:** `git fetch origin sf/<id>`, then `git switch sf/<id>`, or open a pull request from that branch.

**Any program on this computer can use the page, including approving pushes** (see [Trust boundary](#trust-boundary)). One tab is signed in at a time; **Use this tab** takes it back. The page signs in again after 15 minutes with no tab open, after 24 hours, or after a restart.

**Settings** (the gear) and the Welcome page's **Start** list open Network access, Credentials, Workspace, Register of exceptions, Export and import and Diagnostics. Settings also has the theme (eight), the bar's side, text size and desktop notifications.

### Git name and email

Set the name and email agents commit with. No session starts or wakes until you do. They become public with each push, so on GitHub use your noreply address (`ID+USERNAME@users.noreply.github.com`, from GitHub's Settings › Emails). The dialog opens by itself on the Projects page until they're set, and it's in Settings (the gear). New sessions use a change at once; stopped ones use it when they wake.

### Trusting a repository

Each new Claude Code session asks at **Start** whether you trust the folder: press **Yes, I trust this folder**. The answer lasts through Stop and Wake. If the clone folder holds start-up files, you approve them first; **Cancel** leaves it untrusted.

If Claude Code shows a screen the engine doesn't know, the page tells you. Answer in a terminal: `docker exec -it -w /workspace/s/<box>/repos/<name> software-factory-box-<box> claude`, choose **Yes, I trust this folder**, then `/exit`.

## Projects

A project is a repository the engine clones from an `https://` address. There are no local folders, so an agent can't plant git hooks that run on your computer. SSH, `http://`, and addresses with a user, password, query or fragment are refused.

- The row shows the start branch and the instruction file. Its session count shows the sessions.
- **Remove**, in the ⋯ menu, needs no sessions (it's greyed until then) and deletes the clone. Adding the same repository again brings back its settings.

**Splitting a goal.** An agent can propose up to 8 tasks with `factory_split`, as cards you can edit or dismiss. **Start** starts a session from main with the card's text as its first message. At most 3 run at once; the rest queue (**Remove from queue** takes one out). Nothing starts until you press Start.

**Another repository.** An agent proposes a task with the other repository's address. **Add project and start** adds it, then starts the session. Nothing is added from the agent's call alone.

### New session

**New session** on a project's row opens the form:

- **Start from:** main (the default), a branch, a pull request's branch (GitHub only), or another session's commit. The engine refuses a start point that moved since you chose it.
- **Model** and **Effort:** Opus and high by default; **Default** sends none.
- **Session name:** optional.
- **Begin with:** a blank session or an issue (below).
- **Expected sign-in:** a start on another kind is refused (see [Cost and models](#cost-and-models)).

The session gets its own branch, `sf/<id>`. It starts in asking mode; auto and bypass can't be chosen. **New session from this one** (⋯ menu) and **New session from here** (in Changes) preselect a commit.

Before it starts, the engine makes the session's container, checks that it can't reach a git host, shows the start-up files for approval, checks the sign-in, and asks you to confirm the cost when it's billed per use.

**Start-up files** can change what Claude Code may do without asking: `.claude/settings.json`, `.claude/settings.local.json`, `.mcp.json`, `.claude/agents/`, `commands/`, `skills/`, `workflows/`, `hooks/`, and `.claude/skills/` in any subfolder. **Read them before you approve.** Hidden characters show as `⟨U+…⟩` or `\u{…}`. Risky lines are marked: `allowed-tools:` with Bash, inline shell, `hooks:`, and scripts in skills. Any change asks again. Changed instruction files (`CLAUDE.md`, `AGENTS.md`, `.claude/rules/`) are listed for information; the search skips `.git`, `node_modules`, `dist`, `build`, `out`, `target`, `vendor`, `coverage`, `__pycache__` and `venv`.

**From an issue.** **An issue** lists open issues, newest first, 30 to a page; or type a number in **Issue #** and press **Pick**. At **Start** the engine reads the issue again and refuses it if it changed. It's sent as a task, not as instructions. Closed issues and issues with token-like text are refused. The pull request says `Closes #N`.

### Git host tokens

The engine pushes, and opens issues and pull requests, with a token you save under **Git host tokens**, beside Projects.

**A pushed branch runs the repository's workflows with the repository's secrets.** Read workflow changes before you allow a push (see [CI workflows on the agents' pushes](#ci-workflows-on-the-agents-pushes)).

Pick **GitHub** or **GitLab**, then **Create a GitHub token** (or **Create a GitLab token**) opens the host's form filled in. GitLab gets `read_repository` and `write_repository`; issues, pull requests and CI work on GitHub only. On GitHub by hand, make a fine-grained token for only the repositories you add, with:

| Permission | Needed for |
|---|---|
| Contents: Read and write | Clone, fetch, push |
| Metadata: Read-only | Set by GitHub |
| Issues: Read and write | Issues, the issue list |
| Pull requests: Read and write | Pull requests, comments, **Ready for review** |
| Actions and Commit statuses: Read-only | CI results |
| Workflows: Read and write | Only if agents may change `.github/workflows/` |
| Administration: Read-only (optional) | Main's review rules |

A missing permission turns off only its feature. One token per host; it's never shown, logged or sent to another container. Public repositories clone without a token; pushing always needs one.

### Project settings

Open it with **Project settings** in the project's ⋯ menu, or from the command palette. It updates every 5 seconds and keeps unsaved forms. The three git-host parts read the host once per opening.

#### Check and setup commands

Two one-line commands of up to 300 bytes, such as `npm ci` (setup) and `make check` (check). Empty is off. They're set on the page only and run in the session's container with model keys cleared.

- **Setup** runs once per session, up to 15 minutes, after you approve the start-up files.
- **Check** runs when a turn ends, up to 5 minutes. On failure the agent gets the last 8 KiB of output, the path of the whole output, and one more try.
- Each run's whole output is kept in a file in the session's container, under `/tmp/software-factory/commands/`, where the agent can read it. It is never in the worktree, so it is never committed. Up to 4 MiB is kept; past that, the first 1 MiB and the last 3 MiB, with a line that says how many bytes were left out. The next run of the same command replaces the file, and it goes when the container stops.
- **Setup** and **Check** in [Session settings](#session-settings) show the end of the output. **Show all output** there opens the whole output, up to 1 MiB of it, with text that looks like a token hidden. Past 20,000 lines it shows the last 20,000; **Copy** copies all of it.

**The check in the session is advisory: it never counts for a push.** A verification run does.

#### Verification runs

A verification run runs the check command on a clean copy of one commit, in a container with no network. Only its exit code counts.

**Put no secret in the image, a variable or a file: agent-written code reads everything in it.**

1. Set **Image for verification runs** to an image with the project's tools, pinned by digest (`node@sha256:…`).
2. Pull it first: `docker pull <name>@sha256:<digest>`. It needs a POSIX shell with `mkdir`, `cp` and `cat`; the command runs as user 10006.
3. Press **Verify this commit**, or set `"verify": ["check"]` in the team file (see [Team file](#team-file)).

- Only tooling variables (`PATH`, `*_HOME` and the like) are kept, as a best-effort backstop, not a filter.
- Limits: 15 minutes, 4 GiB of memory, 2 CPUs, 2 GiB of files (`SF_VERIFY_*`). The engine checks free space first.
- Two run at once; one more queues per session.
- A push starts a run only when it could go out: not after Allow or Deny, not for a work type that doesn't push, and not while it waits for your approval. A push you pressed stays held: press **Push** again after the run passes.
- A run cut off by a restart isn't restarted: press **Verify this commit**.
- It counts only for that commit, command and image. **A failure, timeout, out-of-memory, restart or stop is never a pass, and a pass doesn't prove the tests are good: the agent wrote them.**

#### Browser checks

A browser check runs the project's **Browser command** (empty is off) like a verification run. Press **Run the browser check**, or set `"verify": ["check", "browser"]`.

**Never put a real or production login in the image or the command.** Tests sign in with seed-data users.

- The image needs a browser at `/usr/bin/chromium`, `/usr/bin/chromium-browser`, `/usr/bin/google-chrome` or `/ms-playwright`, and `/out` owned by user 10006: `RUN install -d -o 10006 -g 10006 /out`.
- For Playwright, set `launchOptions.executablePath`, or `PLAYWRIGHT_BROWSERS_PATH=/ms-playwright`.
- Example: `chromium --headless --dump-dom file://$PWD/index.html | grep -q '<h1>ok</h1>' && chromium --headless --window-size=800,600 --screenshot=$SF_OUT/home.png file://$PWD/index.html`.
- Files in `$SF_OUT` are kept: up to 20 images of 10 MiB and `.zip` traces of 50 MiB, 100 MiB a run, for the latest 3 runs and 1 GiB per project. They're deleted with the session or project and never exported.
- The images are the test's claim, not proof.

#### Caps

**Caps** stop a runaway session. Set them here for new sessions, or in a session's [Session settings](#session-settings). Empty or 0 is off; none is set at first.

| Cap | Counts | Most |
|---|---|---|
| Spending | The cost the agent reports | $100,000 |
| Time | Minutes since start, idle included | 43,200 |
| Turns | Turns | 10,000 |
| Disk | GiB in the session's folder, measured every 2 minutes | 4,096 |

You get a warning at 75% and 90%. At a cap the turn is interrupted and automatic work waits until you press **Raise the cap** or **Stop**. At the disk cap, delete files with `docker exec`, then press **Check again**. **The spending and disk caps are soft:** an agent can report less than it spends, write fast between measurements, or hide files in a folder the engine can't read. Each cap reached goes in the audit file.

#### Review depth

**Review depth** only adds reviews. It's set on the page only.

- **Big change:** more than the number of changed lines you set; 0 is off. Set it first, or **Big changes only** is refused. An uncountable change counts as big.
- **Twice** runs blocking reviewers twice on big changes: findings are pooled, a block in either blocks, and one Override clears both.
- Each run uses the reviewers' sign-in.

##### Reviewers

The work type's reviewers are locked as **Always (required)**. **Add reviewer** adds up to 5 in all from the pack: **Always**, **Big changes only**, or **Warn only** (the default, which never holds a push). A reviewer that is no longer installed is marked.

#### New packages

Off by default. When on, the push card marks new packages that don't exist on npm, PyPI, proxy.golang.org or crates.io, or are under 30 days old. Only package names are sent, at most 20 per push.

#### Ready for agents

A checklist read from main's latest commit:

| Row | Looks for |
|---|---|
| Instruction file | `CLAUDE.md` or `AGENTS.md` |
| Tests | A test script or target, or `go.mod`, `pyproject.toml`, `pytest.ini`, `Cargo.toml` |
| CI | `.github/workflows/` |
| Linter | `.golangci.yml`, ESLint, Ruff, RuboCop or Biome config |
| Check command | The project's own |
| Check on main | Its last result on main |

**Run on main** runs the check in an untrusted verification container. It counts for no push, and nothing in the list holds or allows anything.

#### CI workflows on the agents' pushes

Lists main's workflows that start on a push to `sf/<id>`, and marks those with secrets or `id-token: write`.

**They run with the repository's secrets on agent-written code.** To keep one off the agents' branches, add `branches-ignore: ['sf/**']` under its `push:` trigger on main. The push card lists them again for each commit.

#### Protection of main

Three rows: **Protected**, **Reviews required** and **Status checks required**. Without Administration: Read-only, a row says "unknown". "At least" means more may be required.

**The push gate stops at the session branch. Merging into main relies on your git host's branch protection.**

#### Team rules from main

Lists main's team file instructions and protected paths, and whether you approved that version (see [Team file](#team-file)).

#### Delivery and reviewer numbers

Pushes, time to first push, review rounds, allowed, denied and overridden counts, pull requests and cost. The window moves as the audit rotates (500 records). **Cost is the agent's own report; on a subscription it isn't a bill.** "After review" counts (CI failures and comments after a push) are counts, not defects, read only while a pull request is open.

##### Per reviewer

One row per review role: reviews, passes and blocks; the tooltip has the rest.

#### Decision log

What you approved, overrode, allowed or denied, and merged pull requests, with no agent text. Name, reword, hide or add entries; your edits survive rebuilds. Each new session gets it with its first message, up to 4 KiB; token-like entries are withheld.

#### Lessons from reviews

Blocking findings that keep coming back, matched approximately. After one blocks 3 commits, **Suggest lines** runs one read-only reviewer that proposes lines for the instruction file. Nothing applies on its own.

##### Suggested lines

**Read each line before you approve it: it came from agent and reviewer text.** **Decline** means it isn't offered again.

##### Approved lines

An approved line goes to a session you pick, which commits it to `CLAUDE.md` or `AGENTS.md` and gets it reviewed.

## Sessions

### Working in a session

Type to the agent; Enter sends. Follow its work in the side panel (execution path, reviews, push, changes).

| Feature | What to know |
|---|---|
| Messages | A message to a stopped session wakes it; up to 20 wait 2 minutes. `!` lines aren't run |
| Top bar and footer | The top bar shows the session's state and how many background tasks run, and its mode only when it isn't the one it started in (Manual). Point at it for the session ID and mode; [Session settings](#session-settings) shows them too. In a Claude Code session, the footer's token count includes cache reads and writes, as the cost estimate does |
| `/` commands | Skills and plugin commands, and `/clear`, `/compact`, `/context`, `/cost`, `/usage`, `/help`, `/status`, `/review`, `/security-review`, `/init`, `/todos`, `/export`, `/release-notes`. Others: in Claude Code itself |
| `@` files | Worktree files; secret-like names hidden. Opens files up to 512 KB |
| Attachments | 4 images (resized to 1,568 px and 2.5 MiB) and 4 files of 10 MiB; removed after 1 hour if not sent |
| Cards | Permissions, questions and plans wait for you. Closing the tab doesn't answer |
| **Away** | Denies new permission cards for you; off by default |
| Hidden text | Output with control characters is marked "hides text" |
| Folded reads, subagents | Read, Grep and Glob calls fold into one line; each subagent's calls get a group |
| Agents | While a subagent runs, a list above the message box shows each one: its type, its task, Running, Done, Failed or Stopped, how long it ran, its tokens and its latest tool line. Select a row to open its calls in the conversation. A subagent that finishes while another still runs stays in the list until your next message; once none runs, the list goes away, and their calls stay in the conversation. **Working** stays on while a background subagent runs |
| Helpers' own copies | A helper agent Claude starts with worktree isolation gets its own copy of the project in the session's helpers folder, `/workspace/s/<box>/helpers/h-<name>`, on a branch `helper/<name>` from the session branch's current commit; at most 8 at once. Its work counts only once Claude merges that branch into the session's branch, where the gate checks it. Only the session's branch is pushed. A helper's writes get the session's holds and refusals, and show as **In a helper's copy**. Once a copy's branch is merged and the copy has nothing uncommitted, it goes when the session is idle; deleting the session removes every copy |
| Background tasks | In a Claude Code session, a list under Agents shows each shell command run in the background, Monitor watch and workflow: what it runs, Running, Done, Failed or Stopped, and how long. They don't keep **Working** on: an idle session's state says, for example, "Idle · 2 tasks running". A task that ends while another still runs stays in the list until your next message; once none runs, the list goes away, and the agent's reply says how they went. Stop a task in Claude Code; the page has no button for it |
| Tool lines | Paths are relative to the worktree. A shell call shows the agent's description and the programs the engine reads from the command; **Show command** shows the whole command. A failed call keeps its exit code and last lines in view |
| Grouped edits | Edits in a row fold into one line, such as "Edited README.md 4 times · +8 −3"; open it for each edit and a link to each file's diff |
| Never folded | sudo, docker, a call outside Claude Code's sandbox, a write outside the worktree, failures and Software Factory's refusals |
| Plans | Approve a plan-mode plan in Claude Code: **Approve in Claude Code**, then attach |
| Stop | The round button in the message box stops the agent's own turn. A ring turns around it while the agent works (it stands still if your system reduces motion). While only subagents or a compaction run, the button is grey: point at it for why |
| Catching up | What happened while you were away, from the engine's records |
| Line comments | In Changes: up to 50, 2,000 characters each, 20,000 in all |
| What this session may and may not do | Its fixed refusals, allowed hosts and saved rules (see [Saved allow rules](#saved-allow-rules)) |
| Command palette | Ctrl+K (⌘K on a Mac) |
| Notifications | Turn on in Settings; shown while the tab is in the background |
| Bring in the latest main | ⋯ menu; the engine fetches main for the agent. Greyed during a turn |
| Long conversations | With knackpack, **Save a checkpoint** before you compact |
| "Not backed by this turn's actions" | A hint for claims nothing in the turn backs; English only, and the agent can hide it |
| Uncommitted edits, parallel sessions | Warnings only; they never hold a push |
| A quiet turn | After 10 minutes with nothing reported, **Stop the turn** is offered |

**Commands that can't be undone always get a card**, with no "don't ask again": `rm -r` on `~`, `/`, `.`, `..`, a glob or a variable, `git reset --hard`, `git clean -f`, `DROP` or `TRUNCATE` to a database, `terraform destroy`, `kubectl delete`, a download piped into a shell, and starting another `claude`.

**In an Incident session, remove customer data and secrets from attachments first.** They go to Anthropic, and the page doesn't scan them.

#### Changes

**Open Changes** shows the branch's commits and diff, from committed work only. **Compare with** picks the base: main (the default), the last reviewed commit, the commit before the last turn, or one of the newest 5,000 commits. Files over 1 MiB, past 8 MiB in all, or binary aren't diffed. Token-like text is hidden. **Viewed** marks are kept for up to 1,000 files.

#### Stopping and deleting

**Stop session** removes the container and keeps the home, folder and conversation; **Wake** makes the container again. Claude's scratch folder (its notes, briefs, plans, checkpoints and drafts, and its commands' temporary files: `$TMPDIR`) is `/workspace/s/<box>/scratch/<session id>/`, outside the worktree, so it survives Stop, Wake and a rebuild, and it counts toward the session's disk cap. **Delete session** removes everything. **If the branch has unpushed work, the page offers to discard it: that work is lost.** Both ask first.

#### Trust levels

- **Locked:** every sandbox check passed.
- **Held:** a check failed or hasn't run. Its pushes are held and no new session starts.
- **Stopped:** the agent's process ended.

If one session's check fails, that session is held and stopped. If the stack-wide check fails, or the helper doesn't answer, every running session stops.

### Session settings

**Session settings**, first in the ⋯ menu, shows the session ID and mode, then these parts:

| Part | What it does |
|---|---|
| **Model** | Switches model and effort from the next turn |
| **Queue** | Up to 10 messages, sent one per turn; it stops when you're needed, and **Resume** goes on |
| **Setup**, **Check** | The last runs of the project's commands; **Show all output** opens a run's whole output |
| **Caps** | Use against the caps, and this session's caps |
| **Ports** | 10 ports from 41000; Claude Code gets the first in `PORT` and `SF_PORT_BASE` |
| **Locked tests** | Claude Code's file tools ask before each write to committed test files (up to 2,000), and the push card shows locked files that changed. A shell command doesn't ask, and neither holds a push |
| **Backup of its commits** | The engine's copy of the branch: 256 MiB per session, 2 GiB in all |

**Restore branch**, under Backup, puts back a branch the agent deleted or rewrote. **Press it twice; it stops the session first.** A commit with token-like text stops the copying there.

A line above the message box with **Open settings** says when a part needs you. **Looks stuck**, usage limits and an ending sign-in still show above the conversation.

### Saved prompts

Up to 50 per project, names up to 60 characters: **Insert**, **Edit**, **Delete**, **New prompt**. Kept in `engine-state` and sent only when you send.

### Open in terminal

Run these in a terminal on this computer:

- `docker exec software-factory-box-<box> claude logs <id>` shows what Claude Code does;
- `docker exec -it software-factory-box-<box> claude attach <id>` takes the session over.

A stopped session wakes first. Press **Make it read-only** to answer in the terminal; **Let this page control it** switches back.

### Preview

Shows the app a running session serves, such as `npm run dev`, inside the page. Choose **Preview** in the session's ⋯ menu, then pick one of the session's 10 ports; the first is the one in `PORT`. **Open in a new tab** shows it full size.

The app must listen on that port on all addresses, not only `localhost`: for Vite, `--host`. Hot reload works. One preview is open at a time, at `127.0.0.1:7800` (`SF_PREVIEW_PORT`); opening another ends the one before.

The preview runs code the agent wrote in your browser. It can't read the page's key or use the page, but treat it like any site you don't know.

### Keyboard shortcuts

Press **?** outside a text field, or choose **Keyboard shortcuts** in Settings (the gear) or the command palette. The list can turn the **?** key off.

### Workspace

**Workspace** shows the volume's free space and each worktree's size, every 30 seconds. It deletes stopped sessions that are pushed or on main; delete others from their own menu.

## Access and credentials

- [Network access](#network-access): hosts for every session.
- [Access](#access): a host, port or credential for one session or project, for a time.
- [Credentials](#credentials): secrets for grants and actions.
- [Copies of other repositories](#copies-of-other-repositories): another repository's code, read-only, for one session.

### Network access

The allowlist for every session. Claude Code's hosts (`api.anthropic.com`, `claude.ai`, `claude.com`, `platform.claude.com`) are always in it. Reviewers reach only those and the gateway.

**Add a host** by name, or `.example.com` for every name under it (not `example.com` itself), with no expiry, 1 hour, 1 day or 1 week, and an optional reason (up to 200 characters). **A host that accepts uploads asks you to confirm, since an agent could send code out through it.** For PyPI, add only `pypi.org` and `files.pythonhosted.org`. Git hosts, a project's host and the sandbox check's outside host can't be added.

The dialog lists refused hosts for all sessions, every 5 seconds, with **Add this host**.

### Company LLM gateway

A gateway's base URL, set under **Network access**, applies to all sessions and reviewers, so a subscription sign-in stops working. It sets `ANTHROPIC_BASE_URL` and removes Anthropic and telemetry hosts from the lists, unless the gateway is one of them.

**The gateway sees all code and decides every tool call: use only your own company's gateway.**

Paste its key for the reviewers beside the address, and for each session in its sign-in panel. It's saved for Claude Code's `apiKeyHelper`, sent as `Authorization: Bearer` and `x-api-key`, never shown again, and replaces a saved API key. **Turning the gateway on, changing its host or removing it (Remove gateway) deletes the saved keys.** Changes are refused, and Remove gateway is greyed, while sessions or reviews run. If `egress.json` can't be read at a start, the key is removed first and Claude Code doesn't start.

### Credentials

Secrets you save for grants and actions. An agent can use one only after you grant it.

**Credentials are kept in their own volume (mode 0600), and never logged, shown again or exported.**

| Type | You give | The agent gets | Agent can copy it? |
|---|---|---|---|
| Token | A value and a variable name | An environment variable | Yes |
| AWS keys | Key id, secret, region, optional role | STS copies (1 hour, renewed) as a named profile | Only a copy, valid until it expires |
| SSH | User, key or password, host key | A key signs logins only, through `/run/sf/ssh-agent.sock`; a password as `SSHPASS`; a `~/.ssh/config` entry | Key: no. Password: yes |
| File | Contents (16 KiB), a home path (200 characters), mode 0600 or 0644 | The file | Yes |
| Variables | Up to 32 `NAME=value` lines | Environment variables | Yes |

- A name of up to 40 small letters, digits, `_` and `-`. Values up to 4,096 characters. At most 32 credentials.
- **Bound hosts** (1 to 16, required): HTTPS names, or `host:port` for TCP, which opens that port with the grant. `.local` doesn't work; use the address.
- Optional tags: `write`, `production`.
- Refused: vendor hosts, things that look like an Anthropic key or Claude token, and git hosts (use [Git host tokens](#git-host-tokens)).
- **Replace** takes back its grants; type the secret again. Presets stay only if type, hosts and delivery are unchanged.
- **Delete removes its grants, presets and actions.** Old files and variables leave the containers when the engine next reaches them.
- Two credentials can't use the same variable, file or SSH host in one project.
- Variables apply from the next command (`CLAUDE_ENV_FILE`); running servers keep old values.

### Access

A **grant** gives one session, or a project's sessions, one HTTPS host, one TCP port, or one credential, for 1 hour, 1 day, 1 week or until you take it back. Session grants end at Stop or Delete. Project grants last until they expire or the project is removed. Reviewers never get grants. At most 64 grants, and 256 hosts and 256 ports per session.

**Anything the agent sends to a host you allow leaves this computer.**

A grant starts when:

- **the agent asks** (`factory_request_access`): one waiting at a time, 20 an hour, none for 10 minutes after you refuse;
- **the agent tries** a refused host (telemetry makes no card);
- **you give it** with **Give access**, then **Check the risk**;
- **a preset applies it** (see [Presets](#presets)).

The card shows the host, the agent's reason (which may come from planted instructions), the risk, who and how long. **Accept the risk** grants it (for This session only while the session runs); a project grant with no expiry needs its box ticked.

| Grant | Risk |
|---|---|
| Host without uploads | Low |
| Host with uploads, whole domain, public port | Medium: data can leave, or others may run names under the domain |
| Address on your network, project with no expiry | High |
| Handed-over credential | High: **a copy outlives the grant, so rotate the secret afterwards** |
| SSH key | The target's: Software Factory signs; the key can't be copied |
| AWS keys | High: **a copy keeps working until it expires (up to 1 hour), even after Take back** |

**Port grants:** ports 1 to 65535. Your network's addresses (`10.x`, `172.16–31.x`, `192.168.x`, `100.64–127.x`, `fc00::/7`) only by address; names reach public addresses only. Port 443 means a host grant. Never reachable: loopback, link-local and metadata addresses (`169.254.x`, `100.100.100.200`), multicast and reserved addresses, Software Factory's own networks, and the Docker host as `host.docker.internal` or `gateway.docker.internal`.

**The Docker host's other gateways (such as `172.17.0.1` or another Compose project's) and your host's own LAN address are not blocked: don't grant them.**

- ssh, scp and rsync go through the proxy by themselves (`ProxyCommand nc -X connect -x egress:3130 %h %p`). Other TCP uses `127.0.0.1:<port>`, or `nc -X connect -x egress:3130 <host> <port>`.
- Tunnels close 10 seconds after the grant ends, after 5 minutes idle, or after 1 hour.

**Git hosts are never granted:** not as a host, a domain, a port, a credential, a preset or an action. The engine pushes and opens issues for the agent instead. Their addresses are looked up every minute, and a host that becomes a git host is taken back within 15 seconds. **A git host the engine can't recognise, such as a mirror or another name, can't be told apart: grant only hosts you know.**

The **Access** dialog (session ⋯ menu) lists grants with **Take back** and **Take back all**, presets and actions. Handed-over files leave the container when the engine next reaches it; a line says it retries every 15 seconds. Every grant event is in the audit.

**Example: a Raspberry Pi with a key.** Under Credentials, add an SSH credential `pi` with user `pi`, its key, and host `192.168.1.20:22`. In the session's **Access**, pick `pi (SSH)`, **Check the risk**, tick the box, **Accept the risk**. Then `ssh pi@192.168.1.20` works, and the key never enters the container.

**With a password,** the agent can copy it: take the grant back and change the password afterwards.

**Example: AWS.** Save AWS keys as `aws-dev`, bound to `.amazonaws.com`. A grant writes the STS copy as profile `aws-dev`: `aws --profile aws-dev sts get-caller-identity`. If STS refuses, the error code shows above the message box.

### Presets

A saved grant. Save one with **Save as preset** on a card, or **Add a preset** under Credentials. A high-risk preset needs its tick at save time. **Apply when a session of the project starts** applies it within 15 seconds, with no card. Deleting a preset keeps its grants; removing its project or credential deletes it. At most 32 presets.

### Actions

A fixed command Software Factory runs for a session with one of your credentials, against one target. The agent asks by name and gets the output. At most 32.

**With the session's files, agent-written files can make the command send or print the secret, so the card is always red.** For example, a link `dist/k` to a readable file makes `rsync -L` or `scp -r dist` send it. A password can leak that way; an SSH key can't.

| Field | Rule |
|---|---|
| Name, Credential | Such as `deploy-pi`, and a saved credential |
| Target | One HTTPS host, or `host:port` the credential is bound to; never a git host. SSH needs a port |
| Command | Program on line 1, one argument a line, no shell. `./` programs from the session's files make it red |
| Arguments | Up to 8, as `name longest pattern` (`tag 40 v[0-9]+\.[0-9]+\.[0-9]+`, a Go pattern up to 200 bytes), used as `{tag}`. Values can't start with `-`, `/` or `~`, hold `..`, or look like tokens |
| Session's files | Read-only at `/work` |
| Time limit | 10 minutes (1 to 60) |

Refused programs: `git`, shells, wrappers like `env` and `sudo`, interpreters like `python` and `node`, task runners like `make` and `npm`, and links to them.

The agent calls `factory_action`; the card shows the command line, target and risk. It's red with the session's files, a handed-over or `write`/`production` credential, a target on your network, or a `./` program. **Accept the risk** runs it **Once**, or for **1 hour** or **1 day** with the same arguments. **Don't allow** bars that command line for 10 minutes. One waits at a time, 20 an hour, 2 run at once. You can also run one from **Access** with no card, while the session runs. It runs in a short-lived locked-down container that reaches only the target; the credential is there for that run only.

The agent gets the exit code and the last 64 KiB of output, with secrets replaced. **A secret in another form, such as base64, gets through.** The latest 8 runs are kept in memory; the audit records each run, never its output.

**Example: deploy to a Raspberry Pi.** With the `pi` credential (and its host key from `ssh-keyscan`), add `deploy-pi`, target `192.168.1.20:22`, session's files ticked, command:

```text
rsync
-a
--delete
dist/
pi@192.168.1.20:/srv/site/
```

The agent builds `dist/` and calls `factory_action`; the card is red, and **Accept the risk** runs it.

### Copies of other repositories

An agent can ask to read another repository with `factory_copy_repository`: an `https://` address on any git host, and a branch or tag if it wants one. The session's container still can't reach any git host. The engine fetches the copy.

- **The card** shows the address, the branch or tag, the host, whether the engine uses the token you saved for that host, and where the files will be. The engine fetches nothing until you press **Approve**. **Decline** tells the agent, and it can't ask for the same copy again for 10 minutes.
- **The token.** The engine uses a saved token only for its own host, and only when the card said so. The token never reaches the session's container, and the engine follows no redirect.
- **What arrives.** Only the files of one commit, read-only: no `.git` folder, no history, no submodules and no Git LFS files. A repository with a `.git` entry or a symbolic link that points outside it is refused.
- **Where.** In the session's container, under `/workspace/refs/<host>/<path>@<branch or tag>` (`@HEAD` for the main branch). That is outside the worktree, so nothing there is committed or pushed.
- **Limits.** At most 512 MiB fetched, counted while it downloads, at most 1 GiB or 100,000 files written, 5 minutes for each fetch, and 5 copies for each session. Past a limit the fetch stops, nothing is kept, and the card and the agent say which limit.
- **Refused addresses:** `http://`, SSH, a user name or password in the address, an IP address, and a local name such as `localhost`. The engine looks up no host name before you approve. When it connects, it refuses a host that resolves to a loopback, private or link-local address or to Software Factory's own networks, and the card says why.
- **Kept** until you press **Remove** on its card or delete the session. The same address and branch or tag asked again gives the same copy.

**The risk: the agent will read this code. Text in it can try to steer the agent; nothing in it runs.** Approve only repositories you'd let the agent read.

### Saved allow rules

The "don't ask again" rules Claude Code saved, from the worktree's and clone folder's `.claude/settings.local.json` and the container's `~/.claude/settings.json`. Wide rules (any Bash, network tools, wildcards) are marked.

**This list is a report, not a control: an agent can add rules back.** **Remove** deletes one rule; the next start or wake asks for approval again. A stopped session's rules can't be read.

## Reviews and pushes

### Execution path (with knackpack)

| Work type | Steps | What a push needs |
|---|---|---|
| Code change | Define, Plan, Implement, Code review, Push | Approved requirements and plan, then the bug hunter's and simplicity checker's reviews |
| Incident | Triage, Reproduce, Root cause, Fix, Code review, Push | Those reviews, and an approved root cause when code changes |
| Explore | None | Never pushes; **Draft issues** turns findings into issues |

- The agent saves the requirements brief, the plan and the root cause in the session's documents folder, `/workspace/s/<box>/docs/<session id>/` (`$SF_DOCS`), outside the worktree. They're never committed, and they're deleted with the session. A file tool's write under `.knackpack/` or the pack's documents folder is denied.
- **A push that adds or changes a file under `.knackpack/` or the pack's documents folder is refused, with no Allow**, even if a later commit deletes it. A push that only deletes such files passes.
- With no type yet, the first file change waits on a card where you pick one. Define's brief, outside the worktree, doesn't wait, so Define can finish first. **The card sees only Claude Code's file tools; a shell write isn't held.**
- With no pack that writes requirements, there are no Define or Plan steps.
- Only the step the agent works on reads **Working**, or else the next step reads **Now**. If the agent works on a later step first, for example a plan started by a typed command, the earlier step reads **Waiting for your approval** (or **Not done**). The step card names the step the agent works on in one short line, with the full sentence in the expanded view, and its bar fills one segment per finished step and half of the step it names.
- **Implement** is done once the work is committed, the agent has stopped working and nothing is left uncommitted; until then it stays the current step and says why. **Code review** reads **Working** only while a reviewer runs.
- While the agent works (its turn, a compaction or a running subagent), the path offers no step button, and the requirements, plan and root cause show as drafts with no **Approve**.
- The first message of a Code change session starts Define, and an Incident's starts triage, with your words sent as its input; the conversation shows your words, marked **Started Define** or **Started triage**. Each message you type, unless it starts with `/`, goes as Define's or Triage's command, with its attached files (with no type picked yet, Define's, as for a Code change), until the engine reads the requirements brief (Incident: the triage in `ROOT-CAUSE.md`) in the documents folder. After that, messages go as you type them. A message too long to send with the command isn't sent: the page says how much to cut. **Continue defining** and **Continue triage** run the step again.
- If the agent changes files in a Code change, or with no type picked yet, before you approve the requirements, the path says so. **Ask for the requirements** sends Define's command. If it works before you approve the requirements or the plan without changing anything in the project, the path says that too.

**Code change:** read the requirements, then **Approve requirements** or **Ask for changes**. `Blocking: yes` or a missing `Acceptance:` blocks approval. Changed requirements show a diff before you approve again. The plan always follows, and can't be skipped. File tools are blocked until you approve the plan. **The engine keeps its own copy of what you approve, and that copy counts:** an edit to the file clears the approval, while a file it can't read keeps it. The plan can be approved only while the branch has no commits since the requirements were approved.

**Incident:** start at the deployed commit, with the evidence in your first message. The session gets no production access. When `ROOT-CAUSE.md` says `CONFIRMED`, read it and **Approve** its SHA-256; until then there's no **Fix**, and a push that changes more than documentation is held. An edit to the file clears the approval. Then **Write the incident review**.

**Review settings**, in this panel: rounds (3, 1 to 20), the default work type, automatic findings (off), a pull request comment after each push (off), holding the agent's push after a block or above a size (off), the pack (see [Packs](#packs)) and your own agents (see [Your own reviewer agents](#your-own-reviewer-agents)).

### Reviews and the push gate

**Review** runs the required reviewers on the latest commit, each in its own container, on a read-only copy. Reviewers can't run commands or write files, and they don't follow commit messages. Each gives its whole report in its reply. Only committed work is reviewed; **Run all again** gets fresh verdicts. **What this review was given** shows the prompt.

While the agent works, **Review**, **Run all again** and **Push** are greyed, with the reason in their tooltip: the agent's own push starts the reviews. **Review** is filled green only when it's the next step: Implement is done and a required review has no verdict on the latest commit.

With knackpack, the reviewers are the bug hunter, the simplicity checker, reviewers for the changed files (chosen by knackpack's catalog when it has one), and the team file's agents.

**Outcomes:** pass, block, no verdict or failed. Over 20 minutes, a read outside its folder, a pass in one turn without reading, or a pass listing a Critical or High finding count as not passed. The panel shows 4,000 characters of the report; 64 KiB are kept.

- Later rounds review only what changed.
- A pass stops counting after a new approval, a team rules change, or a new minimum reviewer version.
- **Write a manual test list**, **Explain this change** and **Review this plan** are read-only aids, never verdicts. They share the limit of 8 reviewer runs at once. **Review this plan** is offered only while the plan waits for your approval; a report made earlier stays readable. Once the engine finds the branch has nothing beyond main, **Explain this change** stays greyed for that commit.

**Push** (or the agent's `factory_push`) pushes the latest commit when every required review passed, the reviews cover current main, the session stayed Locked, and nothing is flagged. It pushes `sf/<id>` only: never forced, never to main. **Push** is greyed, with the reason in its tooltip, while something only the agent or a review can fix is missing: an approval, committed work, a review still to run or a block. When all that's missing is something you may allow, it stays on, and the held push's **Allow** decides.

**A held push** shows what is missing:

| Action | What it does |
|---|---|
| **Allow** | Pushes anyway, with a reason, once, for that commit, within 15 minutes. **Never while the sandbox check fails** |
| **Deny** | Ends the request; reviews that finish later don't push it. Remembered for the newest 20 commits |
| **Override** | Counts a block as a pass for that commit, with a reason |
| **Allow it** | Allows a flagged file version or commit field, with a reason |
| **Ask the agent to remove it** / **rewrite it** | Asks the agent to fix a flagged file or commit. This, Send findings and Hand off are greyed while the page can't message the session |
| **Send findings** | Sends the blocking reports to the agent once per commit, marked untrusted. The push stays held |
| **Hand off** | After the round limit: asks for a handoff, then denies |

**Flagged** are secret-like file names, files over 1 MiB, private keys, tokens and passwords in URLs, unreviewed file versions, and commits with token-like text. **The card never shows a flagged file's contents.**

- After the round limit, a reviewer isn't run again; you decide.
- **About this change** and the requirements table are facts to check, not gates.
- When reviewers block an agent's push, the agent is told how many blocked and that you can send it their findings. It gets the findings only through **Send findings**, yours or the automatic one.
- An agent's push starts its reviews and pushes when they pass. It can ask 5 times in 10 minutes, with up to 12 reviewer runs per branch. When 8 runs are going, it queues.
- **On a billed sign-in, the engine starts no review by itself:** press **Review**.
- After a restart, a waiting automatic push is picked up again.
- **Download this push's record** saves a JSON record with no secrets.
- Cited CVE and GHSA IDs are checked at OSV. **A real ID can be missing from OSV.**

### How the latest commit is being checked

The **How this commit is checked** card under the Reviews heading says first what the gate decided, then the commit and its reviews, with a small strip from the commit through each required reviewer and the gate to the push. Press it to open the diagram, which follows them live and closes when you open another session. The same line sits above the diagram. Each container has its own colour, and green, amber and red mark what passed, waits on you or failed; every state also keeps its icon and word.

### How this change was checked

A decided push's card lists, from the engine's records, the reviews, verification runs and the gate's decision for that commit. Its **How this commit is checked** card opens them as a diagram.

### Packs

A project's reviewers come from its pack, chosen per project under **Reviewer pack for this project**.

**No pack means nothing reviews: every push needs your Allow with a reason.** A pack can make reviews better or worse, but can't grant anything.

- **knackpack** (default) fills the roles and the file reviewers.
- **A plugin from git:** its address and full commit, then **Fetch and check** and **Use this pack**. Up to 8 packs.
- **An uploaded pack:** **Upload a pack folder**; up to 200 files, 256 KiB each, 2 MiB in all, text only.

A session keeps its pack across Stop and Wake. A pack's session files are on the start-up card (knackpack's aren't). The manifest:

```json
{
  "format": "software-factory-pack/1",
  "name": "acme",
  "version": "1.2.0",
  "roles": { "bug-hunter": "bug-finder", "simplicity": "simplicity-checker" },
  "steps": { "define": "/acme:requirements", "plan": "/acme:plan", "review": "/acme:handoff" },
  "documents": { "folder": ".acme", "requirements": "REQUIREMENTS.md", "plan": "PLAN.md" }
}
```

`documents` names the files the engine reads from the session's documents folder; `folder` must be `.<name>`, and a push that adds or changes a file there is refused. `name` can't be `knackpack` or `software-factory`, start with `git`, or be `forgejo`, `gogs` or `azuredevops`, since those folders belong to git or a git host. Other keys are refused, among them `tools`, `model`, `effort`, `permissions`, `network`, `hosts`, `verdict`, `required`, `rounds`, `hooks`, `mcpServers` and `env`. Reviewers get only the pack's `agents/*.md`; a pack that could run programs is refused, with the reason. Each reviewer must end with `Verdict: pass` or `Verdict: block`. The audit records packs by digest. Import turns packs off.

### Your own reviewer agents

Use your own agent file (`.md`) for the bug hunter or simplicity checker, under **Reviewer agents for this project**.

- Up to 192 KiB. Front matter may hold only `name`, `description`, `model` and `tools` (`Read`, `Grep`, `Glob`, `Agent`, `ToolSearch`).
- 4 per project, 12 in all.
- It runs read-only like knackpack's reviewers, from engine state, never from the repository.
- It must end with `Verdict: pass` or `Verdict: block`.
- A new version is a new reviewer.

### Issues, pull requests and CI

The agent can't reach the git host. It asks the engine with `factory_pull_request`, `factory_issue`, `factory_checks` (CI results, marked untrusted) and `factory_review_comments` (only your own comments).

Issues and pull requests wait as cards. **Read each one: the engine posts it under your token.** **This looks like a secret** needs a tick. Pull requests open as drafts by default. If the answer is lost, the card says **check on the host**. Cards survive a restart for 24 hours, and new commits mean Open asks again.

- **A comment after each push** (off; GitHub only) holds only engine records.
- **Following the pull request:** once a minute, the engine reads its state, checks and new comments from you and the bots you list. **Send to the agent** sends them marked untrusted; automatic sending is for subscriptions only, up to 5 times. A merged or closed pull request marks the session done. It backs off near the rate limit and writes nothing.
- **Open pull request** (once the engine has pushed the branch) and **Ready for review** work from the page. The engine can assign what it opens to you.
- **Open issues for these** makes issue cards from non-blocking findings.
- The audit records every push request and answer, permission answers by hash. Git LFS and SHA-256 repositories are refused.

### Register of exceptions

Lists, read only, the pushes you allowed with a reason, verdicts you overrode, flagged files and commits you allowed, hosts you added with your reasons, and saved allow rules. It updates every 5 seconds.

## Safety and cost

### Trust boundary

The engine, helper, reviewers and proxy are trusted; the sessions' containers aren't. An agent acting on planted instructions can run anything in its own container, but can't see other sessions.

- **No route to a git host.** Each session's network leads only to the proxy, which allows only model API hosts and refuses git hosts. The engine checks this before every start and every 5 minutes.
- **The containers.** Only the helper makes them, from a fixed recipe, checked before each start: no capabilities, a read-only root, no ports, no host folders. The helper holds Docker's socket, and the sessions can't reach it.
- **The push.** Only the engine holds the git token.
- **Copies of other repositories.** The engine fetches one only after your **Approve**, and the session sees its files read-only.
- **The verdicts.** Each reviewer runs alone on a read-only copy, unreachable from sessions.
- **Your decisions.** Approvals, Allow, Deny and Override come only from the page. From the sessions, the engine accepts only reports and requests, never a decision, a verdict or a push.

A locked `/etc/claude-code/managed-settings.json` turns off messages between sessions, Remote Control, synced plugins and connectors, and auto memory. So put corrections in `CLAUDE.md`. It also turns off the plugin guard, so a plugin in a session's home could override its permissions.

**Any program on this computer can use the page, including approving pushes.** It's only on `127.0.0.1`, checks `Host`, `Origin` and `Sec-Fetch-Site`, and has a strict content security policy, so websites and agents can't drive it. A card allows only what it showed.

### Cost and models

Software Factory holds no model key. Usage goes to your own subscription or API key.

- Before each start, wake and review, `claude auth status` reports the sign-in kind (subscription, API key, gateway, Bedrock, Vertex AI or none), never the account. A kind other than the one you expect is refused. **On any sign-in but a subscription, the page asks you to confirm the cost.**
- Costs shown are estimates.
- Models: Opus and high effort by default; reviewers use their default unless a role sets one.
- Cost is kept down by reviewing each commit once, reviewing only changes later, the round limit, 12 runs per branch, and [Caps](#caps).
- **Usage limits:** a Claude limit belongs to the account, so every session and reviewer signed in with that account shares it. The Sessions page shows a limit from 75% used, or when Claude refuses work on it, and in a warning color from 90%. From 90%, automatic reviews pause: **Keep them running** or **Wake at reset**. [Diagnostics](#diagnostics) lists every reading.
- **Looks stuck:** the same call 3 times in 8 shows **Interrupt**.
- **Expiry warnings:** 10 days before a claude.ai sign-in ends (it can end up to 3 days earlier), and 7 days before a GitHub token expires.

### Limitations

- **Don't connect a vendor's cloud agent to repositories you gate with Software Factory:** it pushes outside the gate. The plugin blocks only cloud hand-off commands the agent types, not ones a script runs.
- Code can carry instructions to its reviewer. The reviewers' read limit relies on a Claude Code setting not yet checked on a signed-in run.
- Containers don't stop kernel exploits.
- Reviewers read the whole change, not each commit; versions only in an intermediate commit are flagged.
- The secret scan misses encoded, split or unknown secrets.
- Nothing trusted runs a repository's checks; a verification run is untrusted.
- An agent can change its own container's settings. Wake lists the changes, but misses user MCP servers in `~/.claude.json` and files they name elsewhere.
- A flaw in the helper would give control of Docker.
- **The disk cap is soft**, and without one nothing limits disk use.
- A hard stop can leave containers behind (see [Clean up after a hard stop](#clean-up-after-a-hard-stop)).
- Organization managed settings replace the image's locked settings; untested.
- Only Claude Code is offered. Plan-mode plans are approved in Claude Code. No auto or bypass mode.
- After a restart, sessions are Stopped, cards are gone (except issue and pull request cards), and pushes wait for the first check.
- SHA-256 and Git LFS repositories, and non-UTF-8 flagged paths, can't be pushed or allowed.
- The flagged-file scan reads at most 1,000 commits and 32 MiB, and reviewers get 200 commit messages; beyond that, a push needs your Allow.
- One computer, one person, one signed-in page.

## Maintenance

After a release install, add `-p software-factory` after `docker compose` in these commands.

### Upgrade

- **Release:** run the install command again with the same variables.
- **Source:** `git pull`, then `make up`.

Sessions stop and wake on the new image. Data, logins and the audit stay.

### Other tasks

- **Install the agents again:** `docker compose exec engine software-factory install-agents`.
- **Sign in again:** see [Sign Claude Code in for the reviewers](#sign-claude-code-in-for-the-reviewers) or [Sign Claude Code in for this session](#sign-claude-code-in-for-this-session).
- **Logs:** `docker compose logs engine`. Event kinds only. Find one action with `docker compose logs | grep <id>` (its `req=<id>`).
- **Dropped records:** the Welcome page says when a store dropped old records.
- **Stop and keep the data:** `make down` or `docker compose down`.

### Roll back after an update

Before an engine changes a store's format, it saves the old file in `backup/<old engine version>/`, keeping the last 3.

**Changes made since the update are lost. For a release install, this isn't checked yet.**

1. `docker compose down`.
2. `docker compose run --rm --no-deps --entrypoint sh engine -c 'cp -p /var/lib/software-factory/backup/<old engine version>/* /var/lib/software-factory/'` (for a release, with `-p software-factory -f compose.yaml -f compose.release.yaml -f software-factory-socket.yaml`, in the install folder).
3. Start the older code: `make up`, or `curl -fsSL https://github.com/bezmoradi/software-factory/releases/download/v<version>/install.sh | sh`.

### Clean up after a hard stop

The next start removes leftover containers. To remove them now (use your project name):

```bash
docker rm -f $(docker ps -aq --filter label=dev.software-factory.stack=software-factory)
docker network rm $(docker network ls -q --filter label=dev.software-factory.stack=software-factory)
```

**Don't delete the volumes with this label here: that deletes every session's login and conversation.** [Uninstall](#uninstall) does that.

### Remove a session's leftover files

Only when the log keeps showing `event=folders-not-removed box=<box>`. The engine retries at start and every 5 minutes.

1. Check `docker ps -a --filter name=software-factory-box-<box>` lists nothing.
2. **Set `box` to exactly the 16-character id from the log. A wrong id could name every session's folder,** so each command checks it first.
3. Run both passes (agents' user, then engine's user):

   ```bash
   box=<the 16-character id from the log line>
   [ "${#box}" -eq 16 ] && printf '%s' "$box" | grep -Eqx '[0-9a-f]{16}' && docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges:true --user 10002:10000 -v software-factory_workspace:/workspace --entrypoint rm software-factory:local -rf -- "/workspace/s/$box"
   [ "${#box}" -eq 16 ] && printf '%s' "$box" | grep -Eqx '[0-9a-f]{16}' && docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges:true --user 10001:10000 -v software-factory_workspace:/workspace --entrypoint rm software-factory:local -rf -- "/workspace/s/$box" "/workspace/r/$box"
   ```

"Permission denied" in the first pass is expected. For a release, use the image from `compose.release.yaml` (`ghcr.io/bezmoradi/software-factory@sha256:<image digest>`) in place of `software-factory:local`; for another project name, its `<project>_workspace` volume.

### Uninstall

**Warning: `docker compose down -v` deletes every clone, unpushed work, tokens, reviews and the audit. It can't be undone, and it doesn't sign out or revoke anything, so do steps 1 to 3 first.**

1. Sign out each running session: `docker exec software-factory-box-<box> claude auth logout`.
2. Revoke a pasted API key at <https://platform.claude.com>, and a gateway key with its issuer.
3. Delete the token on your git host.
4. `docker compose down -v`, then `docker volume rm $(docker volume ls -q --filter label=dev.software-factory.stack=software-factory)` for the sessions' and reviewers' home volumes.

For a release, read the image digest from `compose.release.yaml` before you delete the install folder, then `docker image rm ghcr.io/bezmoradi/software-factory@sha256:<image digest>`.

### Volumes

Only `engine-state` can be exported; nothing backs up the rest.

| Volume | Holds |
|---|---|
| `engine-state` | Projects, tokens, reviews, approvals, settings, sessions, browser check files, the audit |
| `workspace` | Repository copies, each session's clone, worktree and screenshots, and the read-only copies of other repositories it asked for |
| `agents` | The installed agents and knackpack |
| `<project>-home-<box>` | A session's login and conversation; not removed by `down -v` |
| `<project>-reviewer-home` | The reviewers' login; not removed by `down -v` |
| `reviews` | Read-only copies under review |
| `verify` | Verification runs' files |
| `uploads` | Your attachments, in memory |
| `actions` | Action runs' command and credential files, in memory |

### Update to Software Factory

Once a day the engine checks GitHub's tags. When a newer published release exists, **Update to** shows the steps: the install command (and its check) for a release, or `git pull` and `make up` for a source build. **Software Factory never updates itself.** Only plain, published versions count. **Remind me later** hides it until the next release. Only `SF_NO_UPDATE_CHECK=1` turns the check off. Failures are logged (`offline`, `timeout`, `refused`, `malformed`, `untrusted`), and Diagnostics shows the state.

### Diagnostics

**Diagnostics** builds a text file for bug reports: versions, digests, the OS, non-secret settings, counts, every usage-limit reading and the last 500 events. No names, hosts, paths, tokens or prompts. It's built once per opening and never sent. It also shows **Vulnerabilities**, **Agent settings** (changed settings in each session) and **Agent versions**. Recheck an agent: `docker compose exec engine software-factory install-agents <id>`.

### Export and import

**The file holds your name, email and every reason you typed: keep it private.** It has no git token.

Import only into an empty engine; it restarts. Nothing counts until you approve it again, and hosts, commands, agent files, packs, automatic sends and review depth come back off. Sessions return as history only. Save the token, then press **Clone again**.

## Privacy

Software Factory runs on your computer. Its author receives none of your data.

- **What leaves:** your messages, code and uploads, to the model API under your account; the commit under review, to Anthropic. The engine talks to your git host, npm and GitHub. **An agent with an Anthropic API key can have Anthropic's servers connect anywhere public, through the [MCP connector](https://platform.claude.com/docs/en/agents-and-tools/mcp-connector).**
- **OSV:** the agent versions daily, and cited vulnerability IDs (up to 20 per review). `SF_OSV=off` stops it. The sessions' and reviewer containers never reach OSV.
- **Update check:** daily, anonymous, User-Agent `software-factory-update-check`; `SF_NO_UPDATE_CHECK=1` stops it.
- **Package lookups:** names only, when you turn them on.
- **Kept:** in your Docker volumes (see [Volumes](#volumes)): repositories, work, tokens, verdicts and reports, agent files, the audit and approvals. Conversations stay in the agents' containers.
- **Never stored or logged: any model credential.** A pasted key goes straight to its container's `apiKeyHelper`. Tokens are never shown or logged.
- **Removing it:** see [Uninstall](#uninstall).

Questions go to the repository's [issues](https://github.com/bezmoradi/software-factory/issues).

## Development

See [CLAUDE.md](../CLAUDE.md) for the code layout, commands and checks, [compose-hand-checks.md](../.claude/rules/compose-hand-checks.md) for the development fakes and hand checks, and [ci.md](../.claude/rules/ci.md) for CI and pull requests. There are no automated tests. Try a change with the development overlay:

```bash
export COMPOSE_FILE=compose.yaml:compose.dev.yaml
docker compose up -d --build --remove-orphans
```

**The development overlay's reviews are fake: never use it for real work.**

**Stop `make dev` before you pull or check out code you haven't read:** your folder runs as the engine, which holds your git host tokens.

## Author

This project is maintained by [Bez Moradi](https://github.com/bezmoradi).

## License

Software Factory is licensed under [MIT](../LICENSE). The image includes the license texts of the Go modules linked into it, Go's own and TypeScript's in `/usr/share/doc/software-factory/`.

The page's icons are [Codicons](https://github.com/microsoft/vscode-codicons) from VS Code, © Microsoft Corporation, used under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/). Its eight themes take their colors from these VS Code themes, adjusted for WCAG AA contrast: [Solarized](https://ethanschoonover.com/solarized/) Dark and Light, VS Code's own Dark Modern and Light Modern ([microsoft/vscode](https://github.com/microsoft/vscode)), GitHub Dark Default and GitHub Light Default ([primer/github-vscode-theme](https://github.com/primer/github-vscode-theme)), [One Dark Pro](https://github.com/Binaryify/OneDark-Pro) and [Dracula](https://draculatheme.com).
