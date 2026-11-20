import { api } from '../api.ts'
import { busy, button, el, focusedKey, refocus, replaceKeeping, say, shown, statusLine, visibleCommand, whileBusy } from '../lib/dom.ts'
import { isRecord } from '../lib/json.ts'
import { shortSha } from '../lib/format.ts'

const ROUTE = '/api/verify'
const SHA = /^[0-9a-f]{40}$/
const RUN = /^[0-9a-f]{16}$/
const STATES = ['running', 'passed', 'failed', 'timed-out', 'oom', 'error', 'interrupted', 'stopped'] as const

type RunState = typeof STATES[number]

interface VerifyRun {
  id: string
  commit: string
  state: RunState
  code: number
  tail: string
  cut: boolean
  withheld: boolean
  reason: string
  by: string
  image: string
  imageId: string
  limit: number
  started: number
  ended: number
}

interface VerifyView {
  head: string
  branch: string
  command: string
  hasCheck: boolean
  image: string
  required: boolean
  busy: boolean
  queued: boolean
  current: VerifyRun | null
  why: string
  runs: VerifyRun[]
}

export interface VerifyTarget {
  sid: string
  disabled: string
}

const text = (v: unknown) => (typeof v === 'string' ? v : '')
const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : 0)
const sha = (v: unknown) => (typeof v === 'string' && SHA.test(v) ? v : '')
const state = (v: unknown): RunState | null => STATES.find(x => x === v) ?? null

function readRun(v: unknown): VerifyRun | null {
  if (!isRecord(v) || typeof v.id !== 'string' || !RUN.test(v.id) || !sha(v.commit)) return null
  const s = state(v.state)
  if (!s) return null
  return {
    id: v.id, commit: sha(v.commit), state: s, code: num(v.code), tail: text(v.tail), cut: v.cut === true, withheld: v.withheld === true, reason: text(v.reason),
    by: text(v.by), image: text(v.image), imageId: text(v.imageId), limit: num(v.limitSeconds), started: num(v.started), ended: num(v.ended),
  }
}

function readView(v: unknown): VerifyView | null {
  if (!isRecord(v)) return null
  const runs = Array.isArray(v.runs) ? v.runs.map(readRun).filter(r => r !== null) : []
  return {
    head: sha(v.head), branch: text(v.branch), command: text(v.command), hasCheck: v.hasCheck === true, image: text(v.image), required: v.required === true,
    busy: v.busy === true, queued: v.queued === true, current: readRun(v.current), why: text(v.why), runs,
  }
}

const WORDS: Record<RunState, string> = {
  running: 'running',
  passed: 'passed',
  failed: 'failed',
  'timed-out': 'ran out of time',
  oom: 'ran out of memory',
  error: 'couldn’t run',
  interrupted: 'cut off by an engine restart',
  stopped: 'stopped',
}

function when(ms: number): string {
  return ms ? new Date(ms).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : ''
}

function outcome(r: VerifyRun): string {
  const what = r.state === 'failed' ? `failed with exit code ${r.code}` : WORDS[r.state]
  const at = r.ended ? `, ended ${when(r.ended)}` : r.started ? `, started ${when(r.started)}` : ''
  return `Commit ${shortSha(r.commit)}: ${what}${at}.`
}

async function problemOf(res: Response | null): Promise<string> {
  if (!res) return 'The engine didn’t answer. Try again.'
  const body = await res.json().catch(() => null) as { message?: unknown } | null
  return typeof body?.message === 'string' && body.message ? shown(body.message) : 'The engine refused. Try again.'
}

class SfVerify extends HTMLElement {
  #target: VerifyTarget | null = null
  #view: VerifyView | null = null
  #key = ''
  #loading = 0
  #busy = false
  #status = statusLine()
  #details = el('details', 'session-work')
  #summary = el('summary', 'small')
  #opened: boolean | null = null
  #shown = false

