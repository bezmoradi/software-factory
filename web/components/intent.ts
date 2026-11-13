import { el, focusedKey, refocus, remembered, shown } from '../lib/dom.ts'
import type { IntentRecord } from '../types.ts'

type Group = 'open' | 'agreed' | 'assumed' | 'built'

const GROUPS: [string, Group, boolean][] = [['Open questions', 'open', true], ['Agreed', 'agreed', false], ['Assumed', 'assumed', false], ['Built', 'built', false]]

class SfIntent extends HTMLElement {
  #record: IntentRecord | null = null
  #open = new Map<string, boolean>()
  #key = ''

  connectedCallback() {
    if (!this.#record) this.hidden = true
  }

  set record(rec: IntentRecord | null) {
    const key = JSON.stringify(rec && rec.found ? rec : null)
    if (key === this.#key) return
    this.#key = key
    const focused = focusedKey(this)
    this.#record = rec && rec.found ? rec : null
    this.hidden = !this.#record
    if (!this.#record) {
      this.replaceChildren()
      return
    }
    this.replaceChildren(el('h2', 'label', 'Intent'), el('p', 'small', shown(rec!.title || 'Intent record')))
    for (const [name, key, openByDefault] of GROUPS) {
      const lines = rec![key] || []
      if (!lines.length) continue
      const summary = el('summary', '', `${name} (${lines.length})`)
      summary.dataset.key = `group:${key}`
      const ul = el('ul')
      for (const line of lines) ul.append(el('li', '', shown(line.replace(/\s+Said: ".*$/, ''))))
      this.append(remembered(this.#open, key, openByDefault, summary, ul))
    }
    refocus(this, focused)
  }
}

customElements.define('sf-intent', SfIntent)

declare global {
  interface HTMLElementTagNameMap {
    'sf-intent': SfIntent
  }
}
