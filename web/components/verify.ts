import { offTip } from '../lib/tooltip.ts'
import { download, request, type EngineAnswer } from '../api.ts'
import { busy, button, el, focusedKey, refocus, replaceKeeping, say, shown, statusLine, visibleCommand, whileBusy } from '../lib/dom.ts'
import { isRecord, saidOf, type Said } from '../lib/json.ts'
import { shortSha } from '../lib/format.ts'
import { pngBlob, saveBlob } from '../lib/files.ts'
import { toast } from './toasts.ts'
import { codeBlock } from './code-block.ts'

const ROUTE = '/api/verify'
const SHA = /^[0-9a-f]{40}$/
const RUN = /^[0-9a-f]{16}$/
const STATES = ['running', 'passed', 'failed', 'timed-out', 'oom', 'error', 'interrupted', 'stopped'] as const
const FILE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,119}\.(png|jpe?g|gif|zip)$/

type RunState = typeof STATES[number]
type Kind = 'check' | 'browser'

interface VerifyFile {
  name: string
  kind: 'image' | 'trace'
  bytes: number
  width: number
  height: number
}

interface VerifyRun {
  id: string
  commit: string
  kind: Kind
  files: VerifyFile[]
  fileNote: string
  state: RunState
  said: Said
  outcome: string
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
  off: string
  runs: VerifyRun[]
  browser: BrowserView
}

interface BrowserView {
  command: string
  hasCommand: boolean
  required: boolean
  queued: boolean
  current: VerifyRun | null
  why: string
  off: string
}

interface Opened {
  run: string
  name: string
  url: string
  width: number
  height: number
}

export interface VerifyTarget {
  sid: string
  disabled: string
}

const text = (v: unknown) => (typeof v === 'string' ? v : '')
const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : 0)
const sha = (v: unknown) => (typeof v === 'string' && SHA.test(v) ? v : '')
const state = (v: unknown): RunState | null => STATES.find(x => x === v) ?? null

function readFile(v: unknown): VerifyFile | null {
  if (!isRecord(v) || typeof v.name !== 'string' || !FILE.test(v.name) || (v.kind !== 'image' && v.kind !== 'trace')) return null
  return { name: v.name, kind: v.kind, bytes: num(v.bytes), width: num(v.width), height: num(v.height) }
}

function readRun(v: unknown): VerifyRun | null {
  if (!isRecord(v) || typeof v.id !== 'string' || !RUN.test(v.id) || !sha(v.commit)) return null
  const s = state(v.state)
  if (!s) return null
  const files = Array.isArray(v.files) ? v.files.map(readFile).filter(f => f !== null).slice(0, 20) : []
  return {
    id: v.id, commit: sha(v.commit), kind: v.kind === 'browser' ? 'browser' : 'check', files, fileNote: text(v.fileNote), state: s, said: saidOf(v.said, s), outcome: text(v.outcome) || s, code: num(v.code), tail: text(v.tail), cut: v.cut === true, withheld: v.withheld === true, reason: text(v.reason),
    by: text(v.by), image: text(v.image), imageId: text(v.imageId), limit: num(v.limitSeconds), started: num(v.started), ended: num(v.ended),
  }
}

function readBrowser(v: unknown): BrowserView {
  const b = isRecord(v) ? v : {}
  return { command: text(b.command), hasCommand: b.hasCommand === true, required: b.required === true, queued: b.queued === true, current: readRun(b.current), why: text(b.why), off: text(b.off) }
}

function readView(v: unknown): VerifyView | null {
  if (!isRecord(v)) return null
  const runs = Array.isArray(v.runs) ? v.runs.map(readRun).filter(r => r !== null) : []
  return {
    head: sha(v.head), branch: text(v.branch), command: text(v.command), hasCheck: v.hasCheck === true, image: text(v.image), required: v.required === true,
    busy: v.busy === true, queued: v.queued === true, current: readRun(v.current), why: text(v.why), off: text(v.off), runs, browser: readBrowser(v.browser),
  }
}

function size(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1 << 20) return `${(n / 1024).toFixed(0)} KiB`
  return `${(n / (1 << 20)).toFixed(1)} MiB`
}

function when(ms: number): string {
  return ms ? new Date(ms).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : ''
}

function outcome(r: VerifyRun): string {
  const what = r.outcome
  const at = r.ended ? `, ended ${when(r.ended)}` : r.started ? `, started ${when(r.started)}` : ''
  return `${r.kind === 'browser' ? 'Browser check of commit' : 'Commit'} ${shortSha(r.commit)}: ${what}${at}.`
}

