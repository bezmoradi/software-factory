import { button, el, replaceKeeping, unspoofed, visible } from '../lib/dom.ts'
import { shortSha } from '../lib/format.ts'
import { icon } from './icon.ts'
import { openFlow, type FlowDialog, type FlowModel } from './push-flow.ts'
import type { CheckedLine, PushCheckedBy, PushCheckedReviewer, PushCheckedRun, SessionPush } from '../types.ts'
import { emptyState } from '../lib/page.ts'

let made = 0

function text(value: string): string {
  return visible(unspoofed(value))
}

function line(l: CheckedLine | undefined, fallback: string): HTMLElement {
  const li = el('li', 'push-checked-item')
  const head = el('div', 'push-checked-head')
  const tags = (l?.tags ?? []).map(t => el('span', t.tone ? `tag ${t.tone}` : 'tag', text(t.label)))
  head.append(icon(l?.icon || 'info', `icon push-icon ${l?.tone ?? ''}`.trim()), el('strong', '', text(l?.name || fallback)), el('span', '', `: ${text(l?.said ?? '')}`), ...tags)
  li.append(head)
  if (l?.details) li.append(el('div', 'small muted', text(l.details)))
  return li
}

function reviewer(r: PushCheckedReviewer): HTMLElement {
  const li = line(r.line, r.name || r.agent)
  if (r.setAside) li.append(el('div', 'small muted', `Set aside: ${text(r.setAside)}`))
  return li
}

function run(r: PushCheckedRun): HTMLElement {
  return line(r.line, r.kind)
}

function checkedOf(v: unknown): PushCheckedBy | null {
  if (!v || typeof v !== 'object') return null
  const c = v as Partial<PushCheckedBy>
  const reviewers = Array.isArray(c.reviewers) ? c.reviewers.filter((r): r is PushCheckedReviewer => !!r && typeof r.agent === 'string' && typeof r.state === 'string') : []
  const runs = Array.isArray(c.runs) ? c.runs.filter((r): r is PushCheckedRun => !!r && typeof r.kind === 'string' && typeof r.state === 'string') : []
  return { reviewers, runs, unreadable: c.unreadable === true }
}

function list(label: string, items: HTMLElement[]): HTMLElement {
  const ul = el('ul', 'small push-checked-list')
  ul.setAttribute('role', 'list')
  ul.setAttribute('aria-label', label)
  ul.append(...items)
  return ul
}

export interface PushCheckedView {
  push: SessionPush
}

class SfPushChecked extends HTMLElement {
  #view: PushCheckedView | null = null
  #key = ''
  #box = el('section', 'push-facts push-checked')
  #title = el('h3', 'small push-checked-title', 'How this change was checked')
  #model: FlowModel | null = null
  #shown: FlowDialog | null = null
  #open = button('Open diagram', 'small', () => this.#show(), 'button', 'push-checked-diagram')
  #diagram = el('div', 'push-checked-diagram')

  constructor() {
    super()
    this.#title.id = `push-checked-title-${++made}`
    this.#box.setAttribute('aria-labelledby', this.#title.id)
    this.#open.title = "Opens the reviews, verification runs and gate's decision of this commit as a diagram"
    this.#diagram.append(this.#open)
  }

  connectedCallback() {
    this.paint()
  }

  set view(value: PushCheckedView | null) {
    this.#view = value
    this.paint()
  }

  paint() {
    const v = this.#view
    const p = v && !v.push.running && v.push.result ? v.push : null
    this.hidden = !p
    const checked = p ? checkedOf(p.checkedBy) : null
    this.#model = p?.flow ?? null
    if (this.#shown && this.#model) this.#shown.diagram.model = this.#model
    else this.#shown?.modal.close()
    const key = JSON.stringify(p)
    if (key === this.#key) return
    this.#key = key
    if (!p) {
      replaceKeeping(this, [])
      return
    }
    replaceKeeping(this.#box, this.#facts(p, checked, p.decision ?? ''))
    replaceKeeping(this, [this.#box])
  }

  #show() {
    if (!this.#model || this.#shown) return
    const shown = openFlow('How this change was checked', this.#model)
    this.#shown = shown
    shown.modal.onClose(() => {
      if (this.#shown === shown) this.#shown = null
    })
  }

  #facts(p: SessionPush, checked: PushCheckedBy | null, decision: string): Node[] {
    const commit = p.commit ? ` of commit ${shortSha(p.commit)}` : ''
    const parts: Node[] = [this.#title, el('p', 'small muted', `From the engine's own records${commit}: the reviews, the verification runs and the gate's decision. Nothing the agent reported about itself is here.`), this.#diagram]
    if (!checked) parts.push(el('p', 'small muted', 'The engine decided this card before it looked at the reviews, so no reviewer is listed.'))
    else {
      if (checked.unreadable) parts.push(el('p', 'small warn', "The engine couldn't read its review records, so this list may be incomplete."))
      if (checked.reviewers.length) parts.push(list('Reviewers', checked.reviewers.map(reviewer)))
      else parts.push(emptyState('No reviewer was required, and none reviewed this commit.'))
      if (checked.runs.length) parts.push(list('Verification runs', checked.runs.map(run)))
    }
    const gate = el('p', 'small push-checked-gate')
    gate.append(el('strong', '', 'Gate: '), text(decision))
    parts.push(gate)
    return parts
  }
}

customElements.define('sf-push-checked', SfPushChecked)

declare global {
  interface HTMLElementTagNameMap {
    'sf-push-checked': SfPushChecked
  }
}
