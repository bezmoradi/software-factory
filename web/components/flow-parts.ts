import { el, unspoofed, visible } from '../lib/dom.ts'
import { icon } from './icon.ts'
import type { FlowLook, FlowModel, FlowNode, FlowRoute, FlowWire, FlowZone } from '../types.ts'

export function text(value: string): string {
  return visible(unspoofed(String(value ?? '')))
}

export function phase(state: string): string {
  return state === 'pending' ? 'push-flow-pending' : state === 'running' ? 'push-flow-running' : ''
}

function tone(value: string): string {
  return value ? `push-flow-tone-${value}` : ''
}

function classes(...names: string[]): string {
  return names.filter(Boolean).join(' ')
}

export function late(m: FlowModel): boolean {
  return !!m.expires && m.expires <= Date.now()
}

export function hidden(cls: string): HTMLElement {
  const line = el('span', cls)
  line.setAttribute('aria-hidden', 'true')
  return line
}

export function node(cls: string, look: FlowLook, name: string, ...extras: Node[]): HTMLElement {
  const box = el('span', classes('push-flow-node', cls, tone(look.tone)))
  const head = el('span', 'push-flow-name')
  if (cls.includes('push-flow-running')) head.append(hidden('push-flow-spin'))
  else if (look.icon) head.append(icon(look.icon, classes('icon push-icon', look.tone)))
  if (name) head.append(el('strong', '', name))
  box.append(head)
  if (look.word) box.append(el('span', 'push-flow-word', text(look.word)))
  box.append(...extras)
  return box
}

function branch(content: HTMLElement, n: FlowNode): HTMLElement {
  const li = el('li', classes('push-flow-branch', n.aside ? 'push-flow-aside' : '', n.phase === 'running' ? 'push-flow-live' : n.phase === 'pending' ? 'push-flow-wait' : '', tone(n.look.tone)))
  li.append(hidden('push-flow-arm push-flow-arm-in'), content, hidden('push-flow-arm push-flow-arm-out'))
  return li
}

export function flowNode(n: FlowNode): HTMLElement {
  const extras = n.tags.map(t => el('span', t.tone ? `tag ${t.tone}` : 'tag', text(t.label)))
  return branch(node(phase(n.phase), n.look, text(n.name), ...extras), n)
}

export function route(r: FlowRoute, m: FlowModel): HTMLElement {
  const li = el('li', classes('push-flow-route', r.taken ? 'push-flow-taken' : r.decided ? '' : 'push-flow-undecided', tone(r.look.tone)))
  const note = text(r.taken && late(m) ? m.expiredNote ?? r.note : r.note)
  const extras = note ? [el('span', 'push-flow-note small', note)] : []
  li.append(hidden('push-flow-arm push-flow-arm-in'), node('', r.look, text(r.label), ...extras))
  return li
}

export function zone(z: FlowZone | undefined, kind: string, ...body: HTMLElement[]): HTMLElement {
  const box = el('div', classes('push-flow-zone', `push-flow-zone-${kind}`, z?.untrusted ? 'push-flow-untrusted' : ''))
  const head = el('p', 'push-flow-head')
  head.append(el('span', 'push-flow-zone-name', text(z?.name ?? '')), el('span', 'push-flow-zone-note', text(z?.note ?? '')))
  box.append(head, ...body)
  return box
}

export function wire(w: FlowWire | undefined, labelled = true): HTMLElement {
  const line = hidden(classes('push-flow-wire', w?.phase === 'running' ? 'push-flow-wire-live' : w?.phase === 'pending' ? 'push-flow-wire-wait' : '', tone(w?.tone ?? '')))
  const label = labelled ? text(w?.label ?? '') : ''
  if (label) line.append(el('span', 'push-flow-wire-label', label))
  return line
}

export function list(cls: string, label: string, items: HTMLElement[]): HTMLElement {
  const ul = el('ul', `push-flow-list ${cls}`)
  ul.setAttribute('role', 'list')
  ul.setAttribute('aria-label', label)
  ul.append(...items)
  return ul
}

export function key(m: FlowModel): HTMLElement {
  const ul = list('push-flow-key', 'Key to the diagram', [])
  for (const k of m.key) {
    const li = el('li', 'push-flow-key-item')
    li.append(hidden(`push-flow-swatch push-flow-swatch-${k.swatch}`), el('span', '', text(k.words)))
    ul.append(li)
  }
  return ul
}

export function status(m: FlowModel): Node[] {
  const look = late(m) && m.expiredStatus ? m.expiredStatus : m.status
  const parts: Node[] = []
  if (look.icon) parts.push(icon(look.icon, classes('icon push-icon', look.tone)))
  else if (m.gate.phase === 'running') parts.push(hidden('push-flow-spin'))
  const words = el('span', 'push-flow-said')
  words.append(el('strong', '', text(look.word)))
  if (m.detail) words.append(' ', el('span', 'push-flow-detail', text(m.detail)))
  parts.push(words)
  return parts
}
