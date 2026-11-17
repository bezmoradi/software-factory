import { api } from '../api.ts'
import { actionButton, busy, button, el, visible, whileBusy } from '../lib/dom.ts'
import { continueBody, day, limitsCall, limitsNow, limitsSession, meterFor, watchLimits, when, windowLine } from './usage.ts'
import type { CapsValues, SessionLimits } from './usage.ts'

const KINDS: { kind: 'money' | 'time' | 'turns'; key: keyof CapsValues; label: string; unit: string; step: string; max: number }[] = [
  { kind: 'money', key: 'money', label: 'Spending cap (USD)', unit: '$', step: '0.01', max: 100000 },
  { kind: 'time', key: 'minutes', label: 'Time cap (minutes)', unit: 'min', step: '1', max: 43200 },
  { kind: 'turns', key: 'turns', label: 'Turn cap', unit: 'turns', step: '1', max: 10000 },
]

let fieldSerial = 0

function amount(kind: string, v: number): string {
  if (kind === 'money') return `$${v.toFixed(2)}`
  if (kind === 'time') return `${Math.floor(v * 10) / 10} min`
  return `${v} turn${v === 1 ? '' : 's'}`
}

function usedAmount(kind: string, v: number): string {
  if (kind !== 'time') return amount(kind, v)
  if (v < 1) return 'less than a minute'
  let min = Math.floor(v)
  let sec = Math.round((v - min) * 60)
  if (sec >= 60) {
    min += 1
    sec = 0
  }
  return `${min} min${sec ? ` ${sec} s` : ''}`
}

function usedOf(kind: string, s: SessionLimits): number {
  if (!s.used) return 0
  return kind === 'money' ? s.used.costUsd : kind === 'time' ? s.used.minutes : s.used.turns
}

function capOf(kind: string, c: CapsValues): number {
  return kind === 'money' ? c.money : kind === 'time' ? c.minutes : c.turns
}

function capName(kind: string): string {
  return kind === 'money' ? 'spending cap' : kind === 'time' ? 'time cap' : 'turn cap'
}

class CapsInputs {
  readonly box = el('div', 'caps-fields field-row')
  readonly inputs = new Map<keyof CapsValues, HTMLInputElement>()

  constructor(label: string) {
    this.box.setAttribute('role', 'group')
    this.box.setAttribute('aria-label', label)
    for (const k of KINDS) {
      const input = el('input', 'textbox')
      input.type = 'number'
      input.min = '0'
      input.max = String(k.max)
      input.step = k.step
      input.placeholder = `${k.unit}: off`
      input.id = `caps-${k.key}-${++fieldSerial}`
      input.setAttribute('aria-label', k.label)
      input.title = `${k.label}; empty or 0 is off`
      this.inputs.set(k.key, input)
      this.box.append(input)
    }
  }

  set(c: CapsValues | null) {
    for (const k of KINDS) {
      const v = c ? c[k.key] : 0
      this.inputs.get(k.key)!.value = v ? String(v) : ''
    }
  }

  value(): CapsValues | null {
    const out: CapsValues = { money: 0, minutes: 0, turns: 0 }
    for (const k of KINDS) {
      const raw = this.inputs.get(k.key)!.value.trim()
      if (!raw) continue
      const n = Number(raw)
      if (!Number.isFinite(n) || n < 0 || n > k.max || (k.key !== 'money' && !Number.isInteger(n))) return null
      out[k.key] = k.key === 'money' ? Math.round(n * 100) / 100 : n
    }
    return out
  }
}

export function capsSet(c: CapsValues | null): boolean {
  return !!c && (c.money > 0 || c.minutes > 0 || c.turns > 0)
}

export function saveSessionCaps(sid: string, caps: CapsValues): Promise<string> {
  return limitsCall({ op: 'session', sid, ...caps })
}

class SfCapsFields extends HTMLElement {
  #inputs = new CapsInputs('Caps for this session')
  #project = ''

  #note = el('p', 'small muted')

