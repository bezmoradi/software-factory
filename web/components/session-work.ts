import { api } from '../api.ts'
import { busy, button, el, focusedKey, refocus, say, statusLine, visible } from '../lib/dom.ts'
import { isRecord } from '../lib/json.ts'

export interface AgentModel {
  id: string
  name: string
  efforts: string[]
  defaultEffort: string
  default: boolean
}

export interface AgentModels {
  models: AgentModel[]
  known: boolean
  efforts: string[]
}

interface CommandRun {
  kind: string
  state: string
  code: number
  tail: string
  cut: boolean
  withheld: boolean
  reason: string
  retry: boolean
  started: number
  ended: number
}

interface Queued {
  id: string
  text: string
  at: number
}

interface WorkView {
  agent: string
  model: { start: string; startEffort: string; model: string; effort: string; now: string; nowEffort: string; from: string; offered: AgentModel[]; known: boolean }
  queue: { items: Queued[]; stopped: string; waiting: string; sending: boolean; max: number }
  check: CommandRun | null
  setup: CommandRun | null
  ports: { first: number; last: number; env: boolean } | null
  hasCheck: boolean
  hasSetup: boolean
}

export interface SessionWorkTarget {
  sid: string
  disabled: boolean
}

const CLAUDE_EFFORTS = ['low', 'medium', 'high', 'xhigh', 'max']
const FALLBACK_EFFORTS = ['low', 'medium', 'high']

function strings(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
}

function modelsOf(v: unknown): AgentModel[] {
  return Array.isArray(v) ? v.filter(isRecord).filter(m => typeof m.id === 'string' && typeof m.name === 'string').map(m => ({ id: m.id as string, name: m.name as string, efforts: strings(m.efforts), defaultEffort: typeof m.defaultEffort === 'string' ? m.defaultEffort : '', default: m.default === true })) : []
}

export async function agentModels(agent: string): Promise<AgentModels | null> {
  const res = await api('/api/agent-models', { op: 'read', agent }).catch(() => null)
  if (!res || !res.ok) return null
  const body = await res.json().catch(() => null) as unknown
  if (!isRecord(body)) return null
  return { models: modelsOf(body.models), known: body.known === true, efforts: strings(body.efforts) }
}

export function modelOptions(select: HTMLSelectElement, models: AgentModel[], first: string) {
  const keep = select.value
  const blank = el('option', '', first)
  blank.value = ''
  select.replaceChildren(blank, ...models.map(m => {
    const o = el('option', '', visible(m.name === m.id ? m.id : `${m.name} (${m.id})`))
    o.value = m.id
    return o
  }))
  if (models.some(m => m.id === keep)) select.value = keep
}

export function effortsOf(offered: AgentModels | AgentModel[] | null, model: string): string[] {
  if (!offered) return CLAUDE_EFFORTS
  const list = Array.isArray(offered) ? offered : offered.models
  const known = Array.isArray(offered) ? list.length > 0 : offered.known
  if (!known) return FALLBACK_EFFORTS
  const m = list.find(x => (model ? x.id === model : x.default))
  return m?.efforts.length ? m.efforts : FALLBACK_EFFORTS
}

export function effortOptions(select: HTMLSelectElement, offered: AgentModels | AgentModel[] | null, model: string, first: string) {
  const blank = el('option', '', first)
  blank.value = ''
  select.replaceChildren(blank, ...effortsOf(offered, model).map(e => {
    const o = el('option', '', `Effort: ${visible(e)}`)
    o.value = e
    return o
  }))
}

function readRun(v: unknown): CommandRun | null {
  if (!isRecord(v) || typeof v.state !== 'string') return null
  return { kind: String(v.kind ?? ''), state: v.state, code: Number(v.code) || 0, tail: typeof v.tail === 'string' ? v.tail : '', cut: v.cut === true, withheld: v.withheld === true, reason: typeof v.reason === 'string' ? v.reason : '', retry: v.retry === true, started: Number(v.started) || 0, ended: Number(v.ended) || 0 }
}

