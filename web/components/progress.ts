import { el, visible } from '../lib/dom.ts'

class SfProgress extends HTMLElement {
  readonly #bar = el('progress', 'progress-bar')
  readonly #label = el('span', 'progress-label')
  #percent: number | null = null
  #text = ''

  constructor() {
    super()
    this.#bar.max = 100
    this.#label.setAttribute('aria-hidden', 'true')
    this.#draw()
  }

  connectedCallback() {
    if (this.#label.parentElement !== this) this.append(this.#label)
    if (this.#bar.parentElement !== this) this.append(this.#bar)
  }

  get percent(): number | null {
    return this.#percent
  }

  set percent(value: number | null) {
    const next = typeof value === 'number' && Number.isFinite(value) ? Math.min(100, Math.max(0, value)) : null
    if (next === this.#percent) return
    this.#percent = next
    this.#draw()
  }

  get label(): string {
    return this.#text
  }

  set label(value: string) {
    const next = typeof value === 'string' ? value : ''
    if (next === this.#text) return
    this.#text = next
    this.#draw()
  }

  get name(): string {
    return this.#bar.getAttribute('aria-label') ?? ''
  }

  set name(value: string) {
    if (value) this.#bar.setAttribute('aria-label', visible(value))
    else this.#bar.removeAttribute('aria-label')
  }

  #draw() {
    if (this.#percent === null) this.#bar.removeAttribute('value')
    else this.#bar.value = this.#percent
    const shown = visible(this.#text)
    if (shown) this.#bar.setAttribute('aria-valuetext', shown)
    else this.#bar.removeAttribute('aria-valuetext')
    this.#label.textContent = shown
  }
}

customElements.define('sf-progress', SfProgress)

declare global {
  interface HTMLElementTagNameMap {
    'sf-progress': SfProgress
  }
}