  connectedCallback() {
    this.#note.setAttribute('role', 'status')
    this.#invalid(false)
    this.replaceChildren(this.#inputs.box, this.#note)
    this.#fill()
  }

  #invalid(on: boolean) {
    this.#note.className = on ? 'small warn' : 'small muted'
    this.#note.textContent = on
      ? 'Each cap is a number from 0 up to $100,000, 43,200 minutes or 10,000 turns; minutes and turns are whole numbers.'
      : 'Caps for this session: money from the agent’s own reported cost, time by the engine’s clock while it runs, and turns. At a cap the engine interrupts the turn and asks you. Empty is off; the project’s caps fill these in.'
  }

  checked(): CapsValues | null | 'invalid' {
    const v = this.value
    this.#invalid(v === 'invalid')
    return v
  }

  set project(name: string) {
    this.#project = name
    this.#fill()
  }

  #fill() {
    this.#inputs.set(limitsNow().projects.get(this.#project) ?? null)
  }

  get value(): CapsValues | null | 'invalid' {
    const v = this.#inputs.value()
    if (v === null) return 'invalid'
    const p = limitsNow().projects.get(this.#project) ?? { money: 0, minutes: 0, turns: 0 }
    return v.money === p.money && v.minutes === p.minutes && v.turns === p.turns ? null : v
  }
}

class SfProjectCaps extends HTMLElement {
  #inputs = new CapsInputs('Caps for new sessions in this project')
  #project = ''
  #said = el('p', 'small')
  #save = button('Save caps', 'small', () => void this.#store())
  #saving = false
  #stop = () => {}

