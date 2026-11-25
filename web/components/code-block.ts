import { button, el, ensureId, replaceKeeping, type NumberedLine } from '../lib/dom.ts'
import { newState, tokens, type Lang, type Token } from '../lib/highlight.ts'
import './copy.ts'
import { icon } from './icon.ts'

const SHOWN = 12
const FOLDS_ABOVE = SHOWN + 3

export interface CodeOptions {
  text: string | NumberedLine[]
  label: string
  title?: string
  details?: string
  tags?: string[]
  lang?: Lang | null
  numbers?: boolean
  marked?: ReadonlySet<number>
  diff?: boolean
  wrap?: boolean
  wrapToggle?: boolean
  copy?: { text: string; label: string }
  keep?: 'start' | 'end'
  size?: 'small'
  tone?: 'bad'
  look?: 'terminal'
}

function numbered(text: string): NumberedLine[] {
  const lines = text.split('\n').map((t, i) => ({ text: t, line: i + 1 }))
  if (lines.length > 1 && lines[lines.length - 1]!.text === '') lines.pop()
  return lines
}

function diffKind(text: string): string {
  return text.startsWith('+') ? 'added' : text.startsWith('-') ? 'removed' : ''
}

class SfCode extends HTMLElement {
  static readonly #watch = new ResizeObserver(entries => {
    for (const e of entries) if (e.target instanceof SfCode) e.target.#reach()
  })

  #o: CodeOptions = { text: '', label: '' }
  #lines: NumberedLine[] = []
  #shown = false
  #open = false
  #wrap = false
  readonly #head = el('div', 'code-head')
  readonly #title = el('span', 'code-title')
  readonly #details = el('span')
  readonly #tools = el('span', 'code-tools')
  readonly #wrapButton = button('', 'code-tool')
  readonly #copy = document.createElement('sf-copy')
  readonly #frame = el('div', 'code-frame')
  readonly #pre = el('pre', 'code-body')
  readonly #code = el('code')
  readonly #more = button('', 'code-more')

  constructor() {
    super()
    this.#title.dir = 'ltr'
    this.#pre.dir = 'ltr'
    this.#pre.append(this.#code)
    this.#more.setAttribute('aria-controls', ensureId(this.#pre))
    this.#more.addEventListener('click', () => {
      this.#open = !this.#open
      this.#paint()
      if (!this.#open) this.#more.scrollIntoView({ block: 'nearest' })
    })
    this.#wrapButton.append(icon('wrap'))
    this.#wrapButton.title = 'Wrap long lines'
    this.#wrapButton.setAttribute('aria-label', 'Wrap long lines')
    this.#wrapButton.addEventListener('click', () => {
      this.#wrap = !this.#wrap
      this.#paint()
    })
  }

  connectedCallback() {
    SfCode.#watch.observe(this)
  }

  disconnectedCallback() {
    SfCode.#watch.unobserve(this)
  }

  show(o: CodeOptions): void {
    const first = !this.#shown
    if (first) this.#wrap = !!o.wrap
    this.#shown = true
    this.#o = o
    this.#lines = typeof o.text === 'string' ? numbered(o.text) : o.text
    if (first && o.marked?.size) this.#open = this.#lines.findIndex(l => o.marked!.has(l.line)) >= SHOWN
    const head = o.title !== undefined
    const end = o.keep === 'end'
    this.classList.toggle('code-small', o.size === 'small')
    this.classList.toggle('code-bad', o.tone === 'bad')
    this.classList.toggle('code-terminal', o.look === 'terminal')
    this.classList.toggle('code-numbered', !!o.numbers)
    this.classList.toggle('code-floating', !!o.copy && !head)
    this.classList.toggle('code-end', end)
    if (o.numbers) this.style.setProperty('--digits', String(String(this.#lines.at(-1)?.line ?? 0).length))
    if (o.copy) {
      this.#copy.text = o.copy.text
      this.#copy.label = o.copy.label
    }
    if (head) {
      this.#title.textContent = o.title ?? ''
      this.#details.textContent = o.details ?? ''
      replaceKeeping(this.#head, [
        this.#title,
        ...(o.tags ?? []).map(t => el('span', 'tag warn', t)),
        ...(o.details ? [this.#details] : []),
        ...(o.wrapToggle || o.copy ? [this.#tools] : []),
      ])
      replaceKeeping(this.#tools, [...(o.wrapToggle ? [this.#wrapButton] : []), ...(o.copy ? [this.#copy] : [])])
    }
    replaceKeeping(this.#frame, [this.#pre, ...(o.copy && !head ? [this.#copy] : [])])
    this.#frame.hidden = head && this.#lines.length === 1 && this.#lines[0]!.text === ''
    replaceKeeping(this, [...(head ? [this.#head] : []), ...(end ? [this.#more] : []), this.#frame, ...(end ? [] : [this.#more])])
    this.#paint()
  }

  #paint() {
    const lines = this.#lines
    const n = lines.length
    const folds = n > FOLDS_ABOVE
    const folded = folds && !this.#open
    const end = this.#o.keep === 'end'
    const [from, to] = !folded ? [0, n] : end ? [n - SHOWN - 1, n] : [0, SHOWN + 1]
    const peek = !folded ? -1 : end ? from : to - 1
    this.classList.toggle('code-wrap', this.#wrap)
    this.classList.toggle('code-folded', folded)
    this.#wrapButton.setAttribute('aria-pressed', String(this.#wrap))
    this.#more.hidden = !folds
    this.#more.setAttribute('aria-expanded', String(this.#open))
    this.#more.replaceChildren(icon('chevron'), this.#open ? 'Show less' : `Show all ${lines.at(-1)?.line ?? n} lines`)
    const lang = this.#o.lang
    const state = newState()
    const rows: HTMLElement[] = []
    for (let i = 0; i < to; i++) {
      const parts = lang ? tokens(lines[i]!.text, lang, state) : null
      if (i >= from) rows.push(this.#row(lines[i]!, parts, i === peek, i === n - 1))
    }
    this.#code.replaceChildren(...rows)
    this.#reach()
  }

  #row(line: NumberedLine, parts: Token[] | null, peek: boolean, last: boolean): HTMLElement {
    const o = this.#o
    const kind = o.diff ? diffKind(line.text) : ''
    const row = el(o.marked?.has(line.line) ? 'mark' : 'span', ['code-line', kind, peek ? 'code-peek' : ''].filter(Boolean).join(' '))
    if (o.numbers) {
      const no = el('span', 'code-no', String(line.line))
      no.setAttribute('aria-hidden', 'true')
      row.append(no)
    }
    const text = el('span', 'code-text')
    if (parts) for (const t of parts) text.append(t.kind ? el('span', `syn-${t.kind}`, t.text) : t.text)
    else if (kind) text.append(el('span', 'code-sign', line.text.slice(0, 1)), line.text.slice(1))
    else text.textContent = line.text
    if (!last) text.append('\n')
    row.append(text)
    return row
  }

  #reach() {
    const pre = this.#pre
    const wide = !this.#wrap && pre.scrollWidth > pre.clientWidth
    if (wide) {
      pre.tabIndex = 0
      pre.setAttribute('role', 'region')
      pre.setAttribute('aria-label', this.#o.label)
    } else if (pre !== document.activeElement) {
      for (const name of ['tabindex', 'role', 'aria-label']) pre.removeAttribute(name)
    }
  }
}

customElements.define('sf-code', SfCode)

declare global {
  interface HTMLElementTagNameMap {
    'sf-code': SfCode
  }
}

export function codeBlock(o: CodeOptions): SfCode {
  const block = document.createElement('sf-code')
  block.show(o)
  return block
}
