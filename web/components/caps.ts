import { request } from '../api.ts'
import { busy, button, el, focusedKey, labelError, refocus, replaceKeeping, say, statusLine, visible, whileBusy } from '../lib/dom.ts'
import { actions, field, fieldRow, form } from '../lib/form.ts'
import { continueBody, day, limitsAnswer, limitsNow, limitsSession, meterFor, watchLimits, when, windowLine } from './usage.ts'
import type { CapsValues, LimitsAnswer, SessionLimits } from './usage.ts'
import { textbox } from '../lib/controls.ts'
import { pageHead } from '../lib/page.ts'
import { toast } from './toasts.ts'

type CapKind = 'money' | 'time' | 'turns' | 'disk'

type CapsAnswer = LimitsAnswer

const KINDS: { kind: CapKind; key: keyof CapsValues; label: string; unit: string }[] = [
  { kind: 'money', key: 'money', label: 'Spending cap (USD)', unit: '$' },
  { kind: 'time', key: 'minutes', label: 'Time cap (minutes)', unit: 'min' },
  { kind: 'turns', key: 'turns', label: 'Turn cap', unit: 'turns' },
  { kind: 'disk', key: 'disk', label: 'Disk cap (GiB)', unit: 'GiB' },
]

const NO_CAPS: CapsValues = { money: 0, minutes: 0, turns: 0, disk: 0 }

function kindOf(kind: string): CapKind | null {
  return KINDS.find(k => k.kind === kind)?.kind ?? null
}

let fieldSerial = 0

const LIMIT_NEXT: Record<string, string[]> = { wake: ['wake-cancel'], 'wake-cancel': ['wake'] }

function amount(kind: CapKind, v: number): string {
  switch (kind) {
    case 'money':
      return `$${v.toFixed(2)}`
    case 'time':
      return `${Math.floor(v * 10) / 10} min`
    case 'turns':
      return `${v} turn${v === 1 ? '' : 's'}`
    case 'disk':
      return `${v} GiB`
  }
}

function usedAmount(kind: CapKind, v: number): string {
  switch (kind) {
    case 'money':
    case 'turns':
      return amount(kind, v)
    case 'disk':
      return `${Math.floor(v * 10) / 10} GiB`
    case 'time': {
      if (v < 1) return 'less than a minute'
      let min = Math.floor(v)
      let sec = Math.round((v - min) * 60)
      if (sec >= 60) {
        min += 1
        sec = 0
      }
      return `${min} min${sec ? ` ${sec} s` : ''}`
    }
  }
}

function usedOf(kind: CapKind, s: SessionLimits): number {
  if (!s.used) return 0
  switch (kind) {
    case 'money':
      return s.used.costUsd
    case 'time':
      return s.used.minutes
    case 'turns':
      return s.used.turns
    case 'disk':
      return s.used.diskBytes / 2 ** 30
  }
}

function capOf(kind: CapKind, c: CapsValues): number {
  switch (kind) {
    case 'money':
      return c.money
    case 'time':
      return c.minutes
    case 'turns':
      return c.turns
    case 'disk':
      return c.disk
  }
}

function capName(kind: CapKind): string {
  switch (kind) {
    case 'money':
      return 'spending cap'
    case 'time':
      return 'time cap'
    case 'turns':
      return 'turn cap'
    case 'disk':
      return 'disk cap'
  }
}

function savedCaps(project: string): CapsValues | null {
  return limitsNow().projects.get(project) ?? null
}

function fillKey(project: string): string {
  const c = savedCaps(project)
  return JSON.stringify([project, c ? [c.money, c.minutes, c.turns, c.disk] : null])
}

class CapsInputs {
  readonly box = fieldRow([], 'caps-fields field-row')
  readonly inputs = new Map<keyof CapsValues, HTMLInputElement>()
  #key = ''
  #project = ''
  #raw = ''

  constructor(label: string) {
    this.box.setAttribute('role', 'group')
    this.box.setAttribute('aria-label', label)
    for (const k of KINDS) {
      const input = textbox({ type: 'number', inputMode: k.key === 'money' ? 'decimal' : 'numeric', placeholder: `${k.unit}: off`, name: k.key })
      input.id = `caps-${k.key}-${++fieldSerial}`
      input.title = `${k.label}; empty or 0 is off`
      input.addEventListener('input', () => labelError(input, ''))
      this.inputs.set(k.key, input)
      this.box.append(field(k.label, input))
    }
  }

  set(c: CapsValues | null) {
    for (const k of KINDS) {
      const v = c ? c[k.key] : 0
      this.inputs.get(k.key)!.value = v ? String(v) : ''
    }
  }

