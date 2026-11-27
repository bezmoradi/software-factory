import { button, el, replaceKeeping } from '../lib/dom.ts'
import { modal, type Modal } from '../lib/dialog.ts'
import { docsId } from '../lib/page.ts'
import { flowNode, key, late, list, node, phase, route, status, text, wire, zone } from './flow-parts.ts'
import type { FlowModel } from '../types.ts'

export type { FlowModel } from '../types.ts'

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
    wire(snapshot),
    checks,
    wire(out),
    zone(zones.get('engine'), 'engine', gate),
    wire(decision),
    zone(zones.get('git'), 'git', routes),
  )
  const caption = el('figcaption', 'push-flow-status')
  caption.append(...status(m))
  figure.append(caption, chart, key(m))
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

export function openFlow(title: string, intro: string, model: FlowModel): FlowDialog {
  const diagram = el('sf-flow-diagram')
  diagram.model = model
  const m = modal({
    kind: 'flow-dialog',
    title,
    docs: docsId(title),
    expand: true,
    body: [el('p', 'small', intro), diagram],
    actions: [button('Close', 'ghost small', () => m.close())],
    focus: 'heading',
  })
  return { modal: m, diagram }
}
