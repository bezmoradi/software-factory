import { button, copyText, el, visible } from '../lib/dom.ts'
import { icon } from './icon.ts'

const COPIED_FOR_MS = 1500
const COPIED = 'Copied'
const FAILED = 'Couldn’t copy: select the text and copy it'

class SfCopy extends HTMLElement {
  static observedAttributes = ['label']

  text = ''

  readonly #button = button('', 'setup-copy')
  readonly #status = el('span', 'sr-only')
  #revert: ReturnType<typeof setTimeout> | undefined
  #tries = 0

  constructor() {
    super()
    this.#status.setAttribute('role', 'status')
    this.#status.setAttribute('aria-live', 'polite')
    this.#button.addEventListener('click', () => void this.#copy())
    this.#name()
    this.#rest()
  }

  connectedCallback() {
    if (this.#button.parentElement !== this) this.append(this.#button)
    if (this.#status.parentElement !== this) this.append(this.#status)
  }

  disconnectedCallback() {
    this.#tries++
    this.#rest()
  }

  attributeChangedCallback() {
    this.#name()
  }

  #name() {
    this.#button.setAttribute('aria-label', this.getAttribute('label') ?? 'Copy the command')
  }

  #rest() {
    clearTimeout(this.#revert)
    this.#revert = undefined
    this.#button.replaceChildren(icon('copy'))
    this.#button.title = 'Copy'
    this.#status.textContent = ''
  }

  async #copy() {
    const attempt = ++this.#tries
    this.#rest()
    const ok = await copyText(this.text)
    if (attempt !== this.#tries || !this.isConnected) return
    if (!ok) {
      this.#button.title = FAILED
      this.#status.textContent = FAILED
      return
    }
    this.#button.replaceChildren(icon('check'))
    this.#button.title = COPIED
    this.#status.textContent = COPIED
    this.#revert = setTimeout(() => this.#rest(), COPIED_FOR_MS)
  }
}

customElements.define('sf-copy', SfCopy)

declare global {
  interface HTMLElementTagNameMap {
    'sf-copy': SfCopy
  }
}

export function commandBlock(command: string): HTMLElement {
  const block = el('div', 'setup-code')
  const copy = document.createElement('sf-copy')
  copy.text = command
  block.append(el('code', '', visible(command)), copy)
  return block
}
