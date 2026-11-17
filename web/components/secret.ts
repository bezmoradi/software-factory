import { button } from '../lib/dom.ts'
import { icon } from './icon.ts'

let made = 0

class SfSecret extends HTMLElement {
  static observedAttributes = ['label']

  readonly #toggle = button('', 'ghost icon-button secret-toggle')
  readonly #watch = new MutationObserver(() => this.#sync())

  constructor() {
    super()
    this.#toggle.addEventListener('pointerdown', e => e.preventDefault())
    this.#toggle.addEventListener('click', () => {
      const input = this.#input()
      if (!input) return
      input.type = input.type === 'password' ? 'text' : 'password'
      this.#sync()
      input.focus()
    })
  }

  connectedCallback() {
    const input = this.#input()
    if (!input) return
    if (this.#toggle.parentElement !== this) this.append(this.#toggle)
    this.#watch.disconnect()
    this.#watch.observe(input, { attributes: true, attributeFilter: ['type'] })
    this.#sync()
  }

  disconnectedCallback() {
    this.#watch.disconnect()
  }

  attributeChangedCallback() {
    this.#sync()
  }

  #input(): HTMLInputElement | null {
    return this.querySelector(':scope > input')
  }

  #sync() {
    const input = this.#input()
    if (!input) return
    const shown = input.type !== 'password'
    const what = this.getAttribute('label') ?? 'value'
    this.#toggle.setAttribute('aria-label', `Show ${what}`)
    this.#toggle.setAttribute('aria-pressed', String(shown))
    this.#toggle.setAttribute('aria-controls', input.id)
    this.#toggle.title = shown ? `Hide ${what}` : `Show ${what}`
    this.#toggle.replaceChildren(icon(shown ? 'eye-closed' : 'eye'))
  }
}

customElements.define('sf-secret', SfSecret)

declare global {
  interface HTMLElementTagNameMap {
    'sf-secret': SfSecret
  }
}

export function secret(input: HTMLInputElement, label: string): SfSecret {
  if (!input.id) input.id = `sf-secret-${++made}`
  const box = document.createElement('sf-secret')
  box.setAttribute('label', label)
  box.append(input)
  return box
}
