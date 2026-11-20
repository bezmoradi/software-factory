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

The page's own routes (`/api/…`, including `/api/review`, `/api/test-list`, `/api/walkthrough-check`, `/api/push`, `/api/push-answer`, `/api/send-findings`, `/api/changes`, `/api/sign-in-expect`, `/api/recap`, `/api/workspace`, `/api/exceptions`, `/api/limits`, `/api/pull-request`, `/api/issues`, `/api/finding-issues`, `/api/review-depth`, `/api/lessons`, `/api/metrics`, `/api/decision-log`, `/api/screenshots`, `/api/screenshot`, `/api/pair`, `/api/split`, `/api/diagnostics`, `/api/viewed`, `/api/lock-tests`, `/api/update` and `/api/packs`) need the page key, the page's `Host` and `Origin`, and JSON; other paths are `404`. The handlers live in `internal/adapters/http/page`.

`/api/keep`, the engine's copy of a session's commits and Restore branch, and `/api/verify`, a session's verification runs (`op` `read`, `start` with `kind` `check`, or `stop` with a run's `id`), take the same checks. `/api/packs` (ARCHITECTURE.md "Packs") takes them too and sets its own write deadline for `fetch` and `add`, which fetch a commit for up to 2 minutes (`session.PackFetchFor`). On the agent listener, `/split-request` (an agent's proposal to split its goal into tasks) is authenticated by the session's push token like `/push-request`.
The export and import routes are `/api/export` (answers a gzip tar stream), `/api/import` (takes `application/gzip`, at most 256 MiB, instead of JSON), `/api/backup` and `/api/clone-again`; `ARCHITECTURE.md` "Export, import and store formats" has them.

Adding or removing a route also updates its rows in Proctor's page-security tables and its entry in `proctor/dimensions.yaml`: [Changing the API](proctor.md#changing-the-api).

## Middleware

Requests other than the health probe pass through a middleware chain composed in `internal/adapters/http/page` (`Server.Page`):

- **recover**: a panic becomes a logged `500` with its stack, not a crash. `http.ErrAbortHandler` passes through untouched, and a panic after the response started aborts the connection rather than appending a `500`.
- **request logging**: method, path, status, duration, and a new random request id for each request (`req=<id>`), which goes on in the request's context to the engine's calls to agent-host and egress (`X-SF-Request`) and, on the page side only, back in the answer's `X-SF-Request` header; an id a request brings is never used. The `slog` adapter also keeps the last 500 events, as kinds only, for Diagnostics (`ARCHITECTURE.md` "Request ids, the session audit and diagnostics").

The checks every page route makes (the key, `Host`, `Origin`, JSON, body caps of 64 KiB, or 1 MiB on the approval routes and `/api/reviewer-agents`, and field names) are in the page's own handler, not the chain: `ARCHITECTURE.md` "Page security checks" lists them. Proctor's specifications of these routes: [proctor.md](proctor.md). Handlers that wait set their own deadlines (`PollFor`, `pushFor`).

**Security headers** (`nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, a strict `Content-Security-Policy`) wrap the whole server, the health probe included.
