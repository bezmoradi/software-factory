import { api, events, exchange, forget, signedIn, trade, upload } from './api.ts'
import { claudeLogin, signInState, type SignInState } from './components/login.ts'
import { button, el, emit, fileLink, shown, visible, visibleCommand } from './lib/dom.ts'
import { complete } from './lib/complete.ts'
import { initPrefs } from './lib/prefs.ts'
import { agentWord, attachCommand, baseName, isClaude, label, logsCommand, modeLabel, setAgentNames, shortSha } from './lib/format.ts'
import { stalled } from './lib/stall.ts'
import { paintPath, type Started, type Step, type Turn } from './lib/path.ts'
import { placeMenu, sessionState, warnings } from './components/overview.ts'
import type { AgentInfo, Catalog, CardHost, CatalogEntry, Changes, Check, Consent, Expect, FileView, GateEvent, IntentRecord, Needs, PlanFileEvent, Project, ProjectsView, PushPanelView, ForgePanelView, Reviewer, ReviewerView, ReviewsView, SessionEvent, SessionInfo, StreamEvent, TeamView, WorkflowEvent } from './types.ts'
import './components/changes.ts'
import './components/docs.ts'
import './components/file.ts'
import { icon } from './components/icon.ts'
import './components/check.ts'
import './components/gate.ts'
import './components/conversation.ts'
import './components/intent.ts'
import './components/projects.ts'
import './components/reviews.ts'
import { when } from './components/push-panel.ts'
import './components/forge-panel.ts'
import './components/setup.ts'
import './components/tabs.ts'
import './components/toasts.ts'
import type { Toast } from './components/toasts.ts'
import { askTrust, consentFor, money } from './components/consent.ts'

