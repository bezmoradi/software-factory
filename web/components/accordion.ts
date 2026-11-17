import { el, emit, replaceKeeping } from '../lib/dom.ts'
import { icon } from './icon.ts'

export interface AccordionItem {
  id: string
  title: string
  note?: string
  state?: string
  good?: boolean
  panel: HTMLElement
}

interface Box {
  details: HTMLDetailsElement
  summary: HTMLElement
  title: HTMLSpanElement
  note: HTMLSpanElement
  state: HTMLSpanElement
}

let made = 0

class SfAccordion extends HTMLElement {
  #items: AccordionItem[] = []
  #boxes = new Map<string, Box>()
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

  #box(id: string): Box {
    const kept = this.#boxes.get(id)
    if (kept) return kept
    const details = el('details', 'accordion-item')
    details.name = this.#name
    details.open = id === this.#open
    const summary = el('summary', 'accordion-head')
    const box = { details, summary, title: el('span', 'accordion-title'), note: el('span', 'small muted'), state: el('span', 'small') }
    summary.append(icon('chevron', 'icon accordion-chevron'), box.title, box.note, el('span', 'spacer'), box.state)
    details.append(summary)
    details.addEventListener('toggle', () => {
      if (details.open === (this.#open === id)) return
      this.#open = details.open ? id : ''
      emit(this, 'sf-expand', this.#open)
    })
    this.#boxes.set(id, box)
    return box
  }

  #render() {
    const ids = new Set(this.#items.map(i => i.id))
    for (const id of this.#boxes.keys()) if (!ids.has(id)) this.#boxes.delete(id)
    const all = this.#items.map(item => {
      const box = this.#box(item.id)
      if (box.title.textContent !== item.title) box.title.textContent = item.title
      if (box.note.textContent !== (item.note ?? '')) box.note.textContent = item.note ?? ''
      box.note.hidden = !item.note
      if (box.state.textContent !== (item.state ?? '')) box.state.textContent = item.state ?? ''
      box.state.hidden = !item.state
      box.state.classList.toggle('ok', !!item.good)
      box.state.classList.toggle('muted', !item.good)
      item.panel.classList.add('accordion-panel')
      replaceKeeping(box.details, [box.summary, item.panel])
      return box.details
    })
    replaceKeeping(this, all)
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
