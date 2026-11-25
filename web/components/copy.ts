import { button, copyText, statusLine } from '../lib/dom.ts'
import { icon } from './icon.ts'

const COPIED_FOR_MS = 1500
const COPIED = 'Copied'
const FAILED = 'Couldn’t copy: select the text and copy it'

class SfCopy extends HTMLElement {
  text = ''

  readonly #button = button('', 'code-copy')
  readonly #status = statusLine('sr-only', 'span')
  #revert: ReturnType<typeof setTimeout> | undefined
  #tries = 0

  constructor() {
    super()
    this.#status.setAttribute('aria-live', 'polite')
    this.#button.setAttribute('aria-label', 'Copy the command')
    this.#button.addEventListener('click', () => void this.#copy())
    this.#rest()
  }

  get label(): string {
    return this.#button.getAttribute('aria-label') ?? ''
  }

  set label(text: string) {
    this.#button.setAttribute('aria-label', text)
  }

  connectedCallback() {
    if (this.#button.parentElement !== this) this.append(this.#button)
    if (this.#status.parentElement !== this) this.append(this.#status)
  }

  disconnectedCallback() {
    this.#tries++
    this.#rest()
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