interface Session {
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

interface AuditEntry {
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
}

const HISTORY: Record<string, string> = { pushed: 'Pushed', 'up-to-date': 'Already on the remote', held: 'Held', denied: 'Denied', refused: 'Refused', unclear: 'Push outcome unclear' }
const EXCEPTION: Record<string, string> = { 'allow-push': 'Push allowed with items missing', override: 'Verdict overridden', 'allow-file': 'Flagged file allowed', issue: 'Issue', 'pull-request': 'Pull request', review: 'Review', findings: 'Blocking findings for the agent', 'pr-comment': 'Pull request comment', 'approve-startup': 'Start-up files approved', 'approve-requirements': 'Requirements approved', 'approve-plan': 'Plan approved', 'approve-team': 'Team approved', 'forget-team': 'Team approval forgotten' }
const OUTCOME: Record<string, string> = { unclear: 'outcome unclear', abandoned: 'abandoned', expired: 'expired', commented: 'posted', sent: 'sent', withheld: 'withheld', 'not-sent': 'not sent' }
const HIDDEN_RESULTS = ['sending', 'posting']
const FRICTION: Record<string, string> = { 'edit-held': 'Claude tried to edit before the plan was approved', 'define-held': 'Define was started with uncommitted files', 'plan-held': 'Plan was started before the requirements were approved', 'reviewer-early': 'a reviewer was started before Implement was done', 'reviewer-fence': 'a reviewer tried to change the repository', 'reviewer-capped': 'a reviewer was started past the round limit', 'reviewer-workflow': 'a reviewer was started from a workflow', 'reviewer-resumed': 'Claude tried to continue a finished reviewer', 'push-held': 'a push was held for what it was missing', 'push-refused': 'the agent ran a push-shaped command, which the gate turned away', 'remote-held': 'a remote tool was held for you', 'protected-write': 'a write by Claude\'s file tools was refused as protected', 'reviewer-provider': "a reviewer came from a provider the gate doesn't trust", 'tasks-held': 'Tasks was started before its step', 'implement-held': 'Implement was started before the plan was approved' }

function stepOf(text: string): Step {
  const skill = /^\s*(?:<command-name>\s*)?\/?(?:knackpack:(challenge-requirements|plan-implementation)|speckit-(specify|plan|tasks))\b/.exec(text)
  return skill?.[1] === 'challenge-requirements' || skill?.[2] === 'specify' ? 'define' : skill?.[1] === 'plan-implementation' || skill?.[2] === 'plan' ? 'plan' : skill?.[2] === 'tasks' ? 'tasks' : ''
}


const COMMIT_ASK = "Commit every uncommitted change on this branch, new files included, so a push sends all of it. If a file shouldn't be committed, such as a secret, a local setting or a scratch file, don't commit it: ask me whether to delete it or add it to .gitignore."

function checkOf(r: Reviewer): Check {
  return { type: r.type, name: r.name, why: r.why ?? '' }
}
const ACTIVE = ['pending', 'running', 'waiting']
const RESET_NOTE = 'The session restarted, so what came before this point is shown only as far as the agent kept it.'

const byId = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T
const scroller = byId('scroller')
const toast = (t: Toast) => byId<HTMLElementTagNameMap['sf-toasts']>('toasts').show(t)
const sessions = new Map<string, Session>()
const state = { connected: false, note: '', stream: '', supervisor: '' }
let reviewer: ReviewerView | null = null
let starterNames = new Map<string, string>()
let projectsAt = 0
const WIDE_REM = 52
let wide: boolean | null = null
let sideAt = ''
let sideFull = false
let sideScroll = 0
let retry = 1000
let lost = 0
const sending = new Set<string>()
const armed = new Set<string>()
const removals = new Map<string, { message: string; offer: { flag: string; value: string } | null }>()
const busyManage = new Map<string, { action: string; at: number }>()
const reviewAsking = new Set<string>()
const pushAsking = new Set<string>()
const pushWatched = new Map<string, number>()
const pushSaid = new Map<string, { text: string; tone: string }>()
const PUSH_SAID: Record<string, { text: string; tone: string }> = { held: { text: 'Held', tone: 'warn' }, denied: { text: 'Denied', tone: 'bad' }, refused: { text: 'Not pushed', tone: 'bad' } }
const answerAsking = new Set<string>()
let paintQueued = false

const checks = new Map<string, { check: Check; node: HTMLElementTagNameMap['sf-check']; li: HTMLLIElement }>()

function syncChecks(list: Reviewer[]): { check: Check; node: HTMLElementTagNameMap['sf-check'] }[] {
  const out = list.map(r => {
    const check = checkOf(r)
    let hit = checks.get(r.type)
    if (!hit) {
      const node = el('sf-check')
      const li = el('li')
      li.append(node)
      hit = { check, node, li }
      checks.set(r.type, hit)
      node.check = check
    } else if (JSON.stringify(hit.check) !== JSON.stringify(check)) {
      hit.check = check
      hit.node.check = check
    }
    return hit
  })
  for (const [type, hit] of checks) if (!list.some(r => r.type === type)) {
    hit.li.remove()
    checks.delete(type)
  }
  const ul = byId('checks')
  out.forEach((hit, i) => {
    if (ul.children[i] !== hit.li) ul.insertBefore(hit.li, ul.children[i] ?? null)
  })
  return out
}

function ensure(sid: string): Session {
  let s = sessions.get(sid)
  if (s) return s
  const view = el('sf-conversation')
  view.dataset.sid = sid
  view.hidden = true
  view.scroller = scroller
  byId('conversations').append(view)
  s = { sid, info: { sid, cwd: '', live: false }, view, epoch: '', seq: 0, replayAt: 0, rebase: false, gate: null, gateKey: '', gateAt: 0, workflow: null, workflowSeq: 0, planfile: null, agents: [], intent: null, catalog: null, total: 0, cost: 0, busy: false, step: '', compacting: '', changes: null, changesKey: '', changesAt: 0, context: null, asked: {}, refused: {}, audit: null, auditSeen: 0 }
  sessions.set(sid, s)
  return s
}

function startedOf(s: Session): Started {
  return !s.changes ? null : s.changes.noBase ? 'nobase' : s.changes.commits.length > 0
}

function inTurn(s: Session): boolean {
  return s.busy || !!s.info.busy
}

function stageOff(s: Session): string {
  if (!state.connected) return 'Step buttons are available once the page reconnects to the engine.'
  if (!controllable(s)) return 'Step buttons are off: this session is read-only on this page.'
  return inTurn(s) ? `Step buttons are available when ${agentWord(s.info.agent)} finishes.` : ''
}

function working(s: Session): boolean {
  return inTurn(s) || !!s.compacting || s.agents.some(a => a.status === 'running')
}

const ASKED_MS = 30000

const stepping = new Map<string, number>()
const ran = new Map<string, Set<Step>>()

function began(s: Session) {
  if (!s.step) return
  const steps = ran.get(s.sid) ?? new Set<Step>()
  steps.add(s.step)
  ran.set(s.sid, steps)
}

function turnOf(s: Session): Turn {
  const gate = s.gate
  const busy = state.connected && inTurn(s)
  const awaiting: Step = gate?.size === 'feature' && gate.requirements?.hash && !gate.requirements.approved ? 'define' : gate?.effective === 'feature' && gate.planDoc && !gate.planDoc.approved ? 'plan' : ''
  return { ran: ran.get(s.sid) ?? new Set<Step>(), running: busy ? s.step || awaiting : '', busy }
}

function once(s: Session | null | undefined, what: string, run: () => Promise<boolean>) {
  if (!s) return
  const key = `${s.sid}:${what}`
  const at = stepping.get(key) ?? 0
  if (at && Date.now() - at < ASKED_MS) return
  const now = Date.now()
  stepping.set(key, now)
  void run().then(ok => {
    if (!ok && stepping.get(key) === now) stepping.delete(key)
  })
}

function reviewsActive(s: Session): boolean {
  const required = s.gate?.reviewers ?? []
  return required.some(r => r.state === 'running' || ACTIVE.includes(s.agents.filter(a => a.type === r.type).at(-1)?.status ?? ''))
}

function pickingSize(s: Session) {
  return !!(state.connected && s.gate && s.gate.chosen === false && s.view.blank && controllable(s))
}

function controllable(s: Session) {
  const i = s?.info
  return !!i && i.controlled && i.verified && i.running && i.live && !i.disputed && !i.outdated
}

function sleeping(s: Session | null) {
  const i = s?.info
  return !!i && i.kind === 'background' && i.verified === true && !i.running && !i.live && /^[0-9a-f]{8}$/.test(i.id || '')
}

function current(): Session | null {
  return current.key.startsWith('s/') ? sessions.get(current.key.slice(2)) || null : null
}
current.key = 'overview'

function requestReplay(s: Session, force = false) {
  if (!state.stream || !s.info.live || s.info.disputed) return
  if (!force && Date.now() - s.replayAt < 3000) return
  s.replayAt = Date.now()
  void api('/api/replay', { stream: state.stream, sid: s.sid, epoch: s.epoch, since: s.seq }).catch(() => null)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function updateSessions(list: unknown) {
  const seen = new Set<string>()
  for (const entry of Array.isArray(list) ? (list as unknown[]) : []) {
    if (!isRecord(entry) || typeof entry.sid !== 'string') continue
    const info = entry as unknown as SessionInfo
    seen.add(info.sid)
    const s = ensure(info.sid)
    const was = s.info
    const could = controllable(s)
    s.info = info
    if (info.live && (!was.live || was.disputed)) requestReplay(s, true)
    else if (info.live && !info.disputed && s.epoch && info.epoch === s.epoch && (info.lastSeq ?? 0) > s.seq) requestReplay(s)
    if (info.live && !was.live && current() === s && byId('hint').textContent?.startsWith('Waking')) say('')
    if (info.live && !was.live) byId<HTMLElementTagNameMap['sf-toasts']>('toasts').close(`wake:${info.sid}`)
    if (could !== controllable(s)) s.view.refreshCards(controllable(s))
  }
  for (const s of [...sessions.values()]) {
    if (seen.has(s.sid)) continue
    if (openTabs.includes(`s/${s.sid}`)) {
      s.info = { ...s.info, live: false, running: false, verified: false, busy: false }
      continue
    }
    sessions.delete(s.sid)
    s.view.remove()
  }
  schedulePaint()
}

function onEvent(ev: StreamEvent) {
  if (ev.type === 'supervisor') {
    state.supervisor = typeof ev.state === 'string' ? ev.state : ''
    return schedulePaint()
  }
  if (ev.type === 'hello') {
    if ('sid' in ev) return
    state.stream = ev.stream
    projectsAt = 0
    void markSignIn()
    updateSessions(ev.sessions)
    for (const s of sessions.values()) requestReplay(s, true)
    pruneTabs()
    return
  }
  if (ev.type === 'sessions') {
    if ('sid' in ev) return
    return updateSessions(ev.sessions)
  }
  if (ev.type === 'reviewer') {
    if (typeof ev.method === 'string' && typeof ev.words === 'string') {
      reviewer = { expect: reviewer?.expect ?? 'any', signIn: { method: ev.method, words: ev.words, endpoint: typeof ev.endpoint === 'string' ? ev.endpoint : undefined, costs: ev.costs === true, checkedAt: ev.checkedAt } }
      schedulePaint()
    }
    return
  }
  if (ev.type === 'repo') {
    const where = visible(ev.url)
    if (ev.state === 'added') {
      projects.say('', '')
      toast({ id: `repo:${ev.url}`, title: `${visible(ev.name)} is cloned.`, description: `Start a session in it from Projects. New sessions start from ${where}'s main branch.`, tone: 'ok' })
    }
    if (ev.state === 'failed') {
      projects.say('', ev.message ? visible(ev.message) : `The clone of ${where} failed.`, true)
      toast({ id: `repo:${ev.url}`, title: `${visible(ev.name)} wasn't added`, description: ev.message ? visible(ev.message) : '', tone: 'bad', timeout: 0 })
    }
    return void loadProjects(true)
  }
  if (ev.type === 'dropped') {
    const known = sessions.get(ev.sid)
    const what = ev.count && ev.count > 1 ? `The ${ev.count} messages` : 'The message'
    const to = known ? visible(label(known.info)) : 'that session'
    toast(ev.why === 'timeout' ? { title: `${what} to ${to} didn't go: it didn't wake within 2 minutes.`, description: 'Wake it from the ⋯ menu, then send the message again.', tone: 'warn', timeout: 0 } : { title: `${what} to ${to} didn't go: the session woke read-only.`, description: 'Let the page control it, then send the message again.', tone: 'warn', timeout: 0 })
    return
  }
  if (typeof ev.sid !== 'string') return
  const s = ensure(ev.sid)
  if (ev.type === 'reset') {
    s.view.reset(s.epoch && ev.epoch !== s.epoch ? RESET_NOTE : '')
    Object.assign(s, { epoch: ev.epoch, seq: 0, replayAt: 0, rebase: true, gate: null, gateKey: '', workflow: null, planfile: null, agents: [], intent: null, busy: false, step: '', compacting: '', context: null, asked: {}, refused: {} })
    return schedulePaint()
  }
  if (typeof ev.seq !== 'number') return
  if (s.epoch && ev.epoch !== s.epoch) return requestReplay(s)
  if (s.rebase) {
    s.rebase = false
    s.seq = ev.seq - 1
  }
  if (ev.seq <= s.seq) return
  if (ev.seq !== s.seq + 1) return requestReplay(s)
  if (!s.epoch) s.epoch = ev.epoch
  s.seq = ev.seq
  applySession(s, ev)
}

function applySession(s: Session, ev: SessionEvent) {
  s.lastAt = typeof ev.ts === 'number' && Number.isFinite(ev.ts) && ev.ts > 0 ? Math.min(ev.ts, Date.now()) : Date.now()
  s.view.who = agentWord(s.info.agent)
  const attach = isClaude(s.info) && /^[0-9a-f]{8}$/.test(s.info.id || '') ? s.info.id : ''
  const card = ev.type === 'plan' || ev.type === 'permission' || ev.type === 'ask' ? { ...ev, attach, who: agentWord(s.info.agent) } : ev
  s.view.apply(card, controllable(s))
  switch (ev.type) {
    case 'workflow':
      s.workflow = ev
      s.workflowSeq++
      s.intent = ev.intent ? { found: true, title: ev.intent.title, status: ev.intent.status, agreed: ev.intent.agreed, assumed: ev.intent.assumed, open: ev.intent.open, built: ev.intent.built } : null
      break
    case 'planfile':
      if (typeof ev.path === 'string' && typeof ev.text === 'string' && typeof ev.hash === 'string' && /^[0-9a-f]{64}$/.test(ev.hash)) s.planfile = ev
      break
    case 'agents':
      s.agents = Array.isArray(ev.agents) ? ev.agents.filter(a => isRecord(a) && typeof a.id === 'string' && typeof a.type === 'string' && typeof a.status === 'string' && (a.description === undefined || typeof a.description === 'string')) : []
      break
    case 'intent':
      s.intent = ev.record
      break
    case 'audit':
    case 'refusal':
      s.auditSeen++
      break
    case 'catalog':
      s.catalog = { commands: entries(ev.commands), agents: entries(ev.agents) }
      break
    case 'busy':
      s.busy = true
      break
    case 'idle':
      s.busy = false
      s.step = ''
      break
    case 'prompt':
      s.step = typeof ev.text === 'string' ? stepOf(ev.text) : ''
      began(s)
      break
    case 'command': {
      const kind = ev.approval === 'requirements' || ev.approval === 'plan' ? ev.approval : null
      const hash = kind && s.asked[kind]?.hash
      if (kind && ev.failed && hash && typeof ev.text === 'string') s.refused[kind] = { hash, text: ev.text }
      if (kind) delete s.asked[kind]
      break
    }
    case 'tool':
      if (ev.tool === 'Skill' && typeof ev.summary === 'string' && stepOf(ev.summary)) s.step = stepOf(ev.summary)
      began(s)
      break
    case 'compact':
      s.compacting = ev.state === 'start' ? (ev.trigger === 'auto' ? 'Claude Code is compacting the conversation on its own…' : 'Compacting the conversation…') : ''
      break
    case 'usage':
      if (typeof ev.total === 'number' && ev.total >= 0) s.total = ev.total
      if (typeof ev.costUsd === 'number' && ev.costUsd >= 0 && Number.isFinite(ev.costUsd)) s.cost = ev.costUsd
      break
    case 'control':
      s.info = { ...s.info, controlled: ev.on === true }
      s.view.refreshCards(controllable(s))
      break
    case 'mode':
      s.info = { ...s.info, mode: typeof ev.mode === 'string' ? ev.mode : '' }
      break
    case 'context':
      const known = (ev.level === 'warn' || ev.level === 'stop') && typeof ev.tokens === 'number' && Number.isFinite(ev.tokens)
      s.context = known ? { tokens: ev.tokens, level: ev.level } : null
      if (!known) markCheckpoint(s.sid, '')
      break
  }
  schedulePaint()
}

function entries(list: unknown): CatalogEntry[] {
  return Array.isArray(list) ? (list as unknown[]).filter((e): e is CatalogEntry => isRecord(e) && typeof e.name === 'string' && typeof e.description === 'string').map(e => ({ name: e.name, description: e.description })) : []
}

function flushPaint() {
  if (!paintQueued) return
  paintQueued = false
  paint()
}

function schedulePaint() {
  if (paintQueued) return
  paintQueued = true
  requestAnimationFrame(flushPaint)
  if (document.hidden) setTimeout(flushPaint, 100)
}

document.addEventListener('visibilitychange', () => {
  if (document.hidden && paintQueued) setTimeout(flushPaint, 100)
})

let openTabs = ['overview']
let recent: string[] = []
const scrolls = new Map<string, number>()

function go(key: string) {
  if (key === current.key) return
  scrolls.set(current.key, scroller.scrollTop)
  const from = current.key
  current.key = key
  swapDraft(from, key)
  armed.clear()
  if (!openTabs.includes(key)) openTabs = [...openTabs, key]
  recent = [key, ...recent.filter(k => k !== key)]
  if (key !== focusFile) focusFile = ''
  if (key.startsWith('f:')) loadFile(key)
  paint()
  grow()
  const view = current()?.view
  scroller.scrollTop = view?.pinned ? scroller.scrollHeight : scrolls.get(key) ?? (key.startsWith('s/') ? scroller.scrollHeight : 0)
  paintJump()
}

const TABS_KEY = 'sf-tabs'
const SAVED_KEY = 'sf-checkpointed'
const checkpointed = new Map<string, string>()
try {
  const saved: unknown = JSON.parse(sessionStorage.getItem(SAVED_KEY) || '{}')
  if (saved && typeof saved === 'object') for (const [sid, level] of Object.entries(saved)) if (typeof level === 'string') checkpointed.set(sid, level)
} catch {}

function markCheckpoint(sid: string, level: string) {
  if (level ? checkpointed.get(sid) === level : !checkpointed.has(sid)) return
  if (level) checkpointed.set(sid, level)
  else checkpointed.delete(sid)
  saveCheckpoints()
}

function saveCheckpoints() {
  try {
    sessionStorage.setItem(SAVED_KEY, JSON.stringify(Object.fromEntries(checkpointed)))
  } catch {}
}
const TAB_KEY = /^(?:projects|docs|s\/[\w-]{1,100}|f:[\w-]{1,100}:\/[^\0]{0,4000})$/

function saveTabs() {
  try {
    sessionStorage.setItem(TABS_KEY, JSON.stringify({ tabs: openTabs.slice(1), active: current.key }))
  } catch {}
}

function restoreTabs() {
  let saved: { tabs?: unknown; active?: unknown } | null = null
  try {
    saved = JSON.parse(sessionStorage.getItem(TABS_KEY) || 'null')
  } catch {}
  const tabs = Array.isArray(saved?.tabs) ? [...new Set((saved.tabs as unknown[]).filter((k): k is string => typeof k === 'string' && TAB_KEY.test(k)))].slice(0, 50) : []
  for (const k of tabs) if (k.startsWith('f:')) files.set(k, { path: k.slice(k.indexOf(':', 2) + 1) })
  openTabs = ['overview', ...tabs]
  const active = typeof saved?.active === 'string' && openTabs.includes(saved.active) ? saved.active : 'overview'
  current.key = active
  recent = active === 'overview' ? [] : [active]
}

function pruneTabs() {
  const sidOf = (k: string) => (k.startsWith('s/') ? k.slice(2) : k.startsWith('f:') ? k.slice(2, k.indexOf(':', 2)) : '')
  for (const k of openTabs) if (sidOf(k) && !sessions.has(sidOf(k))) closeTab(k)
  if (current.key.startsWith('f:') && !files.get(current.key)?.text) loadFile(current.key)
}

function closeTab(key: string) {
  if (key === 'overview') return
  openTabs = openTabs.filter(k => k !== key)
  files.delete(key)
  recent = recent.filter(k => k !== key && openTabs.includes(k))
  scrolls.delete(key)
  dropDraft(key)
  if (key === current.key) go(recent[0] || 'overview')
  else paint()
}

function waitingOf(s: Session) {
  return s.info.live ? s.view.waiting : []
}

function paint() {
  const s = current()
  if (current.key.startsWith('s/') && !openTabs.includes(current.key)) openTabs = [...openTabs, current.key]
  const list = [...sessions.values()]
  byId<HTMLElementTagNameMap['sf-overview']>('overview').supervisor = state.supervisor
  const shown = list.map(x => ({ ...x.info, busy: working(x), waiting: waitingOf(x) }))
  byId<HTMLElementTagNameMap['sf-overview']>('overview').sessions = shown
  const setup = byId<HTMLElementTagNameMap['sf-setup']>('setup')
  setup.state = { signIn: signIns, projects: projectCount, sessions: list.length }
  byId('recent').classList.toggle('setup-only', !setup.hidden && !list.length)
  byId('home').hidden = !!s
  byId('welcome').hidden = current.key !== 'overview'
  byId('projects').hidden = current.key !== 'projects'
  byId('docs').hidden = current.key !== 'docs'
  byId('file').hidden = !current.key.startsWith('f:')
  document.body.classList.toggle('editor', current.key.startsWith('f:'))
  document.body.dataset.view = current.key === 'projects' || current.key === 'docs' ? current.key : ''
  byId<HTMLElementTagNameMap['sf-file']>('file').view = files.get(current.key) ?? null
  const sessionTab = (k: string) => k.startsWith('s/') && openTabs.includes(k)
  byId<HTMLElementTagNameMap['sf-docs']>('docs').session = recent.find(sessionTab) ?? [...openTabs].reverse().find(sessionTab) ?? ''
  if (current.key === 'docs') void loadDocs()
  for (const a of document.querySelectorAll<HTMLElement>('.activity a[data-view]')) {
    if (a.dataset.view === current.key) a.setAttribute('aria-current', 'page')
    else a.removeAttribute('aria-current')
  }
  byId<HTMLElementTagNameMap['sf-projects']>('projects').sessions = shown
  void loadProjects()
  byId('live-view').hidden = !s
  byId('composer').hidden = !s
  const box = byId<HTMLTextAreaElement>('text')
  const blocked = !!s && waitingOf(s).length > 0
  if (box.disabled !== blocked) box.disabled = blocked
  const placeholder = blocked ? 'Answer the card above to keep going' : 'Type a message and press Enter'
  if (box.placeholder !== placeholder) box.placeholder = placeholder
  const picking = !!s && pickingSize(s)
  byId('size-pick').hidden = !picking
  if (picking) paintTeamPicks(s)
  byId('composer').classList.toggle('picking', picking)
  document.body.classList.toggle('live', !!s)
  for (const x of list) x.view.hidden = x !== s
  const needs = list.some(x => waitingOf(x).length)
  const waitingCount = list.filter(x => waitingOf(x).length).length
  byId('need-badge').textContent = String(waitingCount)
  byId('need-badge').hidden = !waitingCount
  byId('need-badge').closest('a')!.setAttribute('aria-label', waitingCount ? `Welcome, ${waitingCount} waiting for you` : 'Welcome')
  byId<HTMLElementTagNameMap['sf-tabs']>('tabs').state = {
    tabs: openTabs.map(k => {
      if (k === 'overview') return { id: k, label: 'Welcome', closable: false, need: needs && !!s }
      if (k === 'projects' || k === 'docs') return { id: k, label: k === 'docs' ? 'Docs' : 'Projects', closable: true, need: false }
      if (k.startsWith('f:')) return { id: k, label: visible(fileLabel(k)), title: files.get(k)?.path ?? '', closable: true, need: false }
      const x = sessions.get(k.slice(2))
      return { id: k, label: x ? visible(label(x.info)) : 'Session', title: x?.info.cwd || '', closable: true, need: !!x && !!waitingOf(x).length }
    }),
    active: current.key,
  }
  if (s) {
    const st = sessionState({ ...s.info, busy: working(s), needs: waitingOf(s).length })
    byId('state').textContent = state.connected ? visible(label(s.info)) : 'Disconnected'
    byId('title').textContent = state.connected ? [st.word, s.info.mode ? `${modeLabel(s.info.mode)} mode` : 'mode unknown', s.sid.slice(0, 8)].join(' · ') : state.note
    byId('title').title = s.info.cwd || ''
    paintCrumbs(s)
    byId('usage').textContent = usageText(s)
    byId('stop').hidden = !(state.connected && inTurn(s) && controllable(s))
    byId('working').hidden = !working(s) || !!waitingOf(s).length
    if (byId('strip-note').textContent !== s.compacting) byId('strip-note').textContent = s.compacting
    paintControl(s)
    paintManage(s)
    byId('session-foot').hidden = !byId('foot-text').textContent && byId('manage').hidden && byId('session-more').hidden
    const notes = state.connected ? warnings(s.info, list.map(x => x.info)) : []
    byId('warnings').replaceChildren(...notes.map(t => el('p', '', visible(t))))
    byId('warnings').hidden = !notes.length
    paintCheckpoint(s)
    paintStall(s)
    const off = stageOff(s)
    if (inTurn(s)) for (const k of [...stepping.keys()]) if (k.startsWith(`${s.sid}:`)) stepping.delete(k)
    for (const b of byId('path').querySelectorAll<HTMLButtonElement>('button.stage')) {
      const stage = b.closest<HTMLElement>('li[data-stage]')?.dataset.stage
      const step = b.id === 'tasks-start' ? 'tasks' : stage === 'define' || stage === 'plan' ? stage : ''
      const hold = step && s.gate?.holds ? (s.gate.holds[step] ?? '') : ''
      b.setAttribute('aria-disabled', String(!!off || !!hold))
      if (off) b.setAttribute('aria-describedby', 'path-busy')
      else if (hold) b.setAttribute('aria-describedby', `${step === 'tasks' ? 'plan' : step}-detail`)
      else b.removeAttribute('aria-describedby')
    }
    if (byId('path-busy').textContent !== off) byId('path-busy').textContent = off
    byId('path-busy').hidden = !off
    paintAgents(s)
    paintChecks(s)
    paintSide(s)
    byId<HTMLElementTagNameMap['sf-reviews']>('reviews').view = reviewsOf(s)
    byId<HTMLElementTagNameMap['sf-push-panel']>('push-panel').view = pushPanelOf(s)
    byId<HTMLElementTagNameMap['sf-forge-panel']>('forge-panel').view = forgePanelOf(s)
    byId<HTMLElementTagNameMap['sf-intent']>('intent').record = s.gate?.adapter === 'knackpack' ? s.intent : null
    byId<HTMLElementTagNameMap['sf-changes']>('changes').changes = s.changes
    void loadChanges(s)
    void loadGate(s)
  } else {
    byId<HTMLElementTagNameMap['sf-changes']>('changes').changes = null
    byId<HTMLElementTagNameMap['sf-reviews']>('reviews').view = null
    byId<HTMLElementTagNameMap['sf-push-panel']>('push-panel').view = null
    byId<HTMLElementTagNameMap['sf-forge-panel']>('forge-panel').view = null
    byId('state').textContent = state.connected ? (current.key === 'docs' ? 'Docs' : current.key === 'projects' ? 'Projects' : current.key.startsWith('f:') ? 'File, read-only' : 'Welcome') : 'Disconnected'
    byId('title').textContent = state.connected ? '' : state.note
    byId('usage').textContent = ''
    byId('stop').hidden = true
    byId('working').hidden = true
    paintSide(null)
  }
  byId('account-item').hidden = !state.connected
  if (!state.connected && byId('account').matches(':popover-open')) byId('account').hidePopover()
  document.title = needs ? 'Needs you · Software Factory' : 'Software Factory'
  saveTabs()
}

function paintControl(s: Session) {
  const i = s.info
  const button = byId('control-button')
  let text = ''
  let action = ''
  const claims = i.disputed ? (i.claims || []).filter(c => typeof c === 'string').map(visible) : []
  byId('control').classList.toggle('alert', !!i.disputed)
  if (i.disputed) {
    text = `${claims.length ? `Claimed by ${claims.length} connections: ${claims.join(', ')}. ` : 'Two connections claim to be this session. '}This page sends it nothing while they disagree. One of them may be a program pretending to be it.`
    action = 'End both connections'
  } else if (!i.verified) text = 'Read-only: this engine didn’t start this session, so the page only shows what it reports.'
  else if (sleeping(s)) {
    if (!i.controlled) {
      text = 'This background session is stopped.'
      action = 'Wake it'
    } else text = i.mode === 'bypassPermissions' ? 'This session is stopped, in bypass permissions mode: its tools run without asking.' : 'This session is stopped.'
  } else if (!i.running) text = 'This session isn’t running.'
  else if (!i.live) text = 'Software Factory isn’t connected to this session, so this page can only list it.'
  else if (i.outdated) text = 'This session runs an older Software Factory. Restart it to control it from here.'
  else if (!i.controlled) {
    text = armed.has(`${s.sid}:control`) ? 'Press Confirm control to let this page send messages and answer prompts in this session.' : 'Read-only on this page. Use the button to let the page control it.'
    action = 'Let this page control it'
  } else {
    text = i.mode === 'bypassPermissions' ? 'Bypass permissions mode: tools run without asking.' : ''
    action = 'Make it read-only'
  }
  const id = i.kind === 'background' && /^[0-9a-f]{8}$/.test(i.id || '') ? i.id! : ''
  const menu = byId('session-menu')
  for (const b of menu.querySelectorAll<HTMLButtonElement>('button[data-copy]')) {
    b.textContent = id ? `Copy ${b.dataset.copy === 'attach' ? attachCommand(id) : logsCommand(id)}` : ''
    b.hidden = !id || !isClaude(i)
  }
  const notice = !!action && action !== 'Make it read-only'
  byId('menu-readonly').hidden = action !== 'Make it read-only'
  byId('menu-wake').hidden = !(sleeping(s) && i.controlled)
  byId('session-more').hidden = !id && action !== 'Make it read-only'
  if (byId('session-more').hidden && menu.matches(':popover-open')) menu.hidePopover()
  byId('foot-text').textContent = notice ? '' : text
  byId('foot-text').classList.toggle('bad', !notice && i.mode === 'bypassPermissions')
  byId('control-text').textContent = text
  button.dataset.action = action === 'Wake it' ? 'wake' : action === 'End both connections' ? 'release' : 'control'
  const labels = [action, button.dataset.action === 'control' && !i.controlled ? 'Confirm control' : '']
  const shown = labels[1] && armed.has(`${s.sid}:control`) ? 1 : 0
  ;[...button.children].forEach((span, n) => {
    span.textContent = labels[n] ?? ''
    span.classList.toggle('off', n !== shown)
  })
  byId('control-cancel').hidden = shown !== 1
  byId('control').hidden = !notice
}

function paintManage(s: Session) {
  const i = s.info
  const box = byId('manage')
  const managed = i.kind === 'background' && !!i.verified && /^[0-9a-f]{8}$/.test(i.id || '') && state.connected
  box.hidden = !managed
  if (!managed) return
  const removal = removals.get(s.sid)
  const button = byId('manage-button')
  const force = byId('force-button')
  const stop = !!i.running
  let pending = busyManage.get(s.sid)
  if (pending && (Date.now() - pending.at > 30000 || (pending.action === 'stop' && !stop))) {
    busyManage.delete(s.sid)
    pending = undefined
  }
  button.dataset.action = stop ? 'stop' : 'remove'
  const labels = stop ? ['Stop session', 'Confirm stop', 'Stopping…'] : ['Delete session', 'Confirm delete', 'Deleting…']
  const shown = pending ? 2 : armed.has(`${s.sid}:${button.dataset.action}`) ? 1 : 0
  ;[...button.children].forEach((span, i) => {
    span.textContent = labels[i] ?? ''
    span.classList.toggle('off', i !== shown)
  })
  button.classList.toggle('danger', shown === 1)
  force.classList.toggle('danger', armed.has(`${s.sid}:force`))
  ;(button as HTMLButtonElement).disabled = !!pending
  ;(force as HTMLButtonElement).disabled = !!pending
  const note = shown === 1 ? (stop ? 'Press Confirm stop to stop this session. It keeps the conversation; Wake brings it back.' : 'Press Confirm delete to delete this session and its worktree.') : armed.has(`${s.sid}:force`) && removal?.offer ? `Press the button again to ${removal.offer.flag === 'discard' ? 'discard' : 'remove the folder of'} ${removal.offer.value}.` : removal?.message || ''
  if (note) {
    byId('foot-text').textContent = note
    byId('foot-text').classList.remove('bad')
  }
  button.title = stop ? 'Stopping keeps the conversation; Wake brings it back.' : 'Deleting removes the session and its worktree. Claude Code refuses if the worktree has unpushed commits.'
  const offer = !stop && removal?.offer
  force.hidden = !offer
  byId('manage-cancel').hidden = !!pending || (shown !== 1 && !(offer && armed.has(`${s.sid}:force`)))
  if (offer) force.textContent = armed.has(`${s.sid}:force`) ?`Confirm: ${offer.flag === 'discard' ? 'discard' : 'remove the folder of'} ${offer.value}` : offer.flag === 'discard' ? `Discard the unpushed work (${offer.value}) and delete` : `Delete the worktree folder anyway (${offer.value})`
}

function confirmed(key: string, prompt = ''): boolean {
  if (armed.delete(key)) {
    if (prompt) say('')
    return true
  }
  armed.add(key)
  if (prompt) say(prompt)
  schedulePaint()
  return false
}

function paintCheckpoint(s: Session) {
  const c = s.workflow?.knackpack === true ? s.context : null
  const shown = !!c && checkpointed.get(s.sid) !== c.level
  if (c && shown && byId('checkpoint').hidden) byId('checkpoint-said').textContent = `Long conversation: about ${Math.round(c.tokens / 1000)}k tokens. Save a checkpoint before you compact.`
  byId('checkpoint').hidden = !shown
  byId('checkpoint').classList.toggle('alert', c?.level === 'stop')
  if (!c) return
  const k = `${Math.round(c.tokens / 1000)}k`
  byId('checkpoint-text').textContent = c.level === 'stop' ? `About ${k} tokens, past knackpack's 500k stop point. Save a checkpoint, then run /compact.` : `About ${k} tokens in context. Save a checkpoint before you compact, while the details are still there.`
  ;(byId('checkpoint-button') as HTMLButtonElement).disabled = !state.connected || !controllable(s) || inTurn(s)
}

const stallDismissed = new Map<string, number>()

function stallOf(s: Session): boolean {
  return state.connected && !!controllable(s) && stalled({ inTurn: inTurn(s), waiting: waitingOf(s).length > 0, subagents: s.agents.some(a => a.status === 'running'), lastAt: s.lastAt ?? 0, dismissedAt: stallDismissed.get(s.sid) ?? 0 }, Date.now())
}

function paintStall(s: Session) {
  const shown = stallOf(s)
  const minutes = Math.floor((Date.now() - (s.lastAt ?? Date.now())) / 60000)
  const since = s.lastAt ? new Date(s.lastAt).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' }) : ''
  if (shown && byId('stall').hidden) byId('stall-said').textContent = `No report from the session for ${minutes} ${minutes === 1 ? 'minute' : 'minutes'}.`
  byId('stall').hidden = !shown
  if (!shown) return
  const id = isClaude(s.info) && s.info.kind === 'background' && /^[0-9a-f]{8}$/.test(s.info.id || '') ? s.info.id! : ''
  byId('stall-text').textContent = `${agentWord(s.info.agent)} is in its turn and hasn't reported anything for ${minutes} ${minutes === 1 ? 'minute' : 'minutes'}, since ${since}. This rests on the session's own reports, so it's only a hint: it may still be working, such as on a long command. ${id ? 'Attach to look, or stop the turn.' : 'Stop the turn if it seems stuck.'}`
  byId('stall-copy').hidden = !id
  byId('stall-copy').dataset.command = id ? attachCommand(id) : ''
}

function forgetSession(sid: string) {
  const s = sessions.get(sid)
  if (!s) return
  sessions.delete(sid)
  removals.delete(sid)
  stallDismissed.delete(sid)
  markCheckpoint(sid, '')
  s.view.remove()
  closeTab(`s/${sid}`)
}

async function manage(s: Session, path: string, body: Record<string, unknown>): Promise<string> {
  busyManage.set(s.sid, { action: path === '/api/stop-session' ? 'stop' : 'remove', at: Date.now() })
  schedulePaint()
  setTimeout(schedulePaint, 30500)
  const { ok, body: out } = await reply(await api(path, { sid: s.sid, ...body }).catch(() => null))
  if (!ok || path !== '/api/stop-session') busyManage.delete(s.sid)
  if (ok) {
    removals.delete(s.sid)
    if (path === '/api/remove-session') forgetSession(s.sid)
    schedulePaint()
    return ''
  }
  const offer = out.offer && typeof out.offer === 'object' ? (out.offer as { flag: string; value: string }) : null
  const message = typeof out.message === 'string' ? out.message : "That didn't work."
  removals.set(s.sid, { message, offer })
  schedulePaint()
  return offer ? `${message} Open the session to choose what to do with it.` : message
}

function reviewsOf(s: Session): ReviewsView | null {
  const i = s.info
  if (!i.verified || !i.project) return null
  const review = i.review && Array.isArray(i.review.roles) && i.review.commit ? i.review : null
  const gate = s.gate
  const planned = gate && gate.head && review?.commit !== gate.head ? gate.reviewers.map(r => ({ name: r.name, agent: r.type, why: r.why ?? '' })) : []
  const unpicked = gate?.chosen === false
  const disabled = !state.connected ? 'Reviews start once the page is connected again.'
    : unpicked ? 'Pick a work type first: it decides which reviews run.'
    : startedOf(s) === false ? 'Nothing to review or push yet: this session has no commits.' : ''
  return { review, disabled, asking: reviewAsking.has(s.sid), pushing: pushAsking.has(s.sid) || !!i.push?.running, pushed: pushedHead(s), pushSaid: pushSaid.get(s.sid) ?? null, reviewer, planned: unpicked ? [] : planned, gate: gate?.reviewers ?? [], head: gate?.head ?? '', testList: i.review?.testList ?? null, testAsking: testAsking.has(s.sid) }
}

function pushPanelOf(s: Session): PushPanelView | null {
  const i = s.info
  if (!i.verified || !i.project) return null
  const push = i.push && Array.isArray(i.push.missing) ? i.push : null
  const gate = state.connected && s.gate?.head ? s.gate : null
  pushOutcome(s)
  return { push, disabled: state.connected ? '' : 'Answers go through once the page is connected again.', answering: answerAsking.has(s.sid), flagged: gate?.flagged ?? [], flagsRead: gate ? gate.flagsRead !== false : true }
}

function forgePanelOf(s: Session): ForgePanelView | null {
  const i = s.info
  if (!i.verified || !Array.isArray(i.forge) || !i.forge.length) return null
  return { forge: i.forge, disabled: state.connected ? '' : 'Answers go through once the page is connected again.', answering: forgeAsking.has(s.sid) }
}

const forgeAsking = new Set<string>()

async function answerForge(s: Session | null, answer: { card: string; approve: boolean; draft?: boolean }) {
  if (!s || forgeAsking.has(s.sid)) return
  forgeAsking.add(s.sid)
  schedulePaint()
  const { ok, body } = await reply(await api('/api/forge-answer', { sid: s.sid, ...answer }).catch(() => null))
  forgeAsking.delete(s.sid)
  schedulePaint()
  if (!ok) toast({ id: `forge-answer:${s.sid}`, title: answer.approve ? "It wasn't opened." : "It wasn't denied.", description: typeof body.message === 'string' ? visible(body.message) : 'Check that the engine is running.', tone: 'bad' })
}

async function sessionAsk(s: Session | null, route: string, body: Record<string, unknown>, title: string, done: string) {
  if (!s) return
  const { ok, body: said } = await reply(await api(route, { sid: s.sid, ...body }).catch(() => null))
  if (!ok) return void toast({ id: `${route}:${s.sid}`, title, description: typeof said.message === 'string' ? visible(said.message) : 'Check that the engine is running.', tone: 'bad' })
  toast({ title: done, tone: 'ok' })
  s.auditSeen++
  void loadGate(s, true)
  schedulePaint()
}

function pushedHead(s: Session): boolean {
  const p = s.info.push
  const head = s.gate?.head ?? ''
  return !!p && !p.running && !pushAsking.has(s.sid) && (p.result === 'pushed' || p.result === 'up-to-date') && !!head && p.commit === head
}

function pushOutcome(s: Session) {
  const since = pushWatched.get(s.sid)
  const p = s.info.push
  if (since === undefined || !p || p.running || pushAsking.has(s.sid) || p.at === since) return
  pushWatched.delete(s.sid)
  const said = PUSH_SAID[p.result]
  if (said) {
    pushSaid.set(s.sid, said)
    setTimeout(() => {
      if (pushSaid.get(s.sid) !== said) return
      pushSaid.delete(s.sid)
      schedulePaint()
    }, 6000)
  }
  requestAnimationFrame(() => {
    if (current() !== s) return
    const panel = byId('push-panel')
    const target = panel.querySelector<HTMLElement>('.push-card') ?? panel.querySelector<HTMLElement>('#push-result')
    if (!target) return
    target.scrollIntoView({ block: 'nearest' })
    const lost = !document.activeElement || document.activeElement === document.body
    if (lost) panel.querySelector<HTMLElement>('#push-result')?.focus({ preventScroll: true })
  })
}

async function startPush(s: Session | null) {
  if (!s || pushAsking.has(s.sid) || s.info.push?.running || pushedHead(s)) return
  pushAsking.add(s.sid)
  pushSaid.delete(s.sid)
  pushWatched.set(s.sid, s.info.push?.at ?? 0)
  schedulePaint()
  const { ok, body } = await reply(await api('/api/push', { sid: s.sid }).catch(() => null))
  pushAsking.delete(s.sid)
  if (!ok) pushWatched.delete(s.sid)
  schedulePaint()
  if (!ok) toast({ id: `push:${s.sid}`, title: "The push didn't start.", description: typeof body.message === 'string' ? visible(body.message) : 'Check that the engine is running.', tone: 'bad' })
}

async function answerPush(s: Session | null, answer: { card: string; commit: string; allow: boolean; reason: string }) {
  if (!s || answerAsking.has(s.sid)) return
  answerAsking.add(s.sid)
  schedulePaint()
  const { ok, body } = await reply(await api('/api/push-answer', { sid: s.sid, ...answer }).catch(() => null))
  answerAsking.delete(s.sid)
  schedulePaint()
  if (!ok) toast({ id: `push-answer:${s.sid}`, title: answer.allow ? "The push wasn't allowed." : "The push wasn't denied.", description: typeof body.message === 'string' ? visible(body.message) : 'Check that the engine is running.', tone: 'bad' })
}

async function startReview(s: Session | null) {
  if (!s || reviewAsking.has(s.sid) || s.info.review?.running) return
  reviewAsking.add(s.sid)
  schedulePaint()
  let consent: Consent = {}
  let answer = await reply(await api('/api/review', { sid: s.sid }).catch(() => null))
  for (let round = 0; !answer.ok && round < 2; round++) {
    const more = await consentFor(answer.body.needs as Needs | undefined, "The reviewer container's Claude Code", 'the review')
    if (!more?.costOk) break
    consent = { costOk: true }
    answer = await reply(await api('/api/review', { sid: s.sid, ...consent }).catch(() => null))
  }
  const { ok, body } = answer
  reviewAsking.delete(s.sid)
  schedulePaint()
  if (!ok) toast({ id: `review:${s.sid}`, title: "The review didn't start.", description: typeof body.message === 'string' ? visible(body.message) : 'Check that the engine is running.', tone: 'bad', timeout: body.needs ? 0 : undefined })
}

const testAsking = new Set<string>()

async function startTestList(s: Session | null) {
  if (!s || testAsking.has(s.sid) || s.info.review?.testList?.state === 'running') return
  testAsking.add(s.sid)
  schedulePaint()
  let answer = await reply(await api('/api/test-list', { sid: s.sid }).catch(() => null))
  for (let round = 0; !answer.ok && round < 2; round++) {
    const more = await consentFor(answer.body.needs as Needs | undefined, "The reviewer container's Claude Code", 'the manual test list')
    if (!more?.costOk) break
    answer = await reply(await api('/api/test-list', { sid: s.sid, costOk: true }).catch(() => null))
  }
  const { ok, body } = answer
  testAsking.delete(s.sid)
  schedulePaint()
  if (!ok) toast({ id: `test-list:${s.sid}`, title: "The test list wasn't started.", description: typeof body.message === 'string' ? visible(body.message) : 'Check that the engine is running.', tone: 'bad', timeout: body.needs ? 0 : undefined })
}

function paintAgents(s: Session) {
  const list = byId('agents')
  list.replaceChildren()
  if (!s.agents.length) return list.append(el('li', 'muted', 'None yet.'))
  for (const a of s.agents.slice(-12).reverse()) {
    const li = el('li')
    const row = el('div', 'row')
    row.append(el('span', '', a.type.replace(/^knackpack:/, '')), el('span', 'spacer'), el('span', `tag${a.status === 'failed' || a.status === 'killed' ? ' bad' : a.status === 'completed' ? ' ok' : ''}`, a.status))
    li.append(row)
    if (a.description) li.append(el('div', 'small muted', shown(a.description)))
    list.append(li)
  }
}

function paintChecks(s: Session) {
  const gate = state.connected ? s.gate : null
  const disabled = !state.connected || !controllable(s)
  const started = startedOf(s)
  const required = gate?.effective === 'explore' ? [] : (gate?.reviewers ?? [])
  for (const { check, node } of syncChecks(required)) {
    const r = gate?.reviewers?.find(x => x.type === check.type) ?? null
    node.status = { gate: r, sid: s.sid }
  }
  const turn = turnOf(s)
  paintPath(byId('path'), gate, started, byId('path-card'), reviewsActive(s) ? 'verify' : state.connected && working(s) && !waitingOf(s).length ? turn.running || s.step || 'work' : '', !!stageOff(s), turn, pushedHead(s))
  byId('path-section').hidden = !s.gate || pickingSize(s)
  for (const b of byId('path').querySelectorAll<HTMLButtonElement>('li[data-stage="deliver"] button.stage')) b.hidden = gate?.adapter !== 'knackpack'
  const size = byId<HTMLSelectElement>('size')
  const types = gate?.types ?? []
  const typesKey = JSON.stringify(types.map(t => [t.id, t.name]))
  const focused = document.activeElement === size
  if (types.length && size.dataset.types !== typesKey && !focused) {
    size.dataset.types = typesKey
    size.replaceChildren(...types.map(t => {
      const o = document.createElement('option')
      o.value = t.id
      o.textContent = visible(t.name)
      return o
    }))
  }
  const typeNow = gate?.workType ?? gate?.size
  if (typeNow && !focused && [...size.options].some(o => o.value === typeNow)) size.value = typeNow
  paintAdapter(gate, turn)
  paintTeam(s, gate)
  size.disabled = disabled || !gate
  byId('size-note').textContent = gate?.sizeNote ?? ''
  byId('size-note').hidden = !gate?.sizeNote
  const doc = gate?.planDoc
  const from = gate?.committed ? ` Read from commit ${shortSha(gate.head)} by the engine.` : ''
  const plan = doc && gate?.effective === 'feature' ? doc : null
  byId<HTMLElementTagNameMap['sf-approval']>('approval').view = plan && { note: `${plan.clipped ? 'The plan is longer than the page shows, so it can\'t be approved as a whole.' : ''}${from}`.trim(), path: plan.path, text: plan.text, hash: plan.hash, approved: !!plan.approved, changed: !!plan.changed, draft: turn.busy && !plan.approved, disabled: disabled || waiting(s.asked.plan, plan.hash), hold: gate?.holds?.approvePlan ?? '', refused: s.refused.plan?.hash === plan.hash ? s.refused.plan.text : '' }
  const reqs = gate?.size === 'feature' && gate.requirements?.hash ? gate.requirements : null
  byId<HTMLElementTagNameMap['sf-approval']>('req-approval').view = reqs && { kind: 'requirements', note: from.trim(), path: reqs.path, text: reqs.text, hash: reqs.hash, approved: !!reqs.approved, changed: !!reqs.changed, draft: turn.busy && !reqs.approved, disabled: disabled || waiting(s.asked.requirements, reqs.hash), hold: gate?.holds?.approveRequirements ?? '', refused: s.refused.requirements?.hash === reqs.hash ? s.refused.requirements.text : '', previous: reqs.previous ?? '' }
  paintExceptions(s)
  const project = projectFor(s)
  byId<HTMLElementTagNameMap['sf-project-agents']>('project-agents').view = gate ? { project: project?.path ?? '', rounds: project?.rounds ?? 3, prComment: !!project?.prComment, type: project?.type ?? '', types: types.map((t): [string, string] => [t.id, t.name]), autoFindings: project?.autoFindings ?? false, disabled, hold: project?.hold } : null
}

function projectFor(s: Session): Project | null {
  return projectList.find(p => p.name === s.info.project) ?? null
}

async function loadGate(s: Session, force = false) {
  const review = s.info.review
  const key = `${working(s)}:${review?.commit ?? ''}:${review?.running ?? ''}:${(review?.roles ?? []).map(r => r.state).join(',')}:${s.info.push?.at ?? ''}:${s.workflowSeq}:${s.info.running ?? ''}`
  if (!state.connected || !s.info.verified || !s.info.project || (!force && s.gateKey === key && Date.now() - s.gateAt < 15000)) return
  if (!force && Date.now() - s.gateAt < 3000) return void setTimeout(schedulePaint, 3100 - (Date.now() - s.gateAt))
  s.gateAt = Date.now()
  s.gateKey = key
  const res = await api('/api/gate', { sid: s.sid }).catch(() => null)
  const body = res && res.ok ? ((await res.json().catch(() => null)) as { gate?: GateEvent } | null) : null
  if (!body?.gate || !Array.isArray(body.gate.reviewers) || !Array.isArray(body.gate.missing)) {
    if (!res || res.status >= 500) s.gateKey = ''
    return
  }
  s.gate = body.gate
  if (current() === s) schedulePaint()
  setTimeout(() => current() === s && schedulePaint(), 15100)
}

let teamShown: TeamView | null = null
let teamFor = ''

function paintAdapter(gate: GateEvent | null, turn: Turn) {
  const c = gate?.commands
  for (const [stage, command] of [['define', c?.define], ['plan', c?.plan]] as const) {
    const b = document.querySelector<HTMLButtonElement>(`#path li[data-stage="${stage}"] > div > button.stage`)
    if (!b || !command) continue
    b.dataset.command = command
    b.title = `Sends ${command} to this session`
  }
  const tasks = byId<HTMLButtonElement>('tasks-start')
  const steps = gate?.types?.find(t => t.id === gate.workType)?.steps ?? []
  tasks.hidden = !c?.tasks || !steps.includes('tasks') || gate?.effective !== 'feature' || !gate.plan || gate.approved === true || !!gate.tasksWritten || turn.running === 'tasks'
  const word = turn.ran.has('tasks') ? 'Continue tasks' : 'Tasks'
  if (tasks.textContent !== word) tasks.textContent = word
  if (c?.tasks) {
    tasks.dataset.command = c.tasks
    tasks.title = `Sends ${c.tasks} to this session`
  }
}

function setupLines(v: TeamView): HTMLElement[] {
  const setup = v.setup
  if (!setup) return []
  const own = setup.types.filter(t => !t.builtin)
  const items = [
    `Workflow: ${setup.adapter === 'spec-kit' ? 'GitHub Spec Kit' : setup.adapter === 'knackpack' ? 'knackpack' : 'the built-in steps'}`,
    `Bug hunter: ${visible(setup.roles['bug-hunter'] ?? '')}; simplicity checker: ${visible(setup.roles.simplicity ?? '')}`,
    ...own.map(t => `Work type ${visible(t.name)}: ${t.steps.join(', ')}; ${t.push ? 'pushes' : "doesn't push"}; edits ${t.edits}${t.roles.length ? `; needs ${t.roles.join(' and ')}` : ''}`),
    ...(setup.defaultType ? [`Default work type: ${visible(setup.defaultType)}`] : []),
    ...(setup.agents.length ? [`Reviewers added: ${setup.agents.map(a => visible(a)).join(', ')}`] : []),
    ...setup.fileRules.map(f => `Files ${visible(f.pattern)} need ${visible(f.agent)}`),
    `Review rounds before you decide: ${setup.rounds}`,
    ...v.setupProblems.map(p => `Problem: ${p}`),
  ]
  const list = el('ul')
  list.append(...items.map(t => el('li', '', t)))
  return [el('p', 'small muted', 'What approving it sets up, with this project’s own settings:'), list]
}

function paintTeam(s: Session, gate: GateEvent | null) {
  const note = gate?.team?.note ?? ''
  if (byId('team-note').textContent !== note) byId('team-note').textContent = note
  byId('team-note').hidden = !note
  if (teamFor !== s.sid) {
    teamFor = s.sid
    teamShown = null
  }
  byId('team-row').hidden = !note && !gate?.team?.approved && !teamShown
  byId('team-open').setAttribute('aria-expanded', String(!!teamShown))
  const view = byId('team-view')
  view.hidden = !teamShown
  if (!teamShown) return
  byId('team-path').textContent = `${teamShown.path}${teamShown.head ? ` at commit ${shortSha(teamShown.head)}` : ''}${teamShown.approved ? (teamShown.approved.same ? ', approved' : ', changed since you approved it') : ', not approved yet'}`
  const remote = teamShown.remote ?? []
  byId('team-remote').textContent = teamShown.text === null ? '' : !remote.length ? "The software factory hasn't fetched the remote's main branch yet, so it can't say whether this version is on it." : remote.map(r => (r.same ? `This version matches the remote's ${visible(r.branch)}.` : r.present ? `This version differs from the one on the remote's ${visible(r.branch)}.` : `The remote's ${visible(r.branch)} has no team file.`)).join(' ')
  byId('team-text').textContent = teamShown.text === null ? 'This commit has no team file.' : visibleCommand(teamShown.text)
  byId('team-problems').replaceChildren(...teamShown.problems.map(p => el('li', '', visible(p))))
  byId('team-setup').replaceChildren(...(teamShown.problems.length ? [] : setupLines(teamShown)))
  const approve = byId<HTMLButtonElement>('team-approve')
  approve.hidden = teamShown.text === null || !!teamShown.problems.length || !!teamShown.approved?.same
  byId<HTMLButtonElement>('team-forget').hidden = !teamShown.approved
}

async function teamOp(op: 'read' | 'approve' | 'forget') {
  const s = current()
  const project = s ? projectFor(s) : null
  if (!s || !project) return void toast({ title: 'Add this project first.', description: 'The team file is read for projects in your list, under Projects.', tone: 'bad' })
  const res = await api('/api/team', { path: project.path, op, ...(op === 'approve' ? { sha: teamShown?.sha ?? '' } : {}) }).catch(() => null)
  const body = res ? ((await res.json().catch(() => null)) as (TeamView & { message?: string }) | null) : null
  if (current()?.sid !== s.sid) return
  if (!res || !res.ok || !body) return void toast({ title: op === 'approve' ? "The team file wasn't approved." : op === 'forget' ? 'The team file is still in use.' : "The team file couldn't be read.", description: body?.message ?? 'Check that the project is still in your list.', tone: 'bad' })
  teamShown = body
  if (op !== 'read') toast({ title: op === 'approve' ? 'You approved the team file.' : 'This project uses the built-in setup now.', tone: 'ok' })
  if (op !== 'read') void loadGate(s, true)
  paintTeam(s, s.gate ?? null)
  if (op !== 'read') byId('team-text').focus()
}

async function openTeam() {
  if (teamShown) {
    teamShown = null
    const s = current()
    if (s) paintTeam(s, s.gate ?? null)
    return
  }
  await teamOp('read')
}

function paintExceptions(s: Session) {
  const gate = s.gate
  const key = gate ? `${s.sid}:${gate.branch}:${gate.head}:${s.auditSeen}` : ''
  if (key && s.audit?.key !== key && state.connected && controllable(s)) {
    s.audit = { key, entries: s.audit?.entries ?? [], refusals: s.audit?.refusals ?? {} }
    void api('/api/audit', { sid: s.sid, branch: gate?.branch ?? '' }).then(r => (r.ok ? r.json() : null)).then((v: { entries?: AuditEntry[]; refusals?: Record<string, number> } | null) => {
      if (!v || s.audit?.key !== key) return
      s.audit = { key, entries: Array.isArray(v.entries) ? v.entries : [], refusals: v.refusals && typeof v.refusals === 'object' ? v.refusals : {} }
      if (current() === s) paint()
    }, () => undefined)
  }
  const box = byId('exceptions')
  const entries = (s.audit?.entries ?? []).filter(e => !(e.kind === 'push' && e.result === 'allowed') && !HIDDEN_RESULTS.includes(e.result)).slice(0, 8)
  const top = Object.entries(s.audit?.refusals ?? {}).filter(([k, n]) => n >= 2 && Object.hasOwn(FRICTION, k)).sort((a, b) => b[1] - a[1]).slice(0, 3)
  const items = entries.map(e => {
    const kind = e.kind === 'push' ? (e.personReason && e.result === 'pushed' ? 'allow-push' : '') : e.kind
    const by = e.by === 'agent' ? ', asked by the agent' : e.by === 'agent, after reviews' ? ', asked by the agent, after its reviews' : e.by === 'page' ? ', from the page' : e.by === 'engine' ? ', by the engine' : e.by === 'engine, at start' ? ', by the engine at start' : ''
    const outcome = kind && Object.hasOwn(OUTCOME, e.result) ? `, ${OUTCOME[e.result]}` : ''
    const what = kind ? `${EXCEPTION[kind] ?? visible(kind)}${outcome}${e.reviewer ? ` (${visible(e.reviewer)}${e.verdict ? `: ${visible(e.verdict)}` : ''})` : e.path ? ` (${visible(e.path)})` : ''}` : HISTORY[e.result] ?? `Push ${visible(e.result)}`
    const said = kind ? visible(e.personReason || e.reason) : e.result === 'held' ? `${e.missing?.length ?? 0} missing` : e.result === 'pushed' || e.result === 'up-to-date' ? '' : e.result === 'denied' && e.personReason ? `“${visible(e.personReason)}”` : visible(e.reason)
    const li = el('li', kind ? 'warn' : '')
    const at = el('time', 'history-time', e.time ? when(e.time) : '')
    if (e.time) at.dateTime = new Date(e.time).toISOString()
    li.append(at, `${what}${e.commit ? ` ${shortSha(e.commit)}` : ''}${kind ? '' : by}${said ? `: ${said}` : ''}`)
    return li
  })
  const tips = top.map(([k, n]) => el('p', '', `${n} times in this project, ${FRICTION[k]}. If it keeps happening, say what to do instead in this project's CLAUDE.md.`))
  const sig = [...items, ...tips].map(i => i.textContent).join('\n')
  if (box.dataset.sig === sig) return
  box.dataset.sig = sig
  const parts: Node[] = []
  if (items.length) {
    const list = el('ol', 'history')
    list.setAttribute('aria-labelledby', 'history-label')
    list.append(...items)
    const label = el('p', 'history-label', 'History on this branch, newest first')
    label.id = 'history-label'
    parts.push(label, list)
  }
  box.replaceChildren(...parts, ...tips)
  box.hidden = !parts.length && !tips.length
}

function projectOf(cwd: string): Project | null {
  return projectList.filter(p => cwd === p.path || cwd.startsWith(`${p.path}/`)).sort((a, b) => b.path.length - a.path.length)[0] ?? null
}


let projectPaths: string[] = []
let projectList: Project[] = []
let projectCount: number | null = null
let signIns: SignInState | null = null

function paintCrumbs(s: Session) {
  const cwd = s.info.cwd || ''
  const home = projectPaths.filter(p => cwd === p || cwd.startsWith(`${p}/`)).sort((a, b) => b.length - a.length)[0] ?? ''
  const crumb = (text: string, view = '', reveal = '') => {
    const li = el('li')
    if (!view) {
      li.append(el('span', '', text))
      return li
    }
    const a = el('a', '', text)
    a.href = './'
    a.dataset.view = view
    if (reveal) a.dataset.reveal = reveal
    li.append(a)
    return li
  }
  const list = byId('crumbs-list')
  const key = JSON.stringify([cwd, home, label(s.info)])
  if (list.dataset.key === key) return
  list.dataset.key = key
  const here = crumb(visible(label(s.info)))
  here.setAttribute('aria-current', 'page')
  const folder = home || cwd.replace(/\/+$/, '')
  const parts = home ? [crumb('Projects', 'projects'), crumb(baseName(home), 'projects', home)] : [crumb('Welcome', 'overview'), ...(folder ? [crumb(baseName(folder))] : [])]
  if (folder) parts[parts.length - 1]!.title = folder
  list.replaceChildren(...parts, here)
}

async function loadProjects(force = false) {
  if (!state.connected || (!force && Date.now() - projectsAt < 10000)) return
  projectsAt = Date.now()
  const res = await api('/api/projects', {}).catch(() => null)
  const body = res && res.ok ? ((await res.json().catch(() => null)) as ProjectsView | null) : null
  if (body) byId<HTMLElementTagNameMap['sf-projects']>('projects').view = body
  if (body) {
    reviewer = body.reviewer ?? null
    starterNames = new Map((body.starters ?? []).map(a => [a.id, a.name]))
    setAgentNames(body.starters ?? [])
    schedulePaint()
  }
  if (body) projectPaths = body.projects.map(p => p.path)
  if (body) projectCount = body.projects.length
  if (body) projectList = body.projects.map(p => ({ ...p, agents: Array.isArray(p.agents) ? p.agents.filter(a => typeof a === 'string') : [] }))
}

const files = new Map<string, FileView>()
const FILE_ERRORS: Record<string, string> = {
  outside: "Only files inside this session's project open here.",
  secret: "This file can hold secrets, so the page doesn't show it.",
  large: 'This file is over 512 KB, too large to show here.',
  binary: "This isn't a text file.",
  missing: "This file isn't there anymore.",
  link: "This path goes through a symbolic link, which the page doesn't follow.",
}

function fileLabel(key: string): string {
  const path = files.get(key)?.path ?? key
  const name = baseName(path)
  const twin = openTabs.some(k => k !== key && k.startsWith('f:') && baseName(files.get(k)?.path ?? k) === name)
  const parts = path.split('/').filter(Boolean)
  return twin && parts.length > 1 ? `${name} · ${parts[parts.length - 2]}` : name
}

const fileLoads = new Set<string>()
let focusFile = ''

function loadFile(key: string) {
  const at = key.indexOf(':', 2)
  const sid = key.slice(2, at)
  const path = key.slice(at + 1)
  if (fileLoads.has(key) || !sessions.has(sid)) return
  fileLoads.add(key)
  void api('/api/file', { sid, path }).catch(() => null).then(async res => {
    const body = res ? ((await res.json().catch(() => null)) as Record<string, unknown> | null) : null
    fileLoads.delete(key)
    if (!openTabs.includes(key)) return
    const text = res?.ok && typeof body?.text === 'string' ? body.text : undefined
    const shown = typeof body?.path === 'string' ? body.path : path
    const old = files.get(key)
    if (text !== undefined && old?.text === text && old.path === shown) return
    files.set(key, text !== undefined ? { path: shown, text } : { path, note: (Object.hasOwn(FILE_ERRORS, String(body?.error)) ? FILE_ERRORS[String(body?.error)] : undefined) ?? "The file couldn't be read." })
    const file = byId<HTMLElementTagNameMap['sf-file']>('file')
    const refocus = current.key === key && (focusFile === key || file.contains(document.activeElement) || document.activeElement === document.body)
    paint()
    if (refocus) file.focusContent()
  })
}

function openFile(sid: string, path: string) {
  const key = `f:${sid}:${path}`
  if (!files.has(key)) files.set(key, { path })
  focusFile = key
  go(key)
  byId<HTMLElementTagNameMap['sf-file']>('file').focusContent()
}
let docsAt = 0

async function loadDocs() {
  const docs = byId<HTMLElementTagNameMap['sf-docs']>('docs')
  if (docs.loaded || !state.connected || Date.now() - docsAt < 10000) return
  docsAt = Date.now()
  const { ok, body } = await reply(await api('/api/docs', {}).catch(() => null))
  docs.text = ok && typeof body.text === 'string' ? body.text : ''
}

function openDocs(id: string, back: { view: string; label: string } | null = null) {
  go('docs')
  byId<HTMLElementTagNameMap['sf-docs']>('docs').show(id, back)
}

async function reply(res: Response | null): Promise<{ ok: boolean; body: Record<string, unknown> }> {
  const body = res ? ((await res.json().catch(() => ({}))) as Record<string, unknown>) : {}
  return { ok: !!res && res.ok, body }
}

async function loadChanges(s: Session) {
  const key = `${s.gate?.head ?? ''}:${s.gate?.branch ?? ''}:${working(s)}:${s.info.review?.commit ?? ''}:${s.info.review?.running ?? ''}:${s.info.push?.at ?? ''}`
  if (!state.connected || s.changesKey === key) return
  if (Date.now() - s.changesAt < 5000) return void setTimeout(schedulePaint, 5100 - (Date.now() - s.changesAt))
  s.changesAt = Date.now()
  const res = await api('/api/changes', { sid: s.sid }).catch(() => null)
  const body = res && res.ok ? ((await res.json().catch(() => null)) as { changes?: Changes | null } | null) : null
  if (body) s.changesKey = key
  else setTimeout(schedulePaint, 5100)
  s.changes = body?.changes ?? null
  if (current() === s) byId<HTMLElementTagNameMap['sf-changes']>('changes').changes = s.changes
}

function say(message: string) {
  byId('hint').textContent = message
  if (message && document.body.classList.contains('side-full')) toast({ id: 'say', title: message, tone: 'info' })
}

async function send(text: string, images: string[] = [], s = current(), pressed = false, files: string[] = []) {
  if (!s) return false
  const asleep = sleeping(s)
  if (asleep && !s.info.controlled) {
    say('Wake the session first: it was read-only on this page when it stopped.')
    return false
  }
  if (asleep && (images.length || files.length)) {
    say('Images and files need a running session. Send a text message first, which wakes it, or choose Wake it without a message in the ⋯ menu.')
    return false
  }
  if (!asleep && !controllable(s)) {
    say('This session is read-only on this page.')
    return false
  }
  if (text.startsWith('!')) {
    const line = text.slice(1).trim()
    toast({ id: 'shell-line', title: "The page doesn't run shell lines.", description: `It sends messages to ${agentWord(s.info.agent)}. Ask it to run the command, or run it yourself in the agent container.`, tone: 'warn', ...(line ? { action: { label: 'Copy the command', run: () => void copy(line) } } : {}) })
    return false
  }
  if (/^\/[^\s/]+(?:\s|$)/.test(text) && (images.length || files.length)) {
    say("A command can't carry images or files. Send them in a message of their own.")
    return false
  }
  const bypass = s.info.mode === 'bypassPermissions'
  if (bypass && !pressed && !confirmed(`${s.sid}:bypass`, 'This session runs with bypassPermissions: Claude runs every tool without asking. Press Enter again to send anyway.')) return false
  say('')
  const body: { sid: string; text: string; images?: string[]; files?: string[]; confirm?: boolean } = { sid: s.sid, text }
  if (images.length) body.images = images
  if (files.length) body.files = files
  if (bypass) body.confirm = true
  const name = starterNames.get(s.info.agent ?? '') ?? 'The agent'
  const answer = asleep ? await withConsent(name, 'the session', consent => api('/api/message', { ...body, ...consent })) : await reply(await api('/api/message', body).catch(() => null))
  if (!answer.ok) {
    const why = answer.body.message
    say(typeof why === 'string' && why ? visible(why) : 'The message did not go through. Check that the session is still running.')
    return false
  }
  if (asleep) {
    say(s.info.live ? '' : `Waking the session. The message goes to ${agentWord(s.info.agent)} once it reconnects, if that happens within 2 minutes.`)
  }
  if (s.context && /^\/knackpack:checkpoint(?:\s|$)/.test(text)) {
    markCheckpoint(s.sid, s.context.level)
    schedulePaint()
  }
  return true
}

const IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp']
const MAX_SIDE = 1568
const MAX_IMAGE = 2.5 * 1024 * 1024
const MAX_DROP = 10 * 1024 * 1024

interface Draft {
  text: string
  images: { blob: Blob; url: string }[]
  files: File[]
}
const drafts = new Map<string, Draft>()

function draftOf(key: string): Draft {
  let d = drafts.get(key)
  if (!d) {
    d = { text: '', images: [], files: [] }
    drafts.set(key, d)
  }
  return d
}

function dropDraft(key: string) {
  for (const item of drafts.get(key)?.images ?? []) URL.revokeObjectURL(item.url)
  drafts.delete(key)
}

function swapDraft(from: string, to: string) {
  if (from.startsWith('s/')) {
    const d = draftOf(from)
    d.text = text.value
    if (!d.text && !d.images.length && !d.files.length) drafts.delete(from)
  }
  text.value = drafts.get(to)?.text ?? ''
  paintAttached()
}

async function shrink(file: File): Promise<Blob | null> {
  if (file.type === 'image/gif') return file
  const bitmap = await createImageBitmap(file).catch(() => null)
  if (!bitmap) return null
  const scale = Math.min(1, MAX_SIDE / Math.max(bitmap.width, bitmap.height))
  if (scale === 1 && file.size <= MAX_IMAGE) {
    bitmap.close()
    return file
  }
  const canvas = document.createElement('canvas')
  canvas.width = Math.max(1, Math.round(bitmap.width * scale))
  canvas.height = Math.max(1, Math.round(bitmap.height * scale))
  canvas.getContext('2d')!.drawImage(bitmap, 0, 0, canvas.width, canvas.height)
  bitmap.close()
  const encode = (type: string, quality: number) => new Promise<Blob | null>(resolve => canvas.toBlob(resolve, type, quality))
  const blob = await encode(file.type === 'image/jpeg' ? 'image/jpeg' : 'image/png', 0.9)
  return blob && blob.size > MAX_IMAGE ? encode('image/jpeg', 0.85) : blob
}

function paintAttached() {
  const attached = drafts.get(current.key)?.images ?? []
  const files = drafts.get(current.key)?.files ?? []
  const box = byId('attachments')
  box.replaceChildren()
  for (const file of files) {
    const chip = el('div', 'file-chip')
    chip.title = `${agentWord(current()?.info.agent)} gets a read-only copy of ${file.name}, deleted when the session ends. Changes don't reach your file.`
    const remove = button('×', 'ghost', () => {
      files.splice(files.indexOf(file), 1)
      paintAttached()
      text.focus()
    })
    remove.setAttribute('aria-label', `Remove ${file.name}`)
    chip.append(icon('file'), el('span', 'file-chip-name', file.name), el('span', 'file-chip-note', 'copy'), remove)
    box.append(chip)
  }
  attached.forEach((item, i) => {
    const figure = el('figure', 'thumb')
    const img = el('img')
    img.src = item.url
    img.alt = `Pasted image ${i + 1}`
    const remove = button('×', 'ghost', () => {
      URL.revokeObjectURL(item.url)
      attached.splice(attached.indexOf(item), 1)
      paintAttached()
      text.focus()
    })
    remove.setAttribute('aria-label', `Remove image ${i + 1}`)
    figure.append(img, remove)
    box.append(figure)
  })
  box.hidden = attached.length === 0 && files.length === 0
}

async function sha256(data: ArrayBuffer): Promise<string> {
  return [...new Uint8Array(await crypto.subtle.digest('SHA-256', data))].map(b => b.toString(16).padStart(2, '0')).join('')
}

function mention(key: string, path: string) {
  const token = `@${path} `
  if (key !== current.key) {
    const d = draftOf(key)
    d.text = `${d.text}${d.text && !/\s$/.test(d.text) ? ' ' : ''}${token}`
    return
  }
  const start = text.selectionStart
  const before = text.value.slice(0, start)
  text.setRangeText(`${before && !/\s$/.test(before) ? ' ' : ''}${token}`, start, text.selectionEnd, 'end')
  grow()
}

async function attachFile(file: File, key: string, sid: string) {
  if (file.size > MAX_DROP) return say(`${file.name} is larger than 10 MB. Type its path with @ instead.`)
  const data = await file.arrayBuffer().catch(() => null)
  if (!data) return say(`The page couldn't read ${file.name}. For a folder, type its path with @ instead.`)
  const res = await api('/api/file-match', { sid, name: file.name, size: file.size, sha256: await sha256(data) }).catch(() => null)
  const found: { path?: unknown } | null = res && res.ok ? await res.json().catch(() => null) : null
  if (typeof found?.path === 'string' && found.path) return mention(key, found.path)
  const d = draftOf(key)
  if (d.files.length >= 4) return say('You can attach up to 4 files to a message.')
  d.files.push(file)
  toast({ id: `copy:${file.name}`, title: `${file.name} isn’t in this project`, description: `${agentWord(sessions.get(sid)?.info.agent)} gets a read-only copy, deleted when the session ends. Changes don’t reach your file.`, tone: 'info', timeout: 8000 })
}

async function attach(files: File[]) {
  const key = current.key
  const attached = draftOf(key).images
  const sid = current()?.sid
  for (const file of files) {
    if (!IMAGE_TYPES.includes(file.type)) {
      if (sid) await attachFile(file, key, sid)
      continue
    }
    if (attached.length >= 4) {
      say('You can attach up to 4 images to a message.')
      break
    }
    const blob = await shrink(file)
    if (!blob || blob.size > MAX_IMAGE || !IMAGE_TYPES.includes(blob.type)) {
      say('That image is too large to send, or not one the page can read.')
      continue
    }
    if (drafts.get(key)?.images !== attached) break
    attached.push({ blob, url: URL.createObjectURL(blob) })
  }
  if (key === current.key) {
    paintAttached()
    text.focus()
  }
}

const SIGN_IN_HINT = 'Press Sign in to use the page in this tab.'
let fresh = true

function showGone(head: string, detail: string, offerSignIn: boolean, busy = false) {
  byId('gone').textContent = head
  byId('gone-detail').textContent = detail
  byId('gone-progress').hidden = !busy
  byId('signin').hidden = !offerSignIn
  document.body.classList.add('out')
}

let generation = 0
let reading: ReadableStreamDefaultReader<string> | null = null

function signOut(head: string, detail: string) {
  generation++
  void reading?.cancel().catch(() => undefined)
  reading = null
  clearTimeout(lost)
  lost = 0
  forget()
  state.connected = false
  state.stream = ''
  state.note = head
  for (const s of sessions.values()) s.view.remove()
  sessions.clear()
  showGone(head, detail, true)
  paint()
}

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms))