  fill(project: string, force = false) {
    const key = fillKey(project)
    if (key === this.#key && !force) return
    const edited = !force && project === this.#project && this.#rawNow() !== this.#raw
    this.#key = key
    this.#project = project
    if (edited) return
    this.set(savedCaps(project))
    this.#raw = this.#rawNow()
  }

  #rawNow(): string {
    return JSON.stringify([...this.inputs.values()].map(i => i.value))
  }

  value(): CapsValues {
    const out: CapsValues = { ...NO_CAPS }
    for (const k of KINDS) {
      const raw = this.inputs.get(k.key)!.value.trim()
      if (raw) out[k.key] = Number(raw)
    }
    return out
  }

  mark(name: string, message = ''): HTMLInputElement | null {
    let bad: HTMLInputElement | null = null
    for (const [key, input] of this.inputs) {
      const hit = !!message && key === name
      labelError(input, hit ? message : '')
      if (hit) bad = input
    }
    return bad
  }
}

export function capsSet(c: CapsValues | null): boolean {
  return !!c && (c.money > 0 || c.minutes > 0 || c.turns > 0 || c.disk > 0)
}

class SfProjectCaps extends HTMLElement {
  #inputs = new CapsInputs('Caps for new sessions in this project')
  #project = ''
  #said = statusLine()
  #save = button('Save', 'primary small', undefined, 'submit')
  #saving = false
  #built = false
  #stop = () => {}

