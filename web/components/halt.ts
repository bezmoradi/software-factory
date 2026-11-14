import { api } from '../api.ts'
import { button, el, visible } from '../lib/dom.ts'

export interface HaltView {
  mode: '' | 'paused' | 'stopped'
  since: number
  awayAll: boolean
  away: string[]
}

export interface HaltSession {
  sid: string
  name: string
}

const SID = /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/
const AWAY_NOTE = 'the person is away; continue without this or stop'
const OFF: HaltView = { mode: '', since: 0, awayAll: false, away: [] }

let view: HaltView = OFF
let session: HaltSession | null = null
let said = ''
let armed = false
let sending = false
const shown = new Set<{ paint(): void }>()

export function haltOf(raw: unknown): HaltView | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  const mode = r.mode === 'paused' || r.mode === 'stopped' || r.mode === '' ? r.mode : null
  if (mode === null) return null
  const since = typeof r.since === 'number' && Number.isFinite(r.since) && r.since > 0 ? r.since : 0
  const away = Array.isArray(r.away) ? r.away.filter((s): s is string => typeof s === 'string' && SID.test(s)).slice(0, 1000) : []
  return { mode, since, awayAll: r.awayAll === true, away }
}

export function setHalt(raw: unknown): void {
  const next = haltOf(raw)
  if (!next) return
  view = next
  if (view.mode) armed = false
  paintAll()
}

export function setHaltSession(s: HaltSession | null): void {
  if (s?.sid === session?.sid && s?.name === session?.name) return
  session = s && SID.test(s.sid) ? s : null
  paintAll()
}

function paintAll() {
  for (const node of shown) node.paint()
}

function clock(ms: number): string {
  return ms ? new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : ''
}

async function send(body: Record<string, unknown>) {
  if (sending) return
  sending = true
  said = ''
  paintAll()
  try {
    const res = await api('/api/halt', body)
    const data: unknown = await res.json().catch(() => null)
    if (res.ok) setHalt(data)
    else said = typeof (data as { message?: unknown } | null)?.message === 'string' ? (data as { message: string }).message : "The engine didn't take that. Try again."
  } catch {
    said = "The engine didn't answer. Try again once the page is connected."
  } finally {
    sending = false
    paintAll()
  }
}

const awayHere = () => !!session && view.away.includes(session.sid)

class SfHalt extends HTMLElement {
  connectedCallback() {
    shown.add(this)
    this.paint()
  }

  disconnectedCallback() {
    shown.delete(this)
  }

  paint() {
    const parts: Node[] = []
    const away = button(view.awayAll || awayHere() ? 'Away: on' : 'Away', `ghost small${view.awayAll || awayHere() ? ' warn' : ''}`)
    away.setAttribute('popovertarget', 'halt-away')
    away.setAttribute('aria-haspopup', 'menu')
    away.title = 'Away mode denies each new permission card with a note to the agent, so a session doesn’t wait for you.'
    const menu = el('div', 'prefs menu surface halt-menu')
    menu.id = 'halt-away'
    menu.setAttribute('popover', '')
    menu.setAttribute('role', 'menu')
    menu.setAttribute('aria-label', 'Away mode')
    const all = button(`${view.awayAll ? '✓ ' : ''}Away for every session`, '', () => void send({ op: 'away', on: !view.awayAll }))
    all.setAttribute('role', 'menuitemcheckbox')
    all.setAttribute('aria-checked', String(view.awayAll))
    menu.append(all)
    if (session) {
      const here = awayHere()
      const one = button(`${here ? '✓ ' : ''}Away for ${visible(session.name)}`, '', () => void send({ op: 'away', on: !here, sid: session!.sid }))
      one.setAttribute('role', 'menuitemcheckbox')
      one.setAttribute('aria-checked', String(here))
      menu.append(one)
    }
    menu.append(el('p', 'small muted', `Each new permission card is denied with the note “${AWAY_NOTE}”. Issue, pull request and push cards still wait for you.`))
    parts.push(away, menu)
    if (!view.mode) {
      const pause = button('Pause all', 'ghost small', () => void send({ op: 'pause' }))
      pause.title = 'Holds new turns and automatic reviews and pushes; running sessions keep running.'
      const stop = button(armed ? 'Confirm stop everything' : 'Stop everything', 'small danger', () => {
        if (!armed) {
          armed = true
          return this.paint()
        }
        armed = false
        void send({ op: 'stop' })
      })
      stop.title = 'Interrupts every running turn, stops every session, holds every push and refuses new starts until Release.'
      parts.push(pause, stop)
      if (armed) parts.push(button('Cancel', 'ghost small', () => {
        armed = false
        this.paint()
      }))
    }
    for (const b of parts) if (b instanceof HTMLButtonElement && b.getAttribute('role') !== 'menuitemcheckbox') b.disabled = sending
    this.replaceChildren(...parts)
  }
}

class SfHaltBanner extends HTMLElement {
  connectedCallback() {
    this.setAttribute('role', 'region')
    this.setAttribute('aria-label', 'Stop and pause')
    shown.add(this)
    this.paint()
  }

  disconnectedCallback() {
    shown.delete(this)
  }

  paint() {
    const parts: Node[] = []
    const at = clock(view.since)
    if (view.mode === 'stopped') {
      const row = el('div', 'halt-row bad')
      row.append(el('p', '', `Stop everything is on${at ? ` since ${at}` : ''}. Every running turn was interrupted and every session stopped; every push is held, and no session starts or wakes until you press Release.`), button('Release', 'small', () => void send({ op: 'release' })))
      parts.push(row)
    } else if (view.mode === 'paused') {
      const row = el('div', 'halt-row warn')
      row.append(el('p', '', `Pause all is on${at ? ` since ${at}` : ''}. Running sessions keep running, but no new turn starts and no automatic review or push runs until you press Release.`), button('Release', 'small', () => void send({ op: 'release' })))
      parts.push(row)
    }
    if (view.awayAll || awayHere()) {
      const row = el('div', 'halt-row warn')
      const which = view.awayAll ? 'every session' : visible(session!.name)
      row.append(el('p', '', `Away mode is on for ${which}: each new permission card is denied with the note “${AWAY_NOTE}”. Issue, pull request and push cards still wait for you.`), button('Turn away mode off', 'small', () => void send(view.awayAll ? { op: 'away', on: false } : { op: 'away', on: false, sid: session!.sid })))
      parts.push(row)
    }
    if (said) {
      const p = el('p', 'halt-row bad small', said)
      p.setAttribute('role', 'status')
      parts.push(p)
    }
    this.hidden = !parts.length
    this.replaceChildren(...parts)
    for (const b of this.querySelectorAll('button')) b.disabled = sending
  }
}

customElements.define('sf-halt', SfHalt)
customElements.define('sf-halt-banner', SfHaltBanner)

declare global {
  interface HTMLElementTagNameMap {
    'sf-halt': SfHalt
    'sf-halt-banner': SfHaltBanner
  }
}
