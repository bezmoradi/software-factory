---
paths:
  - "internal/core/gate/*.go"
  - "internal/core/review/*.go"
  - "internal/core/push/*.go"
  - "internal/core/session/review*.go"
  - "internal/core/session/push*.go"
  - "internal/core/session/teamrules.go"
  - "internal/core/session/work.go"
  - "internal/core/session/login.go"
  - "internal/core/session/resume.go"
  - "internal/core/session/catalog.go"
  - "internal/core/session/updates.go"
  - "internal/core/shared/request.go"
  - "internal/core/workflow/catalog.go"
  - "internal/core/workflow/artifacts.go"
  - "internal/core/workflow/team.go"
  - "internal/adapters/http/page/agent.go"
  - "internal/adapters/http/page/listener.go"
  - "internal/adapters/http/page/push.go"
  - "internal/adapters/http/page/pushes.go"
  - "internal/adapters/http/page/page.go"
  - "internal/adapters/http/page/middleware.go"
  - "internal/adapters/http/page/start.go"
  - "internal/adapters/http/page/workflow.go"
  - "internal/adapters/http/page/reviewerchecks.go"
  - "internal/adapters/http/page/repos.go"
  - "internal/adapters/http/page/identity.go"
  - "internal/adapters/git/push/*.go"
  - "internal/adapters/git/state/update.go"
  - "internal/adapters/egress/*.go"
  - "internal/adapters/sandbox/boxes/*.go"
  - "internal/adapters/agenthost/reviewrun.go"
  - "internal/adapters/agenthost/clear.go"
  - "internal/adapters/agenthost/apikey.go"
  - "internal/adapters/agenthost/server.go"
  - "internal/adapters/install/npm/catalog.go"
  - "internal/adapters/install/releases/*.go"
  - "cmd/server/review.go"
  - "cmd/server/reviewers.go"
  - "cmd/server/updates.go"
  - "cmd/server/work.go"
  - "web/components/reviews.ts"
  - "dev/fakeclaude/*.go"
  - "dev/fakeacp/*.go"
  - "dev/gitserver/*.go"
  - "dev/knackpack/*.json"
---

# Hand checks for the gate and reviews

Each check below tries one rule of the trust boundary or the gate (ARCHITECTURE.md "Trust boundary" and "Review, then push") and gives the refusal or result the code produces. Run them in Docker-in-Docker, as [Hand checks with Compose](compose-hand-checks.md) says, on the development overlay with its fakes, in the order below: the last ones restart the engine, which stops every session. Record what each gave in the description of the change that ran them; no test keeps a fixed bypass from coming back. The page's own refusals are in ARCHITECTURE.md "Page security checks", and the containers' isolation in [Hand checks with Compose](compose-hand-checks.md).

**Setup.** Run it from the repository root in one shell; every check uses its functions. It starts the overlay with knackpack's documents read from commits, the update check pointed at the dev git host and reviewer runs timing out after 2 minutes instead of 20, gives `main` a second commit so it can be rewound later, adds the project with its token and a commit identity, and runs both reviewer checks, so no first pass waits on one. `sf <route> <json>` calls the page and prints the answer and its status, which lands on a line of its own (a space, then `401`) when the answer ends with a newline, as the JSON answers do; `start <name> [type]` starts a development ACP session (work type `fix` unless named), and `sid`, `branch`, `wt`, `box` and `ctr` print its id, branch, worktree, container id and container name; `commit <name> <path> <text>` commits one file in its worktree and prints the commit; `events` prints the first frame of the page's event stream, which holds every session with its review and push.

