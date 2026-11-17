import { button, el } from '../lib/dom.ts'
import { icon } from './icon.ts'

export type Tone = 'info' | 'ok' | 'warn' | 'bad'
export interface Toast {
  id?: string
  title: string
  description?: string
  tone?: Tone
  action?: { label: string; run: () => void }
  timeout?: number
}

const STAY: Record<Tone, number> = { info: 5000, ok: 5000, warn: 8000, bad: 0 }
const ICON: Record<Tone, string> = { info: 'info', ok: 'check', warn: 'warning', bad: 'error' }
const LIMIT = 3

class SfToasts extends HTMLElement {
  #timers = new Map<HTMLElement, number>()
  #held = false
  #count = 0

  connectedCallback() {
    if (this.hasAttribute('role')) return
    this.setAttribute('role', 'region')
    this.setAttribute('aria-label', 'Notifications')
    this.setAttribute('aria-live', 'polite')
    this.setAttribute('aria-relevant', 'additions text')
    const sync = () => this.#sync()
    this.addEventListener('pointerenter', sync)
    this.addEventListener('pointerleave', sync)
    this.addEventListener('focusin', sync)
    this.addEventListener('focusout', () => queueMicrotask(sync))
    document.addEventListener('visibilitychange', sync)
    this.addEventListener('keydown', e => {
      const item = (e.target as Element).closest<HTMLElement>('.toast')
      if (e.key === 'Escape' && item) this.#close(item)
    })
  }

  show({ id, title, description = '', tone = 'info', action, timeout = STAY[tone] }: Toast): string {
    const key = id || `toast-${++this.#count}`
    let item = [...this.querySelectorAll<HTMLElement>('.toast')].find(t => t.dataset.id === key || (!id && t.dataset.title === title && !t.dataset.named))
    if (!item) {
      item = el('div', 'toast')
      this.prepend(item)
    }
    item.className = `toast toast-${tone}`
    item.dataset.id = key
    item.dataset.title = title
    if (id) item.dataset.named = 'yes'
    item.dataset.stay = String(timeout)
    if (tone === 'bad') item.setAttribute('role', 'alert')
    else item.removeAttribute('role')
    const body = el('div', 'toast-body')
    body.append(el('p', 'toast-title', title))
    if (description) body.append(el('p', 'toast-text', description))
    const shown = item
    if (action) body.append(button(action.label, 'link toast-action', () => { this.#close(shown); action.run() }))
    const close = button('', 'ghost icon-button toast-close', () => this.#close(shown))
    close.setAttribute('aria-label', 'Dismiss')
    close.append(icon('close'))
    item.replaceChildren(icon(ICON[tone], 'icon toast-icon'), body, close)
    const all = this.querySelectorAll<HTMLElement>('.toast')
    for (let i = LIMIT; i < all.length; i++) this.#close(all[i]!)
    this.#restart(item)
    return key
  }

  close(id = ''): void {
    for (const item of this.querySelectorAll<HTMLElement>('.toast')) if (!id || item.dataset.id === id) this.#close(item)
  }

  #restart(item: HTMLElement) {
    clearTimeout(this.#timers.get(item))
    this.#timers.delete(item)
    const stay = Number(item.dataset.stay)
    if (stay > 0 && !this.#held) this.#timers.set(item, window.setTimeout(() => this.#close(item), stay))
  }

  #sync() {
    const held = this.matches(':hover') || this.contains(document.activeElement) || document.hidden
    if (held === this.#held) return
    this.#held = held
    for (const item of this.querySelectorAll<HTMLElement>('.toast')) this.#restart(item)
  }

  #close(item: HTMLElement) {
    clearTimeout(this.#timers.get(item))
    this.#timers.delete(item)
    const focused = item.contains(document.activeElement)
    item.remove()
    if (focused) this.querySelector<HTMLElement>('.toast-close')?.focus()
    this.#sync()
  }
}

customElements.define('sf-toasts', SfToasts)

declare global {
  interface HTMLElementTagNameMap {
    'sf-toasts': SfToasts
  }
}