function readView(v: unknown): WorkView | null {
  if (!isRecord(v) || !isRecord(v.model) || !isRecord(v.queue)) return null
  const m = v.model
  const q = v.queue
  const text = (x: unknown) => (typeof x === 'string' ? x : '')
  const items = Array.isArray(q.items) ? q.items.filter(isRecord).filter(i => typeof i.id === 'string' && typeof i.text === 'string').map(i => ({ id: i.id as string, text: i.text as string, at: Number(i.at) || 0 })) : []
  const ports = isRecord(v.ports) && typeof v.ports.first === 'number' && typeof v.ports.last === 'number' ? { first: v.ports.first, last: v.ports.last, env: v.ports.env === true } : null
  return {
    agent: text(v.agent),
    model: { start: text(m.start), startEffort: text(m.startEffort), model: text(m.model), effort: text(m.effort), now: text(m.now), nowEffort: text(m.nowEffort), from: text(m.from), offered: modelsOf(m.offered), known: m.known === true },
    queue: { items, stopped: text(q.stopped), waiting: text(q.waiting), sending: q.sending === true, max: Number(q.max) || 10 },
    check: readRun(v.check),
    setup: readRun(v.setup),
    ports,
    hasCheck: v.hasCheck === true,
    hasSetup: v.hasSetup === true,
  }
}

async function problemOf(res: Response | null): Promise<string> {
  if (!res) return 'The engine didn\'t answer. Try again.'
  const body = await res.json().catch(() => null) as { message?: unknown } | null
  return typeof body?.message === 'string' && body.message ? body.message : 'The engine refused the change. Try again.'
}

const STATE_WORDS: Record<string, string> = { running: 'is running', passed: 'passed', failed: 'failed', 'timed-out': 'didn\'t finish in time', error: 'couldn\'t run' }

function when(ms: number): string {
  return ms ? new Date(ms).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' }) : ''
}

class SfSessionWork extends HTMLElement {
  #target: SessionWorkTarget | null = null
  #view: WorkView | null = null
  #busy = false
  #busyKey = ''
  #status = statusLine()
  #model = el('select', 'textbox')
  #effort = el('select', 'textbox')
  #add = el('textarea', 'textbox')
  #editing = ''
  #edit = el('textarea', 'textbox')
  #loading = 0
  #key = ''
  #details = el('details', 'session-work')
  #summary = el('summary', 'small')
  #opened: boolean | null = null
  #shown = false