  connectedCallback() {
    this.#said.setAttribute('role', 'status')
    const note = el('p', 'small muted', 'Each new session in this project gets these caps unless its start form sets its own. Empty or 0 is off. Money counts the agent’s own reported cost, so it can only stop work, never allow more; time is the engine’s clock while the session runs.')
    this.#inputs.box.append(this.#save)
    this.replaceChildren(el('h3', 'label', 'Caps'), note, this.#inputs.box, this.#said)
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
    if (!this.#saving && !this.contains(document.activeElement)) this.#inputs.set(limitsNow().projects.get(this.#project) ?? null)
  }

  async #store() {
    const v = this.#inputs.value()
    if (!v) {
      this.#said.className = 'small warn'
      this.#said.textContent = 'Each cap is a number from 0 up to $100,000, 43,200 minutes or 10,000 turns; minutes and turns are whole numbers.'
      return
    }
    this.#saving = true
    const problem = await whileBusy(this.#save, () => limitsCall({ op: 'project', name: this.#project, ...v }))
    this.#saving = false
    this.#said.className = problem ? 'small warn' : 'small muted'
    this.#said.textContent = problem || 'Saved.'
  }
}

class SfSessionLimits extends HTMLElement {
  #stop = () => {}
  #said = el('p', 'small warn')
  #raise = new Map<string, HTMLInputElement>()
  #edit = new CapsInputs('Caps for this session')
  #editing = false
  #busy = false
  #busyKey = ''
  #editSave = button('Save caps', 'small', () => void this.#saveEdit(), 'button', 'caps-save')

  connectedCallback() {
    this.setAttribute('role', 'region')
    this.setAttribute('aria-label', 'Caps and limits for this session')
    this.#said.setAttribute('role', 'status')
    if (!this.#edit.box.querySelector('button')) this.#edit.box.append(this.#editSave)
    this.#stop = watchLimits(this)
    this.paint()
  }

  disconnectedCallback() {
    this.#stop()
  }

  paint() {
    if (this.contains(document.activeElement) && document.activeElement instanceof HTMLInputElement) return
    const sid = limitsSession()
    const v = limitsNow()
    const s = sid ? v.sessions.get(sid) : undefined
    const parts: Node[] = []
    if (s?.reached) parts.push(this.#reached(s))
    if (s?.stuck) parts.push(this.#stuck(sid, s))
    if (s?.caps && capsSet(s.caps)) parts.push(this.#caps(s))
    if (s?.signInUntil) parts.push(el('p', 'notice warn small', `This agent's claude.ai sign-in ends between ${day(s.signInFrom)} and ${day(s.signInUntil)}. Renewing it doesn't move those dates: sign in again before then (Finish setting up on the Welcome page).`))
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
    if (this.#said.textContent) parts.push(this.#said)
    this.hidden = !sid
    this.replaceChildren(...parts)
    for (const b of this.querySelectorAll('button')) {
      const held = this.#busy && !!b.dataset.key && b.dataset.key === this.#busyKey
      b.disabled = this.#busy && !held
      if (held) busy(b, true)
    }
  }

  #reached(s: SessionLimits): HTMLElement {
    const box = el('div', 'notice alert caps-reached')
    const cap = s.caps ? capOf(s.reached, s.caps) : 0
    box.append(el('p', '', `This session reached its ${capName(s.reached)} of ${amount(s.reached, cap)}${s.reachedAt ? ` at ${when(s.reachedAt)}` : ''}: ${usedAmount(s.reached, usedOf(s.reached, s))} used. The engine interrupted its turn, holds its automatic reviews and Send findings, and passes on no message until you answer.`))
    const k = KINDS.find(x => x.kind === s.reached)!
    let input = this.#raise.get(s.sid + s.reached)
    if (!input) {
      input = el('input', 'textbox')
      input.type = 'number'
      input.step = k.step
      input.max = String(k.max)
      input.setAttribute('aria-label', `New ${capName(s.reached)}`)
      input.value = String(k.kind === 'money' ? Math.round((cap * 2 || 1) * 100) / 100 : Math.max(cap * 2, Math.ceil(usedOf(s.reached, s)) + 1))
      this.#raise.set(s.sid + s.reached, input)
    }
    input.min = String(usedOf(s.reached, s))
    const row = el('div', 'field-row')
    row.append(input, button('Raise the cap', 'primary small', () => void this.#call({ op: 'raise', sid: s.sid, kind: s.reached, value: Number(input!.value) }, 'raise'), 'button', 'raise'), button('Stop', 'small danger', () => void this.#call({ op: 'stop', sid: s.sid }, 'stop'), 'button', 'stop'))
    box.append(row)
    return box
  }

  #stuck(sid: string, s: SessionLimits): HTMLElement {
    const st = s.stuck!
    const box = el('div', 'notice warn small')
    const what = st.failing ? `the same failing command ran ${st.count} times` : `${visible(st.tool)} repeated ${st.count} times`
    box.append(el('p', '', `Looks stuck: ${what} in its last few calls.`))
    if (st.detail) box.append(el('code', 'caps-detail', visible(st.detail)))
    box.append(actionButton('Interrupt', 'small', () => api('/api/interrupt', { sid }).catch(() => null)))
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
      bits.push(`${usedAmount(k.kind, used)} of ${amount(k.kind, cap)}${level === 'warn90' ? ' (over 90%)' : level === 'warn75' ? ' (over 75%)' : ''}`)
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
    d.append(el('summary', '', s?.caps && capsSet(s.caps) ? 'Change caps' : 'Set caps'), this.#edit.box)
    return d
  }

  async #saveEdit() {
    const sid = limitsSession()
    const v = this.#edit.value()
    if (!sid || !v) {
      this.#said.textContent = 'Each cap is a number from 0 up to $100,000, 43,200 minutes or 10,000 turns; minutes and turns are whole numbers.'
      return this.paint()
    }
    this.#editing = false
    await this.#call({ op: 'session', sid, ...v }, 'caps-save')
  }

  async #call(body: Record<string, unknown>, key: string) {
    this.#busy = true
    this.#busyKey = key
    this.#said.textContent = ''
    this.paint()
    this.#said.textContent = await limitsCall(body)
    this.#busy = false
    busy(this.#editSave, false)
    this.paint()
  }
}

customElements.define('sf-caps-fields', SfCapsFields)
customElements.define('sf-project-caps', SfProjectCaps)
customElements.define('sf-session-limits', SfSessionLimits)

declare global {
  interface HTMLElementTagNameMap {
    'sf-caps-fields': SfCapsFields
    'sf-project-caps': SfProjectCaps
    'sf-session-limits': SfSessionLimits
  }
}
