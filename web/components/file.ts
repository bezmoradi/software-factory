import { button, el, emit, replaceKeeping, sayWhenShown, statusLine, visible } from '../lib/dom.ts'
import { shortSha } from '../lib/format.ts'
import { langFor, newState, tokens } from '../lib/highlight.ts'
import type { FileView } from '../types.ts'
import { icon } from './icon.ts'
import './markdown.ts'

let wrapLines = true
let rendered = true
const MARKDOWN = /\.(md|markdown)$/i
const MAX_LINES = 20000
const MAX_HIGHLIGHT_CHARS = 400_000

class SfFile extends HTMLElement {
  #view: FileView | null = null
  #lines: HTMLElement | null = null
  #note = el('p', 'file-note')
  #noteSaid = statusLine('', 'span')
  #retry = button('Try again', 'ghost', () => emit(this, 'sf-reload'))

  #onKey = (e: KeyboardEvent) => {
    if (this.hidden || !this.#view || !e.altKey || e.ctrlKey || e.metaKey || e.code !== 'KeyZ') return
    e.preventDefault()
    this.#setWrap(!wrapLines)
  }

  constructor() {
    super()
    this.#note.tabIndex = -1
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
    const at = v.line && v.line > 0 ? v.line : 0
    if (v.commit) bar.append(el('span', 'file-at', `as commit ${shortSha(v.commit)} has it${at ? `, line ${at}` : ''}`))
    if (v.note !== undefined || v.text === undefined) {
      const note = this.#note
      const retried = v.note === undefined && document.activeElement === this.#retry
      sayWhenShown(this.#noteSaid, v.note ?? `Loading ${visible(v.path)}…`)
      replaceKeeping(note, v.note !== undefined ? [this.#noteSaid, this.#retry] : [this.#noteSaid])
      replaceKeeping(this, [bar, note])
      if (retried) note.focus({ preventScroll: true })
      return
    }
    const all = v.text.replace(/\r\n?/g, '\n').replace(/\n$/, '').split('\n')
    const shown = all.slice(0, MAX_LINES)
    const markdown = MARKDOWN.test(v.path)
    if (markdown && !at) bar.append(this.#swap())
    const lines = markdown && rendered && !at ? null : this.#source(v.path, shown, at)
    this.#lines = lines
    this.replaceChildren(bar, lines ?? this.#doc(v.path, shown))
    if (all.length > shown.length) this.append(el('p', 'file-note', `Showing the first ${MAX_LINES.toLocaleString()} of ${all.length.toLocaleString()} lines.`))
    if (at && at > shown.length) this.append(el('p', 'file-note', `The finding names line ${at.toLocaleString()}, but the file shown has ${shown.length.toLocaleString()} lines.`))
    this.#setWrap(wrapLines)
    const target = lines?.querySelector<HTMLElement>('.lc.at')
    if (target) requestAnimationFrame(() => target.scrollIntoView({ block: 'center', inline: 'nearest' }))
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

  #source(path: string, shown: string[], at = 0): HTMLElement {
    const lines = el('div', 'file-lines')
    lines.setAttribute('role', 'document')
    lines.setAttribute('aria-label', `${path}, read-only. Alt+Z turns word wrap off and on.`)
    lines.tabIndex = 0
    lines.style.setProperty('--digits', String(String(shown.length).length))
    const frag = document.createDocumentFragment()
    const lang = langFor(path)
    const state = newState()
    let budget = MAX_HIGHLIGHT_CHARS
    shown.forEach((line, i) => {
      const here = i + 1 === at
      const n = el('span', here ? 'ln at' : 'ln', String(i + 1))
      n.setAttribute('aria-hidden', 'true')
      budget -= line.length
      const parts = lang && line && budget >= 0 ? tokens(line, lang, state) : null
      const c = el('span', here ? 'lc at' : 'lc')
      if (parts) for (const t of parts) c.append(t.kind ? el('span', `syn-${t.kind}`, visible(t.text)) : visible(t.text))
      else c.textContent = visible(line) || ' '
      if (here) c.setAttribute('aria-current', 'location')
      frag.append(n, c)
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
