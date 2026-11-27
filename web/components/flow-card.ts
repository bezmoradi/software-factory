import { button, el } from '../lib/dom.ts'
import { icon } from './icon.ts'
import { hidden, late, node, phase, status, text, wire } from './flow-parts.ts'
import type { FlowModel } from '../types.ts'

const UNDECIDED = { icon: 'more', tone: '', word: '' }

function strip(m: FlowModel): Node[] {
  const counted = m.reviews.filter(n => !n.aside)
  const reviews = el('span', 'flow-card-reviews')
  reviews.append(...(counted.length ? counted : m.reviews).map(n => node(`${phase(n.phase)}${n.aside ? ' push-flow-aside-node' : ''}`, { ...n.look, word: '' }, '')))
  const taken = m.routes.find(r => r.taken)
  const [snapshot, out, decision] = m.wires
  return [
    node('push-flow-commit', { icon: 'code', tone: '', word: '' }, 'Commit'),
    wire(snapshot, false),
    reviews,
    wire(out, false),
    node(`push-flow-gate ${phase(m.gate.phase)}`, { ...m.gate.look, word: '' }, text(m.gate.name)),
    wire(decision, false),
    taken ? node('push-flow-taken', { ...taken.look, word: '' }, 'Push') : node('push-flow-pending', UNDECIDED, 'Push'),
  ]
}

let cards = 0

export interface FlowCard {
  button: HTMLButtonElement
  show(m: FlowModel): void
}

export function flowCard(key: string, open: () => void): FlowCard {
  const card = button('', 'flow-card', open, 'button', key)
  const said = el('span', 'flow-card-status')
  said.id = `flow-card-status-${++cards}`
  card.setAttribute('aria-label', 'How this commit is checked: open the diagram')
  card.setAttribute('aria-describedby', said.id)
  const head = el('span', 'flow-card-head')
  head.append(icon('diagram', 'icon flow-card-mark'), el('span', 'flow-card-title', 'How this commit is checked'), icon('expand', 'icon flow-card-open'))
  const steps = hidden('flow-card-strip')
  card.append(head, said, steps)
  let painted = ''
  return {
    button: card,
    show(m) {
      const now = JSON.stringify([m, late(m)])
      if (now === painted) return
      painted = now
      said.replaceChildren(...status(m))
      steps.replaceChildren(...strip(m))
    },
  }
}
