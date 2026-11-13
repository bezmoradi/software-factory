import { el } from '../lib/dom.ts'
import { icon } from './icon.ts'

export interface AccordionItem {
  id: string
  title: string
  note?: string
  state?: string
  good?: boolean
  panel: HTMLElement
}

let made = 0

class SfAccordion extends HTMLElement {
  #items: AccordionItem[] = []
  #open = ''
  readonly #name = `sf-accordion-${++made}`

  get open(): string {
    return this.#open
  }

  set items(items: AccordionItem[]) {
    this.#items = items
    if (!items.some(i => i.id === this.#open)) this.#open = ''
    this.#render()
  }

  #render() {
    const all = this.#items.map(item => {
      const d = el('details', 'accordion-item')
      d.name = this.#name
      d.open = item.id === this.#open
      const summary = el('summary', 'accordion-head')
      summary.append(icon('chevron', 'icon accordion-chevron'), el('span', 'accordion-title', item.title))
      if (item.note) summary.append(el('span', 'small muted', item.note))
      summary.append(el('span', 'spacer'))
      if (item.state) summary.append(el('span', `small ${item.good ? 'ok' : 'muted'}`, item.state))
      item.panel.classList.add('accordion-panel')
      d.append(summary, item.panel)
      d.addEventListener('toggle', () => {
        if (d.open === (this.#open === item.id)) return
        this.#open = d.open ? item.id : ''
        this.dispatchEvent(new CustomEvent('sf-expand', { detail: this.#open }))
      })
      return d
    })
    this.replaceChildren(...all)
  }
}

customElements.define('sf-accordion', SfAccordion)

declare global {
  interface HTMLElementTagNameMap {
    'sf-accordion': SfAccordion
  }
  interface HTMLElementEventMap {
    'sf-expand': CustomEvent<string>
  }
}