async function markSignIn() {
  const state = await signInState(api)
  if (!state) return
  signIns = state
  schedulePaint()
}

async function restarted(): Promise<boolean> {
  const res = await fetch(import.meta.url, { cache: 'no-store' }).catch(() => null)
  return res?.status === 404
}

async function connect(): Promise<void> {
  if (!signedIn()) return signOut('You’re signed out', 'The engine didn’t answer. Check that it’s running (docker compose ps), then press Sign in.')
  const gen = generation
  let openedAt = 0
  try {
    const res = await events()
    if (gen !== generation) return void res.body?.cancel().catch(() => undefined)
    if (res.status === 401) {
      forget()
      if (!fresh && await restarted()) return location.reload()
      if (!fresh) return signOut('You were signed out', `The page was opened in another tab, the engine restarted, or the sign-in expired. ${SIGN_IN_HINT}`)
      fresh = false
      await trade()
      return gen === generation ? connect() : undefined
    }
    if (res.status === 403) {
      const body: { wait?: unknown } = await res.json().catch(() => ({}))
      showGone('Signing in…', 'This takes a few seconds.', false, true)
      await sleep(Math.max(500, Math.min(20000, Number(body.wait) || 1000)))
      return gen === generation ? connect() : undefined
    }
    if (!res.ok || !res.body) throw new Error(String(res.status))
    fresh = false
    clearTimeout(lost)
    lost = 0
    document.body.classList.remove('out')
    state.connected = true
    state.note = ''
    openedAt = Date.now()
    paint()
    const reader = res.body.pipeThrough(new TextDecoderStream()).getReader()
    reading = reader
    let buf = ''
    for (;;) {
      const { value, done } = await reader.read()
      if (done || gen !== generation) break
      buf += value
      for (let i = buf.indexOf('\n'); i >= 0; i = buf.indexOf('\n')) {
        const line = buf.slice(0, i)
        buf = buf.slice(i + 1)
        if (!line.trim()) continue
        try {
          const ev: unknown = JSON.parse(line)
          if (isRecord(ev) && gen === generation) onEvent(ev as unknown as StreamEvent)
        } catch {}
      }
    }
  } catch {
    state.note = 'The engine may have stopped. Retrying…'
  }
  if (gen !== generation) return
  reading = null
  if (openedAt && Date.now() - openedAt >= 30000) retry = 1000
  state.connected = false
  state.stream = ''
  state.note = state.note || 'Reconnecting…'
  paint()
  lost ||= setTimeout(() => showGone('Disconnected from the engine', 'Retrying…', false, true), 3000)
  setTimeout(connect, retry)
  retry = Math.min(retry * 2, 10000)
}

