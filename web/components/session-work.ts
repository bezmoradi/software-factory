import { request, savedTone, type EngineAnswer } from '../api.ts'
import { busy, button, el, emit, focusedKey, labelError, refocus, replaceKeeping, say, statusLine, unspoofed, visible } from '../lib/dom.ts'
import { actions, answerControl, field, form } from '../lib/form.ts'
import { plural, shortSha } from '../lib/format.ts'
import { isRecord, saidOf, type Said } from '../lib/json.ts'
import type { Attention, LockedTests, TestLockView, TypeChange } from '../types.ts'
import { sectionHead } from '../lib/page.ts'
import { typeChangeOf, typeChangeParts } from './push-facts.ts'
import { textarea, dropdown, option } from '../lib/controls.ts'
import { outcome } from './toasts.ts'
import './caps.ts'
import { codeBlock } from './code-block.ts'

export interface AgentModel {
  id: string
  name: string
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
  said: Said
}

interface Queued {
  id: string
  text: string
  at: number
  up: boolean
  down: boolean
  editable: boolean
}

interface WorkView {
  agent: string
  model: { start: string; startEffort: string; model: string; effort: string; now: string; nowEffort: string; from: string; offered: AgentModel[]; efforts: string[] }
  queue: { items: Queued[]; stopped: string; waiting: string; sending: boolean; max: number; canAdd: boolean; full: string }
  check: CommandRun | null
  setup: CommandRun | null
  ports: { first: number; last: number; env: boolean } | null
  hasCheck: boolean
  hasSetup: boolean
  canSwitchModel: boolean
}

export interface SessionWorkTarget {
  sid: string
  disabled: boolean
  reason?: string
}


function strings(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
}

function modelsOf(v: unknown): AgentModel[] {
  return Array.isArray(v) ? v.filter(isRecord).filter(m => typeof m.id === 'string' && typeof m.name === 'string').map(m => ({ id: m.id as string, name: m.name as string, efforts: strings(m.efforts) })) : []
}

export function modelOptions(select: HTMLSelectElement, models: AgentModel[], first: string) {
  const keep = select.value
  select.replaceChildren(option(first, ''), ...models.map(m => option(visible(m.name === m.id ? m.id : `${m.name} (${m.id})`), m.id)))
  if (models.some(m => m.id === keep)) select.value = keep
}

export function effortOptions(select: HTMLSelectElement, first: string, efforts: string[]) {
  select.replaceChildren(option(first, ''), ...efforts.map(e => option(`Effort: ${visible(e)}`, e)))
}

function readRun(v: unknown): CommandRun | null {
  if (!isRecord(v) || typeof v.state !== 'string') return null
  return { kind: String(v.kind ?? ''), state: v.state, code: Number(v.code) || 0, tail: typeof v.tail === 'string' ? v.tail : '', cut: v.cut === true, withheld: v.withheld === true, reason: typeof v.reason === 'string' ? v.reason : '', retry: v.retry === true, started: Number(v.started) || 0, ended: Number(v.ended) || 0, said: saidOf(v.said, v.state) }
}

function readView(v: unknown): WorkView | null {
  if (!isRecord(v) || !isRecord(v.model) || !isRecord(v.queue)) return null
  const m = v.model
  const q = v.queue
  const text = (x: unknown) => (typeof x === 'string' ? x : '')
  const items = Array.isArray(q.items) ? q.items.filter(isRecord).filter(i => typeof i.id === 'string' && typeof i.text === 'string').map(i => ({ id: i.id as string, text: i.text as string, at: Number(i.at) || 0, up: i.up === true, down: i.down === true, editable: i.editable === true })) : []
  const ports = isRecord(v.ports) && typeof v.ports.first === 'number' && typeof v.ports.last === 'number' ? { first: v.ports.first, last: v.ports.last, env: v.ports.env === true } : null
  return {
    agent: text(v.agent),
    model: { start: text(m.start), startEffort: text(m.startEffort), model: text(m.model), effort: text(m.effort), now: text(m.now), nowEffort: text(m.nowEffort), from: text(m.from), offered: modelsOf(m.offered), efforts: strings(m.efforts) },
    queue: { items, stopped: text(q.stopped), waiting: text(q.waiting), sending: q.sending === true, max: Number(q.max) || 0, canAdd: q.canAdd === true, full: text(q.full) },
    check: readRun(v.check),
    setup: readRun(v.setup),
    ports,
    hasCheck: v.hasCheck === true,
    hasSetup: v.hasSetup === true,
    canSwitchModel: v.canSwitchModel === true,
  }
}

