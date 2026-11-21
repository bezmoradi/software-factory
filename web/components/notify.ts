import { el, statusLine, visible } from '../lib/dom.ts'

const SETTING_KEY = 'sf-notify'
const SEEN_KEY = 'sf-notified'
const MAX_SEEN = 1000
const MAX_AT_ONCE = 4

export interface Watched {
  sid: string
  name: string
  cards: string[]
  pushHeld: number
}

const supported = typeof window !== 'undefined' && 'Notification' in window

function readSetting(): boolean {
  try {
    return sessionStorage.getItem(SETTING_KEY) === 'on'
  } catch {
    return false
  }
}

function readSeen(): string[] {
  try {
    const v: unknown = JSON.parse(sessionStorage.getItem(SEEN_KEY) || '[]')
    return Array.isArray(v) ? v.filter((k): k is string => typeof k === 'string').slice(-MAX_SEEN) : []
  } catch {
    return []
  }
}

let on = supported && readSetting() && Notification.permission === 'granted'
let opener: (sid: string) => void = () => undefined
const seen = new Set<string>(readSeen())
let dirty = false
let fired = 0

function saveSetting() {
  try {
    if (on) sessionStorage.setItem(SETTING_KEY, 'on')
    else sessionStorage.removeItem(SETTING_KEY)
  } catch {}
}

function saveSeen() {
  if (!dirty) return
  dirty = false
  while (seen.size > MAX_SEEN) seen.delete(seen.values().next().value!)
  try {
    sessionStorage.setItem(SEEN_KEY, JSON.stringify([...seen]))
  } catch {}
}

export function onNotifyOpen(f: (sid: string) => void): void {
  opener = f
}

function away(): boolean {
  return document.hidden || !document.hasFocus()
}

function show(sid: string, name: string, what: string, tag: string) {
  try {
    const n = new Notification(`${visible(name)}: ${what}`, { tag, body: 'Software Factory' })
    n.addEventListener('click', () => {
      window.focus()
      opener(sid)
      n.close()
    })
  } catch {}
}

function once(sid: string, name: string, what: string, key: string) {
  if (seen.has(key)) return
  seen.add(key)
  dirty = true
  if (on && away() && fired < MAX_AT_ONCE && Notification.permission === 'granted') {
    fired++
    show(sid, name, what, key)
  }
}

export function notify(sid: string, name: string, what: string, key: string): void {
  fired = 0
  once(sid, name, visible(what), key)
  saveSeen()
}

export function watchNotify(list: Watched[]): void {
  fired = 0
  for (const s of list) {
    for (const id of s.cards) once(s.sid, s.name, 'card waiting', `card:${s.sid}:${id}`)
    if (s.pushHeld) once(s.sid, s.name, 'push held', `push:${s.sid}:${s.pushHeld}`)
  }
  saveSeen()
}

class SfNotify extends HTMLElement {
  #box = el('input')
  #said = statusLine('small muted')

  connectedCallback() {
    if (this.childElementCount) return
    this.#box.type = 'checkbox'
    this.#box.checked = on
    this.#box.addEventListener('change', () => void this.#change())
    const label = el('label', 'notify-pref small')
    label.append(this.#box, el('span', '', 'Desktop notifications'))
    const legend = el('legend', 'label', 'Notifications')
    const set = el('fieldset', 'themes')
    set.append(legend, label, this.#said)
    this.append(set)
    if (!supported) {
      this.#box.disabled = true
      this.#said.textContent = 'This browser has no desktop notifications.'
    }
  }

  async #change() {
    if (!supported) return
    if (!this.#box.checked) {
      on = false
      saveSetting()
      this.#said.textContent = ''
      return
    }
    let permission = Notification.permission
    if (permission === 'default') permission = await Notification.requestPermission().catch(() => 'denied' as NotificationPermission)
    on = permission === 'granted'
    this.#box.checked = on
    saveSetting()
    this.#said.textContent = on ? '' : 'The browser blocks notifications for this page. Allow them in its site settings, then turn this on again.'
  }
}

customElements.define('sf-notify', SfNotify)

declare global {
  interface HTMLElementTagNameMap {
    'sf-notify': SfNotify
  }
}
