import { api } from '../api.ts'
import { button, el, visible } from '../lib/dom.ts'

export interface CapsValues {
  money: number
  minutes: number
  turns: number
}

export interface RateWindowView {
  kind: string
  name: string
  utilization: number
  resetsAt: number
  status: string
  near: boolean
  continued: boolean
}

export interface MeterSide {
  side: string
  label: string
  plan: string
  windows: RateWindowView[]
}

export interface Stuck {
  tool: string
  detail: string
  count: number
  failing: boolean
  at: number
}

export interface SessionLimits {
  sid: string
  caps: CapsValues | null
  own: boolean
  used: { costUsd: number; minutes: number; turns: number } | null
  levels: Record<string, string>
  reached: string
  reachedAt: number
  stuck: Stuck | null
  wakeAt: number
  wakeOffer: number
  signInFrom: number
  signInUntil: number
}

export interface ProjectCaps extends CapsValues {
  name: string
}

export interface Expiry {
  what: 'sign-in' | 'token'
  label: string
  host: string
  from: number
  until: number
}

export interface Pause {
  sid: string
  side: string
  kind: string
  what: string
  at: number
}

export interface LimitsView {
  meter: MeterSide[]
  sessions: Map<string, SessionLimits>
  projects: Map<string, CapsValues>
  expiry: Expiry[]
  paused: Pause[]
  unread: boolean
}

const SID = /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/
const EMPTY: LimitsView = { meter: [], sessions: new Map(), projects: new Map(), expiry: [], paused: [], unread: false }

let view: LimitsView = EMPTY
let current = ''
const shown = new Set<{ paint(): void }>()

const rec = (x: unknown): Record<string, unknown> => (x && typeof x === 'object' && !Array.isArray(x) ? (x as Record<string, unknown>) : {})
const num = (x: unknown, max = 1e9): number => (typeof x === 'number' && Number.isFinite(x) && x >= 0 ? Math.min(x, max) : 0)
const text = (x: unknown, max = 200): string => (typeof x === 'string' ? x.slice(0, max) : '')
const list = (x: unknown, max = 1000): unknown[] => (Array.isArray(x) ? x.slice(0, max) : [])

function capsOf(x: unknown): CapsValues {
  const c = rec(x)
  return { money: num(c.money, 1e5), minutes: num(c.minutes, 43200), turns: num(c.turns, 10000) }
}

export function limitsOf(raw: unknown): LimitsView | null {
  const r = rec(raw)
  if (r.type !== 'limits' && !Array.isArray(r.meter)) return null
  const meter = list(r.meter, 8).map((m): MeterSide => {
    const s = rec(m)
    return {
      side: text(s.side, 40), label: text(s.label), plan: text(s.plan, 40),
      windows: list(s.windows, 8).map((w): RateWindowView => {
        const x = rec(w)
        return { kind: text(x.kind, 60), name: text(x.name), utilization: num(x.utilization, 10), resetsAt: num(x.resetsAt, 1e15), status: text(x.status, 40), near: x.near === true, continued: x.continued === true }
      }),
    }
  })
  const sessions = new Map<string, SessionLimits>()
  for (const item of list(r.sessions)) {
    const s = rec(item)
    const sid = text(s.sid, 40)
    if (!SID.test(sid)) continue
    const used = s.used ? rec(s.used) : null
    const stuck = s.stuck ? rec(s.stuck) : null
    const levels = rec(s.levels)
    sessions.set(sid, {
      sid, caps: s.caps ? capsOf(s.caps) : null, own: s.own === true,
      used: used ? { costUsd: num(used.costUsd), minutes: num(used.minutes), turns: num(used.turns) } : null,
      levels: { money: text(levels.money, 20), time: text(levels.time, 20), turns: text(levels.turns, 20) },
      reached: ['money', 'time', 'turns'].includes(text(s.reached, 20)) ? text(s.reached, 20) : '', reachedAt: num(s.reachedAt, 1e15),
      stuck: stuck ? { tool: text(stuck.tool, 80), detail: text(stuck.detail, 200), count: num(stuck.count, 100), failing: stuck.failing === true, at: num(stuck.at, 1e15) } : null,
      wakeAt: num(s.wakeAt, 1e15), wakeOffer: num(s.wakeOffer, 1e15), signInFrom: num(s.signInFrom, 1e15), signInUntil: num(s.signInUntil, 1e15),
    })
  }
  const projects = new Map<string, CapsValues>()
  for (const item of list(r.projects)) {
    const p = rec(item)
    const name = text(p.name, 400)
    if (name) projects.set(name, capsOf(p))
  }
  const expiry = list(r.expiry, 220).flatMap((item): Expiry[] => {
    const e = rec(item)
    const what = e.what === 'sign-in' || e.what === 'token' ? e.what : null
    return what ? [{ what, label: text(e.label), host: text(e.host, 260), from: num(e.from, 1e15), until: num(e.until, 1e15) }] : []
  })
  const paused = list(r.paused, 20).flatMap((item): Pause[] => {
    const p = rec(item)
    const sid = text(p.sid, 40)
    return SID.test(sid) ? [{ sid, side: text(p.side, 40), kind: text(p.kind, 60), what: text(p.what, 20), at: num(p.at, 1e15) }] : []
  })
  return { meter, sessions, projects, expiry, paused, unread: r.unread === true }
}

export function setLimits(raw: unknown): void {
  const next = limitsOf(raw)
  if (!next) return
  view = next
  paintAll()
}