interface LockRead {
  head: string
  lock: TestLockView
  typeChange: TypeChange | null
}

function lockedOf(v: unknown): LockedTests | null {
  if (!isRecord(v) || typeof v.commit !== 'string') return null
  return { commit: v.commit, count: Number(v.count) || 0, paths: strings(v.paths), problem: typeof v.problem === 'string' ? v.problem : '' }
}

function readLock(v: unknown): LockRead | null {
  if (!isRecord(v) || !isRecord(v.gate) || !isRecord(v.gate.testLock)) return null
  const l = v.gate.testLock
  const text = (x: unknown) => (typeof x === 'string' ? x : '')
  return {
    head: text(v.gate.head),
    typeChange: typeChangeOf(v.gate.typeChange),
    lock: { offered: l.offered === true, why: text(l.why), commit: text(l.commit), at: Number(l.at) || 0, files: Number(l.files) || 0, paths: strings(l.paths), cut: l.cut === true, branchLeft: Number(l.branchLeft) || 0, changed: lockedOf(l.changed) },
  }
}

function shown(value: string): string {
  return visible(unspoofed(value))
}

function problemOf(a: EngineAnswer): string {
  return a.message
}

function when(ms: number): string {
  return ms ? new Date(ms).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' }) : ''
}

function runSaid(run: CommandRun, title: string): string {
  return `${title === 'Check' ? 'The project\'s check' : 'The setup command'} ${visible(run.said.label)}${run.state === 'failed' ? ` (exit code ${run.code})` : ''}${run.ended ? ` at ${when(run.ended)}` : run.started ? `, started ${when(run.started)}` : ''}.${run.retry ? ' That was its run after the agent\'s retry.' : ''}`
}

