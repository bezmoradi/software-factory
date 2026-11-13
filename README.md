<h1 align="center">⚙️ Engineered, Not Vibe Coded</h1>

<p align="center">
  <img src="https://img.shields.io/badge/Engineered-not%20vibe%20coded-blue?style=for-the-badge" alt="Engineered, not vibe coded">
</p>

## #1 STOP VIBE CODING <img src="https://img.shields.io/badge/Merged-8957e5?style=flat-square&logo=git&logoColor=white" alt="Merged">

```diff
- vibe coded: shipped because it compiled
+ engineered: designed, reviewed, documented
```

<sub>✅ Merged only through a pull request · 15 required checks passed, Claude's approval among them · No bypass, admins included</sub>

# Software Factory

Software Factory runs coding agents (Claude Code and Codex CLI) in a sandboxed container on your own computer and lets them change code freely, while you follow and steer every session from one page in your browser. The agents can't reach your git host. Only the engine pushes their work, to a branch of its own, and only after independent reviewers have passed that exact commit. It runs in Docker as four containers, and each agent uses your own sign-in with its vendor.

- Every agent session works in its own git worktree and branch, `sf/<id>`, in a Docker volume. Nothing on your computer's disk is mounted into the agents' container.
- The agents reach the internet only through a proxy that allows their model APIs and always refuses git hosts. The engine checks this before every start and every 5 minutes.
- Reviews run in a separate container that the agents can't reach, on a read-only copy of the commit, with [knackpack](https://github.com/bezmoradi/knackpack)'s bug hunter and simplicity checker, plus reviewers for the kinds of files the change touches.
- A push goes only to the session's own branch, never forced and never to the main branch, and every push request is written to an audit file.
- Each agent's permission requests, questions and plans wait on the page as cards for your answer.
- Software Factory never reads, stores or passes on an agent's sign-in. You sign each agent in yourself, with the vendor's own login. [ARCHITECTURE.md](ARCHITECTURE.md) describes how the parts work and why.

## Table of Contents

