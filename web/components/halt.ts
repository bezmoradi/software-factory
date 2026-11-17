import { api } from '../api.ts'
import { busy, button, el, focusedKey, refocus, replaceKeeping, rovingKeys, sayWhenShown, statusLine, visible } from '../lib/dom.ts'
import { icon } from './icon.ts'

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
let sending = ''
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

export function haltView(): HaltView {
  return { ...view, away: [...view.away] }
}

export function openAway(forSession = false): void {
  const menu = document.getElementById('halt-away')
  if (!menu) return
  if (!menu.matches(':popover-open')) menu.showPopover()
  const items = [...menu.querySelectorAll<HTMLElement>('[role="menuitemcheckbox"]:not([hidden])')]
  ;(forSession ? items[items.length - 1] : items[0])?.focus()
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

async function send(body: Record<string, unknown>, key: string) {
  if (sending) return
  sending = key
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
    sending = ''
    paintAll()
  }
}

const awayHere = () => !!session && view.away.includes(session.sid)

const HAND_OFF: Record<string, string> = {
  pause: 'sf-halt-banner [data-key="release"]',
  stop: 'sf-halt-banner [data-key="release"]',
  cancel: 'sf-halt-banner [data-key="release"]',
  release: 'sf-halt [data-key="pause"]',
  'away-off': 'sf-halt [data-key="away"]',
}

function handOff(key: string) {
  const to = HAND_OFF[key]
  const a = document.activeElement
  if (!to || (a && a !== document.body)) return
  queueMicrotask(() => {
    const now = document.activeElement
    if (!now || now === document.body) document.querySelector<HTMLElement>(to)?.focus()
  })
}

function held(b: HTMLButtonElement, key: string): HTMLButtonElement {
  if (key && sending === key) busy(b, true)
  else b.disabled = !!sending
  return b
}

function dress(b: HTMLButtonElement, name: string, shape: string, cls: string, tip: string, shown = ''): HTMLButtonElement {
  const words = el('span', 'halt-words')
  if (shown && name.startsWith(shown)) words.append(el('span', 'halt-keep', shown), el('span', 'halt-text', name.slice(shown.length)))
  else words.append(el('span', 'halt-text', name))
  b.className = cls
  b.replaceChildren(icon(shape, 'icon halt-icon'), words)
  b.title = `${name}. ${tip}`
  return b
}

function control(name: string, shape: string, cls: string, tip: string, onClick?: () => void, shown = '', key = ''): HTMLButtonElement {
  return dress(button('', cls, onClick, 'button', key), name, shape, cls, tip, shown)
}

function menuItem(onClick: () => void): HTMLButtonElement {
  const b = button('', '', onClick)
  b.setAttribute('role', 'menuitemcheckbox')
  return b
}

function check(b: HTMLButtonElement, label: string, on: boolean, key: string) {
  const mark = el('span', '', on ? '✓ ' : '')
  mark.setAttribute('aria-hidden', 'true')
  b.replaceChildren(mark, label)
  b.setAttribute('aria-checked', String(on))
  busy(b, sending === key)
}

