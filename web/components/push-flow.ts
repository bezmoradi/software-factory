import { el, replaceKeeping, unspoofed, visible } from '../lib/dom.ts'
import { modal, type Modal } from '../lib/dialog.ts'
import { icon } from './icon.ts'
import type { FlowModel, FlowNode, FlowRoute, FlowZone } from '../types.ts'

export type { FlowModel } from '../types.ts'

function text(value: string): string {
  return visible(unspoofed(String(value ?? '')))
}

function phase(state: string): string {
  return state === 'pending' ? 'push-flow-pending' : state === 'running' ? 'push-flow-running' : ''
}

function late(m: FlowModel): boolean {
  return !!m.expires && m.expires <= Date.now()
}

function hidden(cls: string): HTMLElement {
  const line = el('span', cls)
  line.setAttribute('aria-hidden', 'true')
  return line
}

function node(cls: string, look: FlowNode['look'], name: string, ...extras: Node[]): HTMLElement {
  const box = el('div', `push-flow-node ${cls}`.trim())
  const head = el('span', 'push-flow-name')
  if (cls.includes('push-flow-running')) head.append(hidden('push-flow-spin'))
  else if (look.icon) head.append(icon(look.icon, `icon push-icon ${look.tone}`.trim()))
  head.append(el('strong', '', name))
  box.append(head)
  if (look.word) box.append(el('span', 'push-flow-word', text(look.word)))
  box.append(...extras)
  return box
}

function branch(content: HTMLElement, aside: boolean, state: string): HTMLElement {
  const li = el('li', `push-flow-branch${aside ? ' push-flow-aside' : ''}${state === 'running' ? ' push-flow-live' : state === 'pending' ? ' push-flow-wait' : ''}`)
  li.append(hidden('push-flow-arm push-flow-arm-in'), content, hidden('push-flow-arm push-flow-arm-out'))
  return li
}

function flowNode(n: FlowNode): HTMLElement {
  const extras = n.tags.map(t => el('span', t.tone ? `tag ${t.tone}` : 'tag', text(t.label)))
  return branch(node(phase(n.phase), n.look, text(n.name), ...extras), n.aside, n.phase)
}

function route(r: FlowRoute, m: FlowModel): HTMLElement {
  const li = el('li', `push-flow-route${r.taken ? ' push-flow-taken' : r.decided ? '' : ' push-flow-undecided'}`)
  const note = text(r.taken && late(m) ? m.expiredNote ?? r.note : r.note)
  const extras = note ? [el('span', 'push-flow-note small', note)] : []
  li.append(hidden('push-flow-arm push-flow-arm-in'), node('', r.look, text(r.label), ...extras))
  return li
}

function zone(z: FlowZone | undefined, kind: string, ...body: HTMLElement[]): HTMLElement {
  const box = el('div', `push-flow-zone push-flow-zone-${kind}${z?.untrusted ? ' push-flow-untrusted' : ''}`)
  const head = el('p', 'push-flow-head')
  head.append(el('span', 'push-flow-zone-name', text(z?.name ?? '')), el('span', 'push-flow-zone-note', text(z?.note ?? '')))
  box.append(head, ...body)
  return box
}

function wire(label: string, live = false): HTMLElement {
  const line = hidden(`push-flow-wire${live ? ' push-flow-wire-live' : ''}`)
  if (label) line.append(el('span', 'push-flow-wire-label', label))
  return line
}

function list(cls: string, label: string, items: HTMLElement[]): HTMLElement {
  const ul = el('ul', `push-flow-list ${cls}`)
  ul.setAttribute('role', 'list')
  ul.setAttribute('aria-label', label)
  ul.append(...items)
  return ul
}

function key(m: FlowModel): HTMLElement {
  const ul = list('push-flow-key', 'Key to the diagram', [])
  for (const k of m.key) {
    const li = el('li', 'push-flow-key-item')
    li.append(hidden(`push-flow-swatch push-flow-swatch-${k.swatch}`), el('span', '', text(k.words)))
    ul.append(li)
  }
  return ul
}

function diagram(m: FlowModel): HTMLElement {
  const zones = new Map(m.zones.map(z => [z.kind, z]))
  const figure = el('figure', 'push-flow')
  const chart = el('div', 'push-flow-chart')
  const sha = m.commit ? [el('code', 'push-flow-sha', text(m.commit))] : []
  const commit = node('push-flow-commit', { icon: 'code', tone: '', word: '' }, 'Commit', ...sha)
  const checks = el('div', 'push-flow-checks')
  checks.append(zone(zones.get('review'), 'review', list('push-flow-fan', 'Reviews of this commit', m.reviews.map(flowNode))))
  if (m.runs.length) checks.append(zone(zones.get('verify'), 'verify', list('push-flow-fan', 'Verification runs of this commit', m.runs.map(flowNode))))
  const gate = node(`push-flow-gate ${phase(m.gate.phase)}`, m.gate.look, text(m.gate.name))
  const routes = list('push-flow-routes', text(m.routesLabel), m.routes.map(r => route(r, m)))
  const [snapshot, out, decision] = m.wires
  chart.append(
    zone(zones.get('session'), 'session', commit),
    wire(text(snapshot ?? '')),
    checks,
    wire(text(out ?? ''), m.outLive),
    zone(zones.get('engine'), 'engine', gate),
    wire(text(decision ?? '')),
    zone(zones.get('git'), 'git', routes),
  )
  figure.append(chart, key(m), el('figcaption', 'small muted push-flow-caption', text(late(m) ? m.expiredCaption ?? m.caption : m.caption)))
  return figure
}

class SfFlowDiagram extends HTMLElement {
  #model: FlowModel | null = null
  #key = ''

  connectedCallback() {
    this.paint()
  }

  get model(): FlowModel | null {
    return this.#model
  }

  set model(value: FlowModel | null) {
    this.#model = value
    this.paint()
  }

  paint() {
    const m = this.#model
    const key = JSON.stringify([m, !!m && late(m)])
    if (key === this.#key) return
    this.#key = key
    this.hidden = !m
    replaceKeeping(this, m ? [diagram(m)] : [])
  }
}

customElements.define('sf-flow-diagram', SfFlowDiagram)

declare global {
  interface HTMLElementTagNameMap {
    'sf-flow-diagram': SfFlowDiagram
  }
}

export interface FlowDialog {
  modal: Modal
  diagram: SfFlowDiagram
}

export function openFlow(title: string, model: FlowModel): FlowDialog {
  const diagram = el('sf-flow-diagram')
  diagram.model = model
  const m = modal({
    kind: 'flow-dialog',
    title,
    closeButton: true,
    body: [diagram],
    focus: 'heading',
  })
  return { modal: m, diagram }
}
