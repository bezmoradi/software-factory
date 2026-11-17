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
  busy: boolean
  seq: number
  reviewRunning: boolean
  review: string
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
const last = new Map<string, { busy: boolean; reviewRunning: boolean }>()
let dirty = false

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

export function watchNotify(list: Watched[]): void {
  let fired = 0
  for (const s of list) {
    const prev = last.get(s.sid)
    last.set(s.sid, { busy: s.busy, reviewRunning: s.reviewRunning })
    const events: [string, string][] = []
    for (const id of s.cards) events.push([`card:${s.sid}:${id}`, 'card waiting'])
    if (s.pushHeld) events.push([`push:${s.sid}:${s.pushHeld}`, 'push held'])
    if (prev?.busy && !s.busy) events.push([`turn:${s.sid}:${s.seq}`, 'turn finished'])
    if (prev?.reviewRunning && !s.reviewRunning && s.review) events.push([`review:${s.sid}:${s.review}`, 'review finished'])
    for (const [key, what] of events) {
      if (seen.has(key)) continue
      seen.add(key)
      dirty = true
      if (on && away() && fired < MAX_AT_ONCE && Notification.permission === 'granted') {
        fired++
        show(s.sid, s.name, what, key)
      }
    }
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
    set.append(legend, label, el('p', 'small muted', 'When this tab is in the background: a card waiting, a push held, a turn or a review finished. Each names the session and nothing an agent wrote. The setting lasts for this tab.'), this.#said)
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