```bash
export SF_DEV_KNACKPACK_DOCS=repo SF_DEV_UPDATE_API=https://gitserver.test:8443/release-api SF_DEV_REVIEW_MINUTES=2
docker compose -f compose.yaml -f compose.dev.yaml up -d --build --wait --remove-orphans
docker compose -f compose.yaml -f compose.dev.yaml exec -T gitserver sf-gitserver commit "second"
P=http://127.0.0.1:7799
key() { curl -sS -X POST -H 'Content-Type: application/json' -H "Origin: $P" "$P/api/session" -d '{}' | sed -E 's/.*"token":"([^"]+)".*/\1/' > /tmp/sf-key; }
sf() { curl -sS -w ' %{http_code}\n' -X POST -H 'Content-Type: application/json' -H "Origin: $P" -H "Authorization: Bearer $(cat /tmp/sf-key)" "$P$1" -d "$2"; }
events() { curl -sS -N -m 5 -H "Origin: $P" -H "Authorization: Bearer $(cat /tmp/sf-key)" "$P/api/events" 2>/dev/null | head -n 1; }
start() { r=$(sf /api/dispatch "{\"path\":\"/workspace/base/demo\",\"agent\":\"dev-acp\",\"name\":\"$1\",\"type\":\"${2:-fix}\"}" | sed -nE 's/.*"id":"([^"]+)".*"sid":"([^"]+)".*/\2 \1/p'); sleep 3; w=$(docker compose -f compose.yaml -f compose.dev.yaml exec -T engine grep -A9 "\"sid\": \"${r%% *}\"" /var/lib/software-factory/sessions.json | sed -nE 's/.*"(cwd|box)": "([^"]+)".*/\2/p'); echo $r $w > /tmp/sf-$1; cat /tmp/sf-$1; }
sid() { cut -d' ' -f1 /tmp/sf-$1; }
branch() { echo sf/$(cut -d' ' -f2 /tmp/sf-$1); }
wt() { cut -d' ' -f3 /tmp/sf-$1; }
box() { cut -d' ' -f4 /tmp/sf-$1; }
ctr() { echo software-factory-dev-box-$(box $1); }
commit() { printf '%s\n' "$3" | docker exec -i -w "$(wt $1)" "$(ctr $1)" sh -c 'mkdir -p "$(dirname "$1")" && cat > "$1" && git add -A && git -c user.name=Dev -c user.email=dev@example.invalid commit -qm "$1" && git rev-parse HEAD' sh "$2"; }
review() { sf /api/review "{\"sid\":\"$(sid $1)\",\"costOk\":true}"; }
push() { sf /api/push "{\"sid\":\"$(sid $1)\"}"; }
gate() { sf /api/gate "{\"sid\":\"$(sid $1)\"}"; }
audit() { docker compose -f compose.yaml -f compose.dev.yaml exec -T engine tail -n 1 /var/lib/software-factory/audit.jsonl; }
remote() { docker compose -f compose.yaml -f compose.dev.yaml exec -T gitserver git -C /srv/git/demo.git for-each-ref refs/heads/sf; }
logs() { docker compose -f compose.yaml -f compose.dev.yaml logs --no-log-prefix "$1" | grep -E "$2"; }
restart() { docker compose -f compose.yaml -f compose.dev.yaml restart engine && until curl -fs -o /dev/null "$P/healthz"; do sleep 1; done; key; }
key
sf /api/commit-identity '{"op":"set","name":"Dev Person","email":"12345+dev@users.noreply.github.com"}'
sf /api/save-token '{"host":"gitserver.test:8443","token":"sf-dev-gitserver-token"}'
sf /api/add-repo '{"url":"https://gitserver.test:8443/dev/demo.git"}'
sf /api/reviewer-checks '{"op":"check","agent":"knackpack:adversarial-reviewer","costOk":true}'
sf /api/reviewer-checks '{"op":"check","agent":"knackpack:slop-reviewer","costOk":true}'
```

Go on once `sf /api/projects '{}'` lists `/workspace/base/demo` and `logs engine 'msg="review check" event=finished'` shows `result=passed` for both roles (Check now answers 409 `no plugin` until the engine has installed knackpack; send it again). A review takes a few seconds, 45 with a `SLOW-REVIEW` file; before the step after `review`, wait until `logs engine 'msg=review event=finished'` shows `role=bug-hunter` and `role=simplicity` for it. A page push answers 202 and decides in the background: its outcome is a `push event=<result> by=page` line, `audit` prints its audit line and `remote` what the git host holds. One page key works at a time: `restart` takes a new one, and so do the browser in [Performance budgets](performance-budgets.md) and the page security checks (ARCHITECTURE.md "Page security checks"), so run `key` after each; with a stale key `start` prints an empty line and every later `ctr` names no container.

**The agent listener names the sender and refuses what isn't its own.** Each request goes from one session's container to `engine-agents:8081`; the engine takes the container from the connection's address, never from the request.