byId('conversations').addEventListener('sf-answer', async e => {
  const sid = (e.target as Element).closest('sf-conversation')?.dataset.sid
  schedulePaint()
  const res = sid ? await api('/api/answer', { sid, ...e.detail }).catch(() => null) : null
  if (!res || !res.ok) {
    ;(e.target as CardHost).error = 'That answer did not go through. It may already have been answered in the session itself.'
    return schedulePaint()
  }
})
byId('conversations').addEventListener('sf-settled', () => {
  if (!byId<HTMLTextAreaElement>('text').disabled) byId('text').focus()
})
byId('reviews').addEventListener('sf-review', () => void startReview(current()))
byId('reviews').addEventListener('sf-test-list', () => void startTestList(current()))
async function setExpect(agent: string, expect: Expect) {
  const { ok, body } = await reply(await api('/api/sign-in-expect', { agent, expect }).catch(() => null))
  if (!ok) toast({ id: `expect:${agent}`, title: "What you expect wasn't saved.", description: typeof body.message === 'string' ? visible(body.message) : 'Check that the engine is running.', tone: 'bad' })
  await loadProjects(true)
}
byId('reviews').addEventListener('sf-push-request', () => void startPush(current()))
byId('push-panel').addEventListener('sf-push-answer', e => void answerPush(current(), e.detail))
byId('forge-panel').addEventListener('sf-forge-answer', e => void answerForge(current(), e.detail))
byId('push-panel').addEventListener('sf-send-findings', e => void sessionAsk(current(), '/api/send-findings', e.detail, "The findings weren't sent.", 'The blocking findings were sent to the session. The push stays held.'))
byId('push-panel').addEventListener('sf-handoff', e => void sessionAsk(current(), '/api/handoff', e.detail, "The push wasn't handed off.", 'The push was denied, and the session was asked for a handoff.'))
byId('push-panel').addEventListener('sf-allow-file', e => void sessionAsk(current(), '/api/allow-file', e.detail, "The file wasn't allowed.", 'You allowed that version of the file. A later change to it is flagged again.'))
byId('push-panel').addEventListener('sf-remove-file', e => void sessionAsk(current(), '/api/flag-remove', e.detail, "The message wasn't sent.", 'The session was asked to remove the file from the branch.'))
byId('reviews').addEventListener('sf-override', e => void sessionAsk(current(), '/api/override', e.detail, "The verdict wasn't overridden.", 'You overrode the verdict for this commit. Press Push to push it.'))
byId('path').addEventListener('click', e => {
  const b = (e.target as HTMLElement).closest<HTMLButtonElement>('button.stage')
  if (!b || b.getAttribute('aria-disabled') === 'true') return
  const command = b.dataset.command || ''
  if (command === 'commit') return once(current(), command, () => send(COMMIT_ASK))
  return once(current(), command, () => send(command))
})
let teamPicks = ''

