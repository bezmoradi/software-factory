---
paths:
  - "internal/adapters/http/page/*.go"
  - "internal/adapters/loggers/slog/*.go"
  - "internal/core/shared/request.go"
  - "cmd/server/server.go"
---

# Page server

## Health

The page server answers the health probe outside its middleware chain, so probes stay cheap and unlogged:

| Method | Path | Purpose | Success |
|---|---|---|---|
| `GET` | `/healthz` | liveness — the process is running | `200 {"status":"ok"}` |

The page's own routes (`/api/…`, including `/api/review`, `/api/test-list`, `/api/walkthrough-check`, `/api/push`, `/api/push-answer`, `/api/send-findings`, `/api/changes`, `/api/sign-in-expect`, `/api/recap`, `/api/workspace`, `/api/exceptions`, `/api/limits`, `/api/pull-request`, `/api/issues`, `/api/finding-issues`, `/api/review-depth`, `/api/lessons`, `/api/metrics`, `/api/decision-log`, `/api/screenshots`, `/api/screenshot`, `/api/split`, `/api/copies`, `/api/diagnostics`, `/api/viewed`, `/api/lock-tests`, `/api/update`, `/api/packs`, `/api/session-name`, `/api/preview`, `/api/access` and `/api/credentials`) need the page key, the page's `Host` and `Origin`, and JSON; other paths are `404`. The handlers live in `internal/adapters/http/page`.

`/api/push-record` (`{sid, commit}`) takes the same checks and answers its record as a JSON attachment rather than through `send`.

