import { el, unspoofed, visible } from '../lib/dom.ts'
import { shortSha } from '../lib/format.ts'
import { icon } from './icon.ts'
import type { PushCheckedBy, PushCheckedReviewer, Reviewer, ReviewRole, ReviewsView, SessionPush } from '../types.ts'

export const RESULTS: Record<string, [string, string, string, string]> = {
  pushed: ['check', 'ok', 'Pushed', 'Pushed to'],
  'up-to-date': ['check', 'ok', 'Pushed', 'Pushed to'],
  held: ['warning', 'warn', 'Push held', 'Push held for'],
  denied: ['error', 'bad', 'Push denied', 'Push denied for'],
  refused: ['error', 'bad', 'Not pushed', 'Not pushed to'],
  unclear: ['warning', 'warn', 'Push outcome unclear', 'Push outcome unclear for'],
}

export type FlowReviewerState = 'pending' | 'running' | 'pass' | 'block' | 'overridden' | 'set-aside' | 'unknown' | 'failed' | 'none'

export interface FlowReviewer {
  name: string
  state: FlowReviewerState
  blocking?: number
  advisory?: boolean
  required?: boolean
  tags?: string[]
}

export interface FlowRun {
  kind: string
  state: string
}

export type FlowGateState = 'waiting' | 'deciding' | 'ready' | 'holding' | 'decided'

export interface FlowModel {
  commit: string
  reviewers: FlowReviewer[] | null
  runs: FlowRun[]
  gate: { state: FlowGateState; decision: string }
  outcome: string
  outcomeNote?: string
}

const ROUTES = ['pushed', 'held', 'denied', 'refused']
const DONE = new Set<FlowReviewerState>(['pass', 'block', 'overridden', 'set-aside', 'unknown'])
const SEVERE = new Set(['Critical', 'High'])

type Look = [shape: string, tone: string, word: string]

function text(value: string): string {
  return visible(unspoofed(String(value ?? '')))
}

