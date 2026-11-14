import { button, el, emit, visible } from '../lib/dom.ts'
import type { Overlap, SessionFactsView } from '../types.ts'

const SHOWN = 20

function paths(list: string[], more: number): HTMLElement {
  const ul = el('ul', 'small list')
  for (const p of list.slice(0, SHOWN)) {
    const li = el('li')
    li.append(el('code', '', visible(p)))
    ul.append(li)
  }
  const rest = Math.max(0, list.length - SHOWN) + more
  if (rest > 0) ul.append(el('li', 'muted', `and ${rest} more`))
  return ul
}

function plural(n: number, one: string, many: string): string {
  return n === 1 ? one : many
}

class SfSessionFacts extends HTMLElement {
  #key = ''

  connectedCallback() {
    if (!this.#key) this.hidden = true
  }

  set view(value: SessionFactsView | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    const u = value?.uncommitted ?? null
    const overlaps = value?.overlaps ?? []
    this.hidden = !value || (!u?.count && !overlaps.length)
    if (!value || this.hidden) {
      this.replaceChildren()
      return
    }
    const parts: HTMLElement[] = []
    if (u && u.count) parts.push(this.#uncommitted(u.count, u.paths, u.cut, value.disabled))
    for (const o of overlaps) parts.push(this.#overlap(o))
    this.replaceChildren(...parts)
  }

  #uncommitted(count: number, list: string[], cut: boolean, disabled: string): HTMLElement {
    const box = el('div', 'notice warn small')
    box.setAttribute('role', 'status')
    const lead = `${count}${cut ? ' or more' : ''} ${plural(count, 'file', 'files')} changed but not committed; ${plural(count, "it won't", "they won't")} be pushed.`
    box.append(el('p', '', lead), paths(list, Math.max(0, count - list.length)))
    box.append(el('p', 'muted', 'A push sends commits only, so these edits are left out of the push, the reviews and the pull request. The engine read the worktree itself when the turn ended.'))
    const ask = button('Ask the agent to commit them', 'small', () => {
      if (!disabled) emit(this, 'sf-commit-ask')
    }, 'button', 'commit-ask')
    ask.setAttribute('aria-disabled', String(!!disabled))
    ask.title = disabled || 'Sends the session a message asking it to commit every uncommitted change, and to ask you about any file that shouldn\'t be committed'
    box.append(ask)
    return box
  }

  #overlap(o: Overlap): HTMLElement {
    const box = el('div', 'notice warn small')
    const n = o.paths.length + o.more
    box.append(el('p', '', `Also changed in session ${visible(o.name)}: ${n} ${plural(n, 'file', 'files')}. The two branches may conflict when they are merged.`), paths(o.paths, o.more))
    return box
  }
}

customElements.define('sf-session-facts', SfSessionFacts)

declare global {
  interface HTMLElementTagNameMap {
    'sf-session-facts': SfSessionFacts
  }
}
