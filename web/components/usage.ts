import { request, savedTone } from '../api.ts'
import { actionButton, el, focusedKey, refocus, replaceKeeping, statusLine, visible } from '../lib/dom.ts'
import { actions } from '../lib/form.ts'

export interface CapsValues {
  money: number
  minutes: number
  turns: number
  disk: number
}

export interface RateWindowView {
  kind: string
  name: string
  short: string
  utilization: number
  resetsAt: number
  status: string
  near: boolean
  continued: boolean
}

export interface MeterSide {
  side: string
  sid: string
  windows: RateWindowView[]
}

export interface AccountWindow {
  kind: string
  name: string
  label: string
  utilization: number
  resetsAt: number
  status: string
  level: string
  near: boolean
  continued: boolean
}

export interface AccountUsage {
  plan: string
  windows: AccountWindow[]
}

interface WindowShown {
  name: string
  utilization: number
  resetsAt: number
  status: string
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
  used: { costUsd: number; minutes: number; turns: number; diskBytes: number; diskMore: boolean; diskAt: number } | null
  levels: Record<string, string>
  reached: string
  reachedAt: number
  raise: { min: number; suggested: number } | null
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
  usage: AccountUsage
  meter: MeterSide[]
  sessions: Map<string, SessionLimits>
  projects: Map<string, CapsValues>
  expiry: Expiry[]
  paused: Pause[]
  unread: boolean
}

const SID = /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/
const EMPTY: LimitsView = { usage: { plan: '', windows: [] }, meter: [], sessions: new Map(), projects: new Map(), expiry: [], paused: [], unread: false }

let view: LimitsView = EMPTY
let current = ''
const shown = new Set<{ paint(): void }>()

const rec = (x: unknown): Record<string, unknown> => (x && typeof x === 'object' && !Array.isArray(x) ? (x as Record<string, unknown>) : {})
const num = (x: unknown, max = 1e9): number => (typeof x === 'number' && Number.isFinite(x) && x >= 0 ? Math.min(x, max) : 0)
const text = (x: unknown, max = 200): string => (typeof x === 'string' ? x.slice(0, max) : '')
const list = (x: unknown, max = 1000): unknown[] => (Array.isArray(x) ? x.slice(0, max) : [])

function capsOf(x: unknown): CapsValues {
  const c = rec(x)
  return { money: num(c.money, 1e5), minutes: num(c.minutes, 43200), turns: num(c.turns, 10000), disk: num(c.disk, 4096) }
}