  connectedCallback() {
    if (!this.#built) {
      this.#built = true
      const head = pageHead({ title: 'Caps', level: 'h2', headingClass: 'label', intro: 'The caps each new session in this project starts with.', docs: { doc: 'caps', key: 'caps-docs' } })
      this.replaceChildren(...head, form({ children: [this.#inputs.box, actions([this.#save])], onSubmit: () => void this.#store() }), this.#said)
    }
    this.#stop()
    this.#stop = watchLimits(this)
    this.paint()
  }

  disconnectedCallback() {
    this.#stop()
  }

  set project(name: string) {
    this.#project = name
    this.paint()
  }

  paint() {
    if (!this.#saving && !this.contains(document.activeElement)) this.#inputs.fill(this.#project)
  }

  async #store() {
    if (this.#saving) return
    const v = this.#inputs.value()
    this.#inputs.mark('')
    this.#saving = true
    const { problem, field: name, said, tone } = await whileBusy(this.#save, () => limitsAnswer({ op: 'project', name: this.#project, ...v }))
    this.#saving = false
    if (!problem) this.#inputs.fill(this.#project, true)
    const bad = this.#inputs.mark(name, problem)
    say(this.#said, bad ? '' : problem, 'warn')
    if (!problem) toast({ id: `project-caps:${this.#project}`, title: said, tone })
    bad?.focus()
  }
}

class SfSessionLimits extends HTMLElement {
  #stop = () => {}
  #said = statusLine('small warn status-slot')
  #raise = new Map<string, { input: HTMLInputElement; box: HTMLDivElement }>()
  #edit = new CapsInputs('Caps for this session')
  #editing = false
  #busy = false
  #busyKey = ''
  #held = new WeakSet<HTMLButtonElement>()
  #dimmed = new WeakSet<HTMLButtonElement>()
  #editSave = button('Save caps', 'primary small', undefined, 'submit', 'caps-save')

  connectedCallback() {
    this.setAttribute('role', 'region')
    this.setAttribute('aria-label', 'Caps and limits for this session')
    this.#stop = watchLimits(this)
    this.paint()
  }

  disconnectedCallback() {
    this.#stop()
  }

  paint() {
    if (this.contains(document.activeElement) && document.activeElement instanceof HTMLInputElement) {
      if (this.#said.parentNode !== this) this.append(this.#said)
      return this.#hold()
    }
    const focused = focusedKey(this)
    const sid = limitsSession()
    const v = limitsNow()
    this.#prune(v.sessions)
    const s = sid ? v.sessions.get(sid) : undefined
    const parts: Node[] = []
    if (s?.reached) parts.push(this.#reached(s))
    if (s?.stuck) parts.push(this.#stuck(sid, s))
    if (s?.caps && capsSet(s.caps)) parts.push(this.#caps(s))
    if (s?.signInUntil) parts.push(el('p', 'notice warn small', `This agent's claude.ai sign-in ends between ${day(s.signInFrom)} and ${day(s.signInUntil)}. Renewing it doesn't move those dates: sign in again before then (Reviewers’ sign-in in the command palette).`))
    let pausedShown = false
    for (const p of v.paused) {
      if (p.sid !== sid) continue
      const w = meterFor(p.side, sid)?.windows.find(x => x.kind === p.kind)
      if (!w) continue
      const box = el('div', 'notice warn small')
      box.append(el('p', '', `At ${when(p.at)} the engine paused an automatic ${p.what === 'findings' ? 'Send findings' : 'review'} of this session: ${visible(w.name)} is ${Math.round(w.utilization * 100)}% used. Press Review to run one yourself, or let automatic work go on until the limit resets.`), windowLine(w), button('Keep automatic work running', 'ghost small', () => void this.#call(continueBody(p.side, sid, p.kind), 'continue'), 'button', 'continue'))
      parts.push(box)
      pausedShown = true
      break
    }
    const own = sid ? v.meter.find(m => m.sid === sid) : undefined
    const near = pausedShown ? undefined : own?.windows.find(w => w.near)
    if (own && near) {
      const box = el('div', 'notice warn small')
      box.append(el('p', '', `${visible(near.name)} is ${Math.round(near.utilization * 100)}% used, so the engine starts no automatic review and no automatic Send findings for this session. Your own messages and the Review button still work.`), windowLine(near), button('Keep them running', 'ghost small', () => void this.#call(continueBody(own.side, sid, near.kind), 'continue'), 'button', 'continue'))
      parts.push(box)
    }
    if (s?.wakeAt) {
      const box = el('div', 'notice small')
      box.append(el('p', '', `The engine wakes this session at ${when(s.wakeAt)}, a minute after its usage limit resets.`), button('Cancel', 'ghost small', () => void this.#call({ op: 'wake', sid, on: false }, 'wake-cancel'), 'button', 'wake-cancel'))
      parts.push(box)
    } else if (s?.wakeOffer) {
      const box = el('div', 'notice warn small')
      box.append(el('p', '', `This session's agent used up a usage limit, which resets ${when(s.wakeOffer)}.`), button('Wake at reset', 'small', () => void this.#call({ op: 'wake', sid, on: true }, 'wake'), 'button', 'wake'))
      parts.push(box)
    }
    if (sid && !s?.reached) parts.push(this.#editor(s))
    parts.push(this.#said)
    this.hidden = !sid
    replaceKeeping(this, parts)
    this.#hold()
    refocus(this, focused, key => [...(LIMIT_NEXT[key] ?? []), 'caps-edit'])
  }

  #hold() {
    for (const b of this.querySelectorAll('button')) {
      const held = this.#busy && !!b.dataset.key && b.dataset.key === this.#busyKey
      if (this.#busy && !held) {
        if (!this.#dimmed.has(b) && b.getAttribute('aria-disabled') !== 'true') {
          b.setAttribute('aria-disabled', 'true')
          this.#dimmed.add(b)
        }
      } else if (this.#dimmed.delete(b)) b.removeAttribute('aria-disabled')
      if (held) {
        busy(b, true)
        this.#held.add(b)
      } else if (this.#held.delete(b)) busy(b, false)
    }
  }

  #prune(sessions: Map<string, SessionLimits>) {
    const live = new Set<string>()
    for (const s of sessions.values()) if (s.reached) live.add(s.sid + s.reached)
    for (const key of this.#raise.keys()) if (!live.has(key)) this.#raise.delete(key)
  }

  #reached(s: SessionLimits): HTMLElement {
    const box = el('div', 'notice alert caps-reached')
    const stop = button('Stop', 'small danger', () => void this.#call({ op: 'stop', sid: s.sid }, 'stop'), 'button', 'stop')
    const kind = kindOf(s.reached)
    const k = KINDS.find(x => x.kind === kind)
    if (!kind || !k) {
      box.append(el('p', '', 'This session reached a cap. The engine interrupted its turn, holds its automatic reviews and Send findings, and passes on no message until you answer.'), stop)
      return box
    }
    const cap = s.caps ? capOf(kind, s.caps) : 0
    const used = usedOf(kind, s)
    const at = s.reachedAt ? ` at ${when(s.reachedAt)}` : ''
    if (kind === 'disk') {
      const least = s.used?.diskMore ? 'at least ' : ''
      const measured = s.used?.diskAt ? `the engine measured ${least}${usedAmount(kind, used)} in the session’s folder at ${when(s.used.diskAt)}` : 'the engine hasn’t measured the session’s folder since it started'
      box.append(el('p', '', `This session reached its disk cap of ${amount(kind, cap)}${at}: ${measured}. The engine interrupted its turn, holds its automatic reviews and Send findings, and passes on no message. Delete files from the session’s folder, for example with docker exec, then press Check again, or press Raise the cap.`))
      if (s.used?.diskMore) box.append(el('p', 'small', 'Part of the folder couldn’t be counted, so Check again lets the session go on only once the whole folder can be counted and is under the cap.'))
    } else {
      box.append(el('p', '', `This session reached its ${capName(kind)} of ${amount(kind, cap)}${at}: ${usedAmount(kind, used)} used. The engine interrupted its turn, holds its automatic reviews and Send findings, and passes on no message until you answer.`))
    }
    let raise = this.#raise.get(s.sid + s.reached)
    if (!raise) {
      const input = textbox({ type: 'number', inputMode: kind === 'money' ? 'decimal' : 'numeric', value: s.raise ? String(s.raise.suggested) : '' })
      input.addEventListener('input', () => labelError(input, ''))
      raise = { input, box: field(`New ${capName(kind)}`, input) }
      this.#raise.set(s.sid + s.reached, raise)
    }
    const raised = raise.input
    const buttons = [button('Raise the cap', 'primary small', undefined, 'submit', 'raise')]
    if (kind === 'disk') buttons.push(button('Check again', 'small', () => void this.#call({ op: 'measure', sid: s.sid }, 'measure'), 'button', 'measure'))
    buttons.push(stop)
    box.append(form({
      inline: true,
      cls: 'field-row',
      children: [raise.box, actions(buttons)],
      onSubmit: () => void this.#call({ op: 'raise', sid: s.sid, kind, value: Number(raised.value) }, 'raise', a => (a.field === 'value' ? raised : null)),
    }))
    return box
  }

  #stuck(sid: string, s: SessionLimits): HTMLElement {
    const st = s.stuck!
    const box = el('div', 'notice warn small')
    const what = st.failing ? `the same failing command ran ${st.count} times` : `${visible(st.tool)} repeated ${st.count} times`
    box.append(el('p', '', `Looks stuck: ${what} in its last few calls.`))
    if (st.detail) box.append(el('code', 'caps-detail', visible(st.detail)))
    const interrupt = button('Interrupt', 'small', () => {
      if (!this.#busy) void whileBusy(interrupt, () => request('/api/interrupt', { sid }))
    }, 'button', 'interrupt')
    box.append(interrupt)
    return box
  }

  #caps(s: SessionLimits): HTMLElement {
    const box = el('p', 'small caps-line')
    const bits: string[] = []
    let tone = 'muted'
    for (const k of KINDS) {
      const cap = capOf(k.kind, s.caps!)
      if (!cap) continue
      const used = usedOf(k.kind, s)
      const level = s.levels[k.kind] ?? ''
      if (level) tone = 'warn'
      const warn = level === 'warn90' ? ' (over 90%)' : level === 'warn75' ? ' (over 75%)' : ''
      if (k.kind === 'disk') {
        if (!s.used?.diskAt) bits.push(`disk not measured yet, cap ${amount(k.kind, cap)}`)
        else bits.push(`${s.used.diskMore ? 'at least ' : ''}${usedAmount(k.kind, used)} of ${amount(k.kind, cap)} on disk, measured ${when(s.used.diskAt)}${warn}`)
      } else bits.push(`${usedAmount(k.kind, used)} of ${amount(k.kind, cap)}${warn}`)
    }
    box.className = `small caps-line ${tone}`
    box.textContent = `Caps${s.own ? '' : ' from the project'}: ${bits.join(' · ')}`
    return box
  }

  #editor(s: SessionLimits | undefined): HTMLElement {
    const d = el('details', 'caps-edit small')
    d.open = this.#editing
    d.addEventListener('toggle', () => {
      this.#editing = d.open
      if (d.open) this.#edit.set(s?.caps ?? null)
    })
    const summary = el('summary', '', s?.caps && capsSet(s.caps) ? 'Change caps' : 'Set caps')
    summary.dataset.key = 'caps-edit'
    d.append(summary, form({ children: [this.#edit.box, actions([this.#editSave])], onSubmit: () => void this.#saveEdit() }))
    return d
  }

  async #saveEdit() {
    const sid = limitsSession()
    if (this.#busy || !sid) return
    this.#edit.mark('')
    const { problem } = await this.#call({ op: 'session', sid, ...this.#edit.value() }, 'caps-save', a => this.#edit.mark(a.field, a.problem))
    if (!problem) {
      this.#editing = false
      this.paint()
    }
  }

  async #call(body: Record<string, unknown>, key: string, mark: (a: CapsAnswer) => HTMLInputElement | null = () => null): Promise<CapsAnswer> {
    if (this.#busy) return { problem: '', field: '', error: '', said: '', tone: 'ok' }
    this.#busy = true
    this.#busyKey = key
    this.#said.textContent = ''
    this.paint()
    const answer = await limitsAnswer(body)
    const bad = answer.problem ? mark(answer) : null
    if (bad) labelError(bad, answer.problem)
    say(this.#said, bad ? '' : answer.problem, 'warn')
    if (!answer.problem) toast({ id: `limits:${key}`, title: answer.said, tone: answer.tone })
    this.#busy = false
    busy(this.#editSave, false)
    this.paint()
    bad?.focus()
    return answer
  }
}

customElements.define('sf-project-caps', SfProjectCaps)
customElements.define('sf-session-limits', SfSessionLimits)

declare global {
  interface HTMLElementTagNameMap {
    'sf-project-caps': SfProjectCaps
    'sf-session-limits': SfSessionLimits
  }
}