function paintTeamPicks(s: Session) {
  const own = (s.gate?.types ?? []).filter(t => !t.builtin)
  const key = JSON.stringify(own.map(t => [t.id, t.name, t.steps]))
  if (key === teamPicks) return
  teamPicks = key
  const box = byId('size-pick').querySelector('.size-options')!
  for (const b of box.querySelectorAll('button[data-team]')) b.remove()
  for (const t of own) {
    const b = el('button', 'size-option')
    b.type = 'button'
    b.dataset.size = t.id
    b.dataset.team = 'yes'
    b.append(el('span', '', visible(t.name)), el('span', 'muted small', `From the team file: ${t.steps.join(', ')}`))
    box.append(b)
  }
}

byId('size-pick').addEventListener('click', async e => {
  const b = (e.target as Element).closest<HTMLButtonElement>('button[data-size]')
  const s = current()
  if (!b || !s) return
  const picked = b.dataset.size ?? ''
  const res = await api('/api/size', { sid: s.sid, size: picked }).catch(() => null)
  if (!res || !res.ok) return say('The work type did not change. Check that the session is still running.')
  if (s.gate) s.gate = { ...s.gate, workType: picked, chosen: true }
  void loadGate(s, true)
  paint()
  byId('text').focus()
})
byId('size').addEventListener('change', async () => {
  const s = current()
  const size = byId<HTMLSelectElement>('size')
  const res = s ? await api('/api/size', { sid: s.sid, size: size.value }).catch(() => null) : null
  if (!res || !res.ok) {
    toast({ title: 'The work type did not change.', description: 'Check that the session is still running.', tone: 'bad' })
    if (s?.gate?.workType ?? s?.gate?.size) size.value = s?.gate?.workType ?? s?.gate?.size ?? ''
    return
  }
  if (s) void loadGate(s, true)
})
byId('team-open').addEventListener('click', () => void openTeam())
byId('team-approve').addEventListener('click', () => void teamOp('approve'))
byId('team-forget').addEventListener('click', () => void teamOp('forget'))
const ASK_WAIT_MS = 15000