`/api/keep`, the engine's copy of a session's commits and Restore branch, and `/api/verify`, a session's verification runs (`op` `read`, `start` with `kind` `check` or `browser`, `stop` with a run's `id`, or `attachment` with a run's `id` and a file's `name`, which answers a trace as a download rather than JSON), take the same checks. `/api/packs` takes them too and sets its own write deadline for `fetch`, `add` and `add-use`, which fetch a commit for up to 2 minutes (`session.PackFetchFor`). On the agent listener, `/split-request` (an agent's proposal to split its goal into tasks), `/work-type-request` (an agent's suggestion of its session's work type), `/copy-request` (an agent's request for a read-only copy of another repository, which `/api/copies` approves, declines or removes), `/access-request` (an agent's request for a host or a saved credential) and `/action-request` (an agent's request to run a saved action, or to read a run's output) are authenticated by the session's push token like `/push-request`. `/helper-copy` (`{name}`, the plugin's `WorktreeCreate` hook) and `/helper-copy/remove` (`{path}`, its `WorktreeRemove` hook) take the session's connection secret, as `/turn-check` does, never the check-only key; the session and its box come from the connection, never the body, and the engine decides the folder (`/workspace/s/<box>/helpers/h-<name>`), the branch (`helper/<name>`), the base (the session branch's head) and the cap (8), and sets its own write deadline of 4 minutes. `/ssh-keys` (GET) and `/ssh-sign` (agent-host's ssh-agent socket's list and sign requests) take no token: the session is the one running in the container whose /28 address pair the connection comes from (`ConnBox`), and the core checks each against that session's live grants.
The export and import routes are `/api/export` (answers a gzip tar stream), `/api/import` (takes `application/gzip`, at most 256 MiB, instead of JSON), `/api/backup` and `/api/clone-again`.

Adding or removing a route also updates its rows in Proctor's page-security tables and its entry in `proctor/dimensions.yaml`: [Changing the API](proctor.md#changing-the-api).

## Middleware

Requests other than the health probe pass through a middleware chain composed in `internal/adapters/http/page` (`Server.Page`):

- **recover**: a panic becomes a logged `500` with its stack, not a crash. `http.ErrAbortHandler` passes through untouched, and a panic after the response started aborts the connection rather than appending a `500`.
- **request logging**: method, route (a known route's path; `asset` for this engine's page files; `old-asset` for an earlier engine's, which the page's restart probe in `web/stream.ts` asks for and gets a `404` by design, since each engine start picks a new `/s/<hex>/` prefix; `other` for the rest), status, duration, and a new random request id for each request (`req=<id>`), which goes on in the request's context to the engine's calls to agent-host and egress (`X-SF-Request`) and, on the page side only, back in the answer's `X-SF-Request` header; an id a request brings is never used. The `slog` adapter also keeps the last 500 events, as kinds only, for Diagnostics.

The checks every page route makes (the key, `Host`, `Origin`, JSON, body caps of 64 KiB, or 1 MiB on the approval routes and `/api/reviewer-agents`, and field names) are in the page's own handler, not the chain. Proctor's specifications of these routes: [proctor.md](proctor.md). Handlers that wait set their own deadlines (`PollFor`, `pushFor`).

**Every refusal carries words.** Each non-2xx JSON answer on the page listener has a non-empty `message` in plain English, the security refusals included (the owner's decision REQ-40), so the page shows the engine's words and none of its own. `refuse` and `reject` take the message as an argument (the shared ones are the `said…` constants in `security.go`), `status()` copies a `session.Error`'s `Message`, which the core's constructors (`invalid`, `conflict`, `notFound`, `limited`, `full`) take as an argument, and an error that isn't a `session.Error` answers 500 `server` with words. The agent listener's answers stay without words: its handlers use `agentRefuse`, and the core's `toAgent` and `unauthorized` serve only its registry methods. `scripts/check-refusals.sh`, which `make files` runs, fails naming file:line on a refusal whose message is an empty literal, a `session.Error`, `problem` or `capsProblem` literal without `Message`, an `"error":` map without `"message"`, `agentRefuse`, `agentError`, `toAgent` or `unauthorized` outside the agent side's functions it lists, or a field message over 60 characters (an `onField` constructor's, a start field's, a `refusal`'s, a gateway `bad`'s and a `withName`'s, counting the literal words of a message joined with `+`, and a `…Rule` constant, a token check's `Problem`, and the page adapter's `Field:`, `refuseCaps` and `refuseDepth` messages); a message passed in a variable or built over several lines can still be empty or longer, which only a review catches. A field message that names something of variable length builds it with `withName` (`internal/core/session/errors.go`), which clips the name so the whole stays within 60, and never carries a Go error's text. A message about one field doesn't start with the field's name ([Page modules](web-page.md)).

**Security headers** (`nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, a strict `Content-Security-Policy`) wrap the whole server, the health probe included. The page itself (`GET /`) adds `frame-src http://127.0.0.1:<preview port>` to that policy while the preview listener runs, so it can frame the preview and nothing else; every other answer keeps the policy as it is.

## Preview listener

`SF_PREVIEW_ADDRESS` (`0.0.0.0:8082` in `compose.yaml`, published on `127.0.0.1:${SF_PREVIEW_PORT:-7800}`) is a third listener, a reverse proxy (`Server.Preview`, `internal/adapters/http/page/preview.go`) to one session's app: the box's address and one of the session's 10 ports, which `/api/preview` (`{sid, port}`, `port` optional, the first by default) sets and `session.Previews` holds, one at a time. On `0.0.0.0` it keeps only the connections the page's listener keeps (`pageOnly`), and at most `PreviewConns` (64) at once.

- `/api/preview` answers `url` (`/.sf-preview?code=<64 hex>` on the preview's port, a code that works once, for 60 seconds), `tab` (the preview's `/`), `port` and `ports`. A new one replaces the target and ends the cookie before.
- `/.sf-preview` swaps the code for a new random `sf-preview` cookie (`HttpOnly`, `SameSite=Strict`, `Path=/`) and answers `303` to `/`.
- Its chain is only **recover** (`side=preview`); it logs each `/api/preview` and code swap, never each request, since an app's dev server loads hundreds of files. The handler refuses another `Host` with `421`, and a request without the current cookie with `403` "Open the preview from Software Factory's page."; while the session isn't running it answers `502` "The session is stopped.", and when nothing listens on the port `502` with what to do. These answers are plain text with `nosniff`, `no-store` and `frame-ancestors` the page's origin.
- What passes goes to the app with the `sf-preview` cookie taken out and the browser's `Host` kept, through `httputil.ReverseProxy`'s `Rewrite`, which passes WebSocket upgrades (Vite's hot reload). It adds `Content-Security-Policy: frame-ancestors <page origin>` to the app's answers, so only the page frames the preview, and changes nothing else in them.