class SfHalt extends HTMLElement {
  #away = button('', '', undefined, 'button', 'away')
  #menu = el('div', 'prefs menu surface halt-menu')
  #all = menuItem(() => void send({ op: 'away', on: !view.awayAll }, 'away-all'))
  #one = menuItem(() => {
    if (session) void send({ op: 'away', on: !awayHere(), sid: session.sid }, 'away-one')
  })
  #built = false

  #items(): HTMLElement[] {
    return [this.#all, this.#one].filter(b => b.isConnected && !b.hidden)
  }

  #build() {
    this.#built = true
    this.#away.setAttribute('popovertarget', 'halt-away')
    this.#away.setAttribute('aria-haspopup', 'menu')
    const menu = this.#menu
    menu.id = 'halt-away'
    menu.setAttribute('popover', '')
    menu.setAttribute('role', 'menu')
    menu.setAttribute('aria-label', 'Away mode')
    menu.addEventListener('beforetoggle', () => menu.style.setProperty('--halt-top', `${Math.round((this.closest('.bar') ?? this).getBoundingClientRect().bottom + 4)}px`))
    menu.addEventListener('toggle', e => {
      if ((e as ToggleEvent).newState !== 'open' || menu.contains(document.activeElement)) return
      const items = this.#items()
      ;(items.find(b => b.getAttribute('aria-checked') === 'true') ?? items[0])?.focus()
    })
    menu.addEventListener('keydown', e => {
      const items = this.#items()
      rovingKeys(e, items, items.indexOf(document.activeElement as HTMLElement), 'vertical')
    })
    menu.append(this.#all, this.#one, el('p', 'small muted', `Each new permission card is denied with the note “${AWAY_NOTE}”. Issue, pull request and push cards still wait for you.`))
    this.replaceChildren(this.#away, menu)
  }

  connectedCallback() {
    if (!this.#built) this.#build()
    shown.add(this)
    this.paint()
  }

  disconnectedCallback() {
    shown.delete(this)
  }

  paint() {
    if (!this.#built) return
    const on = view.awayAll || awayHere()
    dress(this.#away, on ? 'Away: on' : 'Away', 'moon', `ghost small${on ? ' warn' : ''}`, 'Away mode denies each new permission card with a note to the agent, so a session doesn’t wait for you.')
    this.#away.disabled = !!sending
    check(this.#all, 'Away for every session', view.awayAll, 'away-all')
    if (session) check(this.#one, `Away for ${visible(session.name)}`, awayHere(), 'away-one')
    else if (document.activeElement === this.#one) this.#all.focus()
    this.#one.hidden = !session
    const focused = focusedKey(this)
    while (this.#menu.nextSibling) this.#menu.nextSibling.remove()
    const parts: Node[] = []
    if (!view.mode) {
      const pause = control('Pause all', 'pause', 'ghost small', 'Holds new turns and automatic reviews and pushes; running sessions keep running.', () => void send({ op: 'pause' }, 'pause'), '', 'pause')
      const stop = control(armed ? 'Confirm stop everything' : 'Stop everything', 'stop', 'small danger', 'Interrupts every running turn, stops every session, holds every push and refuses new starts until Release.', () => {
        if (!armed) {
          armed = true
          return this.paint()
        }
        armed = false
        void send({ op: 'stop' }, 'stop')
      }, armed ? 'Confirm' : '', 'stop')
      parts.push(held(pause, 'pause'), held(stop, 'stop'))
      if (armed) parts.push(held(control('Cancel', 'close', 'ghost small', 'Keeps everything running.', () => {
        armed = false
        this.paint()
      }, '', 'cancel'), ''))
    }
    this.append(...parts)
    refocus(this, focused, key => key === 'cancel' ? ['stop'] : [])
    handOff(focused)
  }
}

class SfHaltBanner extends HTMLElement {
  #said = statusLine('halt-row bad small status-slot')

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
    const focused = focusedKey(this)
    const parts: Node[] = []
    const at = clock(view.since)
    if (view.mode === 'stopped') {
      const row = el('div', 'halt-row bad')
      row.append(el('p', '', `Stop everything is on${at ? ` since ${at}` : ''}. Every running turn was interrupted and every session stopped; every push is held, and no session starts or wakes until you press Release.`), held(button('Release', 'small', () => void send({ op: 'release' }, 'release'), 'button', 'release'), 'release'))
      parts.push(row)
    } else if (view.mode === 'paused') {
      const row = el('div', 'halt-row warn')
      row.append(el('p', '', `Pause all is on${at ? ` since ${at}` : ''}. Running sessions keep running, but no new turn starts and no automatic review or push runs until you press Release.`), held(button('Release', 'small', () => void send({ op: 'release' }, 'release'), 'button', 'release'), 'release'))
      parts.push(row)
    }
    if (view.awayAll || awayHere()) {
      const row = el('div', 'halt-row warn')
      const which = view.awayAll ? 'every session' : visible(session!.name)
      row.append(el('p', '', `Away mode is on for ${which}: each new permission card is denied with the note “${AWAY_NOTE}”. Issue, pull request and push cards still wait for you.`), held(button('Turn away mode off', 'small', () => void send(view.awayAll ? { op: 'away', on: false } : { op: 'away', on: false, sid: session!.sid }, 'away-off'), 'button', 'away-off'), 'away-off'))
      parts.push(row)
    }
    sayWhenShown(this.#said, said, 'bad')
    this.hidden = !parts.length && !said
    replaceKeeping(this, [...parts, this.#said])
    refocus(this, focused)
    handOff(focused)
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
