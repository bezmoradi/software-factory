---
paths:
  - "web/**/*.ts"
  - "web/styles/*.css"
  - "web/index.html"
  - "internal/core/session/bridge.go"
  - "internal/adapters/http/page/stream.go"
---

# Performance budgets

The page has three budgets: switching to another session under 150 ms, an event shown under 200 ms after the page reads it from the event stream, and a diff open under 300 ms. They are checked by hand, not by a test, in the containerized Chromium against the development overlay, after "Setup" in [Hand checks for the gate and reviews](gate-hand-checks.md) (its shell functions and project). Each time is measured in the page with the Performance API: `performance.now()` before the action, and again in the first task after the next frame (`requestAnimationFrame`, then a zero-delay timer). A run over a budget is a defect to profile and fix.

**The browser.** Chromium is in the development image, `software-factory:dev`, which the development overlay from "Setup" builds; the first command below builds it again without touching the running stack. Run Chromium from that image in a container of its own on the host network: it reaches the page at `127.0.0.1:7799` as a person who types the address does, so the page's `Sec-Fetch-Site` check passes (an extension's navigation counts as cross-site and is refused), and its DevTools port, 9222, stays on loopback. The image's `chromium` command adds `--no-sandbox --disable-dev-shm-usage`.

```bash
COMPOSE_FILE=compose.yaml:compose.dev.yaml docker compose build helper
docker run -d --name sf-browser --network host --entrypoint chromium software-factory:dev --headless=new --remote-debugging-address=127.0.0.1 --remote-debugging-port=9222 --user-data-dir=/tmp/profile --window-size=1440,900 --no-first-run about:blank
```

**The data.** 20 sessions: `p01` gets a 5,000-line diff, `p03` to `p20` a turn of 100 steps each, so no conversation is empty, and `p02` gets its 5,000 events from the page itself during the run. They can't be sent before: for a page that opens later, the engine keeps only the last 2,000 events of a development ACP session (`bridgeRing`); a message holds at most 20,000 characters (`session.MaxText`), so they take two scripts of 2,500 steps; and one page key works at a time, so `sf` can't send while the browser holds it.

```bash
for n in $(seq -w 1 20); do start p$n; done
docker exec -w "$(wt p01)" "$(ctr p01)" sh -c 'for i in $(seq 5000); do echo "var v$i = $i"; done > big.go && git add big.go && git -c user.name=Dev -c user.email=dev@example.invalid commit -qm big'
for n in $(seq -w 3 20); do sf /api/message "{\"sid\":\"$(sid p$n)\",\"text\":\"say: hello$(printf '\\nrun: x%.0s' $(seq 100))\"}"; done
```

**The run.** It opens the page, sends `p02` its two scripts and then ten one-line turns `say: sf-mark-<run>-<n>`, where `<run>` is the run's start in milliseconds, so a second run on the same sessions doesn't time the first run's marks, timing each from the moment the page parses the stream line that first carries the mark to the frame after the mark shows in `p02`'s conversation; then switches through the 20 sessions twice; then opens `p01`'s Changes tab (`c:<sid>`, its Diff open) from its conversation five times. `TRACE=1` adds a Chrome trace of the whole run with the JavaScript call tree, written to stderr before the script exits, which goes to `/tmp/trace.json` outside the repository.

