import { button, el, emit, visible } from '../lib/dom.ts'
import type { FileView } from '../types.ts'
import { icon } from './icon.ts'
import './markdown.ts'

let wrapLines = true
let rendered = true
const MARKDOWN = /\.(md|markdown)$/i
const MAX_LINES = 20000

class SfFile extends HTMLElement {
  #view: FileView | null = null
  #lines: HTMLElement | null = null

  #onKey = (e: KeyboardEvent) => {
    if (this.hidden || !this.#view || !e.altKey || e.ctrlKey || e.metaKey || e.code !== 'KeyZ') return
    e.preventDefault()
    this.#setWrap(!wrapLines)
  }

  connectedCallback() {
    document.addEventListener('keydown', this.#onKey)
  }

  disconnectedCallback() {
    document.removeEventListener('keydown', this.#onKey)
  }

  set view(value: FileView | null) {
    if (value === this.#view) return
    this.#view = value
    this.#render()
  }

  focusContent() {
    this.querySelector<HTMLElement>('.file-lines, .file-doc, .file-note')?.focus({ preventScroll: true })
  }

  #setWrap(on: boolean) {
    wrapLines = on
    this.#lines?.classList.toggle('wrap', on)
  }

  #render() {
    const v = this.#view
    this.#lines = null
    if (!v) {
      this.replaceChildren()
      return
    }
    const crumbs = el('div', 'file-crumbs')
    const parts = v.path.split('/').filter(Boolean)
    parts.forEach((part, i) => {
      if (i) crumbs.append(icon('chevron'))
      crumbs.append(el('span', i === parts.length - 1 ? 'crumb last' : 'crumb', visible(part)))
    })
    const bar = el('div', 'file-bar')
    bar.append(crumbs)
    if (v.note !== undefined || v.text === undefined) {
      const note = el('p', 'file-note')
      note.tabIndex = -1
      note.setAttribute('role', 'status')
      note.append(el('span', '', v.note ?? `Loading ${visible(v.path)}…`))
      if (v.note !== undefined) {
        note.append(button('Try again', 'ghost', () => emit(this, 'sf-reload')))
      }
      this.replaceChildren(bar, note)
      return
    }
    const all = v.text.replace(/\r\n?/g, '\n').replace(/\n$/, '').split('\n')
    const shown = all.slice(0, MAX_LINES)
    const markdown = MARKDOWN.test(v.path)
    if (markdown) bar.append(this.#swap())
    const lines = markdown && rendered ? null : this.#source(v.path, shown)
    this.#lines = lines
    this.replaceChildren(bar, lines ?? this.#doc(v.path, shown))
    if (all.length > shown.length) this.append(el('p', 'file-note', `Showing the first ${MAX_LINES.toLocaleString()} of ${all.length.toLocaleString()} lines.`))
    this.#setWrap(wrapLines)
  }

  #swap(): HTMLElement {
    const swap = button('', 'ghost icon-button file-swap', () => {
      rendered = !rendered
      this.#render()
      this.querySelector<HTMLElement>('.file-swap')?.focus()
    })
    swap.title = rendered ? 'Show source' : 'Show rendered'
    swap.setAttribute('aria-label', swap.title)
    swap.append(icon(rendered ? 'code' : 'book'))
    return swap
  }

  #doc(path: string, shown: string[]): HTMLElement {
    const doc = el('div', 'file-doc')
    doc.setAttribute('role', 'document')
    doc.setAttribute('aria-label', `${path}, rendered, read-only`)
    doc.tabIndex = 0
    const md = el('sf-markdown')
    md.text = shown.join('\n')
    doc.append(md)
    return doc
  }

  #source(path: string, shown: string[]): HTMLElement {
    const lines = el('div', 'file-lines')
    lines.setAttribute('role', 'document')
    lines.setAttribute('aria-label', `${path}, read-only. Alt+Z turns word wrap off and on.`)
    lines.tabIndex = 0
    lines.style.setProperty('--digits', String(String(shown.length).length))
    const frag = document.createDocumentFragment()
    shown.forEach((line, i) => {
      const n = el('span', 'ln', String(i + 1))
      n.setAttribute('aria-hidden', 'true')
      frag.append(n, el('span', 'lc', visible(line) || ' '))
    })
    lines.append(frag)
    return lines
  }
}

customElements.define('sf-file', SfFile)

declare global {
  interface HTMLElementTagNameMap {
    'sf-file': SfFile
  }
}
