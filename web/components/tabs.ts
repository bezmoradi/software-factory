import { button, el, emit, rovingKeys } from '../lib/dom.ts'
import type { Tab, TabsState } from '../types.ts'

class SfTabs extends HTMLElement {
  #tabs: Tab[] = []
  #active = ''
  #key = ''
  #dragged = ''
  #targets: HTMLElement[] = []

  constructor() {
    super()
    this.addEventListener('click', e => {
      const tab = (e.target as Element).closest<HTMLElement>('.tab')
      if (!tab) return
      if ((e.target as Element).closest('.x')) return this.#emit('sf-close', tab.dataset.id)
      this.#emit('sf-open', tab.dataset.id)
    })
    this.addEventListener('mousedown', e => {
      if (e.button === 1 && (e.target as Element).closest('.tab')) e.preventDefault()
    })
    this.addEventListener('auxclick', e => {
      const tab = (e.target as Element).closest<HTMLElement>('.tab')
      if (e.button === 1 && tab?.querySelector('.x')) this.#emit('sf-close', tab.dataset.id)
    })
    this.addEventListener('wheel', e => {
      if (e.ctrlKey || Math.abs(e.deltaY) <= Math.abs(e.deltaX) || this.scrollWidth <= this.clientWidth) return
      e.preventDefault()
      this.scrollLeft += e.deltaY
    }, { passive: false })
    this.addEventListener('dragstart', e => {
      const tab = (e.target as Element).closest<HTMLElement>('.tab')
      if (!tab?.draggable || !e.dataTransfer) return
      this.#dragged = tab.dataset.id ?? ''
      e.dataTransfer.effectAllowed = 'move'
      e.dataTransfer.setData('application/x-sf-tab', tab.dataset.id ?? '')
      tab.classList.add('dragged')
    })
    this.addEventListener('dragover', e => {
      const tab = (e.target as Element).closest<HTMLElement>('.tab')
      if (!this.#dragged || !tab) return
      e.preventDefault()
      if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
      this.#mark(this.#gap(e, tab))
    })
    this.addEventListener('dragleave', e => {
      if (!this.contains(e.relatedTarget as Node | null)) this.#mark(null)
    })
    this.addEventListener('drop', e => {
      const tab = (e.target as Element).closest<HTMLElement>('.tab')
      const id = this.#dragged
      if (!id || !tab) return
      e.preventDefault()
      const gap = this.#gap(e, tab)
      this.#mark(null)
      if (gap) this.#emitMove(id, gap.index)
    })
    this.addEventListener('dragend', () => {
      this.#dragged = ''
      this.#mark(null)
      this.querySelector('.tab.dragged')?.classList.remove('dragged')
    })
    this.addEventListener('keydown', e => {
      const tab = (e.target as Element).closest<HTMLElement>('.tab')
      if (!tab) return
      const all = [...this.querySelectorAll<HTMLElement>('.tab')]
      if (e.ctrlKey && e.shiftKey && (e.key === 'PageUp' || e.key === 'PageDown') && tab.draggable) {
        e.preventDefault()
        const at = all.indexOf(tab)
        this.#emitMove(tab.dataset.id ?? '', e.key === 'PageUp' ? at - 1 : at + 2)
        return
      }
      const next = rovingKeys(e, all, all.indexOf(tab), 'horizontal')
      if (next) this.#emit('sf-open', next.dataset.id)
      else if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault()
        this.#emit('sf-open', tab.dataset.id)
      } else if ((e.key === 'Delete' || e.key === 'Backspace') && tab.querySelector('.x')) {
        e.preventDefault()
        this.#emit('sf-close', tab.dataset.id)
      }
    })
  }

  connectedCallback() {
    this.setAttribute('role', 'tablist')
    this.setAttribute('aria-label', 'Open views')
  }

  set state({ tabs, active }: TabsState) {
    const key = JSON.stringify([tabs, active])
    if (key === this.#key) return
    this.#key = key
    this.#tabs = tabs
    this.#active = active
    this.#render()
  }

  #gap(e: DragEvent, tab: HTMLElement): { index: number; left: HTMLElement | null; right: HTMLElement | null } | null {
    const all = [...this.querySelectorAll<HTMLElement>('.tab')]
    const at = all.indexOf(tab)
    const r = tab.getBoundingClientRect()
    const index = e.clientX - r.left <= r.width / 2 ? at : at + 1
    const first = all.findIndex(t => t.draggable)
    if (first < 0 || index < first) return null
    return { index, left: all[index - 1] ?? null, right: all[index] ?? null }
  }

  #mark(gap: { left: HTMLElement | null; right: HTMLElement | null } | null) {
    for (const t of this.#targets) t.classList.remove('drop-target-left', 'drop-target-right')
    this.#targets = []
    if (!gap) return
    if (gap.left) gap.left.classList.add('drop-target-left')
    if (gap.right) gap.right.classList.add('drop-target-right')
    this.#targets = [gap.left, gap.right].filter((t): t is HTMLElement => !!t)
  }

  #emitMove(id: string, index: number) {
    if (id) emit(this, 'sf-move', { id, index })
  }

  #emit(name: 'sf-open' | 'sf-close', id: string | undefined) {
    if (id === undefined) return
    emit(this, name, { id })
  }

  #render() {
    const focused = this.contains(document.activeElement) ? document.activeElement!.closest<HTMLElement>('.tab')?.dataset.id : null
    this.replaceChildren(...this.#tabs.map(t => this.#tab(t)))
    const active = this.querySelector<HTMLElement>('.tab.active')
    active?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
    if (focused) (this.querySelector<HTMLElement>(`.tab[data-id="${CSS.escape(focused)}"]`) || active)?.focus()
  }

  #tab(t: Tab) {
    const on = t.id === this.#active
    const tab = el('div', `tab${on ? ' active' : ''}${t.need ? ' need' : ''}${t.closable ? '' : ' pinned'}`)
    tab.dataset.id = t.id
    tab.setAttribute('role', 'tab')
    tab.setAttribute('aria-selected', String(on))
    const panel = this.getAttribute('panel')
    if (panel) tab.setAttribute('aria-controls', panel)
    tab.tabIndex = on ? 0 : -1
    tab.draggable = t.closable
    const title = [t.need ? 'Needs you: a card is waiting for your answer' : '', t.title ?? ''].filter(Boolean).join('\n')
    if (title) tab.title = title
    tab.append(el('span', 'caption', t.label))
    const notes = [t.need ? 'needs you' : '', t.closable ? 'Delete closes it; Control Shift Page Up or Page Down moves it' : ''].filter(Boolean)
    if (notes.length) tab.setAttribute('aria-description', notes.join('; '))
    if (t.closable) {
      const x = button('', 'x')
      x.tabIndex = -1
      x.setAttribute('aria-hidden', 'true')
      x.title = t.need ? 'Close the tab (Delete). The session keeps running and its card keeps waiting.' : 'Close the tab (Delete). The session keeps running.'
      x.append(el('span', 'close', '×'), el('span', 'flag', '●'))
      tab.append(x)
    } else if (t.need) {
      const flag = el('span', 'flag', '●')
      flag.setAttribute('aria-hidden', 'true')
      tab.append(flag)
    }
    return tab
  }
}

customElements.define('sf-tabs', SfTabs)

declare global {
  interface HTMLElementTagNameMap {
    'sf-tabs': SfTabs
  }
}