function waiting(asked: { hash: string; at: number } | undefined, hash: string): boolean {
  return asked?.hash === hash && Date.now() - asked.at < ASK_WAIT_MS
}

async function notThrough(res: Response | null, title: string) {
  if (res?.ok) return
  const why = res ? await res.json().then((b: { message?: unknown }) => (typeof b.message === 'string' ? b.message : ''), () => '') : ''
  toast({ title, description: why ? visible(why) : res ? 'Try again.' : 'Check that the engine is running.', tone: 'bad' })
}
async function approve(kind: 'requirements' | 'plan', hash: string) {
  const s = current()
  if (!s) return
  s.asked[kind] = { hash, at: Date.now() }
  delete s.refused[kind]
  schedulePaint()
  setTimeout(schedulePaint, ASK_WAIT_MS)
  const doc = kind === 'plan' ? s.gate?.planDoc : s.gate?.requirements
  const res = doc?.hash === hash ? await api(kind === 'plan' ? '/api/approve-plan' : '/api/approve-requirements', { sid: s.sid, hash, text: doc.text }).catch(() => null) : null
  delete s.asked[kind]
  void loadGate(s, true)
  schedulePaint()
  if (!res?.ok) await notThrough(res, 'The approval did not go through.')
}
byId('req-approval').addEventListener('sf-approve', e => void approve('requirements', e.detail.hash))
byId('req-approval').addEventListener('sf-revise', e => {
  const s = current()
  if (!s) return
  const note = e.detail.note
  once(s, `revise-req:${note}`, () => send(`Before going on, change the agreed requirements in this session's intent record: ${note}`, [], s).then(ok => {
    if (ok) byId<HTMLElementTagNameMap['sf-approval']>('req-approval').sent(note)
    return ok
  }))
})
byId('approval').addEventListener('sf-approve', e => void approve('plan', e.detail.hash))
byId('approval').addEventListener('sf-revise', e => {
  const s = current()
  const path = s?.gate?.planDoc?.path
  if (!s || !path) return void toast({ title: "This page doesn't know the plan's file.", description: 'Ask for the change in the conversation instead.', tone: 'warn' })
  const note = e.detail.note
  once(s, `revise:${note}`, () => send(`Before going on, change the plan in ${path}: ${note}`, [], s).then(ok => {
    if (ok) byId<HTMLElementTagNameMap['sf-approval']>('approval').sent(note)
    return ok
  }))
})
byId('project-agents').addEventListener('sf-project-setting', async e => {
  const res = await api('/api/project', e.detail).catch(() => null)
  if (!res || !res.ok) {
    const why = res ? await res.json().then((b: { message?: unknown }) => (typeof b.message === 'string' ? b.message : ''), () => '') : ''
    byId<HTMLElementTagNameMap['sf-project-agents']>('project-agents').reset()
    return void toast({ title: 'The project’s setting did not change.', description: why || 'Try again.', tone: 'bad' })
  }
  await loadProjects(true)
  paint()
})
byId('checkpoint-button').addEventListener('click', () => once(current(), 'checkpoint', () => send('/knackpack:checkpoint')))
byId('stall-stop').addEventListener('click', () => {
  const s = current()
  if (s) void api('/api/interrupt', { sid: s.sid }).catch(() => null)
})
byId('stall-copy').addEventListener('click', () => {
  const command = byId('stall-copy').dataset.command
  if (command) void copy(command)
})
byId('stall-dismiss').addEventListener('click', () => {
  const s = current()
  if (!s) return
  stallDismissed.set(s.sid, s.lastAt ?? 0)
  paint()
  const text = byId<HTMLTextAreaElement>('text')
  ;(text.disabled ? byId('composer') : text).focus()
})
setInterval(() => {
  const s = current()
  if (s && inTurn(s)) schedulePaint()
}, 30000)
byId('checkpoint-cancel').addEventListener('click', () => {
  const s = current()
  if (!s?.context) return
  markCheckpoint(s.sid, s.context.level)
  paint()
  const text = byId<HTMLTextAreaElement>('text')
  ;(text.disabled ? byId('composer') : text).focus()
})
byId('session-menu').addEventListener('toggle', e => {
  if ((e as ToggleEvent).newState !== 'open') return
  const menu = byId('session-menu')
  placeMenu(byId('session-more'), menu)
  menu.querySelector<HTMLButtonElement>('button:not([hidden])')?.focus()
})
byId('session-menu').addEventListener('keydown', e => {
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) return
  const items = [...byId('session-menu').querySelectorAll<HTMLButtonElement>('button:not([hidden])')]
  if (!items.length) return
  e.preventDefault()
  const at = items.indexOf(document.activeElement as HTMLButtonElement)
  const down = e.key === 'ArrowDown'
  const next = e.key === 'Home' ? 0 : e.key === 'End' ? items.length - 1 : at < 0 ? (down ? 0 : items.length - 1) : (at + (down ? 1 : items.length - 1)) % items.length
  items[next]!.focus()
})
byId('menu-readonly').addEventListener('click', async () => {
  const s = current()
  byId('session-menu').hidePopover()
  byId('session-more').focus()
  if (!s || !s.info.controlled) return
  const res = await api('/api/control', { sid: s.sid, on: false, confirm: true }).catch(() => null)
  if (!res || !res.ok) toast({ title: 'The page could not make this session read-only.', tone: 'bad' })
})
async function wakeSession(s: Session) {
  const name = starterNames.get(s.info.agent ?? '') ?? 'The agent'
  const { ok, body } = await withConsent(name, 'the session', consent => api('/api/wake', { sid: s.sid, ...consent }))
  toast(ok ? { id: `wake:${s.sid}`, title: `Waking ${visible(label(s.info))}…`, description: 'It reconnects in a few seconds.', tone: 'info', timeout: 30000 } : { id: `wake:${s.sid}`, title: 'The session could not be woken.', description: typeof body.message === 'string' ? visible(body.message) : 'Try again in a moment.', tone: 'bad', timeout: body.needs ? 0 : undefined })
}

