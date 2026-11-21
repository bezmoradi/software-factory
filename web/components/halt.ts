import { request } from '../api.ts'
import { busy, button, el, focusedKey, refocus, replaceKeeping, rovingKeys, sayWhenShown, statusLine, visible } from '../lib/dom.ts'
import { icon } from './icon.ts'

export interface HaltView {
  awayAll: boolean
  away: string[]
}

export interface HaltSession {
  sid: string
  name: string
  own: boolean
}

const SID = /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/
const AWAY_NOTE = 'the person is away; continue without this or stop'
const OFF: HaltView = { awayAll: false, away: [] }

let view: HaltView = OFF
let session: HaltSession | null = null
let said = ''
let sending = ''
const shown = new Set<{ paint(): void }>()

export function haltOf(raw: unknown): HaltView | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  if (!Array.isArray(r.away) || typeof r.awayAll !== 'boolean') return null
  const away = r.away.filter((s): s is string => typeof s === 'string' && SID.test(s)).slice(0, 1000)
  return { awayAll: r.awayAll, away }
}

export function setHalt(raw: unknown): void {
  const next = haltOf(raw)
  if (!next) return
  view = next
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
  if (s?.sid === session?.sid && s?.name === session?.name && s?.own === session?.own) return
  session = s && SID.test(s.sid) ? s : null
  paintAll()
}

function paintAll() {
  for (const node of shown) node.paint()
}

async function send(body: Record<string, unknown>, key: string) {
  if (sending) return
  sending = key
  said = ''
  paintAll()
  const a = await request('/api/halt', body)
  if (a.ok) setHalt(a.body)
  else said = a.message
  sending = ''
  paintAll()
}

const awayHere = () => !!session && view.away.includes(session.sid)


function dress(b: HTMLButtonElement, name: string, shape: string, cls: string, tip: string): HTMLButtonElement {
  const words = el('span', 'halt-words')
  words.append(el('span', 'halt-text', name))
  b.className = cls
  b.replaceChildren(icon(shape, 'icon halt-icon'), words)
  b.title = `${name}. ${tip}`
  return b
}

function menuItem(onClick: () => void): HTMLButtonElement {
  const b = button('', '', onClick)
  b.setAttribute('role', 'menuitemcheckbox')
  return b
}

function check(b: HTMLButtonElement, label: string, on: boolean, key: string) {
  const mark = el('span', 'halt-box')
  mark.setAttribute('aria-hidden', 'true')
  if (on) {
    const tick = document.createElement('sf-icon')
    tick.setAttribute('name', 'check')
    mark.append(tick)
  }
  b.replaceChildren(mark, el('span', '', label))
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
    this.#away.addEventListener('click', () => void this.#off())
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

  async #off() {
    if (!view.awayAll && !awayHere()) return
    const sid = awayHere() ? session!.sid : ''
    if (view.awayAll) await send({ op: 'away', on: false }, 'away-off')
    if (sid && view.away.includes(sid)) await send({ op: 'away', on: false, sid }, 'away-off')
  }

  paint() {
    if (!this.#built) return
    const on = view.awayAll || awayHere()
    dress(this.#away, on ? 'Away: on' : 'Away', 'moon', `ghost small${on ? ' warn' : ''}`, on ? 'Press to turn away mode off.' : 'Away mode denies each new permission card with a note to the agent, so a session doesn’t wait for you.')
    if (on) {
      this.#away.removeAttribute('popovertarget')
      this.#away.removeAttribute('aria-haspopup')
      if (this.#menu.matches(':popover-open')) this.#menu.hidePopover()
    } else {
      this.#away.setAttribute('popovertarget', 'halt-away')
      this.#away.setAttribute('aria-haspopup', 'menu')
    }
    this.#away.disabled = !!sending
    check(this.#all, 'Away for every session', view.awayAll, 'away-all')
    const offered = session && (session.own || awayHere()) ? session : null
    if (offered) check(this.#one, `Away for ${visible(offered.name)}`, awayHere(), 'away-one')
    else if (document.activeElement === this.#one) this.#all.focus()
    this.#one.hidden = !offered
  }
}

class SfHaltBanner extends HTMLElement {
  #said = statusLine('halt-row bad small status-slot')

  connectedCallback() {
    this.setAttribute('role', 'region')
    this.setAttribute('aria-label', 'Away mode')
    shown.add(this)
    this.paint()
  }

  disconnectedCallback() {
    shown.delete(this)
  }

  paint() {
    const focused = focusedKey(this)
    const parts: Node[] = []
    if (view.awayAll || awayHere()) {
      const row = el('div', 'halt-row warn')
      const which = view.awayAll ? 'every session' : visible(session!.name)
      row.append(el('p', '', `Away mode is on for ${which}: new permission cards are denied for you. Press Away to turn it off.`))
      parts.push(row)
    }
    sayWhenShown(this.#said, said, 'bad')
    this.hidden = !parts.length && !said
    replaceKeeping(this, [...parts, this.#said])
    refocus(this, focused)
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
