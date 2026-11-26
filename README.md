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

Software Factory runs Claude Code in sandboxed containers on your own computer, one container per session, and lets you follow and steer every session from one page in your browser. The agents can't reach your git host: only the engine pushes their work, to a branch of its own, after independent reviewers have passed that exact commit.

## What you need

- Docker Engine 28 or later with Docker Compose 2.24.4 or later: Docker Desktop on macOS or Windows, or Docker Engine with the Compose plugin on Linux. On Windows, run the commands in a WSL terminal.
- An `amd64` or `arm64` computer. Each running session may use up to 2 GiB of memory and 2 CPUs, and so may each running reviewer.
- A Claude account for Claude Code: a Claude subscription or an Anthropic API key.
- A git repository reachable over `https://`, and a token for its git host to push.
- Internet access at first start, when the engine downloads Claude Code and knackpack.

## Install

```bash
curl -fsSL https://github.com/bezmoradi/software-factory/releases/latest/download/install.sh | sh
```

The script installs the newest release into `~/software-factory` and starts it. Run it again to upgrade. To check the script before it runs, see [Run it with Docker](web/docs.md#run-it-with-docker).

Or build it from source:

```bash
git clone https://github.com/bezmoradi/software-factory.git
cd software-factory
make up
```

## Get started

1. Open <http://127.0.0.1:7799> if `make up` didn't. On first start the engine downloads the agents, which takes a few minutes.
2. Sign the reviewers in: open **Reviewers’ sign-in** in the command palette.
3. On the Projects page, save a token for your git host, enter the repository's `https://` address and press **Add repository**. Set the Git name and email agents commit with when the page asks.
4. Press **New session** on the project's row, then **Start**, and sign the session's agent in when the page asks.

## Docs

The page has the full documentation: press **Docs** (the book icon) in the activity bar, or **Guides and how-tos** on the Welcome page. Every dialog's **View in docs** link opens its section there.

## Contributing

[CLAUDE.md](CLAUDE.md) has the code layout, the commands and the checks.

## Privacy

Software Factory runs on your own computer. Its author runs no server for it and receives none of your data. [Privacy](web/docs.md#privacy) in the full docs says what leaves your computer.

## License

Software Factory is licensed under [MIT](LICENSE). The image includes the license texts of the Go modules linked into it, Go's own and TypeScript's in `/usr/share/doc/software-factory/`.

The page's icons are [Codicons](https://github.com/microsoft/vscode-codicons) from VS Code, © Microsoft Corporation, used under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/). Its eight themes take their colors from these VS Code themes, adjusted for WCAG AA contrast: [Solarized](https://ethanschoonover.com/solarized/) Dark and Light, VS Code's own Dark Modern and Light Modern ([microsoft/vscode](https://github.com/microsoft/vscode)), GitHub Dark Default and GitHub Light Default ([primer/github-vscode-theme](https://github.com/primer/github-vscode-theme)), [One Dark Pro](https://github.com/Binaryify/OneDark-Pro) and [Dracula](https://draculatheme.com).