export function limitsOf(raw: unknown): LimitsView | null {
  const r = rec(raw)
  if (r.type !== 'limits' && !Array.isArray(r.meter)) return null
  const meter = list(r.meter, 1001).map((m): MeterSide => {
    const s = rec(m)
    const sid = text(s.sid, 40)
    return {
      side: text(s.side, 40), sid: SID.test(sid) ? sid : '',
      windows: list(s.windows, 8).map((w): RateWindowView => {
        const x = rec(w)
        return { kind: text(x.kind, 60), name: text(x.name), short: text(x.short), utilization: num(x.utilization, 10), resetsAt: num(x.resetsAt, 1e15), status: text(x.status, 40), near: x.near === true, continued: x.continued === true }
      }),
    }
  })
  const u = rec(r.usage)
  const usage: AccountUsage = {
    plan: text(u.plan, 60),
    windows: list(u.windows, 8).map((w): AccountWindow => {
      const x = rec(w)
      return { kind: text(x.kind, 60), name: text(x.name), label: text(x.label), utilization: num(x.utilization, 10), resetsAt: num(x.resetsAt, 1e15), status: text(x.status, 40), level: text(x.level, 20), near: x.near === true, continued: x.continued === true }
    }),
  }
  const sessions = new Map<string, SessionLimits>()
  for (const item of list(r.sessions)) {
    const s = rec(item)
    const sid = text(s.sid, 40)
    if (!SID.test(sid)) continue
    const used = s.used ? rec(s.used) : null
    const stuck = s.stuck ? rec(s.stuck) : null
    const levels = rec(s.levels)
    const raise = s.raise ? rec(s.raise) : null
    sessions.set(sid, {
      sid, caps: s.caps ? capsOf(s.caps) : null, own: s.own === true,
      used: used ? { costUsd: num(used.costUsd), minutes: num(used.minutes), turns: num(used.turns), diskBytes: num(used.diskBytes, 2 ** 53), diskMore: used.diskMore === true, diskAt: num(used.diskAt, 1e15) } : null,
      levels: { money: text(levels.money, 20), time: text(levels.time, 20), turns: text(levels.turns, 20), disk: text(levels.disk, 20) },
      reached: ['money', 'time', 'turns', 'disk'].includes(text(s.reached, 20)) ? text(s.reached, 20) : '', reachedAt: num(s.reachedAt, 1e15),
      raise: raise ? { min: num(raise.min), suggested: num(raise.suggested) } : null,
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
  return { usage, meter, sessions, projects, expiry, paused, unread: r.unread === true }
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

export function meterFor(side: string, sid: string): MeterSide | undefined {
  return view.meter.find(m => m.side === side && m.sid === (side === 'reviewers' ? '' : sid))
}

export function continueBody(side: string, sid: string, kind: string): Record<string, unknown> {
  return side === 'reviewers' ? { op: 'continue', side, kind } : { op: 'continue', side, sid, kind }
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

export interface LimitsAnswer {
  problem: string
  field: string
  error: string
  said: string
  tone: 'ok' | 'warn'
}

export async function limitsAnswer(body: Record<string, unknown>): Promise<LimitsAnswer> {
  const a = await request('/api/limits', body)
  if (a.ok) {
    setLimits(a.body)
    return { problem: '', field: '', error: '', said: a.message, tone: savedTone(a) }
  }
  return { problem: a.message, field: a.field, error: a.error, said: '', tone: 'ok' }
}

export async function limitsCall(body: Record<string, unknown>): Promise<string> {
  return (await limitsAnswer(body)).problem
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

export function windowLine(w: WindowShown, warn: boolean, name = w.name): HTMLElement {
  const row = el('div', 'usage-window')
  const share = Math.round(w.utilization * 100)
  const bar = el('meter')
  bar.min = 0
  bar.max = 100
  bar.low = 90
  bar.high = 100
  bar.optimum = 0
  bar.value = Math.min(share, 100)
  bar.setAttribute('aria-label', visible(w.name))
  const label = el('span', 'usage-name', visible(name))
  const value = el('span', warn ? 'warn' : 'muted', `${share}% used${w.resetsAt ? ` · resets ${when(w.resetsAt)}` : ''}${w.status === 'rejected' ? ' · used up' : ''}`)
  row.append(label, bar, value)
  return row
}

export function expiryText(e: Expiry): string {
  if (e.what === 'token') return `The token saved for ${visible(e.host)} expires ${when(e.until)}. Save a new one under Projects, Git host tokens, before then, or the engine can't push or open pull requests there.`
  return `${visible(e.label)} has a claude.ai sign-in that ends between ${day(e.from)} and ${day(e.until)}. Renewing it doesn't move those dates: sign in again before then (Reviewers’ sign-in in the command palette).`
}

class SfUsage extends HTMLElement {
  #stop = () => {}
  #said = statusLine('small warn status-slot')

  connectedCallback() {
    this.setAttribute('role', 'region')
    this.setAttribute('aria-label', 'Claude usage limits')
    this.#stop = watchLimits(this)
    this.paint()
  }

  disconnectedCallback() {
    this.#stop()
  }

  paint() {
    const focused = focusedKey(this)
    const parts: Node[] = []
    const u = view.usage
    if (u.windows.length) {
      const head = el('h2', 'label', 'Claude usage limits')
      head.tabIndex = -1
      head.dataset.key = 'usage-head'
      parts.push(head, el('p', 'small muted', `${u.plan ? `${visible(u.plan)} plan. ` : ''}Shared by every session and reviewer signed in with this account.`))
      for (const w of u.windows) {
        parts.push(windowLine(w, w.level === 'warn90', w.label))
        if (w.near) {
          const ask = el('div', 'usage-ask small')
          ask.append(el('p', 'warn', 'Automatic reviews and automatic Send findings are paused near this limit. Your own messages and the Review button still work.'), actions([actionButton('Keep them running', 'ghost small', () => this.#continue('reviewers', w.kind), 'button', `continue:${w.kind}`)]))
          parts.push(ask)
        } else if (w.continued) parts.push(el('p', 'small muted', 'You chose to keep automatic work running until this limit resets.'))
      }
    }
    for (const e of view.expiry) if (e.what === 'sign-in') parts.push(el('p', 'notice warn small', expiryText(e)))
    if (view.unread) parts.push(el('p', 'notice warn small', "The engine couldn't read caps.json in its state folder, so it counts every session as at its cap: it refuses messages, starts no automatic review, and saves no change. Fix or remove the file, then restart the engine."))
    const shown = parts.length > 0 || !!this.#said.textContent
    parts.push(this.#said)
    this.hidden = !shown
    replaceKeeping(this, parts)
    refocus(this, focused, key => [key.startsWith('continue:') ? 'usage-head' : ''])
  }

  async #continue(side: string, kind: string) {
    this.#said.textContent = await limitsCall(continueBody(side, '', kind))
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