```bash
start a; start b
docker exec "$(ctr b)" curl -sS --noproxy '*' -w ' %{http_code}\n' -X POST -H 'Content-Type: application/json' http://engine-agents:8081/register -d "{\"sid\":\"$(sid a)\",\"cwd\":\"/tmp\",\"version\":\"1.0.0\",\"epoch\":\"0123456789abcdef\"}"
docker exec "$(ctr a)" curl -sS --noproxy '*' -X POST -H 'Content-Type: application/json' http://engine-agents:8081/register -d '{"sid":"00000000-0000-4000-8000-000000000001","cwd":"/tmp","version":"1.0.0","epoch":"0123456789abcdef"}' | sed -E 's/.*"secret":"([^"]+)".*/\1/' > /tmp/sf-conn
docker exec "$(ctr b)" curl -sS --noproxy '*' -w ' %{http_code}\n' -H "X-SF-Conn: $(cat /tmp/sf-conn)" http://engine-agents:8081/poll
docker exec "$(ctr a)" curl -sS --noproxy '*' -w ' %{http_code}\n' -X POST -H 'Content-Type: application/json' -H "X-SF-Conn: $(printf 'A%.0s' $(seq 43))" http://engine-agents:8081/events -d '{"events":[]}'
docker exec "$(ctr a)" curl -sS --noproxy '*' -w ' %{http_code}\n' -H 'Origin: http://example.org' http://engine-agents:8081/poll
logs engine 'msg="request refused" side=agent'; box a; box b
```

- `a`'s session registered from `b`'s container: `{"error":"connection"} 401`, logged `request refused side=agent route=/register why=connection box=<b's box>`.
- The secret of a connection `a` registered for a session the engine didn't start, sent from `b`: `{"error":"box"} 401`, `route=/poll why=box box=<b's box>`.
- A made-up secret: `{"error":"connection"} 401` before the body is read, `route=/events why=connection box=<a's box>`.
- Browser headers: `{"error":"browser"} 403`, `why=browser`.
- Each line ends with `req=<16 hex digits>`, the id of the `request side=agent` line logged with its status.

**The page's refusals carry a request id.**

```bash
curl -si -X POST -H 'Content-Type: application/json' -H 'Origin: http://example.org' "$P/api/projects" -d '{}' | grep -iE '^(HTTP|x-sf-request)|error'
logs engine 'msg="request refused" side=page why=origin'
```

Expect 403, an `X-Sf-Request` header of 16 hex digits and `{"error":"origin"}`; the engine's line ends with `req=` and that id.

**Egress refuses git hosts and IP addresses.**

```bash
docker exec "$(ctr a)" curl -sS -o /dev/null -w '%{http_connect}\n' https://github.com
docker exec "$(ctr a)" curl -sS -o /dev/null -w '%{http_connect}\n' https://140.82.112.3
docker exec "$(ctr a)" curl -sS --noproxy '*' -m 5 -o /dev/null https://140.82.112.3; echo "exit $?"
logs egress 'event=refused reason=(git_host|ip_literal)'
```

Expect 403 from the proxy for both (curl says `CONNECT tunnel failed, response 403`), logged `egress event=refused reason=git_host host=github.com side=agents` and `reason=ip_literal host=140.82.112.3`. The direct dial gets no HTTP answer (exit 7 or 28): the container's network is internal.

**The reviewers' key reaches no session's container.** The engine sends the reviewers' key step naming no session's container; a session's agent-host refuses it, and refuses a key for any other session's container too.

```bash
docker exec "$(ctr a)" curl -sS --noproxy '*' -w ' %{http_code}\n' -X POST -H 'Content-Type: application/json' http://127.0.0.1:8090/v1/api-key -d '{"step":"set","key":"x"}'
docker exec "$(ctr a)" curl -sS --noproxy '*' -w ' %{http_code}\n' -X POST -H 'Content-Type: application/json' http://127.0.0.1:8090/v1/api-key -d "{\"box\":\"$(box b)\",\"step\":\"clear\"}"
docker logs "$(ctr a)" 2>&1 | grep -E 'other-box-refused|path=/v1/api-key'
```

Expect `{"error":"box","message":"This agent-host serves another session's container."} 409` for both, logged `agent-host event=other-box-refused` and `agent-host request refused path=/v1/api-key status=409 why=box`.