async function withConsent(name: string, what: string, send: (c: Consent) => Promise<Response>): Promise<{ ok: boolean; body: Record<string, unknown> }> {
  let consent: Consent = {}
  let answer = await reply(await send(consent).catch(() => null))
  for (let round = 0; !answer.ok && round < 3; round++) {
    const needs = answer.body.needs as Needs | undefined
    if (needs?.trust) {
      if (!(await askTrust(needs.trust))) break
      toast({ id: 'trust', title: 'Answering Claude Code’s trust question…', description: 'This takes a few seconds.', tone: 'info', timeout: 60000 })
      const trusted = await reply(await api('/api/claude-trust', { path: needs.trust }).catch(() => null))
      toast({ id: 'trust', title: trusted.ok ? 'Claude Code now trusts this project.' : 'Claude Code’s trust question wasn’t answered.', description: !trusted.ok && typeof trusted.body.message === 'string' ? visible(trusted.body.message) : undefined, tone: trusted.ok ? 'info' : 'bad', timeout: trusted.ok ? 5000 : 0 })
      if (!trusted.ok) return trusted
      answer = await reply(await send(consent).catch(() => null))
      continue
    }
    const more = await consentFor(needs, name, what)
    if (!more) break
    consent = { ...consent, ...more }
    answer = await reply(await send(consent).catch(() => null))
  }
  return answer
}

function usageText(s: Session): string {
  const parts: string[] = []
  if (s.total) parts.push(`${s.total.toLocaleString()} tokens`)
  const cost = money(s.cost)
  if (cost) parts.push(`≈ ${cost} (the agent's estimate)`)
  const si = s.info.signIn
  const text = parts.length ? `This session: ${parts.join(', ')}` : ''
  return si ? [text, si.method === 'not-signed-in' ? 'not signed in' : `signed in with ${visible(si.words)}`].filter(Boolean).join(' · ') : text
}
byId('menu-wake').addEventListener('click', () => {
  const s = current()
  byId('session-menu').hidePopover()
  byId('session-more').focus()
  if (s && sleeping(s)) void wakeSession(s)
})
byId('session-menu').addEventListener('click', async e => {
  const b = (e.target as Element).closest<HTMLButtonElement>('button[data-copy]')
  if (!b) return
  byId('session-menu').hidePopover()
  byId('session-more').focus()
  await copy((b.textContent || '').replace(/^Copy /, ''))
})