  constructor() {
    super()
    this.#details.addEventListener('toggle', () => {
      if (this.#details.open !== this.#shown) this.#opened = this.#details.open
      this.#shown = this.#details.open
    })
  }

  set view(value: VerifyTarget | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    const changed = value?.sid !== this.#target?.sid
    this.#target = value
    if (!value?.sid) {
      this.#view = null
      this.hidden = true
      this.replaceChildren()
      return
    }
    if (changed) {
      this.#view = null
      this.#opened = null
      this.#status.textContent = ''
      this.hidden = true
      void this.refresh(value.sid)
    } else this.#paint()
  }

  async refresh(sid: string) {
    if (!sid || sid !== this.#target?.sid) return
    const n = ++this.#loading
    const res = await api(ROUTE, { op: 'read', sid }).catch(() => null)
    if (n !== this.#loading || sid !== this.#target?.sid) return
    if (!res || !res.ok) {
      if (res && res.status !== 404 && res.status !== 409 && res.status !== 501) return
      this.#view = null
      this.hidden = true
      this.replaceChildren()
      return
    }
    const v = readView(await res.json().catch(() => null))
    if (!v) return
    this.#view = v
    this.#paint()
  }

  async #ask(body: Record<string, unknown>, said: string) {
    const sid = this.#target?.sid
    if (!sid || this.#busy) return
    this.#busy = true
    this.#opened = true
    this.#paint()
    const res = await api(ROUTE, { ...body, sid }).catch(() => null)
    this.#busy = false
    if (sid !== this.#target?.sid) return
    if (!res || !res.ok) {
      say(this.#status, await problemOf(res), 'warn')
      void this.refresh(sid)
      return
    }
    const v = readView(await res.json().catch(() => null))
    if (v) this.#view = v
    say(this.#status, said, 'muted')
    this.#paint()
  }

  #summaryText(v: VerifyView): string {
    const c = v.current
    if (c) return `Verification run: ${WORDS[c.state]} on ${shortSha(c.commit)}`
    if (v.queued) return 'Verification run: queued for a free slot'
    return v.required ? 'Verification run: none for this commit, and a push needs one' : 'Verification run: none for this commit'
  }

  #run(r: VerifyRun, current: boolean): HTMLElement[] {
    const tone = r.state === 'passed' ? 'small ok' : r.state === 'running' ? 'small' : 'small warn'
    const out: HTMLElement[] = [el('p', tone, outcome(r))]
    const facts: string[] = []
    if (r.image) facts.push(`image ${shown(r.image)}`)
    if (r.imageId) facts.push(`image ID ${r.imageId.replace(/^sha256:/, '').slice(0, 12)}`)
    if (r.limit) facts.push(`time limit ${Math.round(r.limit / 60)} min`)
    facts.push(r.by === 'engine' ? 'started by the engine for a push' : 'started from the page')
    out.push(el('p', 'small muted', facts.join(' · ')))
    if (r.reason) out.push(el('p', 'small', shown(r.reason)))
    if (r.withheld) out.push(el('p', 'small muted', 'Its output held a token-shaped string, so the engine didn’t keep it.'))
    else if (r.tail) {
      const pre = el('pre', 'small command-tail', visibleCommand(r.tail))
      pre.tabIndex = 0
      pre.setAttribute('role', 'region')
      pre.setAttribute('aria-label', current ? 'End of the verification run’s output' : `End of the output of the run of ${shortSha(r.commit)}`)
      if (r.cut) out.push(el('p', 'small muted', 'The end of its output:'))
      out.push(pre)
    }
    return out
  }

  #paint() {
    const t = this.#target
    const v = this.#view
    if (!t?.sid || !v) return
    const focused = focusedKey(this)
    const parts: HTMLElement[] = [
      el('p', 'small muted', 'A verification run copies this commit’s files, read with the engine’s own hash-checked reader, into a new container of the project’s image, with no network, no token, no sign-in and no other session’s files, and runs the project’s check command there. Only the exit code Docker records counts. A pass shows the project’s check passes on a clean copy of this commit; the agent wrote the tests too. The image must hold no secret, since the code the run executes reads everything in it.'),
    ]
    if (v.required) parts.push(el('p', 'small', 'The approved team file requires a passing run of the pushed commit, so a push waits for one unless you allow it with a reason.'))
    if (!v.hasCheck) parts.push(el('p', 'small warn', 'This project has no check command. Set one under Check and setup commands on the project’s card.'))
    else parts.push(el('p', 'small', `Check command: ${v.command ? visibleCommand(v.command) : 'set, but not shown, since it holds a token-shaped string'}`))
    if (!v.image) parts.push(el('p', 'small warn', 'This project names no image for verification runs, so it can’t run one. Set an image pinned by digest under Check and setup commands on the project’s card.'))
    if (v.current) parts.push(...this.#run(v.current, true))
    else if (v.queued) parts.push(el('p', 'small', `A run of commit ${shortSha(v.head)} that a push needs is queued for a free verification slot; the engine starts it when one frees up, in the order pushes queued theirs.`))
    else if (v.head) parts.push(el('p', 'small muted', `No run of commit ${shortSha(v.head)} with the current command and image yet.`))
    if (v.why) parts.push(el('p', 'small muted', shown(v.why)))
    const running = v.runs.find(r => r.state === 'running')
    const row = el('div', 'row')
    const why = t.disabled || (!v.hasCheck ? 'Set a check command first.' : !v.image ? 'Set an image first.' : !v.head ? 'The branch has no commit to verify.' : running ? 'A run of this session is running.' : v.busy ? 'The engine runs at most 2 verification runs at once.' : '')
    const go = button('Verify this commit', 'small', () => {
      if (!why && !this.#busy) void whileBusy(go, () => this.#ask({ op: 'start', kind: 'check' }, 'The verification run started.'))
    }, 'button', 'verify-start')
    go.setAttribute('aria-disabled', String(!!why || this.#busy))
    if (this.#busy) busy(go, true)
    row.append(go)
    if (running) {
      row.append(button('Stop the run', 'ghost small', () => {
        if (!this.#busy) void this.#ask({ op: 'stop', id: running.id }, 'The run is stopping.')
      }, 'button', 'verify-stop'))
    }
    if (why) {
      const said = el('span', 'small muted', why)
      said.id = 'verify-why'
      go.setAttribute('aria-describedby', 'verify-why')
      row.append(said)
    }
    parts.push(row)
    const earlier = v.runs.filter(r => r.id !== v.current?.id)
    if (earlier.length) {
      const list = el('ul', 'list')
      for (const r of earlier) {
        const li = el('li', 'small')
        li.append(...this.#run(r, false))
        list.append(li)
      }
      parts.push(el('p', 'small muted', 'Earlier runs of this session:'), list)
    }
    parts.push(this.#status)
    this.#summary.textContent = this.#summaryText(v)
    this.#shown = this.#opened ?? (v.required || v.queued || !!v.current)
    this.#details.open = this.#shown
    replaceKeeping(this.#details, [this.#summary, ...parts])
    if (this.#details.parentNode !== this) this.replaceChildren(this.#details)
    this.hidden = false
    refocus(this, focused, () => ['verify-start'])
  }
}

customElements.define('sf-verify', SfVerify)

declare global {
  interface HTMLElementTagNameMap {
    'sf-verify': SfVerify
  }
}