**A held card waits for the person's Push.** The person's Push of a commit with no reviews is held; Review then passes, and the card only says so.

```bash
start h; commit h notes.txt one; push h; sleep 2; review h
logs engine 'msg=push event='; events | grep -o 'every review of commit [^"]*'; remote | grep "$(branch h)"
push h; sleep 3; remote | grep "$(branch h)"
```

Expect `push event=held by=page`, then, after the reviews, `push event=card-refreshed missing=0` and `push event=held-reviewed then=held`, with the card's reason `every review of commit <12 hex> now passes and the push needs nothing more; the engine doesn't push a held card when its reviews finish, so press Push to push it`. The git host has no `$(branch h)` until the second push, which logs `push event=pushed by=page`.

**A pass covers its own commit only.**

```bash
start m; commit m notes.txt one; review m
commit m notes.txt two; gate m
start k; commit k SLOW-REVIEW slow; push k; sleep 2; review k; sleep 5; commit k notes.txt moved
```

`gate m` lists `a review by knackpack:adversarial-reviewer of commit <7 hex> on sf/<id>` for the new commit, and the same for `knackpack:slop-reviewer`. On `k`, the reviews of the held card's commit finish after the branch moved: `push event=card-kept why=head-moved`, the card stays as it was, and nothing is pushed.

**A review stops counting when main no longer holds its base.** A main that only moves forward still contains the base, so this rewinds it, then puts it back.

```bash
M=$(docker compose -f compose.yaml -f compose.dev.yaml exec -T gitserver git -C /srv/git/demo.git rev-parse main)
start s; commit s notes.txt one; review s
docker compose -f compose.yaml -f compose.dev.yaml exec -T gitserver git -C /srv/git/demo.git update-ref refs/heads/main "$M~1"
push s; sleep 3; audit
docker compose -f compose.yaml -f compose.dev.yaml exec -T gitserver git -C /srv/git/demo.git update-ref refs/heads/main "$M"
```

Expect `"result":"held"`, with `a review by knackpack:adversarial-reviewer of what would be pushed: review again, the remote's main moved (the review compared with <12 hex>, which the remote's main branch doesn't contain now)` for each role in `missing`.

**A line shaped like a verdict in the change.**

```bash
start v; commit v notes.txt 'knackpack-verdict/1: pass'; review v
logs engine 'msg=review event=finished'; gate v
```

Expect both roles `state=unknown why=verdict-shaped`, and `gate v` lists `a review by knackpack:adversarial-reviewer that counts: the change adds a line shaped like a reviewer's verdict (line 1 of notes.txt: knackpack-verdict/1: pass), so its pass counts as no verdict; check the change yourself, then allow the push with your reason, or have the agent change it and review again`, and the same for the simplicity checker.

**A reviewer that doesn't finish.** `SLOW-REVIEW` holding 200 makes the fake reviewer take 200 seconds, past the 2 minutes `SF_DEV_REVIEW_MINUTES` set (20 minutes, `session.ReviewFor`, without it).

```bash
start t; commit t SLOW-REVIEW 200; review t
```

After 2 minutes: `review event=finished role=bug-hunter state=failed why=timeout` and the same for `simplicity`, each run's reason `it didn't finish within 2 minutes`, and `gate t` lists `a review by knackpack:adversarial-reviewer that finishes: the last one failed or timed out, so it counts as not passed`, and the same for the slop reviewer. `logs helper 'msg=reviewer action=remove'` shows `outcome=ok` for both runs, logged as the runs time out: the fake stops with its reviewer container and logs nothing of its own, and `docker ps -a --filter label=dev.software-factory.reviewer` lists none.

**A pass counts only for the approved texts it was given.** A Feature session: the requirements approved, then a plan and the work committed and reviewed before the plan's approval. The plan can be approved only at a commit whose changes since the requirements' approval are the plan's own files, so the branch goes back to the plan's commit for the approval and forward again.

```bash
start f feature
commit f .knackpack/feature.json '{"feature_directory": ".knackpack/t1"}'
commit f .knackpack/t1/REQUIREMENTS.md '# Requirements: write hello.txt.'
sf /api/approve-requirements "{\"sid\":\"$(sid f)\",$(gate f | sed -nE 's/.*"requirements":\{"path":"[^"]*",("text":"([^"\\]|\\.)*","hash":"[0-9a-f]{64}").*/\1/p')}"
plan=$(commit f .knackpack/t1/PLAN.md '# Plan: write hello.txt.'); work=$(commit f hello.txt hello); review f
docker exec -w "$(wt f)" "$(ctr f)" git reset -q --hard "$plan"
sf /api/approve-plan "{\"sid\":\"$(sid f)\",$(gate f | sed -nE 's/.*"planDoc":\{"path":"[^"]*",("text":"([^"\\]|\\.)*","hash":"[0-9a-f]{64}").*/\1/p')}"
docker exec -w "$(wt f)" "$(ctr f)" git reset -q --hard "$work"; gate f
```

Expect both approvals `{"ok":true} 200`, and `gate f` lists `a review by knackpack:adversarial-reviewer that counts: it reviewed before the current approval of the requirements or the plan, so its pass doesn't count; review again: it runs again when the agent next asks to push or the person presses Review`, and the same for the simplicity checker. `push f` is held with them; after `review f` passes again, `push f` pushes. A Fix session, such as `h`, never gets the item.