function problemOf(a: EngineAnswer): string {
  return a.message
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
  #file: Opened | null = null
  #fetching = ''

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
    if (changed) this.#close()
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
    const a = await request(ROUTE, { op: 'read', sid })
    if (n !== this.#loading || sid !== this.#target?.sid) return
    if (!a.ok) {
      if (a.status !== 0 && a.status !== 404 && a.status !== 409 && a.status !== 501) return
      this.#view = null
      this.hidden = true
      this.replaceChildren()
      return
    }
    const v = readView(a.body)
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
    const a = await request(ROUTE, { ...body, sid })
    this.#busy = false
    if (sid !== this.#target?.sid) return
    if (!a.ok) {
      say(this.#status, problemOf(a), 'warn')
      void this.refresh(sid)
      return
    }
    const v = readView(a.body)
    if (v) this.#view = v
    say(this.#status, '')
    toast({ id: `verify:${sid}`, title: said, tone: 'ok' })
    this.#paint()
  }

  disconnectedCallback() {
    this.#close()
  }

  #close() {
    if (this.#file) URL.revokeObjectURL(this.#file.url)
    this.#file = null
  }

  async #openImage(run: VerifyRun, f: VerifyFile) {
    const sid = this.#target?.sid
    if (!sid || this.#fetching) return
    if (this.#file?.run === run.id && this.#file.name === f.name) {
      this.#close()
      this.#paint()
      return
    }
    this.#fetching = `${run.id}/${f.name}`
    this.#paint()
    const a = await request(ROUTE, { op: 'attachment', sid, id: run.id, name: f.name })
    this.#fetching = ''
    if (sid !== this.#target?.sid) return
    const body = a.body
    const blob = a.ok && body.type === 'image/png' && typeof body.data === 'string' ? pngBlob(body.data) : null
    if (!blob) {
      say(this.#status, a.ok ? 'The engine’s answer wasn’t a PNG image.' : problemOf(a), 'warn')
      this.#paint()
      return
    }
    this.#close()
    this.#file = { run: run.id, name: f.name, url: URL.createObjectURL(blob), width: f.width, height: f.height }
    this.#paint()
  }

  async #download(run: VerifyRun, f: VerifyFile) {
    const sid = this.#target?.sid
    if (!sid || this.#fetching) return
    this.#fetching = `${run.id}/${f.name}`
    this.#paint()
    const got = await download(ROUTE, { op: 'attachment', sid, id: run.id, name: f.name })
    const blob = got.type === 'application/octet-stream' ? got.file : null
    this.#fetching = ''
    if (!blob) {
      say(this.#status, got.answer.ok ? 'The engine didn’t send that file.' : problemOf(got.answer), 'warn')
      this.#paint()
      return
    }
    saveBlob(new Blob([blob], { type: 'application/octet-stream' }), f.name)
    say(this.#status, '')
    toast({ id: `verify:${sid}`, title: `Saved ${f.name}. The page doesn’t open it; open it with Playwright’s trace viewer, which runs it as the test’s own data.`, tone: 'ok' })
    this.#paint()
  }

  #files(r: VerifyRun): HTMLElement[] {
    const out: HTMLElement[] = []
    if (r.files.length) {
      out.push(el('p', 'small muted', 'Files the test code wrote to $SF_OUT. The agent wrote that code, so what an image shows is the test’s claim, not a finding of the engine, and it never changes the result.'))
      const list = el('ul', 'small list')
      for (const f of r.files) {
        const li = el('li', 'small')
        const key = `${r.id}/${f.name}`
        const open = this.#file?.run === r.id && this.#file.name === f.name
        const label = f.kind === 'image' ? `${open ? 'Hide' : 'Show'} ${f.name} (${f.width} × ${f.height}, ${size(f.bytes)})` : `Download ${f.name} (trace, ${size(f.bytes)})`
        const b = button(label, 'link small', () => void (f.kind === 'image' ? this.#openImage(r, f) : this.#download(r, f)), 'button', `verify-file-${key}`)
        if (f.kind === 'image') b.setAttribute('aria-pressed', String(open))
        if (this.#fetching === key) busy(b, true)
        li.append(b)
        if (open && this.#file) {
          const figure = el('figure', 'screenshot-view')
          const img = el('img')
          img.src = this.#file.url
          img.alt = `Image ${f.name} written by run ${shortSha(r.commit)}’s test code`
          figure.append(img, el('figcaption', 'small muted', `${f.name} · ${this.#file.width} × ${this.#file.height} · written by the test code`))
          li.append(figure)
        }
        list.append(li)
      }
      out.push(list)
    }
    if (r.fileNote) out.push(el('p', 'small muted', shown(r.fileNote)))
    return out
  }

  #summaryText(v: VerifyView): string {
    const c = v.current
    const b = v.browser.current
    const browser = b ? `; browser check: ${b.said.label}` : v.browser.queued ? '; browser check: queued' : ''
    if (c) return `Verification run: ${c.said.label} on ${shortSha(c.commit)}${browser}`
    if (v.queued) return `Verification run: queued for a free slot${browser}`
    return (v.required ? 'Verification run: none for this commit, and a push needs one' : 'Verification run: none for this commit') + browser
  }

  #run(r: VerifyRun, current: boolean): HTMLElement[] {
    const tone = r.said.tone ? `small ${r.said.tone}` : 'small'
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
      const tail = visibleCommand(r.tail)
      if (r.cut) out.push(el('p', 'small muted', 'The end of its output:'))
      out.push(codeBlock({ text: tail, label: current ? 'End of the verification run’s output' : `End of the output of the run of ${shortSha(r.commit)}`, copy: { text: tail, label: 'Copy the output' }, keep: 'end', wrap: true, size: 'small', tone: r.said.tone === 'bad' ? 'bad' : undefined }))
    }
    out.push(...this.#files(r))
    return out
  }

  #browserPart(t: VerifyTarget, v: VerifyView): HTMLElement[] {
    const b = v.browser
    const ran = v.runs.some(r => r.kind === 'browser')
    if (!b.hasCommand && !b.required && !ran) return []
    const parts: HTMLElement[] = [el('h4', 'small', 'Browser check')]
    parts.push(el('p', 'small muted', 'A browser check runs the project’s browser command the same way, in its own container of the same image, with $SF_OUT set to a folder whose files the engine keeps: up to 20 .png, .jpg, .jpeg or .gif images of at most 10 MiB, which it re-encodes, and .zip traces of at most 50 MiB, which it never opens, 100 MiB a run, for the latest 3 runs per project. The test’s own exit code is the result. The image must hold the browser and a folder /out owned by user 10006; the run has no network, so the app under test starts inside the run, and its test user comes from the repository’s seed data, never a real login.'))
    if (b.required) parts.push(el('p', 'small', 'The approved team file requires a passing browser check of the pushed commit.'))
    if (!b.hasCommand) parts.push(el('p', 'small warn', 'This project has no browser command. Set one under Check and setup commands on the project’s card.'))
    else parts.push(el('p', 'small', `Browser command: ${b.command ? visibleCommand(b.command) : 'set, but not shown, since it holds a token-shaped string'}`))
    if (b.current) parts.push(...this.#run(b.current, true))
    else if (b.queued) parts.push(el('p', 'small', `A browser check of commit ${shortSha(v.head)} that a push needs is queued for a free verification slot.`))
    else if (v.head && b.hasCommand) parts.push(el('p', 'small muted', `No browser check of commit ${shortSha(v.head)} with the current command and image yet.`))
    if (b.why) parts.push(el('p', 'small muted', shown(b.why)))
    const running = v.runs.find(r => r.state === 'running' && r.kind === 'browser')
    const row = el('div', 'row')
    const why = t.disabled || shown(b.off)
    const go = button('Run the browser check', 'small', () => {
      if (!why && !this.#busy) void whileBusy(go, () => this.#ask({ op: 'start', kind: 'browser' }, 'The browser check started.'))
    }, 'button', 'verify-browser-start')
    go.setAttribute('aria-disabled', String(!!why || this.#busy))
    if (this.#busy) busy(go, true)
    row.append(...(why ? offTip(go, why) : [go]))
    if (running) {
      row.append(button('Stop the browser check', 'ghost small', () => {
        if (!this.#busy) void this.#ask({ op: 'stop', id: running.id }, 'The browser check is stopping.')
      }, 'button', 'verify-browser-stop'))
    }
    parts.push(row)
    return parts
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
    const running = v.runs.find(r => r.state === 'running' && r.kind === 'check')
    const row = el('div', 'row')
    const why = t.disabled || shown(v.off)
    const go = button('Verify this commit', 'small', () => {
      if (!why && !this.#busy) void whileBusy(go, () => this.#ask({ op: 'start', kind: 'check' }, 'The verification run started.'))
    }, 'button', 'verify-start')
    go.setAttribute('aria-disabled', String(!!why || this.#busy))
    if (this.#busy) busy(go, true)
    row.append(...(why ? offTip(go, why) : [go]))
    if (running) {
      row.append(button('Stop the run', 'ghost small', () => {
        if (!this.#busy) void this.#ask({ op: 'stop', id: running.id }, 'The run is stopping.')
      }, 'button', 'verify-stop'))
    }
    parts.push(row)
    parts.push(...this.#browserPart(t, v))
    const earlier = v.runs.filter(r => r.id !== v.current?.id && r.id !== v.browser.current?.id)
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
    this.#shown = this.#opened ?? (v.required || v.queued || !!v.current || v.browser.required || v.browser.queued || !!v.browser.current)
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
