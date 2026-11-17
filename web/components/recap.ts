import { button, el, visible } from '../lib/dom.ts'

type Post = (path: string, body: unknown) => Promise<Response>

const ROUTE = '/api/recap'
const SEEN_EVERY_MS = 30000
const AWAY_MS = 120000

interface Line {
  time: number
  kind: string
  text: string
}

function lineOf(v: unknown): Line | null {
  if (typeof v !== 'object' || v === null) return null
  const r = v as Record<string, unknown>
  if (typeof r.text !== 'string' || typeof r.time !== 'number' || typeof r.kind !== 'string') return null
  return { time: r.time, kind: r.kind, text: r.text }
}

function stamp(ms: number): HTMLTimeElement {
  const d = new Date(ms)
  const same = d.toDateString() === new Date().toDateString()
  const t = el('time', 'recap-time', same ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : d.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }))
  t.dateTime = d.toISOString()
  return t
}

class SfRecap extends HTMLElement {
  #post: Post | null = null
  #sid = ''
  #loading = false
  #seenAt = 0
  #hiddenAt = 0
  #every = 0

  #mark = () => {
    if (this.#sid && !this.#loading) void this.#seen(this.#sid)
  }

  #visibility = () => {
    if (document.hidden) {
      this.#hiddenAt = Date.now()
      this.#mark()
    } else if (this.#sid && !this.#loading && this.#hiddenAt && Date.now() - this.#hiddenAt > AWAY_MS) void this.#load(this.#sid)
  }

  connectedCallback() {
    window.clearInterval(this.#every)
    this.#every = window.setInterval(() => {
      if (!document.hidden) this.#mark()
    }, SEEN_EVERY_MS)
    document.addEventListener('visibilitychange', this.#visibility)
  }

  disconnectedCallback() {
    window.clearInterval(this.#every)
    this.#every = 0
    document.removeEventListener('visibilitychange', this.#visibility)
  }

  set post(p: Post) {
    this.#post = p
  }

  show(sid: string, looking: boolean) {
    if (sid !== this.#sid) {
      if (this.#sid && looking && !this.#loading) void this.#seen(this.#sid)
      this.#sid = sid
      this.#seenAt = 0
      this.hidden = true
      this.replaceChildren()
      if (sid) void this.#load(sid)
      return
    }
    if (sid && looking && !this.#loading && Date.now() - this.#seenAt > SEEN_EVERY_MS) void this.#seen(sid)
  }

  async #seen(sid: string) {
    if (!this.#post) return
    this.#seenAt = Date.now()
    await this.#post(ROUTE, { op: 'seen', sid }).catch(() => null)
  }

  async #load(sid: string) {
    if (!this.#post) return
    this.#loading = true
    const res = await this.#post(ROUTE, { op: 'read', sid }).catch(() => null)
    const body: unknown = res && res.ok ? await res.json().catch(() => null) : null
    this.#loading = false
    if (this.#sid !== sid) return
    if (!document.hidden) void this.#seen(sid)
    if (typeof body !== 'object' || body === null) return
    const v = body as Record<string, unknown>
    const since = typeof v.since === 'number' ? v.since : 0
    const lines = Array.isArray(v.lines) ? (v.lines as unknown[]).map(lineOf).filter((l): l is Line => l !== null) : []
    const waiting = typeof v.waiting === 'number' && v.waiting > 0 ? Math.floor(v.waiting) : 0
    if (!since || !lines.length) return
    const head = el('p', 'recap-head')
    head.append(el('strong', '', 'Since you last looked'), ' ', el('span', 'muted', `(${new Date(since).toLocaleString()})`))
    const list = el('ul', 'recap-lines')
    for (const l of lines) {
      const li = el('li', `recap-${l.kind.replace(/[^a-z]/g, '')}`)
      li.append(stamp(l.time), ' ', visible(l.text))
      list.append(li)
    }
    const parts: Node[] = [head, list]
    if (v.more === true) parts.push(el('p', 'muted', 'Only the latest 20 are shown; the session’s history has the rest.'))
    if (waiting) parts.push(el('p', 'warn', waiting === 1 ? '1 card is waiting for you.' : `${waiting} cards are waiting for you.`))
    const dismiss = button('Dismiss', 'ghost small', () => {
      this.hidden = true
      this.replaceChildren()
    })
    dismiss.setAttribute('aria-label', 'Dismiss the recap')
    parts.push(dismiss)
    this.replaceChildren(...parts)
    this.hidden = false
  }
}

customElements.define('sf-recap', SfRecap)

declare global {
  interface HTMLElementTagNameMap {
    'sf-recap': SfRecap
  }
}