export function limitsNow(): LimitsView {
  return view
}

export function limitsSession(): string {
  return current
}

export function setLimitsSession(sid: string): void {
  if (sid === current) return
  current = SID.test(sid) ? sid : ''
  paintAll()
}

export function watchLimits(node: { paint(): void }): () => void {
  shown.add(node)
  return () => shown.delete(node)
}

function paintAll() {
  for (const node of shown) node.paint()
}

export async function limitsCall(body: Record<string, unknown>): Promise<string> {
  try {
    const res = await api('/api/limits', body)
    const data: unknown = await res.json().catch(() => null)
    if (res.ok) {
      setLimits(data)
      return ''
    }
    const message = rec(data).message
    return typeof message === 'string' && message ? visible(message) : "The engine didn't take that. Try again."
  } catch {
    return "The engine didn't answer. Try again once the page is connected."
  }
}

export function loadLimits(): Promise<string> {
  return limitsCall({ op: 'read' })
}

export function when(ms: number): string {
  if (!ms) return ''
  const at = new Date(ms)
  const today = at.toDateString() === new Date().toDateString()
  return at.toLocaleString(undefined, today ? { hour: 'numeric', minute: '2-digit' } : { weekday: 'short', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })
}

export function day(ms: number): string {
  return ms ? new Date(ms).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' }) : ''
}

export function windowLine(w: RateWindowView): HTMLElement {
  const row = el('div', 'usage-window')
  const share = Math.round(w.utilization * 100)
  const bar = el('meter')
  bar.min = 0
  bar.max = 100
  bar.low = 75
  bar.high = 90
  bar.optimum = 0
  bar.value = Math.min(share, 100)
  bar.setAttribute('aria-label', visible(w.name))
  const label = el('span', 'usage-name', visible(w.name))
  const value = el('span', w.near ? 'warn' : 'muted', `${share}% used${w.resetsAt ? ` · resets ${when(w.resetsAt)}` : ''}${w.status === 'rejected' ? ' · used up' : ''}`)
  row.append(label, bar, value)
  return row
}

export function expiryText(e: Expiry): string {
  if (e.what === 'token') return `The token saved for ${visible(e.host)} expires ${when(e.until)}. Save a new one under Projects, Tokens, before then, or the engine can't push or open pull requests there.`
  return `${visible(e.label)} has a claude.ai sign-in that ends between ${day(e.from)} and ${day(e.until)}. Renewing it doesn't move those dates: sign in again before then (Finish setting up on the Welcome page).`
}

class SfUsage extends HTMLElement {
  #stop = () => {}
  #said = el('p', 'small warn')

  connectedCallback() {
    this.setAttribute('role', 'region')
    this.setAttribute('aria-label', 'Usage limits')
    this.#said.setAttribute('role', 'status')
    this.#stop = watchLimits(this)
    this.paint()
  }

  disconnectedCallback() {
    this.#stop()
  }

  paint() {
    const parts: Node[] = []
    for (const m of view.meter) {
      const box = el('section', 'usage-side')
      const head = el('h3', 'label', visible(m.label))
      if (m.plan) head.append(el('span', 'muted small', ` · plan ${visible(m.plan)}`))
      box.append(head)
      for (const w of m.windows) {
        box.append(windowLine(w))
        if (w.near) {
          const ask = el('div', 'usage-ask small')
          ask.append(el('p', 'warn', 'Automatic reviews and automatic Send findings are paused near this limit. Your own messages and the Review button still work.'), button('Keep them running', 'ghost small', () => void this.#continue(m.side, w.kind)))
          box.append(ask)
        } else if (w.continued) box.append(el('p', 'small muted', 'You chose to keep automatic work running until this limit resets.'))
      }
      parts.push(box)
    }
    for (const e of view.expiry) if (e.what === 'sign-in') parts.push(el('p', 'notice warn small', expiryText(e)))
    if (view.unread) parts.push(el('p', 'notice warn small', "The engine couldn't read caps.json in its state folder, so caps saved earlier aren't in effect and changes aren't saved. Fix or remove the file, then restart the engine."))
    if (this.#said.textContent) parts.push(this.#said)
    if (parts.length && view.meter.length) parts.unshift(el('h2', 'label', 'Usage limits'))
    this.hidden = !parts.length
    this.replaceChildren(...parts)
  }

  async #continue(side: string, kind: string) {
    this.#said.textContent = await limitsCall({ op: 'continue', side, kind })
    this.paint()
  }
}

class SfTokenExpiry extends HTMLElement {
  #url = ''
  #stop = () => {}

  set url(value: string) {
    this.#url = value
    this.paint()
  }

  connectedCallback() {
    this.#stop = watchLimits(this)
    this.paint()
  }

  disconnectedCallback() {
    this.#stop()
  }

  paint() {
    let host = ''
    try {
      host = this.#url ? new URL(this.#url).host.toLowerCase() : ''
    } catch {}
    const e = view.expiry.find(x => x.what === 'token' && x.host === host)
    this.hidden = !e
    this.replaceChildren(...(e ? [el('p', 'notice warn small', expiryText(e))] : []))
  }
}

customElements.define('sf-usage', SfUsage)
customElements.define('sf-token-expiry', SfTokenExpiry)

declare global {
  interface HTMLElementTagNameMap {
    'sf-usage': SfUsage
    'sf-token-expiry': SfTokenExpiry
  }
}