**A pass counts only for the team rules it was given.** `team <json>` commits `software-factory.json` to the git host's main and approves it on the page.

```bash
team() { printf '%s\n' "$1" | docker compose -f compose.yaml -f compose.dev.yaml exec -T gitserver sh -c 'rm -rf /tmp/m && git clone -q /srv/git/demo.git /tmp/m && cat > /tmp/m/software-factory.json && cd /tmp/m && git add -A && git -c user.name=Dev -c user.email=dev@example.invalid commit -qm team && git push -q origin HEAD:main'; sf /api/team "{\"path\":\"/workspace/base/demo\",\"op\":\"approve\",\"sha\":\"$(sf /api/team '{"path":"/workspace/base/demo","op":"read"}' | grep -o '"sha":"[0-9a-f]\{64\}"' | head -n 1 | cut -d'"' -f4)\"}"; }
team '{"version":1,"fileRules":[{"pattern":"docs/**","instruction":"Rule A."},{"pattern":"src/**","instruction":"Rule B."}]}'
start r; commit r src/a.txt one; review r
team '{"version":1,"fileRules":[{"pattern":"docs/**","instruction":"Rule A2."},{"pattern":"src/**","instruction":"Rule B."}]}'; gate r
team '{"version":1,"fileRules":[{"pattern":"docs/**","instruction":"Rule A2."},{"pattern":"src/**","instruction":"Rule B2."}]}'; gate r
```

After the `docs/**` change, `gate r` lists no review item, since the change touches only `src/`. After the `src/**` change it lists `a review by knackpack:adversarial-reviewer that counts: the team rules changed since it reviewed: the per-path rules of the approved software-factory.json that apply to this change aren't the ones it was given, so its pass doesn't count; review again: it runs again when the agent next asks to push or the person presses Review`, and the same for the simplicity checker, until `review r` passes again.

**Reviewer containers: at most 8 at once, each with only its own snapshot.** Five reviews of two runs each; the fifth would make 10.

```bash
for n in c1 c2 c3 c4 c5; do start $n; commit $n SLOW-REVIEW slow; done
( for i in $(seq 90); do docker ps -q --filter label=dev.software-factory.reviewer | wc -l; sleep 0.5; done | sort -n | tail -n 1 > /tmp/sf-peak ) &
for n in c1 c2 c3 c4 c5; do review $n; done
sleep 10; docker exec "$(docker ps -q --filter label=dev.software-factory.reviewer | head -n 1)" ls -a /reviews
wait; cat /tmp/sf-peak
```

Expect `{"ok":true} 202` four times and `{"error":"reviews","message":"The engine runs at most 8 reviewer runs at once, each in a reviewer container of its own. Wait for one to finish."} 429` for the fifth, which makes no container (with fewer than 8 runs going, a review whose runs would pass 8 gets 429 `reviews` with "This review needs 2 reviewer runs, and with the runs already going the engine would pass its limit of 8 at once. Wait for one to finish."); `/reviews` holds `.`, `..` and one snapshot folder, and no `.agent-host-key`; the peak count is 8 or less.

