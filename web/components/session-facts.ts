import { button, el, emit, replaceKeeping, sayIfChanged, sayWhenShown, statusLine, visible } from '../lib/dom.ts'
import { offTip } from '../lib/tooltip.ts'
import type { Overlap, SessionFactsView, Uncommitted } from '../types.ts'

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
  #uncommittedSaid = statusLine('status-slot')
  #uncommittedBox = el('div', 'notice warn small status-box')

  connectedCallback() {
    if (!this.#key) this.hidden = true
  }

  set view(value: SessionFactsView | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    const u = value?.uncommitted ?? null
    const overlaps = value?.overlaps ?? []
    const hidden = !value || (!u?.count && !overlaps.length)
    const parts: HTMLElement[] = [this.#uncommitted(value && !hidden && u?.count ? u : null, value?.disabled ?? '')]
    if (!hidden) for (const o of overlaps) parts.push(this.#overlap(o))
    this.hidden = hidden
    replaceKeeping(this, parts)
  }

  #uncommitted(u: Uncommitted | null, disabled: string): HTMLElement {
    const box = this.#uncommittedBox
    const lead = this.#uncommittedSaid
    if (!u) {
      sayIfChanged(lead, '')
      replaceKeeping(box, [lead])
      return box
    }
    const { count, paths: list, cut } = u
    sayWhenShown(lead, `${count}${cut ? ' or more' : ''} ${plural(count, 'file', 'files')} changed but not committed; ${plural(count, "it won't", "they won't")} be pushed.`)
    const ask = button('Ask the agent to commit them', 'small', () => {
      if (!disabled) emit(this, 'sf-commit-ask')
    }, 'button', 'commit-ask')
    ask.setAttribute('aria-disabled', String(!!disabled))
    if (!disabled) ask.title = 'Sends the session a message asking it to commit every uncommitted change, and to ask you about any file that shouldn\'t be committed'
    replaceKeeping(box, [lead, paths(list, Math.max(0, count - list.length)), el('p', 'muted', 'A push sends commits only, so these edits are left out of the push, the reviews and the pull request. The engine read the worktree itself when the turn ended.'), ...(disabled ? offTip(ask, disabled) : [ask])])
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