- [What you need](#what-you-need)
- [Configuration](#configuration)
- [Run it with Docker](#run-it-with-docker)
- [Deploy on a Linux server](#deploy-on-a-linux-server)
- [Running it](#running-it)
- [Maintenance](#maintenance)
- [Development](#development)
- [Privacy](#privacy)
- [Author](#author)
- [License](#license)

## What you need

- Docker Desktop on macOS or Windows, or Docker Engine 28 or later with the Compose plugin on Linux, on an `amd64` or `arm64` machine. Before Engine 28, other machines on your local network can reach a port published on `127.0.0.1`, such as the page's. The agent and reviewer containers may each use up to 4 GiB of memory, the engine 1 GiB and the proxy 256 MiB.
- A browser on the same computer. The page is published only on `127.0.0.1`.
- A Claude account for Claude Code: a Claude subscription or an Anthropic API key. Claude Code signs in twice, once in the agents' container and once in the reviewers' container, because every review runs on Claude Code.
- Optionally, a ChatGPT account or an OpenAI API key for Codex CLI.
- A git repository reachable over `https://`. Private repositories, and every push, need a token for the git host; see [Git host tokens](#git-host-tokens).
- Internet access at first start, when the engine downloads the agents from npm and knackpack and Spec Kit from GitHub.

## Configuration

Most settings live on the page. A few are environment variables that Docker Compose reads when you run `docker compose up`.

| Variable | Default | Purpose |
|---|---|---|
| `SF_PORT` | `7799` | The port on `127.0.0.1` where the page is published |
| `SF_ALLOW` | `api.anthropic.com,claude.ai,claude.com,platform.claude.com,api.openai.com,auth.openai.com,chatgpt.com` | The hosts the agents and reviewers may reach, over HTTPS only. Setting it replaces the whole list |
| `SF_EXTRA_ALLOW` | empty | Hosts added to that list, such as a company's model gateway: `gateway.example.com` |
| `SF_EXTRA_DENY` | empty | Git hosts to refuse besides the built-in ones (GitHub, GitLab, Bitbucket), such as your company's own git server. If an `SF_ALLOW` or `SF_EXTRA_ALLOW` entry covers the host of a project you added, the sandbox check fails until that host is here |

Give a variable on the same line, such as `SF_EXTRA_ALLOW=gateway.example.com docker compose up -d`, or `export` it in your shell. Compose reads the variables again on every `up`, so give them each time; a later `up` without them goes back to the defaults. Changing one recreates the engine, which stops the page's sessions until you wake them ([Maintenance](#maintenance) says what a restart keeps).

On the page:

- **Git host tokens**, under Projects; see [Git host tokens](#git-host-tokens).
- **The sign-in you expect** of Claude Code for the agents and for the reviewers: the kind you last signed in with in the sign-in dialog the **Finish setting up** checklist opens (a subscription, or an API key, which also covers a gateway, Amazon Bedrock or Google Vertex AI). Until you sign in there, it is the kind of the first sign-in the engine sees. Codex's is beside **Start** in the new-session form, where any sign-in is a third choice. A start or review on another kind is refused; see [Cost and models](#cost-and-models).
- **Project settings**, in a session's Execution path panel under Code review: the review rounds before you decide (1 to 20, default 3), the work type new sessions start as, whether blocking findings go to the agent by themselves (off by default; see [Reviews and the push gate](#reviews-and-the-push-gate)), whether the engine comments on the pull request after each push (off by default), and whether the agent's own push waits for your Push after a reviewer blocked on the branch or above a number of changed files or lines (off by default; the engine counts files and lines, never looks at which files).
- **Appearance**, the gear in the activity bar: one of eight themes, the activity bar's side and the text size.

**A team file.** A repository can hold `software-factory.json` at its root, on its main branch. It counts only after you read and approve it on the page, with **Review the team file** in a session's Execution path panel, and any change to it needs your approval again. It's strict JSON with `"version": 1` and these optional keys:

- `adapter`: `"knackpack"` or `"spec-kit"`, the workflow its steps use;
- `workTypes`: extra work types, each with an `id`, a `name`, `steps` from `define`, `plan`, `tasks`, `implement`, `review` and `deliver`, `edits` (`"any"` or `"docs"`), `push` (true or false) and `roles` (`bug-hunter`, `simplicity`);
- `defaultType`: the work type new sessions start as;
- `agents`: up to 20 reviewers that every push needs, from the installed plugins;
- `fileRules`: a `pattern` of paths and the `agent` that must review changes to them;
- `rounds`: the review round limit, 1 to 20. The lower of this and the project's setting applies.

A team file can't choose who fills the bug hunter and simplicity checker roles, or name commands to run (`roles` and `checks` are refused): only the reviewers installed by the engine fill those roles, and nothing trusted runs a repository's code.

## Run it with Docker

These steps were checked with Docker Desktop on macOS. They weren't tried on Windows; there, run the commands in a WSL terminal with Docker Desktop's WSL integration turned on.

1. **Get the code** and go into its folder. Run every later command from this folder:

   ```bash
   git clone https://github.com/bezmoradi/software-factory.git
   cd software-factory
   ```

2. **Build and start it:**

   ```bash
   make up
   ```

   Compose builds the image locally and starts four containers: `engine` (the page, the gate, the only git credential), `agent` (every agent session), `reviewer` (the reviewers) and `egress` (the proxy). `make up` waits until all four are healthy, prints the page's address and opens it in your default browser (on macOS, or on Linux with `xdg-open`). Without `make`, run `docker compose up -d --build --wait` and open the address yourself. The image holds no agent: on first start the engine downloads the pinned releases of Claude Code (2.1.322) and Codex CLI (0.167.1) from npm, and knackpack (0.24.0) and Spec Kit (1.4.0) at pinned commits from GitHub, checks each against the hash pinned in the code, and installs them into the `agents` volume. That takes a few minutes; the page offers an agent once it's installed. If a download failed, run `docker compose exec engine software-factory install-agents` to try again.

3. **Open the page** if `make up` didn't: <http://127.0.0.1:7799>, or the port you set in `SF_PORT`. It signs itself in.

4. **Sign Claude Code in, twice.** Until setup is done, the Welcome page shows a **Finish setting up** checklist in place of the sessions: sign an agent in (Claude Code from the page, Codex CLI with the command in step 5), sign the reviewers in, add a project, start a session. Only the next step has a button. The agents and the reviewers each run their own Claude Code, with separate logins the agents can't read or change, so sign in both:
   1. Press **Sign in** beside one of them, then **Open claude.com to sign in**, and sign in with your Claude account.
   2. Copy the code claude.com shows, paste it into the page, and press **Finish**. The row then says how it's signed in.

   The page starts Claude Code's own `claude auth login` in that container and passes it the code you paste. Software Factory doesn't keep or log the code. If you'd rather not paste it into the page, sign in from a terminal instead: `docker compose exec -it agent claude`, then `/login` and `/exit`, and the same with `reviewer`.

5. **Optionally, sign Codex CLI in.** Open the link it prints and enter the one-time code. Device code sign-in must first be switched on in your ChatGPT account's security settings.

   ```bash
   docker compose exec -it agent codex login --device-auth
   ```

   The logins are kept in the `agent-home` and `reviewer-home` volumes, so you sign in once.

Then add a repository and start a session, as in [Usage](#usage).

## Deploy on a Linux server

Software Factory has no server deployment. It publishes its page only on `127.0.0.1` of the computer that runs it, and accepts only requests addressed to `127.0.0.1:<port>`. On a Linux computer you use yourself, follow [Run it with Docker](#run-it-with-docker).

## Running it

Run the commands below from the repository folder. Each `docker compose exec` that the page shows you, such as a sign-in step, runs there too.

### Usage

1. **Open the page** at <http://127.0.0.1:7799>. The Welcome view lists every session, those waiting for you first.
2. **Add a project.** Under **Projects**, save a token for the git host if the repository is private or you want the engine to push ([Git host tokens](#git-host-tokens)), then enter the repository's `https://` address and press **Add repository**. The engine clones it; the page shows the progress.
3. **Trust the repository once:** the first time you start a Claude Code session in it, the page asks ([Trusting a repository](#trusting-a-repository)).
4. **Start a session.** Press **New session** on the project's row, pick the agent, and press **Start**. Open the session from the project's list when it connects.
5. **Pick a work type** (Feature, Fix, Docs only or Exploring) and write the first message. Answer the cards the agent shows, and follow its work in the side panel.
6. **Review and push.** Press **Review**, then **Push**, or let the agent call its push tool. The engine pushes the session's branch once the reviews pass ([Reviews and the push gate](#reviews-and-the-push-gate)).
7. **Get the work** from your git host: `git fetch origin sf/<id>` in your own clone, then `git switch sf/<id>`, or open a pull request from that branch on the host.

**The page's sign-in.** Opening the page signs it in, with no code or password. **Any program on this computer can do the same and use the page,** including approving pushes; other websites and the agents can't. One tab is signed in at a time: opening the page in another tab or browser ends the earlier one, which then offers **Sign in** to take it back. Refreshing a tab keeps it signed in. After 15 minutes with no tab open, 24 hours, **Disconnect** in the account menu, or an engine restart, press **Sign in** again.

### Projects and new sessions

A project is a repository the engine cloned by its `https://` address into the `workspace` volume. There are no local folders, by design: a folder mounted from your computer would let an agent plant git hooks or editor tasks that later run on your computer. Addresses over SSH or `http://`, and addresses with a user name, password, query or fragment, are refused.

Each new session fetches the main branch again from the address recorded when the project was added, and starts in a new worktree on its own branch, `sf/<id>`, at that commit. The new-session form has:

- **the agent:** Claude Code or Codex CLI;
- **a name**, optional;
- **the model**, for Claude Code: its default, Fable, Opus, Sonnet or Haiku;
- **the work type**, or the project's default;
- **the sign-in you expect** of Codex; Claude Code's is in the sign-in dialog the **Finish setting up** checklist opens.

A session starts in the agent's own asking mode, so it asks before it acts; auto and bypass modes can't be chosen. Before it starts, the engine:

1. checks that the agents' container still can't reach a git host;
2. shows you the repository's **start-up files**, if it has any, and waits for your approval. These are the files that agent loads at start and that can change what it may do without asking: for Claude Code `.claude/settings.json`, `.claude/settings.local.json`, `.mcp.json` and the `.claude/agents/`, `commands/`, `skills/`, `workflows/` and `hooks/` folders; for Codex `.codex/`, `.agents/skills/` and `.agents/plugins/`. The page shows each one with characters that change text direction or hide shown as `⟨U+…⟩`, other invisible and control characters as `\u{…}`, long runs of blank lines and spaces shortened, a symbolic link as its target, and a note on any file that differs from the commit. Your approval covers that exact set for this repository and agent; any change to any file asks again. Instruction files such as `CLAUDE.md` and `AGENTS.md` change no permission, so they aren't shown;
3. checks the agent's own sign-in, and asks first when it's billed per use ([Cost and models](#cost-and-models)).

A project's row shows the branch new sessions start from and whether the repository has a `CLAUDE.md` or `AGENTS.md`. Expand it to see its sessions. **Remove** takes a project off the list once it has no sessions, running or stopped; its clone stays in the `workspace` volume.

### Git host tokens

The engine clones, fetches and pushes with a token you save on the page, under **Git host tokens**: the host, such as `https://github.com`, and the token. It also uses the token to open the issues and pull requests you approve, to read CI results and your own pull request comments for the agent, and, if you switch it on, to comment on a pull request after each push ([Issues, pull requests and CI](#issues-pull-requests-and-ci)). For GitHub, create a fine-grained personal access token:

1. On GitHub, open **Settings → Developer settings → Personal access tokens → Fine-grained tokens** and choose **Generate new token**.
2. Under **Repository access**, choose **Only select repositories** and pick the repositories you add here.
3. Under **Repository permissions**, set:
   - **Contents: Read and write** (clone, fetch and push);
   - **Metadata: Read-only** (GitHub sets it with the others);
   - **Issues: Read and write** (`factory_issue`);
   - **Pull requests: Read and write** (`factory_pull_request`, `factory_review_comments` and the comment after each push);
   - **Actions: Read-only** and **Commit statuses: Read-only** (`factory_checks`);
   - **Workflows: Read and write**, only if agents may change `.github/workflows/`: without it GitHub refuses a push that changes them.
4. Generate it, copy it, and save it on the page with the host `https://github.com`.

A permission left out only turns off what needs it: the page and the agent are told which permission a refused call needs. The engine keeps one token per host, in its own volume only. It never shows the token again, never logs it and never sends it to another container; the page shows only which hosts have one and when it was saved, and lets you replace or remove it. A public repository can be cloned without a token, but pushing always needs one. **A pushed branch runs the repository's own workflows,** such as GitHub Actions that start on `push`, with that repository's secrets. Those workflows come from the branch, which an agent wrote and the reviews checked, so read a change to them (`.github/workflows/` on GitHub) before you allow its push, or limit which branches may use secrets in your host's settings.

### Trusting a repository

Claude Code asks once per repository whether you trust it, and a Claude Code session can't start until you answer. The first time you press **Start** for a repository, the page shows Claude Code's question. Press **Yes, I trust this folder**, and the session starts. Your answer covers every later session of that repository. Codex needs no such step.

To answer it, the engine runs Claude Code in the repository's folder behind a virtual terminal. There it accepts Claude Code's first-run screens (the text style and its security notes), answers **Yes** to the trust question, and starts Claude Code again to check that the answer was saved. If Claude Code shows a screen Software Factory doesn't know, it answers nothing and tells you. You can then answer in a terminal instead: `docker compose exec -it -w /workspace/repos/<name> agent claude`, choose **Yes, I trust this folder**, then type `/exit`.

### Working in a session

A session the engine started is controlled from the page: what you type goes to the agent as your own words.

- **Messages.** Press Enter to send. A message to a stopped session wakes it first; up to 20 messages wait 2 minutes for it to come back. A line starting with `!` isn't run: the page offers to copy the command instead.
- **Commands.** A message starting with `/` runs a skill, a plugin's or an MCP server's command, or one of the built-in commands that change nothing: `/clear`, `/compact`, `/context`, `/cost`, `/usage`, `/help`, `/status`, `/review`, `/security-review`, `/init`, `/todos`, `/export` and `/release-notes`. Type any other built-in command in Claude Code itself (see the ⋯ menu below).
- **Files.** Type `@` for suggestions from the session's worktree; names that look like secrets are left out. A file path in a reply opens the file read-only, up to 512 KB; binary files, files with secret-looking names and paths through a symbolic link aren't shown.
- **Images and files.** Paste or drop up to 4 images (PNG, JPEG, GIF or WebP, 2.5 MiB each) and up to 4 other files (10 MiB each) on a message. The page uploads them for the session to read; uploads not sent within an hour are removed. A folder can't be dropped; type its path with `@`.
- **Cards.** A permission request shows the whole call with **Yes**, the agent's own other choices (such as allowing it for the rest of the session), and **No**, with an optional note on what to do instead. A call the page can't show in full can only be denied here. Questions wait as cards with their options and a box for your own answer. While a card waits, the message box waits too. Closing the tab doesn't answer the card.
- **Plans.** Claude Code lets only its own prompt approve a plan made in its plan mode. **Approve in Claude Code** shows that prompt there; then attach to the session and answer it. **Tell Claude what to change** works from the page.
- **Stop.** The round button beside the message box stops the agent's current turn.
- **The ⋯ menu** makes the session read-only on the page or wakes it without a message, and copies the commands that attach to the session in a terminal (`docker compose exec -it agent claude attach <id>`) or show its log (`docker compose exec agent claude logs <id>`). In a read-only session, answer its cards in Claude Code itself.
- **Stopping and deleting.** Stopping keeps the conversation, and Wake brings it back. Deleting removes the session and its worktree; Claude Code refuses while the worktree has unpushed commits, and the page then offers to discard them. Press each button twice to confirm.
- **A long conversation.** With knackpack, a notice above the message box offers **Save a checkpoint** (`/knackpack:checkpoint`) before you compact, and a stronger one past knackpack's 500k-token stop point.
- **A quiet turn.** When the agent has been in its turn for 10 minutes with nothing reported, no card or question waiting and no subagent running, a notice above the message box says so and offers **Stop the turn** and, for Claude Code, **Copy attach command**. It rests on the session's own reports, so it's only a hint: the agent may still be working, such as on a long command. **Dismiss** hides it until the session reports something again.

The footer shows the session's tokens, its cost estimate and its sign-in method. Each session also shows a **trust level**:

- **Locked:** every sandbox check since the session's process last started passed.
- **Held:** a check failed or couldn't run, or none has run since the session started or woke. Every push from the session is held, and no new session starts.
- **Stopped:** the agent's process ended, or the agents' container lost it.

The side panel holds the execution path, the Reviews and Push panels, the issues, pull requests and comments, the intent record, the session's **Changes** (its commits, changed files and diff against the remote's main branch, from committed work only, the diff cut at 1 MiB) and the subagents it ran.

### Execution path (with knackpack)

The Execution path panel shows the steps of the session's work type and where the session is:

| Work type | Steps | What a push needs |
|---|---|---|
| Feature | Define, Plan, Implement, Code review, Deliver | Requirements and a plan you approved, then the bug hunter's and the simplicity checker's reviews |
| Fix | Implement, Code review, Deliver | The bug hunter's and the simplicity checker's reviews |
| Docs only | Implement, Code review, Deliver | The simplicity checker's review. Edits outside documentation are refused, and a branch that changes other files needs what a Feature needs |
| Exploring | Implement | Nothing: Exploring never pushes |

Pick the work type before the first message; you can change it later under **Work type**. A team file can add others ([Configuration](#configuration)).

Each step's button sends the matching command to the session. Claude Code sessions use knackpack's workflow: **Define** sends `/knackpack:challenge-requirements`, **Plan** `/knackpack:plan-implementation`, and **Deliver** offers **Issues** (`/knackpack:create-issues`) and **Handoff** (`/knackpack:handoff`). Codex sessions use GitHub Spec Kit's steps, which add **Tasks** (`/speckit-tasks`). Spec Kit's commands run scripts from the repository's own `.specify/` folder, so they work only in a repository that has run `specify init` and committed its files to main; the engine doesn't add them to a session. **Commit** under Implement asks the session to commit every change and to ask you about any file that shouldn't be committed. Step buttons work only while the agent isn't in a turn.

In a Feature, **Read the agreed requirements** opens them; **Approve** them, or **Ask for changes** with a note. When the requirements change after you approved them, the panel shows what changed, as lines added and removed, before you approve them again; the engine keeps the text you approved for this, in its own volume (very long requirements, or many sessions' worth, are shown without the comparison). The plan works the same way, without the comparison, and approving the requirements again clears the plan's approval. Approving the requirements sends the session the Plan step; approving the plan tells it to implement the plan (`/speckit-implement` with Spec Kit). Until the plan is approved, Claude Code's file tools can't change files in the worktree, apart from Spec Kit's own files for the current step. Docs only refuses edits outside documentation in the same way. These refusals guide the agent; the gate's approvals are what count for the push. The panel's project settings set the review round limit, the default work type, whether blocking findings go to the agent by themselves, whether the engine comments on the pull request after each push (see [Issues, pull requests and CI](#issues-pull-requests-and-ci)), and whether the agent's own push is held after a blocked round or above a size; the last three are off by default.

### Reviews and the push gate

**Review** runs the required reviewers on the session branch's latest commit, each as its own run of Claude Code in the reviewers' container. The engine writes the commit and the point where it leaves the remote's main branch into a fresh read-only folder, and the reviewers can only read it: they can't run commands or write files. The required reviewers are the work type's roles, the bug hunter (`knackpack:adversarial-reviewer`) and the simplicity checker (`knackpack:slop-reviewer`), plus reviewers the changed files call for, such as the Go, Python, TypeScript, React, CSS, UI, Docker, Kubernetes, Terraform and database reviewers, and those a team file adds. The panel lists them before you start. Only committed work is reviewed.

Each reviewer ends as pass, block, no verdict or failed, with its verdict and its final message. A reviewer that takes more than 20 minutes, fails or answers without a verdict counts as not passed. So does one that tried to read outside its folder, passed in a single turn without reading a file, or passed while its own report lists a Critical or High finding; the panel gives the reason. A reply holding something shaped like a token isn't kept at all. Under each reviewer the panel shows what produced the verdict: knackpack's commit, the Claude Code version and the model. A later round reviews only what changed since that reviewer's previous round: it reviews on its own first, then reads its earlier findings from a file in its folder and says which are still open. The panel says which kind each review was.

**Write a manual test list**, under the reviews, asks for the steps to try by hand on the latest commit: one Claude Code run in the reviewers' container, read-only like a review, on the same sign-in and with the same cost confirmation. It isn't a verdict: the push gate and the agent never see it, and it counts toward the two reviews the container runs at once. The page shows the list as text until the engine restarts; a reply holding something shaped like a token or a private key isn't kept or shown.

**Push**, or the agent's own push tool (`factory_push`, which takes no input), asks the engine to push the session's branch at its latest commit. The engine checks, for that exact commit:

- every required review passed on it;
- each review still covers the remote's main branch. If main moved since, the page asks for a new review;
- the session stayed Locked;
- no file the push would send is flagged.

Then it pushes `sf/<id>` to the address recorded when the project was added, with your token, never forced, never to main, and only if the remote branch is still where the engine last saw it. The result shows on the page as **pushed**, **held**, **denied** or **refused**, with the reason.

**A held push** shows a card with the commit, the branch, the remote and every missing item:

- **Allow** pushes anyway. It needs your reason when items are missing, works once, for that commit only, for 15 minutes, and runs the whole check again first. It never pushes while the sandbox check fails for that session: fix the check, then review and push the work from a new session.
- **Deny** takes an optional note and ends that commit's request: the engine refuses the agent's next request for it, and reviews that finish later don't push it. Only your **Push** or **Allow** pushes it after that.
- **Override**, on a blocking review, counts that verdict as passed for that commit only, with your reason. A new commit needs a new review.
- **Flagged files** are files with secret-looking names (`.env`, `*.pem`, `*.key`, SSH keys, `credentials*`, `*.tfstate` and others), files over 1 MiB, and files holding private keys or known token formats, in any commit the push would send. **Allow it** takes your reason and covers that version of the file only; a later change flags it again. **Ask the agent to remove it** asks the session to take it out of every commit on the branch and add it to `.gitignore`. The page never shows a flagged file's contents.
- **Send findings**, when a reviewer blocks, sends the session the blocking reviewers' final messages, marked as untrusted findings to check, so the agent can fix them and push again. The push stays held, and Allow and Deny still work. It reaches only a running session this page controls, once per commit, and nothing is sent when the messages hold something shaped like a token.
- **The round limit.** After a reviewer's verdicts on the branch blocked as many times as the project's limit (overridden ones aside), it isn't started again. You decide: Override, Allow, Deny, or **Hand off**, which asks the session for a handoff of the open findings and then denies the push.

**When the agent asks first.** If the agent calls its push tool before the commit was reviewed, the engine starts that commit's reviews once, and when they all pass it pushes without you. If one blocks, the card waits for you. If the reviewers' container is signed in with a sign-in billed per use, the engine starts no review itself, and the push waits until you press Review. The agent can ask at most 5 times in 10 minutes, and the engine runs at most 12 reviewer runs per branch on the agent's requests, the runs it repeats after a restart included; after that you press Review. When two sessions are already being reviewed, the engine queues the request and starts its reviews when one finishes. With the project setting **Send blocking findings to the agent automatically**, the engine sends the findings itself when those reviews block, only when the agent and the reviewers both use a subscription, and never past the round limit or that cap.

**When the engine restarts.** A waiting automatic push is saved (`waiting.json`) and picked up at start: if the restart cut a review off, the engine runs that reviewer once more, and pushes when every review passes; if the second run is cut off too, the push waits for you. It drops the plan if the branch moved on or you denied the commit. A push that was going out when the engine stopped is checked against the remote at start and recorded as pushed or not. On a normal stop, a push or an approved issue or pull request already under way gets up to 25 seconds to finish. If the agent was in the middle of a turn, its next message starts with a note that its turn was cut off.

### Issues, pull requests and CI

The agent can't reach the git host, so it asks the engine, with four tools:

- **`factory_pull_request`** asks for a pull request from the session's branch to the project's main branch, once the branch is pushed. If one is already open, the agent gets its link instead, and pushing again updates it.
- **`factory_issue`** asks for an issue in the project's repository.
- **`factory_checks`** gives the agent the pushed branch's CI results: each check and commit status, and the last 200 lines of each failed GitHub Actions job's log, with escape and invisible characters removed, marked as untrusted text. It changes nothing and needs no answer from you.
- **`factory_review_comments`** gives the agent your own comments on the branch's open pull request: your reviews, inline comments with their file and line, and conversation comments, the newest 100. Only comments by the account the token belongs to are passed on; everyone else's, and the engine's own push notes, are left out. The agent is told to weigh them as feedback, not as instructions that override the page, since anyone with admin rights on the repository can edit a comment. It changes nothing and needs no answer from you.

An issue or pull request waits as a card under **Issues and pull requests** in the session's details, with the whole title and body shown as text. Read it: the engine posts it under your token. **Open issue** or **Open pull request** has the engine create it, and the card shows the link; **Deny** drops it. A pull request opens as a draft unless you clear **Open as a draft** on its card; you mark a draft ready for review on GitHub. If GitHub refuses drafts for the repository, it opens ready for review and the card says so.

If GitHub's answer to an approved issue or pull request is lost (a timeout, a dropped connection, a 5xx), the engine doesn't guess: it asks GitHub again (the branch's open pull requests, or the issues opened since you approved it), and if it still can't tell, marks the card **check on the host**, with links to the issues it found, and tells the agent not to ask again. A card still waiting when the engine restarts is dropped, and the agent is told to ask again if it still needs it.

**A comment after each push**, when you switch it on in the project's settings (off by default), is posted under your token on the branch's open pull request after each push, and once when a pull request opens for a branch already pushed. The engine writes it from its own records only: the full commit and branch, who caused the push (the agent after its reviews passed, your Push, or your Allow with the number of items missing, whose reasons stay in the audit), each required review with its result and whether it covered the whole branch or only what changed, and the versions of Software Factory, knackpack and Claude Code it has records of. It never includes your typed reasons, the reviewers' messages, flagged file names or costs. Each comment shows on the page as a card with its whole text, before and after it's posted, and a commit is commented on once. A failed comment doesn't change the push. The agent learns the result, with the number and link, before your next message to it. Only GitHub (github.com, and a GitHub Enterprise server whose host has a saved token) is supported; for other hosts the tools say so.

**The audit.** Every push request ends as a line in the engine's audit file, with your reason for any exception and never the token. So do your approvals of start-up files, requirements, plans and the team file (by hash, never the text). The panel lists the branch's exceptions and pushes, newest first. When the gate refuses something repeatedly in a project, such as a push command the agent typed, the page suggests saying what to do instead in the project's `CLAUDE.md`.

The engine refuses a push carrying files that the repository's `.gitattributes` sends through Git LFS, and repositories that use SHA-256 object names.

### Trust boundary

The engine, the reviewers' container and the proxy are trusted; the agents' container isn't. An agent acting on instructions planted in a repository, an issue or a web page can run anything there, including changing other sessions' worktrees and the plugin's reports. What the design relies on sits outside that container:

- **No route to a git host.** The agents' container is on an internal network whose only way out is the proxy, which allows only the model API hosts on port 443 and always refuses git hosts. The engine checks this before every start and every 5 minutes; a failed check stops new sessions and marks running ones Held.
- **The push.** Only the engine holds the git host token, and it never sends it to another container. Issues and pull requests the agent asks for are opened only when you approve them on the page.
- **The verdicts.** Reviewers run in their own container, on a copy of the commit the engine wrote, with read-only tools and hooks off. The agents' container can't reach it.
- **Your decisions.** Approvals, Allow, Deny and Override come only from the page. From the agents' network the engine accepts reports and requests, never a decision, a verdict or a push.

The page is published only on `127.0.0.1` and signs in without a code, so any program on the computer can use it. It checks every request's `Host`, `Origin` and `Sec-Fetch-Site`, sends no CORS headers and a strict content security policy, and shows everything from a session as text, so other websites and the agents can't drive it. A permission card allows only a call whose whole content the page showed you.

What it can't defend against is under [Limitations](#limitations).

### Cost and models

Software Factory calls no model itself and holds no model key. Every session runs in the vendor's own agent, on the sign-in you made in its container, so usage counts against your subscription or is billed to your API key as it would be without Software Factory. Every review, and every manual test list you ask for, is also a Claude Code run, on the reviewers' container's sign-in.

Before each start, wake and review, the engine has the agent's own status command (`claude auth status`, `codex login status`) report which kind of sign-in it uses: a subscription, an API key, a gateway (a custom base URL), Amazon Bedrock, Google Vertex AI, or not signed in. It reads only that kind and the endpoint's host, never the account. A start that isn't signed in, or uses another kind than the one you expect, is refused with the agent's sign-in step; the engine never tries another credential. On any sign-in but a subscription, the page asks you to confirm the cost before each start, wake, review and manual test list.

Costs on the page are the agents' own estimates, which may differ from your bill: each Claude Code session's cost, each review's and the session's reviews' total, and tokens for Codex.

Models: pick Claude Code's model in the new-session form (its default, Fable, Opus, Sonnet or Haiku). Codex uses its own default. Reviewers run on the reviewers' container's Claude Code default model, which the Code review panel names for each review.

To keep cost down: an agent's push request starts each reviewer at most once per commit (twice when an engine restart cut the first run off), later rounds review only what changed, the round limit stops a reviewer that keeps blocking, and the agent's requests start at most 12 reviewer runs per branch.

### Limitations

- The model API host an agent must reach also serves its vendor's other products, and an agent can send code to its model API. With the stored login, an agent could start a vendor cloud session that has its own access to your repository. **Don't connect a vendor's cloud agent (Codex cloud, Claude Code on the web and the like) to repositories you gate with Software Factory**: it pushes from the vendor's servers, outside the gate.
- The code under review can address its reviewer, such as with instructions written into the code. Reviewers may read only their review folder, and a reviewer that tries to read elsewhere counts as no verdict; that limit relies on a Claude Code setting not yet checked on a signed-in run. Findings you send to the agent are that reviewer's words, so they can carry such instructions too; the engine marks them as untrusted.
- Containers don't stop a kernel exploit.
- Nothing trusted runs a repository's own checks, such as `make check` or its tests, because the engine and the reviewers must never run what an agent wrote. Reviewers only read the code.
- Every agent can read every session's uploads, so another session's agent could read an image or file you sent.
- Sessions share one user, one home and one workspace in the agents' container. An agent misled by what it read could commit to another session's branch, or change user-level settings that every later session loads, such as Claude Code's or Codex's settings in that home. The reviews still bind to each commit, and the start-up approval covers only the repository's files.
- Only Claude Code (Anthropic) and Codex CLI (OpenAI) are offered.
- A plan made in Claude Code's plan mode can be approved only in Claude Code itself.
- The page can't start a session in auto or bypass permission mode.
- After an engine restart, the sessions it started come back Stopped: press **Wake** to continue one, which runs the start checks again. Cards that were waiting for an answer are gone (an issue or pull request card is recorded as dropped and the agent is told), and the conversation continues from what Claude Code saved. A session from before Software Factory recorded its sandbox checks comes back not sandboxed and can't push: start a new session for that work.
- Repositories that use SHA-256 object names are refused, and so are pushes carrying Git LFS files. The LFS check reads only the latest commit's `.gitattributes`.
- A flagged file whose path isn't valid UTF-8 can't be allowed.
- The Changes diff is cut at 1 MiB, and the flagged-file scan reads at most 1,000 commits and 32 MiB per push; beyond that, the push needs your Allow with a reason.
- The page runs on one computer, for one person, with one signed-in page at a time.

## Maintenance

- **Upgrade** with `git pull`, then `make up`. The engine installs the agent versions the new code pins when it starts. Logins, repositories, tokens, reviews and the audit stay; an open page reloads itself and signs in again once the new engine is up.
- **Install the agents again** with `docker compose exec engine software-factory install-agents`, after a failed download.
- **Sign Claude Code in again** when the page says its login failed: while it isn't signed in, the Welcome page's **Finish setting up** checklist offers **Sign in**; otherwise run `docker compose exec -it agent claude` (or `reviewer`), then `/login`. Codex signs in again with the command in [Run it with Docker](#run-it-with-docker).
- **Read the engine's log** with `docker compose logs engine`. It holds event kinds only, never prompts, file contents, keys or tokens.
- **Stop everything and keep the data** with `make down` (or `docker compose down`).

  **Warning:** `docker compose down -v` also deletes the volumes: every repository clone and the sessions' unpushed work, the git host tokens, the agents' and the reviewers' logins, the review records and the audit. It can't be undone.

The data lives in these Compose volumes, and nothing backs them up:

| Volume | Holds |
|---|---|
| `engine-state` | The project list, git host tokens, review records, approvals, expected sign-ins, project settings, the sessions it started with their work types and your approvals of requirements and plans, and the audit file. Only the engine mounts it |
| `workspace` | The repository clones and every session's worktree |
| `agents` | The installed agents, knackpack and Spec Kit |
| `agent-home`, `reviewer-home` | Claude Code's and Codex's own logins, in each container |
| `reviews` | The read-only copies under review, removed when a review ends |
| `uploads` | Images and files you sent, held in memory and gone when the stack stops |

## Development

[AGENTS.md](AGENTS.md) has the code layout, the dependency rule, the commands and every CI job. Software Factory has no automated tests: a change is checked with the build, `go vet`, `make lint`, `make arch-check`, `make fix-check`, `make vuln`, `make web-check`, `make plugin-check`, `make plugin-validate` and the other checks AGENTS.md lists, then by trying it with the development overlay.

**The development overlay** is for developing Software Factory itself. Never use it for real work: its reviews are fake.

It runs as its own Compose project, `software-factory-dev`, with its own volumes, so its fake reviews never reach the real stack's repositories and tokens. Both stacks publish port 7799, so stop the real one first (`docker compose down` keeps its data), or give the overlay another `SF_PORT`. Set `COMPOSE_FILE` in the shell you use for it, so every `docker compose` command, including the ones the page shows, goes to the overlay:

```bash
export COMPOSE_FILE=compose.yaml:compose.dev.yaml
docker compose up -d --build
```

It adds:

- a fake agent, **Development ACP agent**, in the agents' container;
- a fake reviewer that runs in place of Claude Code (the engine logs a warning). It passes, or blocks when the reviewed commit holds a file named `BLOCK-REVIEW`, and answers after 45 seconds when it holds `SLOW-REVIEW`;
- a local git server that only the engine can reach. Save the token in `GITSERVER_TOKEN` in `compose.dev.yaml` for `https://gitserver.test:8443`, then add `https://gitserver.test:8443/demo.git`. Add a commit to its main branch with `docker compose exec gitserver sf-gitserver commit "a new commit"`, and see a pushed branch with `docker compose exec gitserver git -C /srv/git/demo.git log --oneline sf/<id>`.

The fake agent and reviewer report the sign-in written in `$HOME/.sf-fake-signin` in their container (`subscription`, `api-key`, `gateway` or `none`), such as `docker compose exec agent sh -c 'echo api-key > $HOME/.sf-fake-signin'`. `make plugin-validate AGENTS_VOLUME=software-factory-dev_agents` validates the plugin with the overlay's installed Claude Code.

**Hand checks.** With no automated tests, run these with the development overlay before a change is merged, for the parts it touches:

1. Each agent the page offers: start a session, and check what the page shows (agent, sign-in, trust level, work type).
2. Send a message, then press Stop during the turn.
3. Answer a question card and a permission card from the page, with Yes, No and a typed answer.
4. Press Review, wait for both reviewers, then Push; with a `BLOCK-REVIEW` file, see the push held, then Deny it and see that the reviews finishing never push it.
5. Run two sessions at once and check that cards, files and pushes stay with their own session.
6. Reload the page with a card open, then restart the engine (`docker compose restart engine`): check that the page reloads itself and signs in again, that the session comes back Stopped with its work type and approvals, and that **Wake** continues its conversation.
7. Make the sandbox check fail ([AGENTS.md](AGENTS.md) "Hand checks with Compose"), and check that starts are refused and every running session shows Held.
8. Inside the agent container, check that `curl --connect-to gitlab.com:443:chatgpt.com:443 https://gitlab.com/` fails, and that `curl https://github.com` is refused.
9. Run the page's curl checks in [ARCHITECTURE.md](ARCHITECTURE.md#page-security-checks).

**Live changes.** `make dev` runs the real stack, on the same port and volumes as `make up`, from the code in your folder, and rebuilds it every time you save:

- it builds the page and a Linux binary on your computer, and `compose.live.yaml` mounts that binary into the four containers in place of the image's;
- it watches `cmd`, `configs`, `internal`, `tools`, `web`, `go.mod`, `go.sum`, `README.md` and `compose.yaml`. A save that changes only the page (`web` or `README.md`) recreates the engine alone, in about 10 seconds; any other change recreates all four containers, in about 15, which also applies a change to `compose.yaml`. The open page reloads itself once the engine is back;
- **each save stops the running Claude Code sessions**, since the engine stops sessions it doesn't track when it starts: they come back Stopped, and Wake continues them;
- a build that fails leaves the running stack as it was; fix the error and save again;
- Ctrl-C stops watching and leaves the stack running the last build, letting a restart in progress finish first; `make up` goes back to the image;
- whatever is in your folder runs as the engine, which holds your git host tokens. **Stop `make dev` before a pull, a branch switch or a checkout of code you haven't read.** If the checked-out commit changes along with the files, `make dev` stops by itself and leaves the last build running;
- uploaded images are kept in memory, so every restart deletes them;
- the `Dockerfile` and `plugin/` are built into the image, which `make dev` uses as the last `make up` built it. A save there prints a reminder: press Ctrl-C, run `make up`, then `make dev` again.

For work on the page alone, `make run` runs the engine on your computer without Docker; it starts no sessions without an agent-host.

**Making a change.** Changes reach `main` only through a pull request merged by squash:

1. Open an issue for the change.
2. Work on a branch, and open a pull request whose description closes the issue, such as `Closes #12`.
3. CI runs its checks, and Claude reviews every push against `CLAUDE.md` and `ARCHITECTURE.md`. Every problem it confirms blocks the merge, however minor; fix it and push again.

A pull request that changes the checks themselves (`.github/`, `scripts/`, the `Makefile` or a linter's settings) also needs the owner's `review-override` label after its latest push. AGENTS.md, under "CI", says what each check does.

## Privacy

Software Factory runs on your own computer. Its author runs no server for it and receives none of your data.

- **What leaves your computer:** what each agent sends to its vendor's model API, under your own account: your messages, the code and files it reads, and your uploads. The reviewers send the commit under review to Anthropic's API the same way. The engine talks to your git host to clone, fetch and push, with your token, and at install to the npm registry and GitHub. The proxy refuses the agents and reviewers every other host.
- **What it keeps, and where:** in Docker volumes on your computer ([Maintenance](#maintenance)): the repositories and the agents' work, the git host tokens, the reviewers' verdicts and final messages (up to 4,000 characters each), the audit file and your approvals. The conversations are kept by the agents themselves, in their own containers; the engine keeps sessions in memory only.
- **What it never stores or logs:** any model credential. The agents' logins are the vendors' own files in their containers, which the engine doesn't mount or read, and every process the engine starts gets the model key variables cleared. An API key you paste in the sign-in dialog goes straight to the container it signs in, where Claude Code reads it through its `apiKeyHelper` setting; the engine doesn't keep it, and the page never shows it again. The git host token is never shown, logged or sent to another container. The log holds event kinds only, never keys, sign-in codes, tokens, prompts, tool input or file contents.
- **Removing it:** delete the token on your git host, then run `docker compose down -v` to delete everything Software Factory stored, the agents' logins included.

Questions go to the repository's [issues](https://github.com/bezmoradi/software-factory/issues).

## Author

This project is maintained by [Bez Moradi](https://github.com/bezmoradi).

## License

Software Factory is licensed under [MIT](LICENSE). The image includes the license texts of the Go modules linked into it, Go's own and TypeScript's in `/usr/share/doc/software-factory/`.

The page's icons are [Codicons](https://github.com/microsoft/vscode-codicons) from VS Code, © Microsoft Corporation, used under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/). Its eight themes take their colors from these VS Code themes, adjusted for WCAG AA contrast: [Solarized](https://ethanschoonover.com/solarized/) Dark and Light, VS Code's own Dark Modern and Light Modern ([microsoft/vscode](https://github.com/microsoft/vscode)), GitHub Dark Default and GitHub Light Default ([primer/github-vscode-theme](https://github.com/primer/github-vscode-theme)), [One Dark Pro](https://github.com/Binaryify/OneDark-Pro) and [Dracula](https://draculatheme.com).
