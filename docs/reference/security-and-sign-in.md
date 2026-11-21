# Reference: security and sign-in

This is part of the Reference section of [ARCHITECTURE.md](../../ARCHITECTURE.md#reference), which describes how Software Factory works and why.

## Page security checks

Every request on the page and the agent listener has a read deadline: 10 seconds for the headers and, from the handler, 15 seconds on the page and 30 on the agent listener for the rest, whatever the method, so a client that sends a `Content-Length` and no body is cut off. A refusal on the agent listener before the handler reads the body (a browser, an unknown route or method, a wrong type, container or secret, a busy container) goes at once and closes the connection, rather than waiting for the body. `/poll` and `/wait` get 40 seconds, and the page's `/api/events` stream none once it is open. A stream re-checks the page key after it attaches and closes with 401 if a sign-in or expiry changed it in between. agent-host gives a request's body 30 seconds to arrive and lifts the limit once it has, so its long runs keep going.

Run these against the published port after any change to `internal/adapters/http/page` or `cmd/server`, with a page key from a sign-in (`Authorization: Bearer <key>`) and `Origin: http://127.0.0.1:<port>` unless the check removes them. Each POST route must answer:

| Request | Expected |
|---|---|
| no `Authorization` header | 401 `{"error":"token"}`, with no `why` |
| `/api/events` or a POST route with a made-up key of the key's length, or the page key under `Basic` | 401 `{"error":"token"}`, with no `why` |
| `/api/disconnect`, the route the page's Disconnect used, even with the page key | 404 `route` |
| `/api/agent-models` and `/api/handover`, routes since removed, even with the page key | 404 `route` |
| `/api/egress` with `op: "agents"`, Network access's removed agents choice | 400 `op` |
| `/api/events` or a POST route with a key a newer sign-in replaced | 401 `{"error":"token","why":"replaced"}` |
| `/api/events` or a POST route with a key that ended after 15 minutes with no stream | 401 `{"error":"token","why":"ended"}` |
| a `Host` other than `127.0.0.1:<port>` | 421 |
| an `Origin` from another site | 403 |
| `Sec-Fetch-Site: cross-site` | 403 |
| a POST with no `Origin` | 403 |
| a `Content-Type` other than `application/json` (`/api/upload` takes the image types and `application/octet-stream`) | 415 |
| a body over 64 KiB (1 MiB on the approval routes and `/api/reviewer-agents`, 2 MiB on `/api/packs`) | 413 |
| a query string | 400 |
| GET on a POST route | 405 |
| unknown or wrong-typed JSON fields | 400 |
| `/api/file-check` with a `paths` that isn't a list of 1 to 20 strings of 1 to 1,000 bytes | 400 `paths` |
| `/api/file-check` for a worktree path that is a symbolic link, one with a secret-looking name, and a missing one | 200, the first two in `unread` as `link` and `secret`, the missing one in neither `unread` nor `found` |
| `/api/screenshot` for a file in the session's screenshots folder that isn't a PNG, JPEG or GIF, such as a text file named `x.png` | 400 `not image`, with no `data` |
| `/api/screenshot` for a file over 10 MiB, or over 8,192 pixels a side | 400 `too big` or `too large`, with no `data` |
| `/api/screenshot` for a symbolic link in that folder, or a name with `/` or `..` | 404 `screenshot` or 400 `name` |
| `/api/screenshot` for a valid image | 200 JSON with `type` `image/png` and `data` in base64; the `Content-Type` is `application/json` |
| `/api/screenshots` or `/api/screenshot` for a session the engine didn't start | 404 `session` |
| `/api/halt` `{op: "away", on: true, sid}` for a `sid` this engine didn't start | 409 `not started here`, and `away` unchanged |
| `/api/review-reply` for a session the engine didn't start, or a commit, agent or run with no review record | 404 `reply` |
| `/api/review-reply` for a role whose panel shows The whole report | 200 JSON with `text` (at most 64 KiB) and `clipped` |
| `/api/changes` with `from` other than `fork`, `reviewed`, `turn` or `commit`, `commit` without `from: "commit"`, `from: "commit"` without a 40-hex `commit`, or another field | 400 `fields` |
| `/api/changes` with `from: "commit"` and a commit that isn't the head or its ancestor, or `from: "reviewed"` or `"turn"` with no such commit | 409, with the reason in `message` |
| `/api/changes` with `from: "reviewed"` after a review | 200, `base` the reviewed commit, `from` `reviewed`, `files` only those changed since it |
| `/api/viewed` with a missing or wrong-typed field, another field, a `blob` that isn't 40 hex digits or a `path` over 4,096 bytes | 400 `fields` |
| `/api/viewed` for a session the engine didn't start | 409 `not started here` |
| `/api/viewed` with `viewed: true` for a blob or path the session's branch doesn't change, such as another session's commit as the blob | 409 `not on branch` |
| `/api/viewed` with `{sid, path, blob, viewed: true}`, then `/api/changes` | 200 `{"ok":true}`, then that file with `viewed: true`; after a commit that changes the file, `viewed: false` and `stale: true` |
| `/api/lock-tests` with an `op` other than `lock` or `unlock`, a `lock` without a `head` of 40 or 64 hex digits, an `unlock` with a `head`, or another field | 400 `op` or `fields` |
| `/api/lock-tests` for a session the engine didn't start | 409 `not started here` |
| `/api/lock-tests` `lock` with a `head` that isn't the session branch's head now | 409 `changed` |
| `/api/lock-tests` `unlock` for a session with no locked tests | 409 `not locked` |
| `/api/verify` with an `op` other than `read`, `start`, `stop` or `attachment`, no `sid`, another field, a `start` without a `kind`, a `stop` without an `id` of 16 hex digits, or an `attachment` without an `id` of 16 hex digits and a `name` | 400 `op` or `fields` |
| `/api/verify` `start` with a `kind` other than `check` or `browser`, or `attachment` with a `name` that isn't a plain `.png`, `.jpg`, `.jpeg`, `.gif` or `.zip` name | 400 `kind` or `name` |
| `/api/verify` for a session the engine didn't start | 409 `not started here` |
| `/api/verify` `start` for a project with no check command, no browser command for `browser`, or no image | 409 `no-check`, `no-browser-command` or `no-image` |
| `/api/verify` `attachment` of a file the run didn't keep | 404 `attachment` |
| `/api/verify` `attachment` of a trace | 200 `application/octet-stream`, `Content-Disposition: attachment`, `X-Content-Type-Options: nosniff` |
| `/api/verify` with both `sid` and `repo`, a `repo` that isn't a project name, a `read` with `repo` and another field, or a `start` with `repo` and no `kind` | 400 `fields` |
| `/api/verify` with `repo` and an `op` other than `read` or `start` | 400 `op` |
| `/api/verify` `start` with `repo` and a `kind` other than `check` | 400 `kind` |
| `/api/verify` `start` with `repo` for a project not in the list | 404 `project` |
| `/api/verify` `start` with `repo` while a run on main of that project runs | 409 `running` |
| `/api/verify` `start` with `repo` when main can't be fetched, such as with the host's token removed | 409 `fetch`, and no run recorded |
| `/api/project-commands` `set` with an `image` that isn't a string, or isn't a name pinned by `@sha256:` and 64 hex digits | 400 `fields`, or `image` with `field` `image` |
| `/api/import` with a `Content-Type` other than `application/gzip`, or with parameters | 415 |
| `/api/import` with a `Content-Length` or a body over 256 MiB | 413 |
| `/api/import` into an engine with a project or a session | 409 |
| `/api/import` of an archive whose `repos.json` holds a project named `../../evil`, or one with an `http://` URL and the branch `--upload-pack=x` | 400 `archive`, naming `repos.json` and the field, and no `.import/` folder left in the state folder |
| `/api/export` | 200, `Content-Type: application/gzip`, `Content-Disposition: attachment`, the headers below |
| `/api/wake` with a `home` that isn't 64 lowercase hexadecimal digits, a `refuse` that isn't `true`, or `refuse` with a field other than `sid` | 400 `fields` |
| `/api/message` with a `home` that isn't 64 lowercase hexadecimal digits | 400 `text` |
| `/api/dispatch` with a `home` | 400 `fields` |
| A start (`/api/dispatch`, `/api/pair`) with a bad `issueHash`, or a `benchmark` beside `from`, `issue` or `message` | 400 `issue`, `benchmark`, `from` or `message`, with `field` |
| `/api/wake` `{sid, refuse: true}` for a session whose wake isn't waiting on the settings question | 409 `not asking` |
| `/api/wake` with `approve` set to the settings question's `digest` | 409 `needs approval` with the same `needs.home` |
| `/api/diagnostics` with any field in the body | 400 `fields` |
| `/api/limits` `session`, `project` or `check`, or a start's `caps`, with a cap that is negative, over its range, fractional where it must be whole, or not a number | 400 `fields`, with `field` naming it and a `message` giving its range |
| `/api/limits` `session`, `project` or `check` with another field; `measure` with a field other than `sid` | 400 `fields` |
| `/api/project-commands` `set`, `/api/review-depth` `set`, `/api/project` `hold`, `/api/split` `edit` or `/api/egress` `gateway` with a value its rule doesn't take | 400 with the route's code (`command`, `browser`, `image`, `depth`, `agent`, `hold`, `text`, `gateway`; a gateway host egress refuses keeps its own code and status, with `field` `url`), `field` naming the body's field (`check`, `setup`, `browser`, `image`, `advisory`, `extra`, `runs`, `maxFiles`, `maxLines`, `extraRuns`, `afterBlock`, `title`, `message`, `url`, `agents`) and a `message` |
| `/api/lessons` `approve` with an empty `sid`, or one that isn't a session of the project | 409 `session`, with `field` `sid`; a `sid` that isn't a string or isn't a session id's shape stays 400 `fields` |
| `/api/message` with `revise` whose `note` is only white space or over 2,000 characters, or `comments` with none, more than 50, one that is only white space or over 2,000 characters, or over 20,000 characters as a message | 400 `note` or `comments`, with a message |
| `/api/message` with `revise`, `comments` or `ask` beside `text`, each other, images or files, or of the wrong shape, or an `ask` the engine doesn't name | 400 `text` |
| `/api/message` with an `ask` the session's workflow doesn't offer now | 409 `step`, with a message |
| `/api/limits` `measure` for a session that isn't at its disk cap | 409 `not reached` |
| `/api/update` with a field other than `later` (`check`, the removed **Check for updates** setting, included), or a `later` that isn't a string of 1 to 40 bytes | 400 `fields` |
| `/api/update` with `{}` | 200 JSON with `running`, `forced`, `unreadable`, `development`, `result`, `checked` and `update` (`null`, or `version`), and no URL |
| `/api/update` with `{later}` naming a version the page doesn't show | 409 `update`, with the reason in `message` |
| `/api/packs` with an `op` other than `list`, `fetch`, `add`, `add-use`, `remove` and `use` | 400 `op` |
| `/api/packs` with no `path`, a missing, empty or wrong-typed field of the op, or another field | 400 `fields` |
| `/api/packs` with a body over 2 MiB | 413 |
| `/api/packs` `upload` with a path outside `agents/`, `skills/<skill>/` and `commands/` (such as `hooks/hooks.json` or `agents/../bin/x`), a file that isn't UTF-8 or holds a NUL, more than 200 files, a file over 256 KiB or more than 2 MiB in all, or the name `knackpack` | 400 `refused upload`, naming the file and the reason |
| `/api/packs` `fetch` with an `http://` address or a commit that isn't 40 hex digits | 400 `url` or `commit` |
| `/api/packs` `fetch` of a commit whose manifest holds an engine-owned key, names another plugin's command or a missing agent, or takes the name `knackpack`, or whose agent's front matter holds `hooks` or `skills` | 400 `pack`, naming the file and the reason, and nothing in `packs/` but the packs already kept |
| `/api/packs` `add` with a digest that isn't the commit's reviewers' digest | 409 `digest`, naming both |
| `/api/packs` `remove` of a pack a project uses | 409 `in use`, naming the projects |
| `/api/packs` on the agent listener (`engine-agents:8081`), from a session's container | 404, as every page route there |
| `/api/push-record` with another field, no `sid`, or a `commit` that isn't 40 or 64 lowercase hex digits | 400 `fields` |
| `/api/push-record` for a session the engine didn't start | 409 `not started here` |
| `/api/push-record` for a commit of a live session that was held, denied or never pushed | 404 `push` |
| `/api/push-record` for a pushed commit | 200, `Content-Type: application/json; charset=utf-8`, `Content-Disposition: attachment; filename="software-factory-push-<12 hex>.json"`, a body whose `format` is `software-factory-push-record/1` and in which a grep for the page key and the git host token finds nothing |
| `/api/diagnostics` with `{}` | 200 JSON with `text`, which starts `software-factory-diagnostics/1` and in which a grep for the project's name, the git host, `/workspace`, `/var/lib`, `https://` and `@` finds nothing |
| any route | an `X-SF-Request` header of 16 hex digits, a new one on each request, which the page doesn't show |

`make proctor` sends the rows it can as Proctor specifications (`proctor/specs/page-security.yaml` and `fields.yaml`), in addition to these checks with curl; [`proctor.md`](../../.claude/rules/proctor.md) lists the rows it can't.

`GET /`, as a navigation (`Sec-Fetch-Mode: navigate`, `Sec-Fetch-Dest: document`), must answer 200 with `Sec-Fetch-Site: none` or `same-origin`, or with no `Sec-Fetch-*` headers, and 403 with `cross-site` or `same-site`. Any other site loading the page would make it sign in again, which ends the person's open tab, so a link to the page from another site or another local port is refused; typing the address or opening a bookmark works. `Sec-Fetch-Site: none` on anything but a navigation of `/` is 403.

Every answer carries `Content-Security-Policy` (`style-src 'self'`, no inline script), `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY` and `Referrer-Policy: no-referrer`, and never an `Access-Control-*` header. The page key travels only in that header, never in a URL after the first exchange. Logs hold event kinds only, never keys, tokens, prompts, tool input or file contents. State files are 0600 in a 0700 folder.

## Sign-in and cost

Code: core `session/signin.go`, adapter `agenthost/signin.go`.

Each agent spec names its status command (`claude auth status`) and its own sign-in step. Before a start or wake, after the start-up files, the engine asks agent-host's `/v1/sign-in` to run it with the session's working folder, passed variables and cleared key variables; for Claude Code agent-host runs it with `--json` (only `loggedIn`, `authMethod`, `apiProvider`, and for a claude.ai login `loginExpiresAt`, `planTier` and `subscriptionType` are decoded; never `email`, `orgId` or `orgName`) and `--text` (only the `Anthropic base URL:` line, reduced to its host). The output, which names the account's email and organisation, never leaves agent-host and is never logged; the reply is a method word, a host, and for a subscription the plan and the sign-in's end, which the engine checks again and maps onto `subscription`, `api-key`, `gateway`, `bedrock`, `vertex`, `not-signed-in` or `unknown`. `signin.json` keeps the method expected per agent and for `reviewer` (`any`, `subscription`, `api-key`, which accepts every per-use sign-in), set with `/api/sign-in-expect` (`{agent, expect}`, which the new-session form uses for the development ACP agent) or, for Claude Code and `reviewer`, by finishing a sign-in in the session's sign-in panel or the reviewers' sign-in dialog; with nothing stored, the first sign-in seen is stored (`Expected.Adopt`). Not signed in or a mismatch refuses with both methods and the step; any sign-in but a subscription refuses until the request carries `costOk: true` (starts, wakes and `/api/review`). Reviews check the reviewers' sign-in the same way, in a reviewer container started for the check (`Reviews.CheckSignIn`, also once at engine start, shown with a `reviewer` frame); reviews `Pushes.reviewFor` would start never carry the confirmation, so on a per-use reviewer the push stays held. Sessions carry `signIn`; reviews carry the reviewer's `signIn` and costs (`costUsd` per role and review, `sessionCostUsd` for every recorded review of the branch, from `total_cost_usd`); `usage` frames carry ACP agents' and Claude Code's (`$.session.usage().cost`, from the plugin) `costUsd` with `estimate: true`. The dev fakes answer `auth status` from `$HOME/.sf-fake-signin` (subscription, api-key, gateway or none) and, like the real command, name an email and an organisation, so a development run can check that neither leaves agent-host. The fake ACP agent's `auth login` prints a `claude.com` sign-in link, reads a code (any code but `bad` passes) and writes `subscription` there, so a development run can go through the session's sign-in panel. The fake ACP agent's subscription also reports `planTier` `max_20x`, and `loginExpiresAt` from the first line of `$HOME/.sf-fake-login-ends` in the session's container when that file exists.

The values Claude Code 2.1.322 reports: `authMethod` is none, claude.ai, api_key, api_key_helper, oauth_token or third_party; `apiProvider` is firstParty, bedrock, vertex, foundry, anthropicAws, anthropicGoogleCloud, mantle or gateway. `oauth_token` without a custom base URL maps to unknown on purpose: it may be a subscription's long-lived token or a bearer for someone's endpoint, and the engine can't tell which. `claude auth status --json` prints indented JSON, so it is read from the first `{` to the end. Recheck both lists when the pins move.