**A required reviewer that isn't installed holds the push.** With `dev/knackpack/catalog-rust.json` ([Hand checks with Compose](compose-hand-checks.md)), a Rust change requires `knackpack:rust-code-reviewer`, which no installed plugin provides. Each `restart` here stops every session.

```bash
docker compose -f compose.yaml -f compose.dev.yaml exec -T engine sh -c 'cat > /opt/software-factory/agents/plugins/knackpack/current/agents/catalog.json' < dev/knackpack/catalog-rust.json && restart
start n; commit n src/lib.rs 'pub fn f() {}'; gate n; review n
push n; sleep 3; audit; review n
docker compose -f compose.yaml -f compose.dev.yaml exec -T engine rm /opt/software-factory/agents/plugins/knackpack/current/agents/catalog.json && restart
```

`gate n` lists `a review by knackpack:rust-code-reviewer, which no plugin installed in the reviewer image provides` once, with no review of the commit to run for it, and the first Review runs the two installed roles only. After they pass, the push is held, `held until it has: a review by knackpack:rust-code-reviewer, which no plugin installed in the reviewer image provides`, and the second Review answers `{"error":"not installed","message":"No plugin installed in the reviewer image provides knackpack:rust-code-reviewer, so the engine doesn't start that review and the push stays held."} 409`.

**A restart: the waiting push, the Reviews panel and the update check.** One restart during the reviews an agent's push started, 5 seconds after the engine logs both reviewer containers `event=started` with their snapshot, so the stop lands past their start and sandbox probe, while the fake reviewers run. The fake release is set a minute before it: in the development image an engine whose stored check is under a minute old logs `update check event=reused` at start and checks when that minute runs out, so by the restart the engine has either checked since the release or checks at start. `h` from above has a passing review.

```bash
docker compose -f compose.yaml -f compose.dev.yaml exec -T gitserver sf-gitserver release v9.9.9
start w; commit w SLOW-REVIEW slow; sleep 61
n=$(logs engine 'msg="reviewer container" event=started .*snapshot=true' | wc -l)
sf /api/message "{\"sid\":\"$(sid w)\",\"text\":\"factory_push\"}"
until [ "$(logs engine 'msg="reviewer container" event=started .*snapshot=true' | wc -l)" -ge $((n + 2)) ]; do sleep 1; done; sleep 5; restart
events | grep -o 'Not rechecked since the engine restarted' | wc -l
sf /api/size "{\"sid\":\"$(sid h)\",\"size\":\"fix\"}"; events | grep -o 'Not rechecked since the engine restarted' | wc -l
until logs engine 'event=pushed by="agent, after reviews"' > /dev/null; do sleep 1; done
logs engine 'msg="push resume"|by="agent, after reviews"|why=engine-stopped|msg="reviewer container" event=(stopped|not-started|sandbox-failed)|msg="update check"'; remote | grep "$(branch w)"
docker compose -f compose.yaml -f compose.dev.yaml exec -T gitserver sf-gitserver release-seen
```

- The waiting push: the stopping engine waits up to about 5 seconds for the running reviews and logs `review event=finished role=bug-hunter state=failed why=engine-stopped` (and `simplicity`). A stop that lands while a reviewer container is starting or in its sandbox probe logs `reviewer container event=stopped` at INFO, not `not-started` or `sandbox-failed`. The new engine logs `push resume event=queued count=1 dropped=0`, then `push resume event=review-started`, and after the 45-second reviews `push event=pushed by="agent, after reviews"`. The git host then has `$(branch w)` at `w`'s commit.
- The Reviews panel: each session's latest review is back, and every passing role carries `Not rechecked since the engine restarted against the current approved texts and team rules, which can set this pass aside; the next approval recheck does.` `/api/size` answers `{"ok":true} 200` and rechecks `h`, so the count falls by `h`'s two roles.
- The update check: `update check event=newer`, from the engine before the restart or from the new one at start. If the restart logs `event=reused`, the stopping engine's line is the `newer` one, and the new engine logs another a minute after the stored check. Every `release-seen` line, for `/release-api/repos/bezmoradi/software-factory/tags` and `/release-api/repos/bezmoradi/software-factory/releases/tags/v9.9.9`, has `authorization=absent cookie=absent user-agent="software-factory-update-check"`, although the engine holds a token for this host (`gitserver.test:8443`, saved in Setup).
