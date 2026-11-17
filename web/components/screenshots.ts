import { api } from '../api.ts'
import { button, el, visible, whileBusy } from '../lib/dom.ts'

export interface ScreenshotsTarget {
  sid: string
  busy: boolean
}

interface Shot {
  name: string
  size: number
  at: number
}

const MAX_DATA = 34 << 20

function isRecord(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v)
}

function shotsOf(v: unknown): Shot[] {
  if (!isRecord(v) || !Array.isArray(v.screenshots)) return []
  return v.screenshots.filter(isRecord).filter(s => typeof s.name === 'string' && typeof s.size === 'number' && typeof s.at === 'number').map(s => ({ name: s.name as string, size: s.size as number, at: s.at as number })).slice(0, 50)
}

function size(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1 << 20) return `${(n / 1024).toFixed(0)} KiB`
  return `${(n / (1 << 20)).toFixed(1)} MiB`
}

function pngBlob(data: string): Blob | null {
  if (data.length > MAX_DATA || !/^[A-Za-z0-9+/]*={0,2}$/.test(data)) return null
  let raw: string
  try {
    raw = atob(data)
  } catch {
    return null
  }
  const bytes = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i)
  if (bytes.length < 8 || bytes[0] !== 0x89 || bytes[1] !== 0x50 || bytes[2] !== 0x4e || bytes[3] !== 0x47) return null
  return new Blob([bytes], { type: 'image/png' })
}

class SfScreenshots extends HTMLElement {
  #target: ScreenshotsTarget | null = null
  #shots: Shot[] = []
  #loading = 0
  #opening = 0
  #url = ''
  #shown = ''
  #details = el('details', 'screenshots')
  #summary = el('summary', 'small')
  #list = el('ul', 'small list screenshot-list')
  #status = el('p', 'small muted')
  #figure = el('figure', 'screenshot-view')
  #img = el('img')
  #caption = el('figcaption', 'small muted')

  constructor() {
    super()
    this.#status.setAttribute('role', 'status')
    this.#img.decoding = 'async'
    this.#figure.append(this.#img, this.#caption)
    this.#figure.hidden = true
    const refresh = button('Refresh', 'ghost small', () => {
      const sid = this.#target?.sid
      if (sid) void whileBusy(refresh, () => this.refresh(sid))
    })
    this.#details.append(this.#summary, el('p', 'small muted', 'Images the agent saved in its screenshots folder, newest first. The engine reads each one, checks it is a PNG, JPEG or GIF of at most 10 MiB and 8,192 pixels a side, and shows it again as a PNG.'), this.#list, refresh, this.#status, this.#figure)
  }

  connectedCallback() {
    if (!this.#details.isConnected) this.append(this.#details)
    if (!this.#target) this.hidden = true
  }

  disconnectedCallback() {
    this.#drop()
  }

  set view(value: ScreenshotsTarget | null) {
    const before = this.#target
    this.#target = value
    if (!value?.sid) {
      this.#shots = []
      this.#drop()
      this.hidden = true
      return
    }
    if (value.sid !== before?.sid) {
      this.#shots = []
      this.#drop()
      this.#status.textContent = ''
      this.hidden = true
      void this.refresh(value.sid)
    } else if (before?.busy && !value.busy) void this.refresh(value.sid)
  }

  async refresh(sid: string) {
    if (sid !== this.#target?.sid) return
    const n = ++this.#loading
    const res = await api('/api/screenshots', { sid }).catch(() => null)
    if (n !== this.#loading || sid !== this.#target?.sid) return
    if (!res || !res.ok) {
      this.#status.textContent = this.#shots.length ? 'The screenshots couldn\'t be listed.' : ''
      return
    }
    this.#shots = shotsOf(await res.json().catch(() => null))
    if (this.#shown && !this.#shots.some(s => s.name === this.#shown)) this.#drop()
    this.#paint()
  }

  #paint() {
    this.hidden = this.#shots.length === 0
    this.#summary.textContent = `Screenshots (${this.#shots.length})`
    this.#list.replaceChildren(...this.#shots.map(s => {
      const li = el('li')
      const open = button(visible(s.name), 'link', () => void whileBusy(open, () => this.#open(s.name)))
      open.setAttribute('aria-pressed', String(s.name === this.#shown))
      li.append(open, el('span', 'muted', ` ${size(s.size)} · ${new Date(s.at).toLocaleString()}`))
      return li
    }))
  }

  async #open(name: string) {
    const sid = this.#target?.sid
    if (!sid) return
    const n = ++this.#opening
    this.#status.textContent = 'Reading the screenshot…'
    const res = await api('/api/screenshot', { sid, name }).catch(() => null)
    if (n !== this.#opening || sid !== this.#target?.sid) return
    const body = res ? await res.json().catch(() => null) as unknown : null
    if (!res || !res.ok || !isRecord(body) || body.type !== 'image/png' || typeof body.data !== 'string') {
      const why = isRecord(body) && typeof body.message === 'string' ? body.message : 'The engine didn\'t show that screenshot.'
      this.#status.textContent = visible(why)
      return
    }
    const blob = pngBlob(body.data)
    if (!blob) {
      this.#status.textContent = 'The engine\'s answer wasn\'t a PNG image.'
      return
    }
    this.#drop()
    this.#url = URL.createObjectURL(blob)
    this.#shown = name
    this.#img.src = this.#url
    this.#img.alt = `Screenshot ${visible(name)}`
    const w = typeof body.width === 'number' ? body.width : 0
    const h = typeof body.height === 'number' ? body.height : 0
    this.#caption.textContent = `${visible(name)}${w && h ? ` · ${w} × ${h}` : ''}`
    this.#figure.hidden = false
    this.#status.textContent = ''
    this.#paint()
  }

  #drop() {
    if (this.#url) URL.revokeObjectURL(this.#url)
    this.#url = ''
    this.#shown = ''
    this.#img.removeAttribute('src')
    this.#img.alt = ''
    this.#caption.textContent = ''
    this.#figure.hidden = true
  }
}

customElements.define('sf-screenshots', SfScreenshots)

declare global {
  interface HTMLElementTagNameMap {
    'sf-screenshots': SfScreenshots
  }
}