  constructor() {
    super()
    this.#model.id = 'work-model'
    this.#model.dataset.key = 'work-model'
    this.#effort.id = 'work-effort'
    this.#effort.dataset.key = 'work-effort'
    this.#effort.setAttribute('aria-label', 'Effort from the next turn')
    this.#model.addEventListener('change', () => this.#fillEfforts())
    this.#add.id = 'work-queue-add'
    this.#add.dataset.key = 'work-queue-add'
    this.#add.rows = 2
    this.#add.maxLength = 20000
    this.#add.placeholder = 'A message for after the current turn'
    this.#edit.rows = 3
    this.#edit.maxLength = 20000
    this.#edit.dataset.key = 'work-queue-edit'
    this.#edit.setAttribute('aria-label', 'Edit the queued message')
    this.#details.addEventListener('toggle', () => {
      if (this.#details.open !== this.#shown) this.#opened = this.#details.open
      this.#shown = this.#details.open
    })
  }

  set view(value: SessionWorkTarget | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    const changed = value?.sid !== this.#target?.sid
    this.#target = value
    this.hidden = !value?.sid
    if (!value?.sid) {
      this.#view = null
      this.replaceChildren()
      return
    }
    if (changed) {
      this.#view = null
      this.#editing = ''
      this.#opened = null
      this.#status.textContent = ''
      void this.refresh(value.sid)
    } else this.#paint()
  }

  async refresh(sid: string) {
    if (sid !== this.#target?.sid) return
    const n = ++this.#loading
    const res = await api('/api/session-work', { op: 'read', sid }).catch(() => null)
    if (n !== this.#loading || sid !== this.#target?.sid || !res || !res.ok) return
    const v = readView(await res.json().catch(() => null))
    if (!v) return
    this.#view = v
    this.#paint()
  }

  focusModel() {
    this.hidden = false
    this.#opened = true
    this.#shown = true
    this.#details.open = true
    this.#model.focus()
  }

  #summaryText(v: WorkView): string {
    const parts: string[] = []
    if (v.agent === 'claude' || v.agent === 'codex') parts.push(`Model: ${v.model.now ? visible(v.model.now) : 'default'}${v.model.nowEffort ? ` (${visible(v.model.nowEffort)})` : ''}`)
    parts.push(v.queue.items.length ? `Queue: ${v.queue.items.length}${v.queue.stopped ? ', stopped' : ''}` : 'Queue: empty')
    if (v.setup) parts.push(`Setup ${STATE_WORDS[v.setup.state] ?? visible(v.setup.state)}`)
    if (v.check) parts.push(`Check ${STATE_WORDS[v.check.state] ?? visible(v.check.state)}`)
    if (v.ports) parts.push(`Ports ${v.ports.first}–${v.ports.last}`)
    return parts.join(' · ')
  }

  #notable(v: WorkView): boolean {
    return v.queue.items.length > 0 || !!v.queue.stopped || v.setup?.state === 'running' || v.check?.state === 'failed' || v.check?.state === 'timed-out'
  }

  #held(b: HTMLButtonElement): boolean {
    if (!this.#busy || this.#busyKey !== b.dataset.key) return false
    busy(b, true)
    return true
  }

  async #call(body: Record<string, unknown>, ok: string, key: string): Promise<boolean> {
    const sid = this.#target?.sid
    if (!sid) return false
    this.#busy = true
    this.#busyKey = key
    this.#paint()
    const res = await api('/api/session-work', { ...body, sid }).catch(() => null)
    this.#busy = false
    if (sid !== this.#target?.sid) return false
    if (!res || !res.ok) {
      say(this.#status, await problemOf(res), 'warn')
      this.#paint()
      return false
    }
    const v = readView(await res.json().catch(() => null))
    if (v) this.#view = v
    say(this.#status, ok, 'muted')
    this.#paint()
    return true
  }

  #fillEfforts() {
    const v = this.#view
    if (!v) return
    const keep = this.#effort.value
    effortOptions(this.#effort, v.agent === 'codex' ? v.model.offered : null, this.#model.value || v.model.start, 'Effort: as it is')
    if ([...this.#effort.options].some(o => o.value === keep)) this.#effort.value = keep
  }

  #modelPart(v: WorkView, off: boolean): HTMLElement[] {
    if (v.agent !== 'claude' && v.agent !== 'codex') return []
    const name = (id: string) => v.model.offered.find(m => m.id === id)?.name ?? id
    const now = v.model.now ? visible(name(v.model.now)) : 'the agent\'s default'
    const effort = v.model.nowEffort ? `, effort ${visible(v.model.nowEffort)}` : ''
    const said = v.model.model || v.model.effort ? `Model: ${now}${effort}, set from this page; it applies from ${v.model.from || 'the next turn'}.` : `Model: ${now}${effort}, as the session started.`
    const parts: HTMLElement[] = [el('h3', 'label', 'Model'), el('p', 'small', said)]
    if (v.agent === 'claude') parts.push(el('p', 'small muted', 'Claude Code\'s own status line keeps naming the model the session started with; the switch rewrites only this session\'s main-loop requests, and never Claude Code\'s saved defaults.'))
    if (v.agent === 'codex' && !v.model.known) parts.push(el('p', 'small muted', 'Codex\'s model list couldn\'t be read, so no other model is offered.'))
    if (!this.contains(document.activeElement) || document.activeElement === document.body) {
      modelOptions(this.#model, v.model.offered, v.agent === 'claude' ? `The start model (${v.model.start || 'default'})` : v.model.start ? `The start model (${visible(v.model.start)})` : 'Pick a model')
      this.#model.value = v.model.offered.some(m => m.id === v.model.model) ? v.model.model : ''
      this.#fillEfforts()
      this.#effort.value = [...this.#effort.options].some(o => o.value === v.model.effort) ? v.model.effort : ''
    }
    const label = el('label', 'small', 'Switch to')
    label.htmlFor = this.#model.id
    this.#model.disabled = off
    this.#effort.disabled = off
    const go = button('Switch model', 'small', () => void this.#call({ op: 'model', model: this.#model.value, effort: this.#effort.value }, 'Switched. The new model applies from the next turn.', 'work-switch'), 'button', 'work-switch')
    go.disabled = !this.#held(go) && off
    const row = el('div', 'row field-row')
    row.append(label, this.#model, this.#effort, go)
    parts.push(row)
    return parts
  }

  #queuePart(v: WorkView, off: boolean): HTMLElement[] {
    const q = v.queue
    const parts: HTMLElement[] = [el('h3', 'label', `Queue (${q.items.length} of ${q.max})`), el('p', 'small muted', 'The engine sends the next message when a turn ends. It stops at a card, a held push, a failed or stopped turn, a sign-in problem, Stop everything or Pause all, and says why here.')]
    if (q.waiting) parts.push(el('p', 'small', q.waiting))
    if (q.stopped) {
      const row = el('div', 'row')
      const resume = button('Resume', 'small', () => void this.#call({ op: 'queue-resume' }, 'The queue sends the next message when the session is free.', 'work-resume'), 'button', 'work-resume')
      resume.disabled = !this.#held(resume) && off
      row.append(el('p', 'small warn', `Stopped: ${q.stopped}`), resume)
      parts.push(row)
    }
    if (q.items.length) {
      const list = el('ol', 'list')
      q.items.forEach((m, i) => {
        const li = el('li')
        const row = el('div', 'row')
        if (this.#editing === m.id) {
          const save = button('Save', 'small', () => void this.#call({ op: 'queue-edit', id: m.id, text: this.#edit.value }, 'Saved.', `work-save:${m.id}`).then(ok => {
            if (ok) {
              this.#editing = ''
              this.#paint()
            }
          }), 'button', `work-save:${m.id}`)
          const cancel = button('Cancel', 'ghost small', () => {
            this.#editing = ''
            this.#paint()
          }, 'button', `work-cancel:${m.id}`)
          save.disabled = !this.#held(save) && off
          row.append(this.#edit, cancel, save)
        } else {
          const text = el('p', 'small queued-text', visible(m.text.length > 400 ? `${m.text.slice(0, 400)}…` : m.text))
          const up = button('Up', 'ghost small', () => void this.#call({ op: 'queue-move', id: m.id, to: i - 1 }, 'Moved.', `work-up:${m.id}`), 'button', `work-up:${m.id}`)
          const down = button('Down', 'ghost small', () => void this.#call({ op: 'queue-move', id: m.id, to: i + 1 }, 'Moved.', `work-down:${m.id}`), 'button', `work-down:${m.id}`)
          const edit = button('Edit', 'ghost small', () => {
            this.#editing = m.id
            this.#edit.value = m.text
            this.#paint()
            this.#edit.focus()
          }, 'button', `work-edit:${m.id}`)
          const remove = button('Remove', 'ghost small', () => void this.#call({ op: 'queue-remove', id: m.id }, 'Removed.', `work-remove:${m.id}`), 'button', `work-remove:${m.id}`)
          up.disabled = !this.#held(up) && (off || i === 0 || (q.sending && i <= 1))
          down.disabled = !this.#held(down) && (off || i === q.items.length - 1 || (q.sending && i === 0))
          edit.disabled = off || (q.sending && i === 0)
          remove.disabled = !this.#held(remove) && edit.disabled
          for (const [b, what] of [[up, 'Move up'], [down, 'Move down'], [edit, 'Edit'], [remove, 'Remove']] as const) b.setAttribute('aria-label', `${what} queued message ${i + 1}`)
          row.append(text, up, down, edit, remove)
        }
        li.append(row)
        list.append(li)
      })
      parts.push(list)
    }
    const label = el('label', 'small', 'Add to the queue')
    label.htmlFor = this.#add.id
    this.#add.disabled = off || q.items.length >= q.max
    const add = button('Add to queue', 'small', () => {
      const text = this.#add.value.trim()
      if (!text) return
      void this.#call({ op: 'queue-add', text }, 'Queued.', 'work-add').then(ok => {
        if (ok) this.#add.value = ''
      })
    }, 'button', 'work-add')
    add.disabled = !this.#held(add) && this.#add.disabled
    const box = el('div', 'field')
    box.append(label, this.#add, add)
    parts.push(box)
    return parts
  }

  #runPart(run: CommandRun | null, title: string, set: boolean): HTMLElement[] {
    if (!run) return set ? [el('h3', 'label', title), el('p', 'small muted', title === 'Check' ? 'No check has run in this session yet.' : 'The setup command hasn\'t run in this session.')] : []
    const word = STATE_WORDS[run.state] ?? visible(run.state)
    const head = `${title === 'Check' ? 'The project\'s check' : 'The setup command'} ${word}${run.state === 'failed' ? ` (exit code ${run.code})` : ''}${run.ended ? ` at ${when(run.ended)}` : run.started ? `, started ${when(run.started)}` : ''}.${run.retry ? ' That was its run after the agent\'s retry.' : ''}`
    const parts: HTMLElement[] = [el('h3', 'label', title), el('p', `small ${run.state === 'passed' ? 'ok' : run.state === 'running' ? 'muted' : 'warn'}`, head)]
    if (title === 'Check') parts.push(el('p', 'small muted', 'Advisory only: it never counts for a review or a push.'))
    if (run.reason) parts.push(el('p', 'small muted', visible(run.reason)))
    if (run.withheld) parts.push(el('p', 'small muted', 'The output held a token-shaped string, so the engine didn\'t keep it.'))
    else if (run.tail) {
      if (run.cut) parts.push(el('p', 'small muted', 'The last 8 KiB of its output:'))
      const pre = el('pre', 'small command-tail', visible(run.tail))
      pre.tabIndex = 0
      pre.setAttribute('aria-label', `${title} output`)
      parts.push(pre)
    }
    return parts
  }

  #paint() {
    const t = this.#target
    const v = this.#view
    if (!t?.sid || !v) return this.replaceChildren()
    const focused = focusedKey(this)
    const off = t.disabled || this.#busy
    const parts: HTMLElement[] = [...this.#modelPart(v, off), ...this.#queuePart(v, off), ...this.#runPart(v.setup, 'Setup', v.hasSetup), ...this.#runPart(v.check, 'Check', v.hasCheck)]
    if (v.ports) parts.push(el('h3', 'label', 'Ports'), el('p', 'small', `This session's ports: ${v.ports.first} to ${v.ports.last}. Its start note names them${v.ports.env ? ', and PORT and SF_PORT_BASE in its shell hold the first one' : ''}.`))
    parts.push(this.#status)
    this.#summary.textContent = this.#summaryText(v)
    this.#shown = this.#opened ?? this.#notable(v)
    this.#details.open = this.#shown
    this.#details.replaceChildren(this.#summary, ...parts)
    if (this.#details.parentNode !== this) this.replaceChildren(this.#details)
    refocus(this, focused, () => ['work-add', 'work-queue-add', 'work-model'])
  }
}

customElements.define('sf-session-work', SfSessionWork)

declare global {
  interface HTMLElementTagNameMap {
    'sf-session-work': SfSessionWork
  }
}