function count(n: unknown): number {
  return Math.max(0, Number(n) || 0)
}

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`
}

function reviewerLook(r: FlowReviewer): Look {
  switch (r.state) {
    case 'pending':
      return ['more', '', 'waiting']
    case 'running':
      return ['', '', 'running']
    case 'pass':
      return ['check', 'ok', 'passed']
    case 'block':
      return ['error', r.advisory || r.required === false ? 'warn' : 'bad', 'blocked']
    case 'overridden':
      return ['warning', 'warn', 'overridden']
    case 'set-aside':
      return ['warning', 'warn', 'set aside']
    case 'unknown':
      return ['warning', 'warn', 'no verdict']
    case 'failed':
      return ['error', 'bad', "didn't finish"]
  }
  return ['info', '', 'no review']
}

function runLook(r: FlowRun): Look {
  if (r.state === 'pending') return ['more', '', 'waiting']
  if (r.state === 'running') return ['', '', 'running']
  if (r.state === 'passed') return ['check', 'ok', 'passed']
  if (r.state === 'failed') return ['error', 'bad', 'failed']
  return ['warning', 'warn', text(r.state)]
}

function gateLook(g: FlowModel['gate']): Look {
  switch (g.state) {
    case 'waiting':
      return ['more', '', text(g.decision) || 'waiting for reviews']
    case 'deciding':
      return ['', '', text(g.decision) || 'deciding']
    case 'ready':
      return ['check', 'ok', text(g.decision) || 'would allow a push']
    case 'holding':
      return ['warning', 'warn', text(g.decision) || 'would hold the push']
  }
  return ['', '', text(g.decision)]
}

function phase(state: string): string {
  return state === 'pending' ? 'push-flow-pending' : state === 'running' ? 'push-flow-running' : ''
}

function hidden(cls: string): HTMLElement {
  const line = el('span', cls)
  line.setAttribute('aria-hidden', 'true')
  return line
}

function node(cls: string, look: Look, name: string, ...extras: Node[]): HTMLElement {
  const box = el('div', `push-flow-node ${cls}`.trim())
  const head = el('span', 'push-flow-name')
  if (cls.includes('push-flow-running')) head.append(hidden('push-flow-spin'))
  else if (look[0]) head.append(icon(look[0], `icon push-icon ${look[1]}`.trim()))
  head.append(el('strong', '', name))
  box.append(head)
  if (look[2]) box.append(el('span', 'push-flow-word', look[2]))
  box.append(...extras)
  return box
}

function branch(content: HTMLElement, aside: boolean, live: boolean): HTMLElement {
  const li = el('li', `push-flow-branch${aside ? ' push-flow-aside' : ''}${live ? ' push-flow-live' : ''}`)
  li.append(hidden('push-flow-arm push-flow-arm-in'), content, hidden('push-flow-arm push-flow-arm-out'))
  return li
}

function reviewer(r: FlowReviewer): HTMLElement {
  const look = reviewerLook(r)
  const extras: HTMLElement[] = []
  const blocking = count(r.blocking)
  const aside = r.required === false
  if (blocking && DONE.has(r.state)) extras.push(el('span', `tag ${!aside && r.state === 'block' ? 'bad' : 'warn'}`, `${blocking} blocking`))
  if (aside) extras.push(el('span', 'tag', r.advisory ? 'advisory' : 'not required'))
  for (const t of r.tags ?? []) extras.push(el('span', 'tag warn', text(t)))
  return branch(node(phase(r.state), look, text(r.name), ...extras), aside, r.state === 'running')
}

function run(r: FlowRun): HTMLElement {
  return branch(node(phase(r.state), runLook(r), `Verification run (${text(r.kind)})`), false, r.state === 'running')
}

function taken(m: FlowModel): string {
  return m.outcome === 'up-to-date' ? 'pushed' : m.outcome
}

function routeLabel(key: string, m: FlowModel): string {
  const shown = key === taken(m) ? m.outcome : key
  return (RESULTS[shown] ?? ['', '', text(shown)])[2]
}

function route(key: string, m: FlowModel): HTMLElement {
  const decided = !!m.outcome
  const on = decided && key === taken(m)
  const li = el('li', `push-flow-route${on ? ' push-flow-taken' : decided ? '' : ' push-flow-undecided'}`)
  const [shape, tone] = RESULTS[on ? m.outcome : key] ?? ['warning', 'warn']
  const note = on ? text(m.outcomeNote ?? '') : ''
  const extras = note ? [el('span', 'push-flow-note small', note)] : []
  li.append(hidden('push-flow-arm push-flow-arm-in'), node('', [shape, on ? tone : '', decided ? (on ? 'taken' : 'not taken') : ''], routeLabel(key, m), ...extras))
  return li
}

function tally(words: string[]): string {
  const seen = new Map<string, number>()
  for (const w of words) seen.set(w, (seen.get(w) ?? 0) + 1)
  return [...seen].map(([w, n]) => `${n} ${w}`).join(', ')
}

function summary(m: FlowModel): string {
  const said: string[] = []
  if (m.commit) said.push(`Commit ${shortSha(text(m.commit))}.`)
  if (!m.reviewers) said.push('The engine decided before it looked at the reviews.')
  else {
    const counted = m.reviewers.filter(r => r.required !== false)
    const others = m.reviewers.filter(r => r.required === false)
    if (counted.length) said.push(`${plural(counted.length, 'required reviewer', 'required reviewers')}: ${tally(counted.map(r => reviewerLook(r)[2]))}.`)
    if (others.length) said.push(`${plural(others.length, "reviewer that doesn't count", "reviewers that don't count")}: ${tally(others.map(r => reviewerLook(r)[2]))}.`)
    if (!m.reviewers.length) said.push('No reviewer.')
  }
  if (m.runs.length) said.push(`${plural(m.runs.length, 'verification run', 'verification runs')}: ${tally(m.runs.map(r => runLook(r)[2]))}.`)
  const note = text(m.outcomeNote ?? '')
  if (m.outcome) said.push(`Gate: ${routeLabel(taken(m), m)}${note ? `, ${note}` : ''}.`)
  else said.push(`Gate: ${gateLook(m.gate)[2]}; no push decided yet.`)
  return said.join(' ')
}

export function flowDiagram(m: FlowModel): HTMLElement {
  const figure = el('figure', 'push-flow')
  const chart = el('div', 'push-flow-chart')
  const sha = m.commit ? [el('code', 'push-flow-sha', shortSha(text(m.commit)))] : []
  const commit = node('push-flow-commit', ['code', '', ''], 'Commit', ...sha)
  const fan = el('ul', 'push-flow-fan')
  fan.setAttribute('role', 'list')
  fan.setAttribute('aria-label', 'Checks of this commit')
  if (!m.reviewers) fan.append(branch(node('', ['info', '', 'not looked at'], 'Reviews'), true, false))
  else for (const r of m.reviewers) fan.append(reviewer(r))
  for (const r of m.runs) fan.append(run(r))
  if (!fan.childElementCount) fan.append(branch(node('', ['info', '', 'none required'], 'No reviewer'), true, false))
  const gate = node(`push-flow-gate ${phase(m.gate.state === 'waiting' ? 'pending' : m.gate.state === 'deciding' ? 'running' : '')}`, gateLook(m.gate), 'Gate')
  const routes = el('ul', 'push-flow-routes')
  routes.setAttribute('role', 'list')
  routes.setAttribute('aria-label', m.outcome ? "The gate's routes" : "The gate's routes, none taken yet")
  const keys = !m.outcome || ROUTES.includes(taken(m)) ? ROUTES : [...ROUTES, taken(m)]
  for (const key of keys) routes.append(route(key, m))
  chart.append(commit, hidden('push-flow-wire'), fan, hidden(`push-flow-wire${m.gate.state === 'deciding' ? ' push-flow-wire-live' : ''}`), gate, hidden('push-flow-wire'), routes)
  figure.append(chart, el('figcaption', 'small muted push-flow-caption', summary(m)))
  return figure
}

function checkedReviewer(r: PushCheckedReviewer): FlowReviewer {
  const states: Record<string, FlowReviewerState> = { pass: 'pass', block: r.overridden ? 'overridden' : 'block', unknown: r.setAside ? 'set-aside' : 'unknown', failed: 'failed', running: 'running' }
  const tags: string[] = []
  if (r.required && !r.counted && r.state === 'pass') tags.push("doesn't count")
  if (r.unchecked) tags.push('not rechecked')
  return { name: r.name || r.agent, state: states[r.state] ?? 'none', blocking: count(r.blocking), advisory: r.advisory, required: r.required, tags }
}

function heldNote(p: SessionPush): string {
  if (p.result === 'up-to-date') return 'already on the remote'
  if (p.result !== 'held' || !p.card) return ''
  return p.expires && p.expires <= Date.now() ? 'card expired' : 'waiting for you'
}

export function cardFlow(p: SessionPush, checked: PushCheckedBy | null): FlowModel {
  const label = (RESULTS[p.result] ?? ['', '', text(p.result)])[2]
  return {
    commit: p.commit,
    reviewers: checked ? checked.reviewers.map(checkedReviewer) : null,
    runs: checked ? checked.runs.map(r => ({ kind: r.kind, state: r.state })) : [],
    gate: { state: 'decided', decision: label.charAt(0).toLowerCase() + label.slice(1) },
    outcome: p.result,
    outcomeNote: heldNote(p),
  }
}

function severe(role: ReviewRole | undefined): string[] {
  if (!role || !Array.isArray(role.findings) || !(role.state === 'pass' || role.state === 'block' || role.state === 'unknown')) return []
  const n = role.findings.filter(f => !!f && SEVERE.has(f.severity)).length
  return n ? [`${n} critical or high`] : []
}

function roleState(role: ReviewRole): FlowReviewerState {
  if (role.state === 'block' && role.override) return 'overridden'
  return role.state
}

function gateState(g: Reviewer, role: ReviewRole | undefined): FlowReviewerState {
  if (role && (g.state === 'none' || g.state === 'stale' || g.state === 'running')) return roleState(role)
  switch (g.state) {
    case 'none':
    case 'stale':
      return 'pending'
    case 'unknown':
      return g.setAside ? 'set-aside' : 'unknown'
    case 'unclean':
    case 'untrusted':
      return 'unknown'
    case 'not-installed':
      return 'none'
  }
  return g.state
}

export function liveFlow(v: ReviewsView): FlowModel | null {
  const r = v.review
  const head = v.head || r?.commit || ''
  const required = v.gate ?? []
  const roles = r && r.commit === head ? r.roles : []
  if (!head || (!required.length && !roles.length)) return null
  const used = new Set<ReviewRole>()
  const reviewers: FlowReviewer[] = required.map(g => {
    const role = roles.find(x => x.agent === g.type && !used.has(x))
    if (role) used.add(role)
    const tags = severe(role)
    if (g.state === 'not-installed') tags.push('not installed')
    if (g.state === 'unclean' || g.state === 'untrusted') tags.push(g.state)
    return { name: g.name || g.type, state: gateState(g, role), required: true, tags }
  })
  for (const role of roles) if (!used.has(role)) reviewers.push({ name: role.name || role.agent, state: roleState(role), required: false, advisory: !!role.advisory, tags: severe(role) })
  const open = reviewers.some(x => x.required !== false && (x.state === 'pending' || x.state === 'running'))
  const missing = v.missing ?? []
  const gate: FlowModel['gate'] = v.pushed ? { state: 'decided', decision: 'pushed' }
    : v.pushing ? { state: 'deciding', decision: 'checking the push' }
    : open ? { state: 'waiting', decision: 'waiting for reviews' }
    : v.allowed ? { state: 'ready', decision: 'would allow a push' }
    : { state: 'holding', decision: missing.length ? `would hold: ${plural(missing.length, 'item', 'items')} missing` : 'would hold the push' }
  return { commit: head, reviewers, runs: [], gate, outcome: v.pushed ? 'pushed' : '', outcomeNote: v.pushed ? 'this commit is on the remote' : '' }
}
