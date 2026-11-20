import { byId } from './lib/dom.ts'
import type { Started, Step } from './lib/path.ts'
import type { Toast } from './components/toasts.ts'
import type { AgentInfo, Catalog, ChangeFrom, Changes, GateEvent, IntentRecord, PlanFileEvent, SessionInfo, WorkflowEvent } from './types.ts'

export interface Session {
  sid: string
  info: SessionInfo
  view: HTMLElementTagNameMap['sf-conversation']
  epoch: string
  seq: number
  replayAt: number
  rebase: boolean
  gate: GateEvent | null
  gateKey: string
  gateAt: number
  workflow: WorkflowEvent | null
  workflowSeq: number
  planfile: PlanFileEvent | null
  agents: AgentInfo[]
  intent: IntentRecord | null
  catalog: Catalog | null
  total: number
  cost: number
  changes: Changes | null
  changesKey: string
  changesAt: number
  compare: { from: ChangeFrom; commit: string }
  forkStart: Started
  busy: boolean
  step: Step
  compacting: string
  context: { tokens: number; level: string } | null
  asked: { requirements?: { hash: string; at: number }; plan?: { hash: string; at: number } }
  refused: { requirements?: { hash: string; text: string }; plan?: { hash: string; text: string } }
  audit: { key: string; entries: AuditEntry[]; refusals: Record<string, number> } | null
  auditSeen: number
  lastAt?: number
}

export interface AuditEntry {
  time: number
  kind: string
  by: string
  result: string
  commit: string
  reviewer?: string
  verdict?: string
  path?: string
  reason: string
  personReason?: string
  missing?: string[]
  session?: string
  branch?: string
  from?: string
  fromRef?: string
  source?: string
}

const ACTIVE = ['pending', 'running', 'waiting']

export const scroller = byId('scroller')
export const toast = (t: Toast) => byId<HTMLElementTagNameMap['sf-toasts']>('toasts').show(t)
export const sessions = new Map<string, Session>()
export const state = { connected: false, note: '', stream: '', supervisor: '' }
export const armed = new Set<string>()
let paintQueued = false
let painter: () => void = () => {}

export function setPainter(fn: () => void) {
  painter = fn
}

export function paint() {
  painter()
}

export function startedOf(s: Session): Started {
  if (s.changes && s.changes.from && s.changes.from !== 'fork') return s.forkStart
  return !s.changes ? null : s.changes.noBase ? 'nobase' : s.changes.commits.length > 0
}

export function inTurn(s: Session): boolean {
  return s.busy || !!s.info.busy
}

export function working(s: Session): boolean {
  return inTurn(s) || !!s.compacting || s.agents.some(a => a.status === 'running')
}

export function reviewsActive(s: Session): boolean {
  const required = s.gate?.reviewers ?? []
  return required.some(r => r.state === 'running' || ACTIVE.includes(s.agents.filter(a => a.type === r.type).at(-1)?.status ?? ''))
}

export function pickingSize(s: Session) {
  return !!(state.connected && s.gate && s.gate.chosen === false && s.view.blank && controllable(s))
}

export function controllable(s: Session) {
  const i = s?.info
  return !!i && i.controlled && i.verified && i.running && i.live && !i.disputed && !i.outdated
}

export function sleeping(s: Session | null) {
  const i = s?.info
  return !!i && i.kind === 'background' && i.verified === true && !i.doubled && !i.running && !i.live && /^[0-9a-f]{8}$/.test(i.id || '')
}

export function current(): Session | null {
  return current.key.startsWith('s/') ? sessions.get(current.key.slice(2)) || null : null
}

export function changesSession(): Session | null {
  return current.key.startsWith('c:') ? sessions.get(current.key.slice(2)) || null : current()
}

current.key = 'overview'

function flushPaint() {
  if (!paintQueued) return
  paintQueued = false
  paint()
}

export function schedulePaint() {
  if (paintQueued) return
  paintQueued = true
  requestAnimationFrame(flushPaint)
  if (document.hidden) setTimeout(flushPaint, 100)
}

document.addEventListener('visibilitychange', () => {
  if (document.hidden && paintQueued) setTimeout(flushPaint, 100)
})

export function waitingOf(s: Session) {
  return s.info.live ? s.view.waiting : []
}

export function say(message: string) {
  byId('hint').textContent = message
  if (message && document.body.classList.contains('side-full')) toast({ id: 'say', title: message, tone: 'info' })
}
