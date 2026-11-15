import { button, el, emit, fileLink, focusedKey, refocus, remembered, visible } from '../lib/dom.ts'
import { plural, shortSha } from '../lib/format.ts'
import type { Changes } from '../types.ts'

const MARK: Record<string, string> = { added: 'A', changed: 'M', deleted: 'D' }
const MAX_COMMENTS = 50
const MAX_COMMENT = 2000
const MAX_MESSAGE = 20000

type RowKind = 'meta' | 'hunk' | 'added' | 'removed' | 'same'

interface DiffRow {
  kind: RowKind
  text: string
  file: string
  line: number
}

interface LineComment {
  file: string
  line: number
  removed: boolean
  text: string
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
      rows.push({ kind: 'added', text: raw, file, line: newLine++ })
      newLeft--
      continue
    }
    if (inHunk && raw.startsWith('-') && oldLeft > 0) {
      rows.push({ kind: 'removed', text: raw, file, line: oldLine++ })
      oldLeft--
      continue
    }
    if (inHunk && (raw.startsWith(' ') || raw === '') && oldLeft > 0 && newLeft > 0) {
      rows.push({ kind: 'same', text: raw, file, line: newLine++ })
      oldLine++
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
      rows.push({ kind: 'hunk', text: raw, file, line: 0 })
      continue
    }
    if (raw.startsWith('diff --git ')) {
      file = ''
      from = ''
      oldLeft = newLeft = 0
    } else if (raw.startsWith('--- ')) from = unquote(raw.slice(4), 'a/')
    else if (raw.startsWith('+++ ')) {
      const to = raw.slice(4)
      file = to === '/dev/null' ? from : unquote(to, 'b/')
    }
    rows.push({ kind: 'meta', text: raw, file, line: 0 })
  }
  return rows
}

function commentMessage(comments: LineComment[]): string {
  const lines = comments.map(c => `- ${c.file} line ${c.line}${c.removed ? ' (a removed line, numbered in the old file)' : ''}: ${c.text.replace(/\s*\n\s*/g, ' ').trim()}`)
  return [`My comments on lines of the change, ${comments.length === 1 ? 'one' : comments.length} in all:`, '', ...lines].join('\n')
}

class SfChanges extends HTMLElement {
  #open = new Map<string, boolean>()
  #key = ''
  #value: Changes | null = null
  #forkable = false
  #branch = ''
  #rows: DiffRow[] = []
  #diff = ''
  #at = -1
  #comments = new Map<string, LineComment[]>()
  #sending = false
  #lines = el('div', 'diff-lines small')
  #status = el('span', 'sr-only')
  #panel = el('div', 'line-comments small')
  #target = el('p', 'small muted')
  #box = el('textarea', 'textbox')
  #list = el('ul', 'list line-comment-list')
  #said = el('p', 'small')
  #add = button('Add comment', 'ghost small', () => this.#addComment())
  #send = button('Send comments', 'primary small', () => this.#sendComments())

  constructor() {
    super()
    this.#status.setAttribute('role', 'status')
    this.#said.setAttribute('role', 'status')
    this.#lines.tabIndex = 0
    this.#lines.setAttribute('role', 'region')
    this.#lines.setAttribute('aria-label', 'The change as a unified diff. Use the arrow keys to move between lines and Enter to comment on one')
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

  #commentable(i: number): boolean {
    const r = this.#rows[i]
    return !!r && !!r.file && (r.kind === 'added' || r.kind === 'removed' || r.kind === 'same')
  }

  #pick(i: number, focusBox: boolean) {
    if (!this.#commentable(i)) return
    this.#lines.querySelector('.diff-line.at')?.classList.remove('at')
    this.#at = i
    const node = this.#lines.querySelector<HTMLElement>(`[data-row="${i}"]`)
    node?.classList.add('at')
    node?.scrollIntoView({ block: 'nearest' })
    const r = this.#rows[i]!
    const where = `${visible(r.file)} line ${r.line}${r.kind === 'removed' ? ', a removed line' : ''}`
    this.#target.textContent = `Commenting on ${where}.`
    this.#status.textContent = `${where}: ${visible(r.text.slice(1))}`
    this.#box.disabled = this.#sending
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
    this.#lines.focus()
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
    this.#send.disabled = this.#sending || !mine.length
    this.#send.textContent = this.#sending ? 'Sending…' : mine.length ? `Send ${mine.length} ${plural(mine.length, 'comment')}` : 'Send comments'
  }

