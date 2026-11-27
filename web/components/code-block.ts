import { button, el, ensureId, replaceKeeping, type NumberedLine } from '../lib/dom.ts'
import { newState, tokens, type Lang, type Token } from '../lib/highlight.ts'
import './copy.ts'
import { icon } from './icon.ts'

const SHOWN = 12
const FOLDS_ABOVE = SHOWN + 3
const CHUNK = 1000
const CHUNKED_ABOVE = 5000
const DRAWN_AT_MOST = 20000

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

function highlighted(into: HTMLElement, parts: Token[]): void {
  for (const t of parts) into.append(t.kind ? el('span', `syn-${t.kind}`, t.text) : t.text)
}

function reuse(kept: HTMLElement | undefined, fresh: HTMLElement): HTMLElement {
  if (!kept) return fresh
  kept.className = fresh.className
  kept.style.cssText = fresh.style.cssText
  kept.replaceChildren(...fresh.childNodes)
  return kept
}

class SfCode extends HTMLElement {
  static readonly #watch = new ResizeObserver(entries => {
    for (const e of entries) if (e.target instanceof SfCode) e.target.#reach()
  })

  #o: CodeOptions = { text: '', label: '' }
  #lines: NumberedLine[] = []
  #chunks: HTMLElement[] = []
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
  readonly #cut = el('p', 'code-cut')

  constructor() {
    super()
    this.#title.dir = 'ltr'
    this.#pre.dir = 'ltr'
    this.#pre.append(this.#code)
    this.#more.setAttribute('aria-controls', ensureId(this.#pre))
    this.#more.addEventListener('click', () => {
      this.#paint(!this.#open)
      if (!this.#open) this.#more.scrollIntoView({ block: 'nearest' })
    })
    this.#wrapButton.append(icon('wrap'))
    this.#wrapButton.title = 'Wrap long lines'
    this.#wrapButton.setAttribute('aria-label', 'Wrap long lines')
    this.#wrapButton.addEventListener('click', () => {
      this.#wrap = !this.#wrap
      this.#paintWrap()
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
    replaceKeeping(this, [...(head ? [this.#head] : []), ...(end ? [this.#more, this.#cut] : []), this.#frame, ...(end ? [] : [this.#cut, this.#more])])
    this.#paint()
  }

  #paint(open = this.#open) {
    const lines = this.#lines
    const n = lines.length
    const folds = n > FOLDS_ABOVE
    const folded = folds && !open
    const end = this.#o.keep === 'end'
    const capped = n > DRAWN_AT_MOST
    const [from, to] = folded ? (end ? [n - SHOWN - 1, n] : [0, SHOWN + 1]) : !capped ? [0, n] : end ? [n - DRAWN_AT_MOST, n] : [0, DRAWN_AT_MOST]
    const total = lines.at(-1)?.line ?? n
    const unshown = !capped ? 0 : end ? lines[n - DRAWN_AT_MOST]!.line - lines[0]!.line : total - lines[DRAWN_AT_MOST - 1]!.line
    const peek = !folded ? -1 : end ? from : to - 1
    const lang = this.#o.lang
    const state = newState()
    const parts = (i: number) => (lang ? tokens(lines[i]!.text, lang, state) : null)
    for (let i = 0; i < from; i++) parts(i)
    if (to - from > CHUNKED_ABOVE) {
      const built: HTMLElement[] = []
      for (let i = from; i < to; i += CHUNK) built.push(this.#chunk(i, Math.min(i + CHUNK, to), parts))
      this.#chunks = built.map((fresh, k) => reuse(this.#chunks[k], fresh))
      replaceKeeping(this.#code, this.#chunks)
    } else {
      const rows = document.createDocumentFragment()
      for (let i = from; i < to; i++) rows.append(this.#row(lines[i]!, parts(i), i === peek, i === n - 1))
      this.#code.replaceChildren(rows)
      this.#chunks = []
    }
    this.#open = open
    this.classList.toggle('code-folded', folded)
    this.#more.hidden = !folds
    this.#more.setAttribute('aria-expanded', String(open))
    this.#more.replaceChildren(icon('chevron'), open ? 'Show less' : !capped ? `Show all ${total} lines` : `Show the ${end ? 'last' : 'first'} ${(total - unshown).toLocaleString()} of ${total.toLocaleString()} lines`)
    this.#cut.hidden = folded || !capped
    this.#cut.textContent = this.#cut.hidden ? '' : `${unshown.toLocaleString()} ${end ? 'earlier' : 'more'} ${unshown === 1 ? 'line' : 'lines'} not shown.${this.#o.copy ? ' Copy copies all of them.' : ''}`
    this.#paintWrap()
  }

  #paintWrap() {
    this.classList.toggle('code-wrap', this.#wrap)
    this.#wrapButton.setAttribute('aria-pressed', String(this.#wrap))
    this.#reach()
  }

  #chunk(from: number, to: number, parts: (i: number) => Token[] | null): HTMLElement {
    const o = this.#o
    const lines = this.#lines
    const last = lines.length - 1
    const merged = !o.numbers && !o.diff && !o.marked?.size && o.look !== 'terminal'
    const chunk = el('span', merged ? 'code-line code-chunk' : 'code-chunk')
    let cols = 0
    for (let i = from; i < to; i++) cols = Math.max(cols, lines[i]!.text.length)
    chunk.style.setProperty('--rows', String(to - from))
    chunk.style.setProperty('--cols', String(cols))
    if (!merged) {
      for (let i = from; i < to; i++) chunk.append(this.#row(lines[i]!, parts(i), false, i === last))
      return chunk
    }
    const text = el('span', 'code-text')
    if (o.lang) {
      for (let i = from; i < to; i++) {
        highlighted(text, parts(i) ?? [])
        if (i !== last) text.append('\n')
      }
    } else {
      text.textContent = lines.slice(from, to).map(l => l.text).join('\n') + (to - 1 === last ? '' : '\n')
    }
    chunk.append(text)
    return chunk
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
    if (parts) highlighted(text, parts)
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