class SfSessionWork extends HTMLElement {
  #target: SessionWorkTarget | null = null
  #view: WorkView | null = null
  #busy = false
  #busyKey = ''
  #status = statusLine()
  #model = dropdown()
  #effort = dropdown()
  #add = textarea({ rows: 2, name: 'text', placeholder: 'A message for after the current turn' })
  #editing = ''
  #edit = textarea({ rows: 3, name: 'text' })
  #bad: { input: HTMLElement; text: string } | null = null
  #loading = 0
  #key = ''
  #told = ''
  #caps = el('sf-session-limits')
  #lock: LockRead | null = null
  #lockLoading = 0

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
    this.#edit.dataset.key = 'work-queue-edit'
    this.#edit.id = 'work-queue-edit'
    for (const input of [this.#add, this.#edit]) input.addEventListener('input', () => {
      if (this.#bad?.input !== input) return
      this.#bad = null
      labelError(input, '')
    })
    this.#caps.caps = true
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
      this.#tell()
      return
    }
    if (changed) {
      this.#view = null
      this.#lock = null
      this.#editing = ''
      this.#status.textContent = ''
      this.#tell()
      void this.refresh(value.sid)
    } else this.#paint()
  }

  async refresh(sid: string) {
    if (sid !== this.#target?.sid) return
    const n = ++this.#loading
    void this.#readLock(sid)
    const a = await request('/api/session-work', { op: 'read', sid })
    if (n !== this.#loading || sid !== this.#target?.sid || !a.ok) return
    const v = readView(a.body)
    if (!v) return
    this.#view = v
    this.#paint()
  }

  async #readLock(sid: string) {
    const n = ++this.#lockLoading
    const a = await request('/api/gate', { sid })
    if (n !== this.#lockLoading || sid !== this.#target?.sid || !a.ok) return
    const v = readLock(a.body)
    if (!v) return
    this.#lock = v
    this.#paint()
  }

  async #lockCall(op: 'lock' | 'unlock', key: string) {
    const sid = this.#target?.sid
    const head = this.#lock?.head ?? ''
    if (!sid) return
    this.#busy = true
    this.#busyKey = key
    this.#paint()
    const a = await request('/api/lock-tests', op === 'lock' ? { sid, op, head } : { sid, op })
    this.#busy = false
    if (sid !== this.#target?.sid) return
    if (!a.ok) {
      say(this.#status, problemOf(a), 'warn')
      this.#paint()
      void this.#readLock(sid)
      return
    }
    say(this.#status, '')
    outcome(this, this.#status, { id: `work:${sid}`, title: op === 'lock' ? `Locked the tests of commit ${shortSha(head)}.` : 'Unlocked the tests.', tone: 'ok' })
    await this.#readLock(sid)
    this.#paint()
  }

  #lockPart(off: boolean): HTMLElement[] {
    const r = this.#lock
    if (!r) return []
    const l = r.lock
    const parts: HTMLElement[] = [el('h3', 'label', 'Locked tests')]
    if (!l.commit) {
      parts.push(el('p', 'small muted', 'Lock the tests once the ones you want kept are written and committed. Claude Code\'s file tools (Edit, Write and the like) then ask you before each write to one of them; a shell command or another agent doesn\'t ask. The push card shows any locked file that changed since, however it was changed. Neither holds a push. Approving the requirements or the plan again unlocks them.'))
      if (!l.offered) {
        if (l.why) parts.push(el('p', 'small muted', shown(l.why)))
        return parts
      }
      const lock = button('Lock tests', 'small', () => void this.#lockCall('lock', 'work-lock'), 'button', 'work-lock')
      lock.disabled = !this.#held(lock) && off
      lock.setAttribute('aria-label', `Lock the test files of commit ${shortSha(r.head)}`)
      const row = el('div', 'row')
      row.append(lock)
      parts.push(row)
      return parts
    }
    const files = `${l.files} test ${l.files === 1 ? 'file' : 'files'}`
    parts.push(el('p', 'small', `${files} locked at commit ${shortSha(l.commit)}${l.at ? ` at ${when(l.at)}` : ''}. Claude Code's file tools (Edit, Write and the like) ask you before each write to one of them; a shell command or another agent doesn't ask. The push card shows any locked file that changed, however it was changed. Neither holds a push.`))
    if (l.cut) {
      parts.push(el('p', 'small warn', `Only these ${files} are locked: the engine locks at most 2,000 test files and 256 KiB of their paths, and this commit has more.`))
      if (l.branchLeft) parts.push(el('p', 'small warn', `${l.branchLeft} test ${l.branchLeft === 1 ? 'file' : 'files'} this branch adds or changes ${l.branchLeft === 1 ? "isn't" : "aren't"} among them.`))
      else parts.push(el('p', 'small muted', 'Every test file this branch adds or changes is among them.'))
    }
    const c = l.changed
    if (c?.problem) parts.push(el('p', 'small warn', `${shown(c.problem.charAt(0).toUpperCase() + c.problem.slice(1))}.`))
    else if (c && c.count) {
      parts.push(el('p', 'small warn', `Locked tests changed since ${shortSha(l.commit)}: ${c.count}`))
      const list = el('ul', 'small list')
      list.append(...c.paths.map(p => el('li', '', shown(p))))
      if (c.count > c.paths.length) list.append(el('li', 'muted', `and ${c.count - c.paths.length} more`))
      parts.push(list)
    } else if (c) parts.push(el('p', 'small muted', `None of the locked files changed since commit ${shortSha(l.commit)}. A new test file or test configuration can still stop them running, and isn't counted here.`))
    if (l.paths.length) {
      const box = el('details', 'small')
      box.append(el('summary', 'small', `The locked files (${l.files})`))
      const list = el('ul', 'small list')
      list.append(...l.paths.map(p => el('li', '', shown(p))))
      if (l.files > l.paths.length) list.append(el('li', 'muted', `and ${l.files - l.paths.length} more`))
      box.append(list)
      parts.push(box)
    }
    const unlock = button('Unlock tests', 'ghost small', () => void this.#lockCall('unlock', 'work-unlock'), 'button', 'work-unlock')
    unlock.disabled = !this.#held(unlock) && off
    const row = el('div', 'row')
    row.append(unlock)
    parts.push(row)
    return parts
  }

  focusModel(): boolean {
    if (!this.#model.isConnected) return false
    this.#model.focus()
    return document.activeElement === this.#model
  }

  get attention(): Attention | null {
    const v = this.#view
    if (!v) return null
    const q = v.queue
    if (q.stopped) return { section: 'queue', rank: 1, title: 'Queue.', text: `Stopped: ${q.stopped}`, alert: false }
    if (v.check && (v.check.state === 'failed' || v.check.state === 'timed-out')) return { section: 'check', rank: 2, title: 'Check.', text: runSaid(v.check, 'Check'), alert: false }
    if (v.setup?.state === 'running') return { section: 'setup', rank: 6, title: 'Setup.', text: runSaid(v.setup, 'Setup'), alert: false }
    if (q.items.length) return { section: 'queue', rank: 7, title: 'Queue.', text: q.waiting || `${q.items.length} ${plural(q.items.length, 'message')} waiting.`, alert: false }
    return null
  }

  #tell() {
    const key = JSON.stringify(this.attention)
    if (key === this.#told) return
    this.#told = key
    emit(this, 'sf-attention')
  }

  #held(b: HTMLButtonElement): boolean {
    if (!this.#busy || this.#busyKey !== b.dataset.key) return false
    busy(b, true)
    return true
  }

  async #call(body: Record<string, unknown>, ok: string | null, key: string): Promise<boolean> {
    const sid = this.#target?.sid
    if (!sid) return false
    this.#busy = true
    this.#busyKey = key
    this.#bad = null
    this.#paint()
    const a = await request('/api/session-work', { ...body, sid })
    this.#busy = false
    if (sid !== this.#target?.sid) return false
    if (!a.ok) {
      const place = body.op === 'queue-add' ? this.#add.form : body.op === 'queue-edit' ? this.#edit.form : null
      const input = place && a.message ? answerControl(place, a) : null
      this.#bad = input && { input, text: a.message }
      say(this.#status, input ? '' : problemOf(a), 'warn')
      this.#paint()
      input?.focus()
      return false
    }
    const v = readView(a.body)
    if (v) this.#view = v
    say(this.#status, '')
    outcome(this, this.#status, { id: `work:${sid}`, title: ok ?? a.message, tone: ok === null ? savedTone(a) : 'ok' })
    this.#paint()
    return true
  }

  #fillEfforts() {
    const v = this.#view
    if (!v) return
    const keep = this.#effort.value
    effortOptions(this.#effort, 'Effort: as it is', v.model.efforts)
    if ([...this.#effort.options].some(o => o.value === keep)) this.#effort.value = keep
  }

  #modelPart(v: WorkView, off: boolean): HTMLElement[] {
    if (!v.canSwitchModel) return []
    const name = (id: string) => v.model.offered.find(m => m.id === id)?.name ?? id
    const now = v.model.now ? visible(name(v.model.now)) : 'the agent\'s default'
    const effort = v.model.nowEffort ? `, effort ${visible(v.model.nowEffort)}` : ''
    const said = v.model.model || v.model.effort ? `Model: ${now}${effort}, set from this page; it applies from ${v.model.from || 'the next turn'}.` : `Model: ${now}${effort}, as the session started.`
    const parts: HTMLElement[] = [sectionHead('Model', 'model'), el('p', 'small', said)]
    parts.push(el('p', 'small muted', 'Claude Code\'s own status line keeps naming the model the session started with; the switch rewrites only this session\'s main-loop requests, and never Claude Code\'s saved defaults.'))
    if (!this.contains(document.activeElement) || document.activeElement === document.body) {
      modelOptions(this.#model, v.model.offered, `The start model (${v.model.start || 'default'})`)
      this.#model.value = v.model.offered.some(m => m.id === v.model.model) ? v.model.model : ''
      this.#fillEfforts()
      this.#effort.value = [...this.#effort.options].some(o => o.value === v.model.effort) ? v.model.effort : ''
    }
    this.#model.disabled = off
    this.#effort.disabled = off
    const go = button('Switch model', 'primary small', undefined, 'submit', 'work-switch')
    go.disabled = !this.#held(go) && off
    parts.push(form({
      inline: true,
      cls: 'field-row',
      children: [field('Switch to', this.#model, { kind: 'inline', cls: 'field-row' }), this.#effort, actions([go])],
      onSubmit: () => void this.#call({ op: 'model', model: this.#model.value, effort: this.#effort.value }, 'Switched. The new model applies from the next turn.', 'work-switch'),
    }))
    return parts
  }

  #queuePart(v: WorkView, off: boolean): HTMLElement[] {
    const q = v.queue
    const parts: HTMLElement[] = [sectionHead(`Queue (${q.items.length} of ${q.max})`, 'queue'), el('p', 'small muted', 'The engine sends the next message when a turn ends. It stops at a card, a held push, a failed or stopped turn, or a sign-in problem, and says why here.')]
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
        if (this.#editing === m.id) {
          const save = button('Save', 'primary small', undefined, 'submit', `work-save:${m.id}`)
          const cancel = button('Cancel', 'ghost small', () => {
            this.#editing = ''
            this.#paint()
          }, 'button', `work-cancel:${m.id}`)
          save.disabled = !this.#held(save) && off
          li.append(form({
            inline: true,
            children: [field('Queued message', this.#edit), actions([cancel, save])],
            onSubmit: () => void this.#call({ op: 'queue-edit', id: m.id, text: this.#edit.value }, null, `work-save:${m.id}`).then(ok => {
              if (ok) {
                this.#editing = ''
                this.#paint()
              }
            }),
          }))
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
          up.disabled = !this.#held(up) && (off || !m.up)
          down.disabled = !this.#held(down) && (off || !m.down)
          edit.disabled = off || !m.editable
          remove.disabled = !this.#held(remove) && edit.disabled
          for (const [b, what] of [[up, 'Move up'], [down, 'Move down'], [edit, 'Edit'], [remove, 'Remove']] as const) b.setAttribute('aria-label', `${what} queued message ${i + 1}`)
          const row = el('div', 'row')
          row.append(text, up, down, edit, remove)
          li.append(row)
        }
        list.append(li)
      })
      parts.push(list)
    }
    this.#add.disabled = off || !q.canAdd
    const add = button('Add to queue', 'primary small', undefined, 'submit', 'work-add')
    add.disabled = !this.#held(add) && this.#add.disabled
    parts.push(form({
      children: [field('Add to the queue', this.#add), actions([add])],
      onSubmit: () => {
        const text = this.#add.value.trim()
        void this.#call({ op: 'queue-add', text }, 'Queued.', 'work-add').then(ok => {
          if (ok) this.#add.value = ''
          else this.#add.focus()
        })
      },
    }))
    if (q.full) parts.push(el('p', 'small muted', visible(q.full)))
    return parts
  }

  #runPart(run: CommandRun | null, title: string, set: boolean): HTMLElement[] {
    const section = title === 'Check' ? 'check' : 'setup'
    if (!run) return set ? [sectionHead(title, section), el('p', 'small muted', title === 'Check' ? 'No check has run in this session yet.' : 'The setup command hasn\'t run in this session.')] : []
    const parts: HTMLElement[] = [sectionHead(title, section), el('p', `small ${run.said.tone}`, runSaid(run, title))]
    if (title === 'Check') parts.push(el('p', 'small muted', 'Advisory only: it never counts for a review or a push.'))
    if (run.reason) parts.push(el('p', 'small muted', visible(run.reason)))
    if (run.withheld) parts.push(el('p', 'small muted', 'The output held a token-shaped string, so the engine didn\'t keep it.'))
    else if (run.tail) {
      if (run.cut) parts.push(el('p', 'small muted', 'The last 8 KiB of its output:'))
      const tail = visible(run.tail)
      parts.push(codeBlock({ text: tail, label: `${title} output`, copy: { text: tail, label: 'Copy the output' }, keep: 'end', wrap: true, size: 'small', tone: run.said.tone === 'bad' ? 'bad' : undefined }))
    }
    return parts
  }

  #paint() {
    const t = this.#target
    const v = this.#view
    if (!t?.sid || !v) return this.replaceChildren()
    const focused = focusedKey(this)
    const off = t.disabled || this.#busy
    const parts: HTMLElement[] = [...(t.reason ? [el('p', 'small muted', t.reason)] : []), ...this.#modelPart(v, off), ...this.#queuePart(v, off), ...this.#runPart(v.setup, 'Setup', v.hasSetup), ...this.#runPart(v.check, 'Check', v.hasCheck), this.#caps]
    if (v.ports) parts.push(sectionHead('Ports', 'ports'), el('p', 'small', `This session's ports: ${v.ports.first} to ${v.ports.last}. Its start note names them${v.ports.env ? ', and PORT and SF_PORT_BASE in its shell hold the first one' : ''}.`))
    parts.push(...this.#lockPart(off))
    if (this.#lock?.typeChange) parts.push(el('h3', 'label', 'The change and its work type'), ...typeChangeParts(this.#lock.typeChange))
    parts.push(this.#status)
    replaceKeeping(this, parts)
    for (const input of [this.#add, this.#edit]) labelError(input, this.#bad?.input === input ? this.#bad.text : '')
    this.#tell()
    refocus(this, focused, key => [...(key === 'work-lock' ? ['work-unlock'] : key === 'work-unlock' ? ['work-lock'] : []), 'work-add', 'work-queue-add', 'work-model'])
  }
}

customElements.define('sf-session-work', SfSessionWork)

declare global {
  interface HTMLElementTagNameMap {
    'sf-session-work': SfSessionWork
  }
}