```bash
docker exec -i -e TRACE= -e BIG="$(sid p01)" -e LONG="$(sid p02)" -e ALL="$(for n in $(seq -w 1 20); do sid p$n; done | tr '\n' ' ')" sf-browser node --input-type=module - <<'EOF' 2> /tmp/trace.json
const env = process.env, all = env.ALL.trim().split(/\s+/), mark = 'sf-mark-' + Date.now()
const version = await (await fetch('http://127.0.0.1:9222/json/version')).json()
const ws = new WebSocket(version.webSocketDebuggerUrl)
await new Promise(r => { ws.onopen = r })
const waiting = new Map(), trace = []
let last = 0, traced
ws.onmessage = e => {
  const m = JSON.parse(e.data)
  if (m.method === 'Tracing.dataCollected') trace.push(...m.params.value)
  if (m.method === 'Tracing.tracingComplete') traced()
  waiting.get(m.id)?.(m)
  waiting.delete(m.id)
}
const cdp = (method, params = {}, sessionId) => new Promise(r => { waiting.set(++last, r); ws.send(JSON.stringify({ id: last, method, params, sessionId })) })
if (env.TRACE) await cdp('Tracing.start', { traceConfig: { includedCategories: ['devtools.timeline', 'disabled-by-default-devtools.timeline', 'blink.user_timing', 'v8.execute', 'disabled-by-default-v8.cpu_profiler'] } })
const { result: { targetId } } = await cdp('Target.createTarget', { url: 'about:blank' })
const { result: { sessionId } } = await cdp('Target.attachToTarget', { targetId, flatten: true })
const run = async expression => {
  const m = await cdp('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true }, sessionId)
  if (m.result.exceptionDetails) throw new Error(m.result.exceptionDetails.exception?.description)
  return m.result.result.value
}
await cdp('Page.enable', {}, sessionId)
await cdp('Page.addScriptToEvaluateOnNewDocument', { source: 'window.sfIn = {}; const parse = JSON.parse; JSON.parse = function (s, f) { const k = typeof s === "string" && s.match(/' + mark + '-\\d+/); if (k && !(k[0] in sfIn)) sfIn[k[0]] = performance.now(); return parse.call(this, s, f) }' }, sessionId)
await cdp('Page.navigate', { url: 'http://127.0.0.1:7799/' }, sessionId)
await new Promise(r => setTimeout(r, 3000))
await run(`
  window.sfShown = {}
  window.frame = () => new Promise(r => requestAnimationFrame(() => setTimeout(r, 0)))
  window.until = async (f, ms = 600000) => { const end = performance.now() + ms; while (!f()) { if (performance.now() > end) throw new Error('timed out waiting for ' + f); await new Promise(r => setTimeout(r, 50)) } }
  window.go = view => { const a = document.createElement('a'); a.dataset.view = view; document.body.append(a); a.click(); a.remove() }
  window.say = async text => { const stop = document.getElementById('stop'); document.getElementById('text').value = text; document.getElementById('composer').requestSubmit(); await until(() => !stop.hidden, 10000).catch(() => {}); await until(() => stop.hidden) }
  window.sfSeen = new MutationObserver(list => { for (const m of list) for (const n of m.type === 'characterData' ? [m.target] : m.addedNodes) {
    const k = (n.textContent || '').match(/${mark}-\\d+/)
    if (k && !(k[0] in sfShown) && (n.nodeType === 1 ? n : n.parentElement)?.closest('sf-conversation[data-sid="${env.LONG}"]')) { sfShown[k[0]] = 0; frame().then(() => { sfShown[k[0]] = performance.now() }) }
  } })
  sfSeen.observe(document.body, { subtree: true, childList: true, characterData: true })
  until(() => document.querySelectorAll('sf-conversation').length >= ${all.length})`)
await run(`(async () => { go('s/${env.LONG}'); await until(() => !document.getElementById('composer').hidden); for (const part of [1, 2]) await say('say: part ' + part + '\\nrun: x'.repeat(2500)) })()`)
const event = await run(`(async () => { const out = []; for (let i = 1; i <= 10; i++) { const k = '${mark}-' + i; await say('say: ' + k); await until(() => sfShown[k] > 0); out.push(sfShown[k] - sfIn[k]) } sfSeen.disconnect(); return out })()`)
const switching = await run(`(async () => { const out = []; for (const round of [1, 2]) for (const sid of ${JSON.stringify(all)}) { await frame(); const t = performance.now(); go('s/' + sid); await frame(); out.push(performance.now() - t) } return out })()`)
const diff = await run(`(async () => { const q = () => document.querySelector('#changes summary[data-key="group:diff"]'); go('c:${env.BIG}'); await until(() => q() && q().offsetParent && document.querySelector('#changes .diff-lines').childElementCount > 0); if (!q().parentElement.open) q().click(); const out = []; for (let i = 0; i < 5; i++) { go('s/${env.BIG}'); await frame(); const t = performance.now(); go('c:${env.BIG}'); await frame(); out.push(performance.now() - t) } return out })()`)
const stat = a => ({ runs: a.length, max: Math.round(Math.max(...a)), median: Math.round(a.toSorted((x, y) => x - y)[a.length >> 1]) })
console.log(JSON.stringify({ chromium: version.Browser, switching: stat(switching), event: stat(event), diff: stat(diff) }))
if (env.TRACE) { const done = new Promise(r => { traced = r }); await cdp('Tracing.end'); await done; await new Promise(r => process.stderr.write(JSON.stringify({ traceEvents: trace }) + '\n', r)) }
process.exit(0)
EOF
key
```

It prints one line: the browser's version and, for `switching` (40 switches), `event` (10 events into a session of 5,000) and `diff` (5 opens of 5,000 lines), the number of runs and the maximum and median in milliseconds. Each budget passes when its maximum is under it. To find what a slow one spent its time on, run it again with `-e TRACE=1` and open `/tmp/trace.json` in the Performance panel of Chrome's DevTools or in `ui.perfetto.dev`. `docker rm -f sf-browser` removes the browser.

**Recording a run.** Put the printed line in the description of the change that ran it, with the date, the commit and tree it ran on (`git rev-parse HEAD`, and `git write-tree` for staged work), the machine and Docker's version. A run over a budget names, in that change, what the trace showed and what was fixed, and a run after the fix shows every budget met.
