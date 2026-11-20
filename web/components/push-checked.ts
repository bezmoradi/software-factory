import { button, el, replaceKeeping, unspoofed, visible } from '../lib/dom.ts'
import { shortSha } from '../lib/format.ts'
import { icon } from './icon.ts'
import { cardFlow, openFlow, type FlowDialog, type FlowModel } from './push-flow.ts'
import type { PushCheckedBy, PushCheckedReviewer, PushCheckedRun, SessionPush } from '../types.ts'

const FINISHED = new Set(['pass', 'block', 'unknown'])
let made = 0

function text(value: string): string {
  return visible(unspoofed(value))
}

function count(n: unknown): number {
  return Math.max(0, Number(n) || 0)
}

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`
}

function resultOf(r: PushCheckedReviewer): [string, string, string] {
  switch (r.state) {
    case 'pass':
      return ['check', 'ok', 'passed']
    case 'block':
      return r.overridden ? ['warning', 'warn', 'blocked, overridden by you'] : ['error', r.advisory ? 'warn' : 'bad', 'blocked']
    case 'unknown':
      return r.setAside ? ['warning', 'warn', 'passed, but set aside'] : ['warning', 'warn', 'no verdict the gate counts']
    case 'failed':
      return ['error', 'bad', "didn't finish"]
    case 'running':
      return ['info', '', 'still running']
  }
  return ['info', '', 'no review of this commit']
}

function details(r: PushCheckedReviewer): string {
  const parts: string[] = []
  const blocking = count(r.blocking)
  if (FINISHED.has(r.state)) parts.push(blocking ? plural(blocking, 'blocking finding', 'blocking findings') : 'no blocking findings')
  const asked = count(r.requirements)
  if (asked) {
    const verified = count(r.verified)
    const violated = count(r.violated)
    const notVerified = count(r.notVerified)
    const unreported = Math.max(0, asked - verified - violated - notVerified)
    parts.push(`requirements: ${verified} verified, ${violated} violated, ${notVerified} not verified${unreported ? `, ${unreported} not reported` : ''}`)
  }
  const runs = count(r.runs)
  if (runs > 1) parts.push(plural(runs, 'run', 'runs'))
  return parts.join(' · ')
}

function tags(r: PushCheckedReviewer): HTMLElement[] {
  const out: HTMLElement[] = []
  if (r.required) out.push(el('span', `tag${r.counted ? ' ok' : ' warn'}`, r.counted ? 'counts for the push' : "doesn't count for the push"))
  else out.push(el('span', 'tag', r.advisory ? 'advisory, never counts' : 'not required for this push'))
  if (r.unchecked) out.push(el('span', 'tag warn', 'not rechecked since the engine restarted'))
  return out
}

function reviewer(r: PushCheckedReviewer): HTMLElement {
  const [name, tone, said] = resultOf(r)
  const li = el('li', 'push-checked-item')
  const head = el('div', 'push-checked-head')
  head.append(icon(name, `icon push-icon ${tone}`.trim()), el('strong', '', text(r.name || r.agent)), el('span', '', `: ${said}`), ...tags(r))
  li.append(head)
  const more = details(r)
  if (more) li.append(el('div', 'small muted', more))
  if (r.setAside) li.append(el('div', 'small muted', `Set aside: ${text(r.setAside)}`))
  return li
}

function run(r: PushCheckedRun): HTMLElement {
  const kind = text(r.kind)
  const [name, tone, said] = r.state === 'passed' ? ['check', 'ok', 'passed'] : r.state === 'running' ? ['info', '', 'still running'] : r.state === 'failed' ? ['error', 'bad', `failed, exit code ${count(r.code)}`] : ['warning', 'warn', text(r.state)]
  const li = el('li', 'push-checked-item')
  const head = el('div', 'push-checked-head')
  head.append(icon(name, `icon push-icon ${tone}`.trim()), el('strong', '', `Verification run (${kind})`), el('span', '', `: ${said}`))
  li.append(head)
  return li
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
  decision: string
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
    this.#model = p ? cardFlow(p, checked) : null
    if (this.#shown && this.#model) this.#shown.diagram.model = this.#model
    else this.#shown?.modal.close()
    const key = JSON.stringify(p ? [p, v?.decision ?? ''] : null)
    if (key === this.#key) return
    this.#key = key
    if (!p) {
      replaceKeeping(this, [])
      return
    }
    replaceKeeping(this.#box, this.#facts(p, checked, v?.decision ?? ''))
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
      else parts.push(el('p', 'small muted', 'No reviewer was required, and none reviewed this commit.'))
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