  #sendComments() {
    const mine = this.#mine
    if (!mine.length || this.#sending) return
    const branch = this.#branch
    this.#sending = true
    this.#paintList()
    this.dispatchEvent(new CustomEvent('sf-line-comments', {
      bubbles: true,
      detail: {
        text: commentMessage(mine),
        done: (ok: boolean) => {
          this.#sending = false
          if (ok) {
            this.#comments.delete(branch)
            this.#said.textContent = 'Sent to the agent as one message.'
          } else this.#said.textContent = 'The comments weren’t sent; they are kept here.'
          this.#paintList()
        },
      },
    }))
  }

  #paintDiff(diff: string) {
    this.#rows = diffRows(diff)
    this.#at = -1
    this.#target.textContent = 'No line chosen.'
    this.#box.disabled = true
    const nodes = this.#rows.map((r, i) => {
      const line = el('div', `diff-line ${r.kind}`)
      const no = el('span', 'diff-no', r.line ? String(r.line) : '')
      no.setAttribute('aria-hidden', 'true')
      line.append(no, visible(r.text))
      if (this.#commentable(i)) line.dataset.row = String(i)
      return line
    })
    this.#lines.replaceChildren(...nodes)
  }

  set forkable(on: boolean) {
    if (on === this.#forkable) return
    this.#forkable = on
    this.changes = this.#value
  }

  set changes(value: Changes | null) {
    const key = JSON.stringify([value, this.#forkable])
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
    const where = [value.branch ? `On ${visible(value.branch)}` : 'Detached', value.worktree === 'linked' ? 'in its own worktree' : '', value.baseRef ? `against ${visible(value.baseRef)}` : ''].filter(Boolean).join(' ')
    const parts: HTMLElement[] = [el('h2', 'label', 'Changes'), el('p', 'small muted', where)]
    if (value.note) parts.push(el('p', 'small muted', value.note))
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
      if (this.#forkable && /^[0-9a-f]{40}$/.test(c.sha)) {
        const fork = button('Fork from here', 'ghost small', () => emit(this, 'sf-fork-commit', { sha: c.sha }), 'button', `fork:${c.sha}`)
        fork.setAttribute('aria-label', `Fork from commit ${shortSha(c.sha)}: start a new session at it`)
        li.append(' ', fork)
      }
      return li
    }), true)
    group('files', `${value.files.length}${more} ${plural(value.files.length, 'file')} changed`, value.files.map(f => {
      const li = el('li', `small ${f.status}`)
      const mark = el('code', '', MARK[f.status] || '?')
      mark.setAttribute('aria-hidden', 'true')
      const name = f.status === 'deleted' ? visible(f.path) : fileLink(f.path, `file:${f.path}`)
      li.append(mark, el('span', 'sr-only', `${f.status}: `), ' ', name)
      li.title = f.status
      return li
    }), false)
    if (value.diff) {
      if (value.diff !== this.#diff) this.#paintDiff(value.diff)
      this.#diff = value.diff
      const summary = el('summary', 'small', value.clipped ? 'Diff (cut short at 1 MiB)' : 'Diff')
      summary.dataset.key = 'group:diff'
      parts.push(remembered(this.#open, 'diff', false, summary, this.#lines, this.#status, this.#panel))
    } else {
      this.#rows = []
      this.#diff = ''
      this.#lines.replaceChildren()
      if (this.#mine.length) parts.push(this.#panel)
    }
    this.#paintList()
    if (!value.commits.length) parts.push(el('p', 'small muted', 'No commits beyond the base branch.'))
    this.replaceChildren(...parts)
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
  }
}
