import { busy, button, el, emit, field, fileLink, focusedKey, refocus, remembered, replaceKeeping, statusLine, visible } from '../lib/dom.ts'
import { plural, shortSha } from '../lib/format.ts'
import { langFor, newState, tokens, type LexState, type Token } from '../lib/highlight.ts'
import type { ChangeFile, ChangeFrom, Changes } from '../types.ts'

const MARK: Record<string, string> = { added: 'A', changed: 'M', deleted: 'D' }
const MAX_COMMENTS = 50
const MAX_COMMENT = 2000
const MAX_MESSAGE = 20000
const MAX_HIGHLIGHT = 20000
const MAX_HIGHLIGHT_CHARS = 400_000
const CHUNK_ROWS = 50
const LAYOUT_KEY = 'sf-diff-layout'
const SHA = /^[0-9a-f]{40}$/
const NOT_DIFFED: Record<string, string> = { large: 'over the 1 MiB a file may have to be diffed', budget: 'past the 8 MiB the view reads in all', binary: 'binary, its lines counted', unreadable: 'its stored object couldn’t be read' }

function sizeWords(n: number): string {
  return n < 1024 ? `${n} bytes` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} KiB` : `${(n / (1 << 20)).toFixed(1)} MiB`
}

type RowKind = 'file' | 'meta' | 'hunk' | 'added' | 'removed' | 'same'
type Layout = { split: boolean; highlight: boolean }

interface DiffRow {
  kind: RowKind
  text: string
  file: string
  line: number
  old: number
  tokens?: Token[]
}

interface LineComment {
  file: string
  line: number
  removed: boolean
  text: string
}

type SideRow = { whole: number } | { left: number; right: number }

const POINTS: Record<ChangeFrom, string> = {
  fork: 'Where the branch leaves main',
  reviewed: 'The last reviewed commit',
  turn: 'Before the turn the session last reported',
  commit: 'A commit you choose',
}

function unquoted(text: string): string {
  try {
    const v: unknown = JSON.parse(text)
    return typeof v === 'string' ? v : ''
  } catch {
    return ''
  }
}

function headerPath(rest: string): string {
  if (rest.startsWith('"')) {
    const at = rest.lastIndexOf(' "')
    const to = at > 0 ? unquoted(rest.slice(at + 1)) : ''
    return to.startsWith('b/') ? to.slice(2) : ''
  }
  const half = (rest.length - 1) / 2
  if (!Number.isInteger(half) || rest[half] !== ' ') return ''
  const a = rest.slice(0, half)
  const b = rest.slice(half + 1)
  return a.startsWith('a/') && b.startsWith('b/') && a.slice(2) === b.slice(2) ? b.slice(2) : ''
}

function diffRows(diff: string): DiffRow[] {
  const rows: DiffRow[] = []
  let file = ''
  let from = ''
  let oldLine = 0
  let newLine = 0
  let oldLeft = 0
  let newLeft = 0
  const unquote = (name: string, side: string) => (name.startsWith(side) ? name.slice(side.length) : name)
  for (const raw of diff.split('\n')) {
    const inHunk = oldLeft > 0 || newLeft > 0
    if (inHunk && raw.startsWith('+') && newLeft > 0) {
      rows.push({ kind: 'added', text: raw, file, line: newLine++, old: 0 })
      newLeft--
      continue
    }
    if (inHunk && raw.startsWith('-') && oldLeft > 0) {
      rows.push({ kind: 'removed', text: raw, file, line: oldLine, old: oldLine++ })
      oldLeft--
      continue
    }
    if (inHunk && (raw.startsWith(' ') || raw === '') && oldLeft > 0 && newLeft > 0) {
      rows.push({ kind: 'same', text: raw, file, line: newLine++, old: oldLine++ })
      oldLeft--
      newLeft--
      continue
    }
    const hunk = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/.exec(raw)
    if (hunk) {
      oldLine = Number(hunk[1])
      oldLeft = hunk[2] === undefined ? 1 : Number(hunk[2])
      newLine = Number(hunk[3])
      newLeft = hunk[4] === undefined ? 1 : Number(hunk[4])
      rows.push({ kind: 'hunk', text: raw, file, line: 0, old: 0 })
      continue
    }
    if (raw.startsWith('diff --git ')) {
      file = headerPath(raw.slice('diff --git '.length))
      from = ''
      oldLeft = newLeft = 0
      rows.push({ kind: 'file', text: raw, file, line: 0, old: 0 })
      continue
    }
    if (raw.startsWith('--- ')) from = unquote(raw.slice(4), 'a/')
    else if (raw.startsWith('+++ ')) {
      const to = raw.slice(4)
      file = to === '/dev/null' ? from : unquote(to, 'b/')
      for (let i = rows.length - 1; i >= 0; i--) {
        const r = rows[i]!
        r.file = file
        if (r.kind === 'file') break
      }
    }
    rows.push({ kind: 'meta', text: raw, file, line: 0, old: 0 })
  }
  return rows
}

function highlightRows(rows: DiffRow[]) {
  let budget = MAX_HIGHLIGHT_CHARS
  let oldSide: LexState = newState()
  let newSide: LexState = newState()
  let file = '\u0000'
  let lang = langFor('')
  for (const r of rows) {
    if (r.file !== file) {
      file = r.file
      lang = langFor(file)
    }
    if (r.kind === 'hunk' || r.kind === 'file') {
      oldSide = newState()
      newSide = newState()
    }
    if (!lang || (r.kind !== 'added' && r.kind !== 'removed' && r.kind !== 'same')) continue
    const code = r.text.slice(1)
    budget -= code.length
    if (budget < 0) {
      r.tokens = [{ kind: '', text: code }]
      continue
    }
    if (r.kind === 'removed') r.tokens = tokens(code, lang, oldSide)
    else {
      r.tokens = tokens(code, lang, newSide)
      if (r.kind === 'same') oldSide = { ...newSide }
    }
  }
}

function sideRows(rows: DiffRow[]): SideRow[] {
  const out: SideRow[] = []
  let i = 0
  while (i < rows.length) {
    const r = rows[i]!
    if (r.kind === 'same') {
      out.push({ left: i, right: i })
      i++
    } else if (r.kind === 'removed' || r.kind === 'added') {
      const removed: number[] = []
      const added: number[] = []
      while (i < rows.length && rows[i]!.kind === 'removed') removed.push(i++)
      while (i < rows.length && rows[i]!.kind === 'added') added.push(i++)
      for (let k = 0; k < Math.max(removed.length, added.length); k++) out.push({ left: removed[k] ?? -1, right: added[k] ?? -1 })
    } else {
      out.push({ whole: i })
      i++
    }
  }
  return out
}

function fileSections(nodes: HTMLElement[]): HTMLElement[] {
  const sections: HTMLElement[] = []
  let chunk: HTMLElement | null = null
  for (const node of nodes) {
    const header = node.classList.contains('file')
    if (header || !sections.length) {
      sections.push(el('div', 'diff-file'))
      chunk = null
    }
    const section = sections[sections.length - 1]!
    if (header) {
      section.append(node)
      continue
    }
    if (!chunk || chunk.childElementCount >= CHUNK_ROWS) {
      chunk = el('div', 'diff-chunk')
      section.append(chunk)
    }
    chunk.append(node)
  }
  return sections
}

function codeOf(r: DiffRow, highlight: boolean): HTMLElement {
  const code = el('span', 'diff-code')
  const body = r.kind === 'added' || r.kind === 'removed' || r.kind === 'same' ? r.text.slice(1) : r.text
  if (highlight && r.tokens) {
    for (const t of r.tokens) {
      if (t.kind) code.append(el('span', `syn-${t.kind}`, visible(t.text)))
      else code.append(visible(t.text))
    }
  } else code.textContent = visible(body)
  return code
}

function signOf(r: DiffRow): HTMLElement {
  return el('span', 'diff-sign', r.kind === 'added' ? '+' : r.kind === 'removed' ? '−' : '')
}

function numberOf(n: number): HTMLElement {
  const no = el('span', 'diff-no', n ? String(n) : '')
  no.setAttribute('aria-hidden', 'true')
  return no
}

function commentMessage(comments: LineComment[]): string {
  const lines = comments.map(c => `- ${c.file} line ${c.line}${c.removed ? ' (a removed line, numbered in the old file)' : ''}: ${c.text.replace(/\s*\n\s*/g, ' ').trim()}`)
  return [`My comments on lines of the change, ${comments.length === 1 ? 'one' : comments.length} in all:`, '', ...lines].join('\n')
}

function savedLayout(): Layout {
  try {
    const v: unknown = JSON.parse(sessionStorage.getItem(LAYOUT_KEY) || '{}')
    const r = v && typeof v === 'object' ? (v as Record<string, unknown>) : {}
    return { split: r.split === true, highlight: r.highlight !== false }
  } catch {
    return { split: false, highlight: true }
  }
}

class SfChanges extends HTMLElement {
  #open = new Map<string, boolean>()
  #key = ''
  #value: Changes | null = null
  #forkable = false
  #readOnly = ''
  #branch = ''
  #rows: DiffRow[] = []
  #diff = ''
  #painted = ''
  #at = -1
  #layout = savedLayout()
  #branchCommits = new Map<string, Changes['commits']>()
  #comments = new Map<string, LineComment[]>()
  #sending = false
  #diffGroup = el('details')
  #diffSummary = el('summary', 'small')
  #lines = el('div', 'diff-lines small')
  #status = statusLine('sr-only', 'span')
  #panel = el('div', 'line-comments small')
  #target = el('p', 'small muted')
  #box = el('textarea', 'textbox')
  #list = el('ul', 'list line-comment-list')
  #said = statusLine()
  #add = button('Add comment', 'ghost small', () => this.#addComment())
  #send = button('Send comments', 'primary small', () => this.#sendComments())

  constructor() {
    super()
    this.#diffSummary.dataset.key = 'group:diff'
    this.#diffGroup.addEventListener('toggle', () => this.#open.set('diff', this.#diffGroup.open))
    this.#lines.tabIndex = 0
    this.#lines.setAttribute('role', 'region')
    this.#lines.addEventListener('click', e => {
      const row = (e.target as Element).closest<HTMLElement>('[data-row]')
      if (row) this.#pick(Number(row.dataset.row), true)
    })
    this.#lines.addEventListener('keydown', e => this.#keys(e))
    this.#box.rows = 3
    this.#box.maxLength = MAX_COMMENT
    this.#box.setAttribute('aria-label', 'Comment on the chosen line')
    this.#box.disabled = true
    this.#box.addEventListener('keydown', e => {
      if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        this.#addComment()
      }
    })
    const acts = el('div', 'row')
    acts.append(this.#add, el('span', 'spacer'), this.#send)
    this.#panel.append(el('h3', 'label', 'Line comments'), el('p', 'small muted', 'Choose a line in the diff, write a comment, then send them all to the agent as one message, each with its file and line.'), this.#target, this.#box, acts, this.#list, this.#said)
  }

  get #mine(): LineComment[] {
    return this.#comments.get(this.#branch) ?? []
  }

  openDiff() {
    this.#open.set('diff', true)
    const summary = this.querySelector<HTMLElement>('summary[data-key="group:diff"]')
    const d = summary?.parentElement
    if (d instanceof HTMLDetailsElement) d.open = true
    const target = d ? this.#lines : this.querySelector<HTMLElement>('select, summary')
    target?.focus({ preventScroll: true })
    this.scrollIntoView({ block: 'start' })
  }

  #commentable(i: number): boolean {
    const r = this.#rows[i]
    return !!r && !!r.file && (r.kind === 'added' || r.kind === 'removed' || r.kind === 'same')
  }

  #pick(i: number, focusBox: boolean) {
    if (!this.#commentable(i)) return
    for (const node of this.#lines.querySelectorAll('.at')) node.classList.remove('at')
    this.#at = i
    const nodes = this.#lines.querySelectorAll<HTMLElement>(`[data-row="${i}"]`)
    for (const node of nodes) node.classList.add('at')
    nodes[0]?.scrollIntoView({ block: 'nearest' })
    const r = this.#rows[i]!
    const where = `${visible(r.file)} line ${r.line}${r.kind === 'removed' ? ', a removed line' : r.kind === 'added' ? ', an added line' : ''}`
    this.#target.textContent = `Commenting on ${where}.`
    this.#status.textContent = `${where}: ${visible(r.text.slice(1))}`
    this.#box.disabled = false
    if (focusBox) this.#box.focus()
  }

  #keys(e: KeyboardEvent) {
    if (e.key === 'Enter' && this.#at >= 0) {
      e.preventDefault()
      return this.#pick(this.#at, true)
    }
    const step = e.key === 'ArrowDown' ? 1 : e.key === 'ArrowUp' ? -1 : 0
    if (!step) return
    e.preventDefault()
    let i = this.#at < 0 ? (step > 0 ? -1 : this.#rows.length) : this.#at
    do {
      i += step
    } while (i >= 0 && i < this.#rows.length && !this.#commentable(i))
    this.#pick(i, false)
  }

  #addComment() {
    const r = this.#rows[this.#at]
    const text = this.#box.value.trim()
    if (!r || !this.#commentable(this.#at)) return void (this.#said.textContent = 'Choose a line in the diff first.')
    if (!text) return void (this.#said.textContent = 'Write the comment first.')
    const mine = this.#mine
    if (mine.length >= MAX_COMMENTS) return void (this.#said.textContent = `At most ${MAX_COMMENTS} comments go in one message. Send these first.`)
    const next = [...mine, { file: r.file, line: r.line, removed: r.kind === 'removed', text: text.slice(0, MAX_COMMENT) }]
    if (commentMessage(next).length > MAX_MESSAGE) return void (this.#said.textContent = 'That would make the message too long. Send these first.')
    this.#comments.set(this.#branch, next)
    this.#box.value = ''
    this.#said.textContent = `${next.length} ${plural(next.length, 'comment')} ready to send.`
    this.#paintList()
    this.#lines.focus({ preventScroll: true })
  }

  #paintList() {
    const mine = this.#mine
    this.#list.replaceChildren(...mine.map((c, i) => {
      const li = el('li', 'row')
      const words = el('span', '', `${visible(c.file)} line ${c.line}${c.removed ? ' (removed)' : ''}: ${visible(c.text)}`)
      const drop = button('Remove', 'ghost small', () => {
        this.#comments.set(this.#branch, this.#mine.filter((_, j) => j !== i))
        this.#paintList()
      })
      drop.setAttribute('aria-label', `Remove the comment on ${visible(c.file)} line ${c.line}`)
      li.append(words, el('span', 'spacer'), drop)
      return li
    }))
    busy(this.#send, false)
    this.#send.disabled = !mine.length || !!this.#readOnly
    this.#send.textContent = mine.length ? `Send ${mine.length} ${plural(mine.length, 'comment')}` : 'Send comments'
    if (this.#sending) busy(this.#send, true)
  }

  #sendComments() {
    const mine = this.#mine
    if (!mine.length || this.#sending) return
    const branch = this.#branch
    this.#sending = true
    this.#paintList()
    emit(this, 'sf-line-comments', {
      text: commentMessage(mine),
      done: (ok: boolean) => {
        this.#sending = false
        if (ok) {
          const kept = (this.#comments.get(branch) ?? []).filter(c => !mine.includes(c))
          if (kept.length) this.#comments.set(branch, kept)
          else this.#comments.delete(branch)
          this.#said.textContent = kept.length ? `Sent to the agent as one message. ${kept.length} ${plural(kept.length, 'comment')} added since ${kept.length === 1 ? 'is' : 'are'} kept here.` : 'Sent to the agent as one message.'
        } else this.#said.textContent = 'The comments weren’t sent; they are kept here.'
        this.#paintList()
      },
    })
  }

  #fileHeader(r: DiffRow, files: Map<string, ChangeFile>): HTMLElement {
    const line = el('div', 'diff-line file')
    line.dataset.file = r.file
    const name = el('span', 'diff-code')
    name.append(el('strong', '', visible(r.file || r.text.slice('diff --git '.length))), el('span', 'diff-mark'))
    line.append(name)
    this.#markHeader(line, files.get(r.file))
    return line
  }

  #markHeader(line: HTMLElement, f: ChangeFile | undefined) {
    const mark = line.querySelector<HTMLElement>('.diff-mark')
    if (!mark) return
    if (f?.viewed) mark.replaceChildren(' ', el('span', 'tag', 'viewed'))
    else if (f?.stale) mark.replaceChildren(' ', el('span', 'tag warn', 'changed since viewed'))
    else mark.replaceChildren()
  }

  #unreadableLine(r: DiffRow, files: Map<string, ChangeFile>): HTMLElement | null {
    if (r.kind !== 'meta' || files.get(r.file)?.notDiffed !== 'unreadable') return null
    if (r.text.startsWith('Binary files ')) return el('div', 'diff-line meta', 'Not diffed: this file’s stored object couldn’t be read.')
    return r.text.startsWith('index ') || r.text.startsWith('--- ') || r.text.startsWith('+++ ') ? el('div', 'diff-gap') : null
  }

  #unifiedLine(r: DiffRow, i: number, highlight: boolean): HTMLElement {
    const line = el('div', `diff-line ${r.kind}`)
    line.append(numberOf(r.kind === 'removed' ? r.old : r.line), signOf(r), codeOf(r, highlight))
    if (this.#commentable(i)) line.dataset.row = String(i)
    return line
  }

  #cell(i: number, side: 'left' | 'right', highlight: boolean): HTMLElement {
    const r = this.#rows[i]
    if (!r) {
      const empty = el('div', 'diff-cell empty')
      empty.setAttribute('aria-hidden', 'true')
      return empty
    }
    const cell = el('div', `diff-cell ${r.kind}`)
    cell.append(numberOf(side === 'left' ? r.old : r.line), signOf(r), codeOf(r, highlight))
    if (this.#commentable(i)) cell.dataset.row = String(i)
    return cell
  }

  #paintDiff() {
    const layout = this.#layout
    const highlight = layout.highlight && this.#rows.length <= MAX_HIGHLIGHT
    const files = new Map((this.#value?.files ?? []).map(f => [f.path, f]))
    const key = `${this.#diff.length}:${layout.split}:${highlight}`
    if (key === this.#painted) {
      for (const line of this.#lines.querySelectorAll<HTMLElement>('.diff-line.file')) this.#markHeader(line, files.get(line.dataset.file ?? ''))
      return
    }
    const picked = this.#at
    this.#painted = key
    if (highlight && this.#rows.some(r => !r.tokens && (r.kind === 'added' || r.kind === 'removed' || r.kind === 'same'))) highlightRows(this.#rows)
    this.#lines.classList.toggle('split', layout.split)
    this.#lines.setAttribute('aria-label', `The change as a ${layout.split ? 'side-by-side' : 'unified'} diff. Use the arrow keys to move between lines and Enter to comment on one`)
    const nodes: HTMLElement[] = []
    if (layout.split) {
      for (const s of sideRows(this.#rows)) {
        if ('whole' in s) {
          const r = this.#rows[s.whole]!
          nodes.push(r.kind === 'file' ? this.#fileHeader(r, files) : this.#unreadableLine(r, files) ?? this.#unifiedLine(r, s.whole, false))
          continue
        }
        const row = el('div', 'diff-pair')
        row.append(this.#cell(s.left, 'left', highlight), this.#cell(s.right, 'right', highlight))
        nodes.push(row)
      }
    } else {
      this.#rows.forEach((r, i) => nodes.push(r.kind === 'file' ? this.#fileHeader(r, files) : this.#unreadableLine(r, files) ?? this.#unifiedLine(r, i, highlight)))
    }
    this.#lines.replaceChildren(...fileSections(nodes))
    if (picked >= 0 && this.#commentable(picked)) {
      for (const node of this.#lines.querySelectorAll<HTMLElement>(`[data-row="${picked}"]`)) node.classList.add('at')
    }
  }

  #setLayout(next: Partial<Layout>) {
    this.#layout = { ...this.#layout, ...next }
    try {
      sessionStorage.setItem(LAYOUT_KEY, JSON.stringify(this.#layout))
    } catch {}
    this.#key = ''
    this.changes = this.#value
  }

  #toolbar(value: Changes): HTMLElement {
    const bar = el('div', 'changes-tools')
    const from: ChangeFrom = value.from || 'fork'
    const points = value.points ?? { reviewed: '', reviewedAt: 0, turn: '' }
    const pick = el('select', 'textbox small')
    pick.id = 'changes-from'
    pick.dataset.key = 'changes-from'
    for (const k of Object.keys(POINTS) as ChangeFrom[]) {
      const sha = k === 'reviewed' ? points.reviewed : k === 'turn' ? points.turn : ''
      const missing = (k === 'reviewed' || k === 'turn') && !sha
      const option = el('option', '', `${POINTS[k]}${sha ? ` (${shortSha(sha)})` : missing ? ' (none yet)' : ''}`)
      option.value = k
      option.disabled = missing && k !== from
      option.selected = k === from
      pick.append(option)
    }
    const commits = this.#branchCommits.get(value.branch) ?? []
    const commitPick = el('select', 'textbox small')
    commitPick.id = 'changes-commit'
    commitPick.dataset.key = 'changes-commit'
    const chosen = from === 'commit' ? value.base : ''
    const preset = chosen || (commits[1] ?? commits[0])?.sha || ''
    for (const c of commits) {
      const option = el('option', '', `${shortSha(c.sha)} ${visible(c.subject)}`)
      option.value = c.sha
      option.selected = c.sha === preset
      commitPick.append(option)
    }
    if (chosen && !commits.some(c => c.sha === chosen)) {
      const option = el('option', '', shortSha(chosen))
      option.value = chosen
      option.selected = true
      commitPick.prepend(option)
    }
    if (!commits.length && !chosen) {
      const option = el('option', '', 'No commit on the branch yet')
      option.value = ''
      commitPick.append(option)
      commitPick.disabled = true
    }
    pick.addEventListener('change', () => {
      const next = pick.value as ChangeFrom
      if (next !== 'commit') return emit(this, 'sf-compare-point', { from: next, commit: '' })
      const sha = SHA.test(commitPick.value) ? commitPick.value : preset
      if (SHA.test(sha)) emit(this, 'sf-compare-point', { from: 'commit', commit: sha })
      else pick.value = from
    })
    commitPick.addEventListener('change', () => {
      if (SHA.test(commitPick.value)) emit(this, 'sf-compare-point', { from: 'commit', commit: commitPick.value })
    })
    bar.append(field('Compare with', pick, undefined, 'field-inline'))
    if (from === 'commit') bar.append(field('Changes after', commitPick, undefined, 'field-inline'))
    const group = el('div', 'segmented')
    group.setAttribute('role', 'group')
    group.setAttribute('aria-label', 'Diff layout')
    const unified = button('Unified', 'ghost small', () => this.#setLayout({ split: false }), 'button', 'layout:unified')
    const split = button('Side by side', 'ghost small', () => this.#setLayout({ split: true }), 'button', 'layout:split')
    unified.setAttribute('aria-pressed', String(!this.#layout.split))
    split.setAttribute('aria-pressed', String(this.#layout.split))
    group.append(unified, split)
    const hl = el('label', 'small check-inline')
    const box = el('input')
    box.type = 'checkbox'
    box.checked = this.#layout.highlight
    box.dataset.key = 'layout:highlight'
    box.addEventListener('change', () => this.#setLayout({ highlight: box.checked }))
    hl.append(box, ' Highlight syntax')
    bar.append(group, hl)
    return bar
  }

  #fileItem(f: ChangeFile): HTMLLIElement {
    const li = el('li', `small changed-file ${f.status}`)
    const mark = el('code', '', MARK[f.status] || '?')
    mark.setAttribute('aria-hidden', 'true')
    const name = f.status === 'deleted' ? el('span', '', visible(f.path)) : fileLink(f.path, `file:${f.path}`)
    const what = el('span', 'changed-name')
    what.append(mark, el('span', 'sr-only', `${f.status}: `), ' ', name)
    what.title = f.status
    li.append(what)
    if (f.stale) li.append(el('span', 'tag warn', 'changed since viewed'))
    const skip = f.notDiffed ? NOT_DIFFED[f.notDiffed] : ''
    if (skip) li.append(el('span', 'tag', `not diffed: ${skip}${f.size ? `, ${sizeWords(f.size)}` : ''}`))
    if (f.blob && SHA.test(f.blob)) {
      const label = el('label', 'viewed-mark small')
      const box = el('input')
      box.type = 'checkbox'
      box.checked = !!f.viewed
      box.disabled = !!this.#readOnly
      box.dataset.key = `viewed:${f.path}`
      box.setAttribute('aria-label', `Viewed: ${visible(f.path)}`)
      box.addEventListener('change', () => emit(this, 'sf-viewed', { path: f.path, blob: f.blob!, viewed: box.checked }))
      const word = el('span', '', 'Viewed')
      word.setAttribute('aria-hidden', 'true')
      label.append(box, word)
      li.append(label)
    }
    return li
  }

  set forkable(on: boolean) {
    if (on === this.#forkable) return
    this.#forkable = on
    this.changes = this.#value
  }

  set readOnly(why: string) {
    if (why === this.#readOnly) return
    this.#readOnly = why
    this.changes = this.#value
  }

  set changes(value: Changes | null) {
    const key = JSON.stringify([value, this.#forkable, this.#readOnly, this.#layout])
    if (key === this.#key) return
    this.#key = key
    this.#value = value
    const focused = focusedKey(this)
    this.hidden = !value
    if (!value) {
      this.replaceChildren()
      return
    }
    if (value.branch !== this.#branch) this.#box.value = ''
    this.#branch = value.branch
    const from: ChangeFrom = value.from || 'fork'
    if (from === 'fork') this.#branchCommits.set(value.branch, value.commits.filter(c => SHA.test(c.sha)))
    const where = [value.branch ? `On ${visible(value.branch)}` : 'Detached', value.worktree === 'linked' ? 'in its own worktree' : ''].filter(Boolean).join(' ')
    const base = el('p', 'small changes-base')
    base.append(el('span', 'muted', `${where}, compared with `), el('strong', '', value.baseRef ? visible(value.baseRef) : 'nothing, so every file is new'))
    const parts: HTMLElement[] = [el('h2', 'label', 'Changes'), base, this.#toolbar(value)]
    if (value.note) parts.push(el('p', 'small muted', value.note))
    if (this.#readOnly) parts.push(el('p', 'small muted', this.#readOnly))
    const skipped = value.files.filter(f => f.notDiffed)
    if (skipped.length) parts.push(el('p', 'small muted', `${skipped.length} ${plural(skipped.length, 'file')} ${skipped.length === 1 ? 'isn’t' : 'aren’t'} diffed here: the view diffs no file over 1 MiB and reads at most 8 MiB of file contents in all; a binary file shows only as changed, with its lines still counted for a project’s size limits, and a file whose stored object can’t be read is named as such. Each is marked in the file list with its size; read it in your own clone.`))
    const group = (key: string, title: string, items: HTMLElement[], openByDefault: boolean) => {
      if (!items.length) return
      const ul = el('ul', 'changes')
      ul.append(...items)
      const summary = el('summary', 'small', title)
      summary.dataset.key = `group:${key}`
      parts.push(remembered(this.#open, key, openByDefault, summary, ul))
    }
    const more = value.more ? ' or more' : ''
    group('commits', `${value.commits.length}${more} ${plural(value.commits.length, 'commit')}`, value.commits.map(c => {
      const li = el('li', 'small')
      li.append(el('code', '', shortSha(c.sha)), ' ', visible(c.subject))
      if (this.#forkable && SHA.test(c.sha)) {
        const fork = button('Fork from here', 'ghost small', () => emit(this, 'sf-fork-commit', { sha: c.sha }), 'button', `fork:${c.sha}`)
        fork.setAttribute('aria-label', `Fork from commit ${shortSha(c.sha)}: start a new session at it`)
        li.append(' ', fork)
      }
      return li
    }), true)
    const viewed = value.files.filter(f => f.viewed).length
    group('files', `${value.files.length}${more} ${plural(value.files.length, 'file')} changed${value.files.length ? `, ${viewed} viewed` : ''}`, value.files.map(f => this.#fileItem(f)), true)
    if (value.diff) {
      if (value.diff !== this.#diff) {
        this.#rows = diffRows(value.diff)
        this.#at = -1
        this.#painted = ''
        this.#target.textContent = 'No line chosen.'
        this.#box.disabled = true
      }
      this.#diff = value.diff
      this.#paintDiff()
      const cutAt = value.clipped ? [...this.#rows].reverse().find(r => r.file)?.file ?? '' : ''
      this.#diffSummary.textContent = value.clipped ? `Diff (cut short at 1 MiB${cutAt ? `, in ${visible(cutAt)}` : ''})` : 'Diff'
      const notes: HTMLElement[] = []
      if (this.#layout.highlight && this.#rows.length > MAX_HIGHLIGHT) notes.push(el('p', 'small muted', `Highlighting is off for a diff of more than ${MAX_HIGHLIGHT.toLocaleString()} lines, to keep the page quick.`))
      this.#diffGroup.open = this.#open.get('diff') ?? true
      replaceKeeping(this.#diffGroup, [this.#diffSummary, ...notes, this.#lines, this.#status, this.#panel])
      parts.push(this.#diffGroup)
    } else {
      this.#rows = []
      this.#diff = ''
      this.#painted = ''
      this.#lines.replaceChildren()
      if (this.#mine.length) parts.push(this.#panel)
    }
    this.#paintList()
    if (!value.commits.length) parts.push(el('p', 'small muted', from === 'fork' ? 'No commits beyond the base branch.' : 'No commits since the commit compared with.'))
    replaceKeeping(this, parts)
    refocus(this, focused)
  }
}

customElements.define('sf-changes', SfChanges)

declare global {
  interface HTMLElementTagNameMap {
    'sf-changes': SfChanges
  }
  interface HTMLElementEventMap {
    'sf-line-comments': CustomEvent<{ text: string; done: (ok: boolean) => void }>
    'sf-fork-commit': CustomEvent<{ sha: string }>
    'sf-compare-point': CustomEvent<{ from: ChangeFrom; commit: string }>
    'sf-viewed': CustomEvent<{ path: string; blob: string; viewed: boolean }>
  }
}
