import { el, fileLink, focusedKey, refocus, remembered, visible } from '../lib/dom.ts'
import { plural, shortSha } from '../lib/format.ts'
import type { Changes } from '../types.ts'

const MARK: Record<string, string> = { added: 'A', changed: 'M', deleted: 'D' }

class SfChanges extends HTMLElement {
  #open = new Map<string, boolean>()
  #key = ''

  set changes(value: Changes | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    const focused = focusedKey(this)
    this.hidden = !value
    if (!value) {
      this.replaceChildren()
      return
    }
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
      const pre = el('pre', 'diff small', visible(value.diff))
      pre.tabIndex = 0
      pre.setAttribute('aria-label', 'The change as a unified diff')
      const summary = el('summary', 'small', value.clipped ? 'Diff (cut short at 1 MiB)' : 'Diff')
      summary.dataset.key = 'group:diff'
      parts.push(remembered(this.#open, 'diff', false, summary, pre))
    }
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
}
