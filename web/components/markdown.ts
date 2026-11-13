import { el, emit, named, unspoofed } from '../lib/dom.ts'

const FENCE = /^ {0,3}(`{3,}|~{3,})(.*)$/
const HEADING = /^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$/
const RULE = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/
const SETEXT = /^ {0,3}(=+|-+)[ \t]*$/
const QUOTE = /^ {0,3}> ?(.*)$/
const ITEM = /^( {0,3})([-*+]|\d{1,9}[.)])(?:([ \t]+)(.*))?$/
const DELIM = /^ {0,3}\|?[ \t]*:?-+:?[ \t]*(?:\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$/
const TASK = /^\[([ xX])\][ \t]+/
const PUNCT = /^[!-/:-@[-`{-~]$/
const URL_TEXT = /https?:\/\/[^\s<]+/g
const AUTOLINK = /<(https?:\/\/[^\s<>]+)>/iy

interface LinkMatch {
  label: string
  href: string
  title: string
  end: number
}

function startsBlock(line: string): boolean {
  return FENCE.test(line) || HEADING.test(line) || RULE.test(line) || QUOTE.test(line) || ITEM.test(line)
}

function safeUrl(href: string): string {
  try {
    const url = new URL(href)
    return url.protocol === 'http:' || url.protocol === 'https:' ? url.href : ''
  } catch {
    return ''
  }
}

function anchor(href: string, title?: string) {
  const a = el('a')
  a.href = href
  a.target = '_blank'
  a.rel = 'noopener noreferrer'
  if (title) a.title = title
  return a
}

function trimUrl(url: string): string {
  let end = url.length
  while (end && /[.,:;!?'"*_~]/.test(url[end - 1]!)) end--
  let cut = url.slice(0, end)
  while (cut.endsWith(')') && (cut.match(/\(/g) || []).length < (cut.match(/\)/g) || []).length) cut = cut.slice(0, -1)
  return cut
}

function plain(parent: HTMLElement, text: string, links: boolean) {
  if (!links) return parent.append(text)
  let last = 0
  for (const m of text.matchAll(URL_TEXT)) {
    const url = trimUrl(m[0])
    const href = safeUrl(url)
    if (!href) continue
    if (m.index > last) parent.append(text.slice(last, m.index))
    const a = anchor(href)
    a.textContent = url
    parent.append(a)
    last = m.index + url.length
  }
  if (last < text.length) parent.append(text.slice(last))
}

function closingTicks(text: string, from: number, size: number): number {
  for (let j = text.indexOf('`', from); j >= 0; j = text.indexOf('`', j)) {
    let k = j
    while (text[k] === '`') k++
    if (k - j === size) return j
    j = k
  }
  return -1
}

function linkEnd(text: string, i: number): LinkMatch | null {
  let depth = 0
  let j = i
  const limit = Math.min(text.length, i + 2000)
  for (; j < limit; j++) {
    if (text[j] === '\\') j++
    else if (text[j] === '`') {
      let k = j
      while (text[k] === '`') k++
      const close = closingTicks(text, k, k - j)
      j = close < 0 ? k - 1 : close + k - j - 1
    } else if (text[j] === '[') depth++
    else if (text[j] === ']' && --depth === 0) break
  }
  if (j >= limit || text[j + 1] !== '(') return null
  const rest = text.slice(j + 2)
  const m = /^[ \t]*(?:<([^<>\n]*)>|((?:[^\s()\\]|\\.|\((?:[^\s()\\]|\\.)*\))*))(?:[ \t]+("[^"]*"|'[^']*'|\([^)]*\)))?[ \t]*\)/.exec(rest)
  if (!m) return null
  return { label: text.slice(i + 1, j), href: (m[1] ?? m[2])!.replace(/\\(.)/g, '$1'), title: m[3] ? m[3].slice(1, -1) : '', end: j + 2 + m[0].length }
}

function emphasis(text: string, i: number, c: string, run: number, dead: Map<string, number>): { n: number; inner: string; end: number } | null {
  const n = run
  if (n > (c === '~' ? 2 : 3) || dead.get(c + n)! <= i) return null
  const after = text[i + run]
  if (!after || /\s/.test(after)) return null
  if (c === '_' && /[\p{L}\p{N}]/u.test(text[i - 1] ?? '')) return null
  for (let j = i + run; j < text.length; ) {
    if (text[j] === '\\') {
      j += 2
      continue
    }
    if (text[j] === '`') {
      let k = j
      while (text[k] === '`') k++
      const close = closingTicks(text, k, k - j)
      j = close < 0 ? k : close + k - j
      continue
    }
    if (text[j] !== c) {
      j++
      continue
    }
    let k = j
    while (text[k] === c) k++
    const fits = k - j === n && !/\s/.test(text[j - 1]!) && !(c === '_' && /[\p{L}\p{N}]/u.test(text[k] ?? ''))
    if (fits && j > i + run) return { n, inner: text.slice(i + n, j), end: k }
    j = k
  }
  dead.set(c + n, i)
  return null
}

function wrap(c: string, n: number): [HTMLElement, HTMLElement] {
  if (n === 3 && c !== '~') {
    const strong = el('strong')
    const em = el('em')
    strong.append(em)
    return [strong, em]
  }
  const node = el(c === '~' ? 'del' : n === 2 ? 'strong' : 'em')
  return [node, node]
}

function target(label: string, link: LinkMatch, href: string): HTMLElement | null {
  const parts = label.trim() === link.href || label.trim() === href ? [] : [link.href]
  if (link.title) parts.push(`"${link.title}"`)
  return parts.length ? el('span', 'muted link-target', ` (${parts.join(' ')})`) : null
}

function inline(parent: HTMLElement, text: string, links: boolean, exact: boolean) {
  const dead = new Map<string, number>()
  const noTicks = new Set<number>()
  let buf = ''
  const flush = () => {
    if (buf) plain(parent, buf, links)
    buf = ''
  }
  let i = 0
  let link: LinkMatch | null = null
  while (i < text.length) {
    const c = text[i]!
    if (c === '\\' && PUNCT.test(text[i + 1] ?? '')) {
      buf += text[i + 1]
      i += 2
    } else if (c === '\\' && text[i + 1] === '\n') {
      buf += '\n'
      i += 2
    } else if (c === '`') {
      let k = i
      while (text[k] === '`') k++
      const close = noTicks.has(k - i) ? -1 : closingTicks(text, k, k - i)
      if (close < 0) {
        noTicks.add(k - i)
        buf += text.slice(i, k)
        i = k
        continue
      }
      flush()
      let code = text.slice(k, close).replace(/\n/g, ' ')
      if (code.length > 2 && code.startsWith(' ') && code.endsWith(' ') && code.trim()) code = code.slice(1, -1)
      parent.append(el('code', '', code))
      i = close + k - i
    } else if (c === '<' && links && ((AUTOLINK.lastIndex = i), AUTOLINK.test(text))) {
      const url = text.slice(i + 1, AUTOLINK.lastIndex - 1)
      const href = safeUrl(url)
      flush()
      if (href) {
        const a = anchor(href)
        a.textContent = url
        parent.append(a)
      } else parent.append(url)
      i += url.length + 2
    } else if (c === '!' && text[i + 1] === '[' && (link = linkEnd(text, i + 1))) {
      buf += text.slice(i, link.end)
      i = link.end
    } else if (c === '[' && (link = linkEnd(text, i))) {
      const href = links && safeUrl(link.href)
      flush()
      if (href) {
        const a = anchor(href, link.title)
        inline(a, link.label, false, exact)
        const shown = (a.textContent || '').trim()
        if (shown !== href && shown !== link.href) a.title = link.title ? `${link.title}\n${href}` : href
        parent.append(a)
        const after = exact && target(shown, link, href)
        if (after) parent.append(after)
      } else {
        const span = el('span')
        if (link.href) span.title = link.href
        inline(span, link.label, links, exact)
        parent.append(span)
        const after = exact && target(span.textContent || '', link, link.href)
        if (after) parent.append(after)
      }
      i = link.end
    } else if (c === '*' || c === '_' || c === '~') {
      let k = i
      while (text[k] === c) k++
      const hit = emphasis(text, i, c, k - i, dead)
      if (!hit) {
        buf += text.slice(i, k)
        i = k
        continue
      }
      flush()
      const [outer, innermost] = wrap(c, hit.n)
      inline(innermost, hit.inner, links, exact)
      parent.append(outer)
      i = hit.end
    } else {
      buf += c
      i++
    }
  }
  flush()
}

function cells(line: string): string[] {
  let s = line.trim()
  if (s.startsWith('|')) s = s.slice(1)
  if (s.endsWith('|') && !s.endsWith('\\|')) s = s.slice(0, -1)
  const out = ['']
  for (let k = 0; k < s.length; k++) {
    if (s[k] === '\\' && s[k + 1] === '|') {
      out[out.length - 1]! += '|'
      k++
    } else if (s[k] === '|') out.push('')
    else out[out.length - 1]! += s[k]
  }
  return out.map(c => c.trim())
}

function table(parent: HTMLElement, head: string[], delim: string[], rows: string[][], exact: boolean) {
  const align = delim.map(d => (d.startsWith(':') && d.endsWith(':') ? 'center' : d.endsWith(':') ? 'right' : ''))
  const row = (values: string[], tag: 'th' | 'td') => {
    const tr = el('tr')
    for (let k = 0; k < Math.max(head.length, values.length); k++) {
      const cell = el(tag, align[k])
      inline(cell, values[k] ?? '', true, exact)
      tr.append(cell)
    }
    return tr
  }
  const t = el('table')
  const thead = el('thead')
  thead.append(row(head, 'th'))
  t.append(thead)
  if (rows.length) {
    const tbody = el('tbody')
    for (const values of rows) tbody.append(row(values, 'td'))
    t.append(tbody)
  }
  const box = el('div', 'table')
  box.append(t)
  parent.append(box)
}

function item(lines: string[], i: number, bullet: string, ordered: boolean) {
  const m = ITEM.exec(lines[i]!)!
  const gap = m[3] ? m[3].length : 1
  const pad = m[1]!.length + m[2]!.length + (gap > 4 ? 1 : gap)
  const body = [m[4] === undefined ? '' : (gap > 4 ? ' '.repeat(gap - 1) : '') + m[4]]
  i++
  let blank = false
  while (i < lines.length) {
    const line = lines[i]!
    if (!line.trim()) {
      blank = true
      body.push('')
      i++
    } else if (line.length - line.trimStart().length >= pad) {
      body.push(line.slice(pad))
      blank = false
      i++
    } else if (!blank && !startsBlock(line) && body[body.length - 1]!.trim()) {
      body.push(line.trim())
      i++
    } else break
  }
  let trailing = false
  while (body.length > 1 && !body[body.length - 1]!.trim()) {
    body.pop()
    trailing = true
  }
  const next = i < lines.length ? ITEM.exec(lines[i]!) : null
  const same = next && !RULE.test(lines[i]!) && (ordered ? /\d/.test(next[2]!) && next[2]!.slice(-1) === bullet : next[2] === bullet)
  const loose = (trailing && same) || body.slice(1).some((l, k) => !l.trim() && body[k]!.trim() && !FENCE.test(body[k]!))
  return { body, end: i, loose, more: Boolean(same), number: ordered ? parseInt(m[2]!, 10) : 0 }
}

function list(parent: HTMLElement, lines: string[], i: number, exact: boolean): number {
  const first = ITEM.exec(lines[i]!)!
  const ordered = /\d/.test(first[2]!)
  const bullet = ordered ? first[2]!.slice(-1) : first[2]!
  const node = el(ordered ? 'ol' : 'ul')
  if (ordered && parseInt(first[2]!, 10) !== 1) (node as HTMLOListElement).start = parseInt(first[2]!, 10)
  const items: { body: string[]; number: number }[] = []
  let loose = false
  for (;;) {
    const it = item(lines, i, bullet, ordered)
    items.push(it)
    loose = loose || it.loose
    i = it.end
    if (!it.more) break
  }
  for (const { body, number } of items) {
    const li = el('li')
    if (exact && ordered) li.value = number
    const task = TASK.exec(body[0]!)
    if (task) body[0] = body[0]!.slice(task[0].length)
    render(li, body, exact)
    if (!loose)
      for (const p of [...li.children])
        if (p.tagName === 'P') {
          if (p.previousSibling) p.before('\n')
          p.replaceWith(...p.childNodes)
        }
    if (task) {
      const box = el('input')
      box.type = 'checkbox'
      box.disabled = true
      box.checked = task[1] !== ' '
      box.toggleAttribute('checked', box.checked)
      box.setAttribute('aria-label', (li.textContent || '').trim().slice(0, 200) || 'Task')
      li.className = 'task'
      ;(loose && li.firstElementChild?.tagName === 'P' ? li.firstElementChild : li).prepend(box)
    }
    node.append(li)
  }
  parent.append(node)
  return i
}

function render(parent: HTMLElement, lines: string[], exact: boolean) {
  let para: string[] = []
  const flush = () => {
    if (!para.length) return
    const p = el('p')
    inline(p, para.join('\n'), true, exact)
    parent.append(p)
    para = []
  }
  let i = 0
  while (i < lines.length) {
    const line = lines[i]!
    let m: RegExpExecArray | null
    if (!line.trim()) {
      flush()
      i++
    } else if (para.length && (m = SETEXT.exec(line)) && !(m[1]![0] === '-' && ITEM.test(line) && m[1]!.length === 1)) {
      const h = el(m[1]![0] === '=' ? 'h1' : 'h2')
      inline(h, para.join('\n'), true, exact)
      para = []
      parent.append(h)
      i++
    } else if (para.length && line.includes('|') && DELIM.test(line) && cells(para[para.length - 1]!).length === cells(line).length) {
      const head = cells(para.pop()!)
      const delim = cells(line)
      flush()
      const rows: string[][] = []
      i++
      while (i < lines.length && lines[i]!.trim() && !startsBlock(lines[i]!)) rows.push(cells(lines[i++]!))
      table(parent, head, delim, rows, exact)
    } else if ((m = FENCE.exec(line)) && !(m[1]![0] === '`' && m[2]!.includes('`'))) {
      flush()
      const close = new RegExp(`^ {0,3}${m[1]![0]}{${m[1]!.length},}[ \\t]*$`)
      const indent = line.length - line.trimStart().length
      const body: string[] = []
      i++
      while (i < lines.length && !close.test(lines[i]!)) body.push(lines[i++]!.replace(new RegExp(`^ {0,${indent}}`), ''))
      i++
      const info = m[2]!.trim()
      if (exact && info) parent.append(el('p', 'small muted fence-info', info))
      const pre = el('pre')
      pre.append(el('code', '', body.join('\n')))
      parent.append(pre)
    } else if ((m = HEADING.exec(line))) {
      flush()
      const h = el(`h${m[1]!.length}`)
      inline(h, m[2] ?? '', true, exact)
      parent.append(h)
      i++
    } else if (RULE.test(line)) {
      flush()
      parent.append(el('hr'))
      i++
    } else if (QUOTE.test(line)) {
      flush()
      const inner: string[] = []
      while (i < lines.length) {
        const q = QUOTE.exec(lines[i]!)
        if (q) inner.push(q[1]!)
        else if (lines[i]!.trim() && inner.length && inner[inner.length - 1]!.trim() && !startsBlock(lines[i]!)) inner.push(lines[i]!)
        else break
        i++
      }
      const quote = el('blockquote')
      render(quote, inner, exact)
      parent.append(quote)
    } else if ((m = ITEM.exec(line)) && (!para.length || (m[4]?.trim() && (!/\d/.test(m[2]!) || parseInt(m[2]!, 10) === 1)))) {
      flush()
      i = list(parent, lines, i, exact)
    } else {
      para.push(line.trim())
      i++
    }
  }
  flush()
}

const SCROLLED = ':is(pre, .table)[role="region"]'
const REACH = ['tabindex', 'role', 'aria-label']

function undecorated(n: ChildNode): Node {
  if (!(n instanceof Element) || !n.matches(SCROLLED) && !n.querySelector(`.file-ref, ${SCROLLED}`)) return n
  const copy = n.cloneNode(true) as Element
  for (const box of [copy, ...copy.querySelectorAll(SCROLLED)]) if (box.matches(SCROLLED)) for (const name of REACH) box.removeAttribute(name)
  for (const code of copy.querySelectorAll('.file-ref')) {
    code.classList.remove('file-ref')
    if (!code.classList.length) code.removeAttribute('class')
    for (const name of ['data-file', 'tabindex', 'role', 'title']) code.removeAttribute(name)
  }
  return copy
}

function linesOf(text: string): string[] {
  return text.split(/\r?\n/).map(l => l.replace(/^\t+/, t => '    '.repeat(t.length)))
}

export function headings(text: string, level: number): string[] {
  const root = el('div')
  try {
    render(root, linesOf(text), false)
  } catch {
    return []
  }
  return [...root.querySelectorAll(`:scope > h${level}`)].map(h => (h.textContent ?? '').trim())
}

function paint(node: HTMLElement, text: string, exact: boolean) {
  const next = el('div')
  try {
    render(next, exact ? linesOf(text).map(named) : linesOf(unspoofed(text)), exact)
  } catch {
    next.replaceChildren(el('p', '', exact ? named(text) : unspoofed(text)))
  }
  const old = [...node.childNodes]
  const fresh = [...next.childNodes]
  let same = 0
  while (same < old.length && same < fresh.length && undecorated(old[same]!).isEqualNode(fresh[same]!)) same++
  for (const n of old.slice(same)) n.remove()
  node.append(...fresh.slice(same))
}

class SfMarkdown extends HTMLElement {
  #text = ''
  #exact = false
  #frame = 0
  #watch = new ResizeObserver(() => this.#reach())

  connectedCallback() {
    this.#watch.observe(this)
  }

  disconnectedCallback() {
    this.#watch.disconnect()
  }

  get text(): string {
    return this.#text
  }

  set text(value: unknown) {
    this.#text = String(value ?? '')
    this.#schedule()
  }

  get exact(): boolean {
    return this.#exact
  }

  set exact(value: unknown) {
    if (this.#exact === Boolean(value)) return
    this.#exact = Boolean(value)
    this.#schedule()
  }

  #schedule() {
    if (!this.#frame) this.#frame = requestAnimationFrame(() => this.#paint())
  }

  #paint() {
    this.#frame = 0
    paint(this, this.#text, this.#exact)
    this.#reach()
    emit(this, 'sf-paint')
  }

  #reach() {
    for (const box of this.querySelectorAll<HTMLElement>('pre, .table')) {
      const wide = box.scrollWidth > box.clientWidth
      if (wide === box.hasAttribute('role') || (!wide && box === document.activeElement)) continue
      if (wide) {
        box.tabIndex = 0
        box.setAttribute('role', 'region')
        box.setAttribute('aria-label', box.matches('pre') ? 'Code' : 'Table')
      } else for (const name of REACH) box.removeAttribute(name)
    }
  }
}

customElements.define('sf-markdown', SfMarkdown)

declare global {
  interface HTMLElementTagNameMap {
    'sf-markdown': SfMarkdown
  }
}
