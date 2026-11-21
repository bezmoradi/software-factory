import { byId } from './lib/dom.ts'
import type { Toast } from './components/toasts.ts'
import type { AgentInfo, Catalog, ChangeFrom, Changes, GateEvent, IntentRecord, PlanFileEvent, SessionAction, SessionInfo, WorkflowEvent } from './types.ts'

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
  busy: boolean
  compacting: string
  context: { level: string; text: string; said: string; stop: boolean } | null
  asked: { requirements?: { hash: string; at: number }; plan?: { hash: string; at: number } }
  refused: { requirements?: { hash: string; text: string }; plan?: { hash: string; text: string } }
  audit: { key: string; entries: AuditEntry[]; refusals: Record<string, number>; history: HistoryLine[]; friction: string[] } | null
  auditSeen: number
}

export interface HistoryLine {
  time: number
  text: string
  tone: string
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


export const scroller = byId('scroller')
export const toast = (t: Toast) => byId<HTMLElementTagNameMap['sf-toasts']>('toasts').show(t)
export const sessions = new Map<string, Session>()
export const state = { connected: false, note: '', stream: '', supervisor: '', imageTypes: [] as string[] }
export const armed = new Set<string>()
let paintQueued = false
let painter: () => void = () => {}

export function setPainter(fn: () => void) {
  painter = fn
}

export function paint() {
  painter()
}

export function inTurn(s: Session): boolean {
  return s.busy || !!s.info.busy
}

export function working(s: Session): boolean {
  return inTurn(s) || !!s.compacting || s.agents.some(a => a.status === 'running')
}


export function pickingSize(s: Session) {
  return !!(state.connected && s.gate && s.gate.chosen === false && s.view.blank && allowed(s, 'control'))
}

export const UNLISTED = 'The engine no longer lists this session.'

export function allowed(s: Session | null | undefined, action: SessionAction): boolean {
  return s?.info.actions?.[action] === ''
}

export function offWhy(s: Session, action: SessionAction): string {
  const why = s.info.actions?.[action]
  return why === undefined ? UNLISTED : why
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