async function copy(command: string) {
  const ok = await navigator.clipboard.writeText(command).then(() => true, () => false)
  toast(ok ? { id: 'copy', title: `Copied ${command}`, description: 'Run it in a terminal on this computer, in the folder that holds compose.yaml.', tone: 'ok' } : { id: 'copy', title: 'The browser did not allow copying.', description: `Type ${command} in a terminal on this computer, in the folder that holds compose.yaml.`, tone: 'bad' })
}
byId('manage-button').addEventListener('click', () => {
  const s = current()
  const action = byId('manage-button').dataset.action
  if (!s || !confirmed(`${s.sid}:${action}`)) return
  void manage(s, action === 'stop' ? '/api/stop-session' : '/api/remove-session', {})
})
byId('force-button').addEventListener('click', () => {
  const s = current()
  const offer = s && removals.get(s.sid)?.offer
  if (!s || !offer || !confirmed(`${s.sid}:force`)) return
  void manage(s, '/api/remove-session', { flag: offer.flag, value: offer.value })
})
byId('manage-cancel').addEventListener('click', () => {
  const s = current()
  if (!s) return
  for (const action of ['stop', 'remove', 'force']) armed.delete(`${s.sid}:${action}`)
  paint()
  byId('manage-button').focus()
})
byId('control-cancel').addEventListener('click', () => {
  const s = current()
  if (!s) return
  armed.delete(`${s.sid}:control`)
  paint()
  byId('control-button').focus()
})
byId('control-button').addEventListener('click', async () => {
  const s = current()
  if (!s) return
  if (byId('control-button').dataset.action === 'release') {
    const res = await api('/api/release', { sid: s.sid }).catch(() => null)
    toast(res && res.ok ? { title: 'Ended both connections.', description: 'The real session reconnects by itself within a few seconds. If the other one comes back too, stop the program that runs it.', tone: 'info', timeout: 10000 } : { title: 'The connections could not be ended.', description: 'Try again, or restart the session.', tone: 'bad' })
    return
  }
  if (byId('control-button').dataset.action === 'wake') return void wakeSession(s)
  if (s.info.controlled) {
    await api('/api/control', { sid: s.sid, on: false, confirm: true }).catch(() => null)
    return
  }
  if (!confirmed(`${s.sid}:control`)) return
  const res = await api('/api/control', { sid: s.sid, on: true, confirm: true }).catch(() => null)
  if (!res || !res.ok) toast({ title: 'The page could not take control of this session.', tone: 'bad' })
  paint()
})
byId('signin').addEventListener('click', async () => {
  showGone('Signing in…', '', false, true)
  await trade()
  void connect()
})
function followView(e: MouseEvent) {
  const link = (e.target as Element).closest<HTMLElement>('a[data-view]')
  if (!link || (e.type === 'auxclick' && e.button !== 1)) return
  e.preventDefault()
  go(link.dataset.view!)
  if (link.dataset.reveal) projects.reveal(link.dataset.reveal)
}
document.addEventListener('click', followView)
document.addEventListener('auxclick', followView)
byId('tabs').addEventListener('sf-open', e => go(e.detail.id))
document.body.addEventListener('sf-file', e => {
  const s = current()
  if (s) openFile(s.sid, e.detail.path)
})
byId('file').addEventListener('sf-reload', () => {
  if (!current.key.startsWith('f:')) return
  focusFile = current.key
  loadFile(current.key)
})
const RECHECK_MS = 30000
const known = new Map<string, Map<string, number | true>>()
const scanning = new Map<string, ReturnType<typeof setTimeout>>()
const PATHLIKE = /^(?!\w+:\/\/)(?:\.\/)?[^\s`'"<>|*?]*(?:\/[^\s`'"<>|*?]*|\.[A-Za-z0-9]{1,10})$/

function fileRef(text: string): string {
  const path = text.trim().replace(/:\d+(?::\d+)?$/, '').replace(/^\.\//, '')
  return path.length <= 300 && path !== '/' && PATHLIKE.test(path) && !/^\.+$/.test(path) ? path : ''
}

function markFiles(view: HTMLElement, sid: string) {
  const seen = known.get(sid) ?? new Map<string, number | true>()
  known.set(sid, seen)
  const ask = new Set<string>()
  const now = Date.now()
  for (const code of view.querySelectorAll<HTMLElement>('.reply sf-markdown :not(pre) > code')) {
    if (code.closest('a')) continue
    const path = fileRef(code.textContent || '')
    if (!path) continue
    const hit = seen.get(path)
    if (hit === undefined || (hit !== true && now - hit > RECHECK_MS)) ask.add(path)
    else if (hit === true && !code.classList.contains('file-ref')) {
      code.classList.add('file-ref')
      code.dataset.file = path
      code.tabIndex = 0
      code.setAttribute('role', 'link')
      code.title = `Open ${path} read-only`
    }
  }
  for (const span of view.querySelectorAll<HTMLElement>('.tool .call span.tool-path[data-path]')) {
    const path = span.dataset.path || ''
    if (path.length > 1000) continue
    const hit = seen.get(path)
    if (hit === true) span.replaceWith(fileLink(path))
    else if (hit === undefined || now - hit > RECHECK_MS) ask.add(path)
  }
  const paths = [...ask].slice(0, 20)
  if (!paths.length) return
  for (const p of paths) seen.set(p, now)
  void api('/api/file-check', { sid, paths }).catch(() => null).then(reply).then(({ ok, body }) => {
    if (!ok || !Array.isArray(body.found)) {
      for (const p of paths) seen.delete(p)
      return
    }
    for (const p of body.found) if (typeof p === 'string') seen.set(p, true)
    markFiles(view, sid)
  })
}

byId('conversations').addEventListener('sf-paint', e => {
  const view = (e.target as Element).closest<HTMLElement>('#conversations > *')
  const s = [...sessions.values()].find(x => x.view === view)
  if (!view || !s) return
  clearTimeout(scanning.get(s.sid))
  scanning.set(s.sid, setTimeout(() => markFiles(view, s.sid), 600))
})
const openRef = (e: Event) => {
  const code = (e.target as Element).closest<HTMLElement>('code.file-ref')
  if (!code?.dataset.file) return
  e.preventDefault()
  emit(code, 'sf-file', { path: code.dataset.file })
}
byId('conversations').addEventListener('click', openRef)
byId('conversations').addEventListener('keydown', e => {
  if (e.key === 'Enter') openRef(e)
})
byId('session-docs').addEventListener('click', () => {
  const s = current()
  openDocs(byId('session-docs').dataset.doc!, s ? { view: current.key, label: 'session' } : null)
})
byId('projects').addEventListener('click', e => {
  const link = (e.target as Element).closest<HTMLElement>('button[data-doc]')
  if (link) openDocs(link.dataset.doc!, { view: 'projects', label: 'Projects' })
})
byId('welcome').addEventListener('click', e => {
  const target = e.target as Element
  const walk = target.closest<HTMLElement>('[data-doc]')
  if (walk) return openDocs(walk.dataset.doc!, { view: 'overview', label: 'Welcome' })
  const start = target.closest<HTMLElement>('[data-start]')?.dataset.start
  if (start === 'docs') openDocs('')
  if (start === 'login') void claudeLogin(api, () => void markSignIn())
  if (start === 'session' || start === 'add') go('projects')
})
const projects = byId<HTMLElementTagNameMap['sf-projects']>('projects')
async function changeRepo(detail: { op: 'add'; url: string } | { op: 'remove'; name: string }) {
  const { ok, body } = detail.op === 'add' ? await reply(await api('/api/add-repo', { url: detail.url }).catch(() => null)) : await reply(await api('/api/remove-repo', { name: detail.name }).catch(() => null))
  if (!ok) return projects.say('', typeof body.message === 'string' ? body.message : "That didn't work. Try again.", true)
  if (detail.op === 'add') {
    projects.added()
    projects.say('', `Cloning ${typeof body.url === 'string' ? body.url : detail.url}…`)
  } else projects.say('', '')
  await loadProjects(true)
}
async function changeToken(detail: { op: 'save'; host: string; token: string } | { op: 'remove'; host: string }) {
  const { ok, body } = detail.op === 'save' ? await reply(await api('/api/save-token', { host: detail.host, token: detail.token }).catch(() => null)) : await reply(await api('/api/remove-token', { host: detail.host }).catch(() => null))
  if (!ok) return projects.tokenSaid(typeof body.message === 'string' ? body.message : "That didn't work. Try again.", true)
  const saved = isRecord(body.host) && typeof body.host.host === 'string' ? body.host.host : detail.host
  projects.tokenSaid(detail.op === 'save' ? `Token saved for ${saved}.` : `Token removed for ${detail.host}.`)
  await loadProjects(true)
}
projects.addEventListener('sf-repo', e => void changeRepo(e.detail))
projects.addEventListener('sf-expect', e => void setExpect(e.detail.agent, e.detail.expect))
projects.addEventListener('sf-token', e => void changeToken(e.detail))
projects.addEventListener('sf-session-action', async e => {
  const s = sessions.get(e.detail.sid)
  const project = s && projectOf(s.info.cwd || '')
  if (!s || !project) return
  const name = label(s.info)
  projects.say(project.path, e.detail.action === 'stop' ? `Stopping ${name}…` : `Deleting ${name}…`)
  const problem = await manage(s, e.detail.action === 'stop' ? '/api/stop-session' : '/api/remove-session', {})
  projects.say(project.path, problem, !!problem)
})
projects.addEventListener('sf-dispatch', async e => {
  const { path } = e.detail
  projects.starting(path)
  const name = starterNames.get(e.detail.agent) ?? 'The agent'
  const { ok, body } = await withConsent(name, 'the session', consent => api('/api/dispatch', { ...e.detail, ...consent }))
  if (!ok) {
    projects.failed()
    const text = typeof body.message === 'string' ? body.message : "The session didn't start. Try again."
    const trust = body.error === 'not trusted'
    toast({ id: `start:${path}`, title: trust ? `Claude Code doesn't trust ${baseName(path)} yet` : `No session started in ${baseName(path)}`, description: visible(text), tone: trust ? 'warn' : 'bad', timeout: 0 })
    return void loadProjects(true)
  }
  byId<HTMLElementTagNameMap['sf-toasts']>('toasts').close(`start:${path}`)
  projects.started(path, typeof body.id === 'string' ? body.id : '')
})
byId('tabs').addEventListener('sf-close', e => closeTab(e.detail.id))
byId('tabs').addEventListener('sf-move', e => {
  const { id } = e.detail
  const from = openTabs.indexOf(id)
  if (from < 1) return
  const rest = openTabs.filter(k => k !== id)
  const to = Math.max(1, Math.min(rest.length, e.detail.index > from ? e.detail.index - 1 : e.detail.index))
  if (to === from) return
  rest.splice(to, 0, id)
  openTabs = rest
  paint()
})
byId('overview').addEventListener('sf-counts', e => {
  const { waiting, working, stopped } = e.detail
  const box = byId('counts')
  box.replaceChildren()
  const item = (n: number, cls: string, words: string) => {
    if (!n) return
    const span = el('span', 'status-count')
    const dot = el('span', `dot ${cls}`)
    dot.setAttribute('aria-hidden', 'true')
    span.append(dot, `${n} ${words}`)
    box.append(span)
  }
  item(waiting, 'need', waiting === 1 ? 'needs you' : 'need you')
  item(working, 'busy', 'working')
  item(stopped, 'stopped', 'stopped before finishing')
})

function placeSide() {
  const rem = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16
  const now = byId('frame').clientWidth / rem >= WIDE_REM
  const at = document.body.classList.contains('side-full') ? 'full' : now ? 'frame' : 'narrow'
  if (at !== sideAt) {
    sideAt = at
    const active = document.activeElement
    if (at === 'frame') byId('frame').append(byId('composer'), byId('side'))
    else {
      byId('frame').after(byId('composer'))
      if (at === 'full') byId('crumbs').parentElement!.after(byId('side'))
      else byId('side-toggle').after(byId('side'))
    }
    if (active instanceof HTMLElement && active !== document.activeElement) active.focus()
  }
  if (now === wide) return
  wide = now
  document.body.classList.toggle('wide', now)
  byId<HTMLElementTagNameMap['sf-docs']>('docs').navSlot = now ? byId('docs-side') : null
  byId<HTMLElementTagNameMap['sf-projects']>('projects').sideSlot = now ? byId('projects-side') : null
}

function paintSide(s: Session | null) {
  const full = !!s && sideFull && !byId('path-section').hidden
  const was = document.body.classList.contains('side-full')
  if (full && !was) sideScroll = scroller.scrollTop
  document.body.classList.toggle('side-full', full)
  placeSide()
  if (!full && was) scroller.scrollTop = sideScroll
  const needs = !!(full && s && waitingOf(s).length)
  byId('side-needs').hidden = !needs
  const said = needs && s ? `${agentWord(s.info.agent)} needs you in the conversation.` : ''
  if (byId('side-needs-said').textContent !== said) byId('side-needs-said').textContent = said
  byId('session-docs').dataset.doc = full ? 'execution-path-with-knackpack' : 'working-in-a-session'
}

function setSide(full: boolean) {
  sideFull = full
  paintSide(current())
  if (full) scroller.scrollTop = 0
}

byId('side-open').addEventListener('click', () => {
  setSide(true)
  byId('side-expand').focus()
})
byId('side-expand').addEventListener('click', () => {
  setSide(false)
  byId('side-open').focus()
})
byId('side-back').addEventListener('click', () => {
  setSide(false)
  jump('latest')
})
document.addEventListener('keydown', e => {
  if (e.key !== 'Escape' || e.defaultPrevented || !document.body.classList.contains('side-full')) return
  if (e.target instanceof HTMLElement && e.target.closest('[popover], select')) return
  setSide(false)
  byId('side-open').focus()
})
byId('side-toggle').addEventListener('click', () => {
  const toggle = byId('side-toggle')
  const open = toggle.getAttribute('aria-expanded') !== 'true'
  toggle.setAttribute('aria-expanded', String(open))
  byId('side').classList.toggle('open', open)
})
new ResizeObserver(placeSide).observe(byId('frame'))
new MutationObserver(placeSide).observe(document.documentElement, { attributes: true, attributeFilter: ['data-scale'] })
placeSide()

let jumping: ReturnType<typeof setTimeout> | null = null

function paintJump() {
  const live = current.key.startsWith('s/') && !jumping
  const screen = scroller.clientHeight
  byId('jump-start').hidden = !live || scroller.scrollTop < screen
  byId('jump-latest').hidden = !live || scroller.scrollHeight - scroller.scrollTop - screen < screen
}

function landed() {
  if (jumping) clearTimeout(jumping)
  jumping = null
  paintJump()
}

function jump(to: 'start' | 'latest') {
  if (jumping) clearTimeout(jumping)
  jumping = setTimeout(landed, 2000)
  paintJump()
  scroller.scrollTo({ top: to === 'start' ? 0 : scroller.scrollHeight, behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
}

scroller.addEventListener('scroll', paintJump, { passive: true })
scroller.addEventListener('scrollend', landed)
const resized = new ResizeObserver(paintJump)
resized.observe(scroller)
resized.observe(byId('conversations'))
byId('jump-start').addEventListener('click', () => jump('start'))
byId('jump-latest').addEventListener('click', () => jump('latest'))
document.addEventListener('keydown', e => {
  if ((e.key !== 'Home' && e.key !== 'End') || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey || !current.key.startsWith('s/')) return
  if (e.defaultPrevented || (e.target instanceof HTMLElement && e.target.closest('input, textarea, select, [contenteditable], [popover], [role="tablist"]'))) return
  if (e.target instanceof HTMLElement && e.target !== document.body && e.target.scrollHeight > e.target.clientHeight + 1) return
  e.preventDefault()
  jump(e.key === 'Home' ? 'start' : 'latest')
})

byId('frame').addEventListener('wheel', e => {
  const side = e.target instanceof Element ? e.target.closest<HTMLElement>('.page-side') : null
  const still = !!side && side.scrollHeight <= side.clientHeight
  if (e.target !== e.currentTarget && !still) return
  scroller.scrollTop += e.deltaMode === 1 ? e.deltaY * 16 : e.deltaMode === 2 ? e.deltaY * scroller.clientHeight : e.deltaY
}, { passive: true })

const text = byId<HTMLTextAreaElement>('text')
complete(text, byId('complete'), {
  catalog: () => current()?.catalog ?? null,
  files: async q => {
    const s = current()
    if (!s) return { files: [], cut: false }
    const res = await api('/api/files', { sid: s.sid, q })
    const body: { files?: unknown; cut?: unknown } | null = res.ok ? await res.json() : null
    const files = Array.isArray(body?.files) ? (body.files as unknown[]).filter((f): f is string => typeof f === 'string') : []
    return { files, cut: body?.cut === true }
  },
})
const grow = () => {
  if (!text.offsetParent) return
  text.style.height = 'auto'
  text.style.height = `${text.scrollHeight + text.offsetHeight - text.clientHeight}px`
}
text.addEventListener('input', grow)
text.addEventListener('paste', e => {
  const files = [...(e.clipboardData?.items || [])].filter(i => i.kind === 'file').map(i => i.getAsFile()).filter((f): f is File => !!f)
  if (!files.length) return
  e.preventDefault()
  void attach(files)
})
document.addEventListener('dragover', e => {
  if (![...(e.dataTransfer?.items || [])].some(i => i.kind === 'file')) return
  e.preventDefault()
  if (e.dataTransfer) e.dataTransfer.dropEffect = current() ? 'copy' : 'none'
})
document.addEventListener('drop', e => {
  const all = [...(e.dataTransfer?.files || [])]
  if (!all.length) return
  e.preventDefault()
  if (!current() || text.disabled) return
  const items = [...(e.dataTransfer?.items || [])].filter(i => i.kind === 'file')
  const folders = items.filter(i => i.webkitGetAsEntry()?.isDirectory).length
  const files = items.length ? items.filter(i => !i.webkitGetAsEntry()?.isDirectory).map(i => i.getAsFile()).filter((f): f is File => !!f) : all
  if (files.length) void attach(files)
  if (folders) say('A folder can’t be dropped here. Type its path with @ instead.')
})
text.addEventListener('keydown', e => {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    byId<HTMLFormElement>('composer').requestSubmit()
  }
})
byId('composer').addEventListener('submit', async e => {
  e.preventDefault()
  const s = current()
  const key = current.key
  const value = text.value.trim()
  const attached = [...(drafts.get(key)?.images ?? [])]
  const files = [...(drafts.get(key)?.files ?? [])]
  if (text.disabled) return
  if (!s || (!value && !attached.length && !files.length) || !state.connected || sending.has(key)) return
  sending.add(key)
  const sent = async (blob: Blob, name = '') => {
    const res = await upload(blob, s.sid, name).catch(() => null)
    const body: { id?: unknown } | null = res && res.ok ? await res.json().catch(() => null) : null
    return body && typeof body.id === 'string' ? body.id : ''
  }
  const ids: string[] = []
  for (const item of attached) {
    const id = await sent(item.blob)
    if (!id) break
    ids.push(id)
  }
  const fids: string[] = []
  if (ids.length === attached.length) {
    for (const file of files) {
      const id = await sent(file, file.name)
      if (!id) break
      fids.push(id)
    }
  }
  if (ids.length !== attached.length) say('An image did not go through. Try again, or check that the session is still running.')
  else if (fids.length !== files.length) say('A file did not go through. Try again, or check that the session is still running.')
  else if (await send(value, ids, s, false, fids)) {
    const d = drafts.get(key)
    if (d) {
      for (const item of attached) {
        URL.revokeObjectURL(item.url)
        if (d.images.includes(item)) d.images.splice(d.images.indexOf(item), 1)
      }
      for (const file of files) if (d.files.includes(file)) d.files.splice(d.files.indexOf(file), 1)
      if (d.text.trim() === value) d.text = ''
      if (!d.text && !d.images.length && !d.files.length) drafts.delete(key)
    }
    if (key === current.key) {
      if (text.value.trim() === value) text.value = ''
      grow()
      paintAttached()
    }
  }
  sending.delete(key)
  if (key === current.key) text.focus()
})
byId('stop').addEventListener('click', () => {
  const s = current()
  if (s) void api('/api/interrupt', { sid: s.sid }).catch(() => null)
})
restoreTabs()
byId('disconnect').addEventListener('click', async () => {
  if (byId('account').matches(':popover-open')) byId('account').hidePopover()
  await api('/api/disconnect', {}).catch(() => null)
  signOut('Disconnected', SIGN_IN_HINT)
})
paint()
initPrefs(body => api('/api/prefs', body).catch(() => null))
addEventListener('pagehide', () => {
  saveTabs()
  saveCheckpoints()
})
void exchange().then(() => connect())
