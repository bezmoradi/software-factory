import { api, exchange, trade } from './api.ts'
import { claudeLogin, signInState } from './components/login.ts'
import { editIdentity, identityState, type CommitIdentity } from './components/identity.ts'
import { GATEWAY_WARNING, openEgress } from './components/egress-panel.ts'
import { busy, button, byId, copyText, el, emit, fileLink, repoPath, shown, visible, visibleCommand, whileBusy } from './lib/dom.ts'
import { startComposer } from './composer.ts'
import { initPrefs } from './lib/prefs.ts'
import { agentWord, baseName, isClaude, label, plural, setAgentNames, shortSha } from './lib/format.ts'
import { paintPath } from './lib/path.ts'
import { placeMenu } from './lib/menu.ts'
import { isRecord } from './lib/json.ts'
import type { CardHost, Changes, Check, Expect, GateEvent, Project, ProjectsView, PushPanelView, ForgePanelView, Reviewer, ReviewerView, ReviewsView, SessionInfo, SessionReview, StreamEvent, TeamView } from './types.ts'
import './components/changes.ts'
import './components/overview.ts'
import './components/docs.ts'
import './components/file.ts'
import './components/check.ts'
import './components/gate.ts'
import './components/conversation.ts'
import './components/intent.ts'
import './components/projects.ts'
import './components/reviews.ts'
import './components/saved-prompts.ts'
import './components/reviewer-agents.ts'
import './components/packs.ts'
import { when } from './components/push-panel.ts'
import { typeChangeOf } from './components/push-facts.ts'
import { reviewCoverage } from './components/push-coverage.ts'
import './components/session-facts.ts'
import './components/session-work.ts'
import './components/screenshots.ts'
import './components/keep.ts'
import './components/verify.ts'
import './components/review-models.ts'
import './components/forge-panel.ts'
import { pullFollowOf } from './components/pull-follow.ts'
import './components/tabs.ts'
import './components/toasts.ts'
import type { Toast } from './components/toasts.ts'
import { money } from './components/consent.ts'
import { openAway, setHalt, setHaltSession } from './components/halt.ts'
import './components/rules.ts'
import { notify, onNotifyOpen, watchNotify } from './components/notify.ts'
import './components/recap.ts'
import { openWorkspace } from './components/workspace.ts'
import { openExceptions } from './components/exceptions.ts'
import { loadLimits, setLimits, setLimitsSession } from './components/usage.ts'
import { agentName, setBranchStarters } from './components/lineage.ts'
import { confirmBoth } from './components/compare.ts'
import './components/split.ts'
import { openBackup, stateNotices } from './components/backup.ts'
import { openDiagnostics } from './components/diagnostics.ts'
import { initUpdates } from './components/update.ts'
import { sendState } from './api.ts'
import { allowed, armed, changesSession, current, inTurn, offWhy, pickingSize, say, schedulePaint, sessions, setPainter, state, toast, waitingOf, working, type AuditEntry, type HistoryLine, type Session } from './app-state.ts'
import { barStatus, paintSide, queueBarFit, startLayout, startTopBar } from './layout.ts'
import { fileLabel, files, keepTab, moveTab, openTabs, recent, reloadFile, restoreTabs, saveTabs, startNavigation } from './navigation.ts'
import { ensure, requestReplay, showGone, startStream, updateSessions } from './stream.ts'
import { answerAsking, busyManage, confirmed, controlBusy, controlBusyWhile, explainAsking, forgeAsking, forkable, once, planAsking, pushAsking, pushedHead, pushOutcome, pushSaid, removals, reviewAsking, startSessionActions, stepping, testAsking, waiting, whilePressed } from './session-actions.ts'
import { startPalette } from './palette-items.ts'

setPainter(paint)


function checkOf(r: Reviewer): Check {
  return { type: r.type, name: r.name, why: r.why ?? '' }
}
const RESET_NOTE = 'The session restarted, so what came before this point is shown only as far as the agent kept it.'

const sessionWork = byId<HTMLElementTagNameMap['sf-session-work']>('session-work')
const keepPanel = byId<HTMLElementTagNameMap['sf-keep']>('keep')
const verifyPanel = byId<HTMLElementTagNameMap['sf-verify']>('verify')
const reviewModels = el('sf-review-models')
reviewModels.hidden = true
byId('reviewer-agents').after(reviewModels)
const modelMenu = button('Switch model…', '')
modelMenu.setAttribute('role', 'menuitem')
modelMenu.hidden = true
byId('menu-wake').after(modelMenu)
let reviewer: ReviewerView | null = null
let starterNames = new Map<string, string>()
let projectsAt = 0

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

function stageOff(s: Session): string {
  if (!state.connected) return 'Step buttons are available once the page reconnects to the engine.'
  return offWhy(s, 'steps')
}

function onEvent(ev: StreamEvent) {
  if ((ev as { type?: unknown }).type === 'halt') return setHalt(ev)
  if ((ev as { type?: unknown }).type === 'session-work') return void sessionWork.refresh(String((ev as { sid?: unknown }).sid ?? ''))
  if ((ev as { type?: unknown }).type === 'keep') return void keepPanel.refresh(String((ev as { sid?: unknown }).sid ?? ''))
  if ((ev as { type?: unknown }).type === 'verify' && typeof (ev as { repo?: unknown }).repo === 'string') return void byId<HTMLElementTagNameMap['sf-projects']>('projects').verifiedOnMain(String((ev as { repo?: unknown }).repo))
  if ((ev as { type?: unknown }).type === 'verify') return void verifyPanel.refresh(String((ev as { sid?: unknown }).sid ?? ''))
  if ((ev as { type?: unknown }).type === 'limits') return setLimits(ev)
  if (ev.type === 'supervisor') {
    state.supervisor = typeof ev.state === 'string' ? ev.state : ''
    return schedulePaint()
  }
  if (ev.type === 'hello') {
    if ('sid' in ev) return
    state.stream = ev.stream
    state.imageTypes = Array.isArray(ev.imageTypes) ? ev.imageTypes.filter((t): t is string => typeof t === 'string') : []
    projectsAt = 0
    void markSignIn()
    void markIdentity()
    void stateNotices(api, byId('state-notices'))
    void loadLimits()
    updateSessions(ev.sessions)
    for (const s of sessions.values()) requestReplay(s, true)
    pruneTabs()
    return
  }
  if (ev.type === 'sessions') {
    if ('sid' in ev) return
    return updateSessions(ev.sessions)
  }
  if ((ev as { type?: unknown }).type === 'notify') {
    const n = ev as { sid?: unknown; what?: unknown; key?: unknown }
    const known = typeof n.sid === 'string' ? sessions.get(n.sid) : undefined
    if (known && typeof n.what === 'string' && typeof n.key === 'string') notify(known.sid, label(known.info), n.what, n.key)
    return
  }
  if (ev.type === 'reviewer') {
    if (typeof ev.method === 'string' && typeof ev.words === 'string') {
      reviewer = { expect: reviewer?.expect ?? 'any', signIn: { method: ev.method, words: ev.words, endpoint: typeof ev.endpoint === 'string' ? ev.endpoint : undefined, costs: ev.costs === true, checkedAt: ev.checkedAt, stale: ev.stale === true } }
      schedulePaint()
      void loadProjects(true)
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
  if (ev.type === 'versions') {
    const known = sessions.get(ev.sid)
    toast({ id: `versions:${ev.sid}`, title: `${known ? visible(label(known.info)) : 'A session'} woke with a note on what it runs.`, description: visible(ev.text), tone: 'info', timeout: 0 })
    return
  }
  if (ev.type === 'worktree-aside') {
    const known = sessions.get(ev.sid)
    toast({ id: `aside:${ev.sid}`, title: `${known ? visible(label(known.info)) : 'A session'}'s worktree was moved aside.`, description: visible(ev.text), tone: 'warn', timeout: 0 })
    return
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
    Object.assign(s, { epoch: ev.epoch, seq: 0, replayAt: 0, rebase: true, gate: null, gateKey: '', workflow: null, planfile: null, agents: [], intent: null, busy: false, compacting: '', context: null, asked: {}, refused: {} })
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

const SAVED_KEY = 'sf-checkpoint-dismissed'
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

function paint() {
  const s = current()
  setHaltSession(s && !s.info.doubled ? { sid: s.sid, name: label(s.info), own: !!s.info.verified } : null)
  setLimitsSession(s?.info.verified && !s.info.doubled ? s.sid : '')
  if (current.key.startsWith('s/')) keepTab(current.key)
  const list = [...sessions.values()]
  byId<HTMLElementTagNameMap['sf-overview']>('overview').supervisor = state.supervisor
  const listed = list.map(x => ({ ...x.info, busy: working(x), waiting: waitingOf(x) }))
  byId<HTMLElementTagNameMap['sf-overview']>('overview').sessions = listed
  byId('home').hidden = !!s
  byId('welcome').hidden = current.key !== 'overview'
  byId('projects').hidden = current.key !== 'projects'
  byId('docs').hidden = current.key !== 'docs'
  byId('file').hidden = !current.key.startsWith('f:')
  paintChangesTab()
  document.body.classList.toggle('editor', current.key.startsWith('f:') || current.key.startsWith('c:'))
  document.body.dataset.view = current.key === 'projects' || current.key === 'docs' ? current.key : ''
  byId<HTMLElementTagNameMap['sf-file']>('file').view = files.get(current.key) ?? null
  const sessionTab = (k: string) => k.startsWith('s/') && openTabs.includes(k)
  byId<HTMLElementTagNameMap['sf-docs']>('docs').session = recent.find(sessionTab) ?? [...openTabs].reverse().find(sessionTab) ?? ''
  if (current.key === 'docs') void loadDocs()
  for (const a of document.querySelectorAll<HTMLElement>('.activity a[data-view]')) {
    if (a.dataset.view === current.key) a.setAttribute('aria-current', 'page')
    else a.removeAttribute('aria-current')
  }
  byId<HTMLElementTagNameMap['sf-projects']>('projects').sessions = listed
  void loadProjects()
  byId('live-view').hidden = !s
  byId('composer').hidden = !s
  composer.paint(s, !!s && waitingOf(s).length > 0, s?.info.readOnly ?? '')
  const picking = !!s && pickingSize(s)
  byId('size-pick').hidden = !picking
  if (picking) paintTeamPicks(s)
  byId('composer').classList.toggle('picking', picking)
  if (document.body.classList.contains('live') !== !!s) {
    document.body.classList.toggle('live', !!s)
    queueBarFit()
  }
  for (const x of list) x.view.away = x !== s
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
      if (k.startsWith('c:')) {
        const c = sessions.get(k.slice(2))
        return { id: k, label: `Changes: ${c ? visible(label(c.info)) : 'session'}`, title: c?.info.cwd || '', closable: true, need: false }
      }
      const x = sessions.get(k.slice(2))
      const twin = x ? sessionTwin(x) : { description: '', short: '' }
      return { id: k, label: x ? visible(label(x.info)) : 'Session', description: visible(twin.description), short: visible(twin.short), title: x?.info.cwd || '', closable: true, need: !!x && !!waitingOf(x).length }
    }),
    active: current.key,
  }
  if (s) {
    barStatus(state.connected ? [shown(s.info.summary?.word ?? ''), s.info.mode ? `${s.info.modeLabel || s.info.mode} mode` : 'mode unknown', s.sid.slice(0, 8)].join(' · ') : state.note)
    byId('title').title = s.info.cwd || ''
    paintCrumbs(s)
    byId('usage').textContent = usageText(s)
    if (s.info.signIn?.method === 'gateway') byId('usage').textContent += ` · ${GATEWAY_WARNING}`
    byId('stop').hidden = !(state.connected && allowed(s, 'stop'))
    byId('working').hidden = !working(s) || !!waitingOf(s).length
    if (byId('strip-note').textContent !== s.compacting) byId('strip-note').textContent = s.compacting
    paintControl(s)
    paintManage(s)
    byId('session-foot').hidden = !byId('foot-text').textContent && byId('manage').hidden && byId('session-more').hidden
    const notes = state.connected ? s.info.warnings ?? [] : []
    byId('warnings').replaceChildren(...notes.map(t => el('p', '', visible(t))))
    byId('warnings').hidden = !notes.length
    s.view.marks(s.info.unbacked, s.epoch)
    paintCheckpoint(s)
    paintStall(s)
    const off = stageOff(s)
    if (inTurn(s)) for (const k of [...stepping.keys()]) if (k.startsWith(`${s.sid}:`)) stepping.delete(k)
    for (const b of byId('path').querySelectorAll<HTMLButtonElement>('button.stage')) {
      const stage = b.closest<HTMLElement>('li[data-stage]')?.dataset.stage
      const hold = s.gate?.path?.stages.find(x => x.id === stage)?.actions.find(x => x.ask === b.dataset.ask)?.hold ?? ''
      b.setAttribute('aria-disabled', String(!!off || !!hold || b.getAttribute('aria-busy') === 'true'))
      if (off) b.setAttribute('aria-describedby', 'path-busy')
      else if (hold) b.setAttribute('aria-describedby', `${stage}-detail`)
      else b.removeAttribute('aria-describedby')
    }
    if (byId('path-busy').textContent !== off) byId('path-busy').textContent = off
    byId('path-busy').hidden = !off
    paintAgents(s)
    paintChecks(s)
    paintSide(s)
    byId<HTMLElementTagNameMap['sf-reviews']>('reviews').view = reviewsOf(s)
    byId<HTMLElementTagNameMap['sf-reviews']>('reviews').explain = { run: s.info.review?.explain ?? null, asking: explainAsking.has(s.sid) }
    byId<HTMLElementTagNameMap['sf-push-panel']>('push-panel').view = withCoverage(pushPanelOf(s), s)
    verifyPanel.view = state.connected && s.info.verified ? { sid: s.sid, disabled: '' } : null
    byId<HTMLElementTagNameMap['sf-session-facts']>('session-facts').view = state.connected ? { uncommitted: s.info.uncommitted ?? null, overlaps: s.info.overlaps ?? [], disabled: stageOff(s) } : null
    byId<HTMLElementTagNameMap['sf-lineage']>('lineage').view = state.connected ? { info: s.info, disabled: stageOff(s) } : null
    sessionWork.view = state.connected ? { sid: s.sid, disabled: !allowed(s, 'control'), reason: s.info.readOnly ?? '' } : null
    byId<HTMLElementTagNameMap['sf-screenshots']>('screenshots').view = state.connected ? { sid: s.sid, busy: inTurn(s) } : null
    keepPanel.view = state.connected ? { sid: s.sid, disabled: offWhy(s, 'restore') } : null
    byId<HTMLElementTagNameMap['sf-forge-panel']>('forge-panel').view = forgePanelOf(s)
    byId<HTMLElementTagNameMap['sf-pull-follow']>('pull-follow').view = pullFollowOf(s.sid, s.info, state.connected)
    byId<HTMLElementTagNameMap['sf-split']>('split').view = state.connected ? { sid: s.sid, info: s.info, sessions: [...sessions.values()].map(x => x.info), disabled: stageOff(s) } : null
    byId<HTMLElementTagNameMap['sf-compare']>('compare').view = state.connected ? { sid: s.sid, info: s.info, sessions: [...sessions.values()].map(x => ({ sid: x.sid, info: x.info, cost: x.cost, total: x.total })), disabled: stageOff(s) } : null
    byId<HTMLElementTagNameMap['sf-intent']>('intent').record = s.gate?.adapter === 'knackpack' ? s.intent : null
    const rulesProject = projectFor(s)
    byId<HTMLElementTagNameMap['sf-rules']>('rules').view = state.connected && s.info.verified ? { sid: s.sid, name: label(s.info), claude: isClaude(s.info), repo: rulesProject?.name ?? s.info.project ?? '', path: rulesProject?.path ?? '', box: s.info.box ?? '' } : null
    paintChangesCard(s)
    void loadChanges(s)
    void loadGate(s)
  } else {
    byId('changes-card').hidden = true
    byId<HTMLElementTagNameMap['sf-rules']>('rules').view = null
    byId<HTMLElementTagNameMap['sf-reviews']>('reviews').view = null
    byId<HTMLElementTagNameMap['sf-push-panel']>('push-panel').view = null
    verifyPanel.view = null
    byId<HTMLElementTagNameMap['sf-session-facts']>('session-facts').view = null
    byId<HTMLElementTagNameMap['sf-lineage']>('lineage').view = null
    sessionWork.view = null
    byId<HTMLElementTagNameMap['sf-screenshots']>('screenshots').view = null
    keepPanel.view = null
    byId<HTMLElementTagNameMap['sf-forge-panel']>('forge-panel').view = null
    byId<HTMLElementTagNameMap['sf-pull-follow']>('pull-follow').view = null
    byId<HTMLElementTagNameMap['sf-split']>('split').view = null
    byId<HTMLElementTagNameMap['sf-compare']>('compare').view = null
    barStatus(state.connected ? '' : state.note)
    byId('usage').textContent = ''
    byId('stop').hidden = true
    byId('working').hidden = true
    paintSide(null)
  }
  watchNotify(list.map(x => ({ sid: x.sid, name: label(x.info), cards: waitingOf(x).map(c => c.id).filter(id => typeof id === 'string'), pushHeld: x.info.push?.result === 'held' && !x.info.push.running ? x.info.push.at || 0 : 0 })))
  byId<HTMLElementTagNameMap['sf-recap']>('recap').show(state.connected && s?.info.verified ? s.sid : '', !document.hidden, !!s?.info.doubled)
  document.title = needs ? 'Needs you · Software Factory' : 'Software Factory'
  saveTabs()
}

function paintChangesCard(s: Session) {
  const c = s.changes
  byId('changes-card').hidden = !c
  if (!c) return
  const more = c.more ? ' or more' : ''
  const viewed = c.files.filter(f => f.viewed).length
  byId('changes-summary').textContent = `${c.commits.length}${more} ${plural(c.commits.length, 'commit')}, ${c.files.length}${more} ${plural(c.files.length, 'file')} changed${c.files.length ? `, ${viewed} viewed` : ''}.`
}

function paintChangesTab() {
  const view = byId<HTMLElementTagNameMap['sf-changes']>('changes')
  const s = current.key.startsWith('c:') ? sessions.get(current.key.slice(2)) ?? null : null
  if (!s) {
    view.changes = null
    return
  }
  view.forkable = forkable(s)
  view.readOnly = s.info.readOnly ?? ''
  view.changes = s.changes
  void loadChanges(s)
}

function paintControl(s: Session) {
  const i = s.info
  const button = byId<HTMLButtonElement>('control-button')
  const c = i.control ?? { text: '', action: '' }
  const text = visible(c.confirmText && armed.has(`${s.sid}:control`) ? c.confirmText : c.text)
  const action = c.action
  byId('control').classList.toggle('alert', !!c.alert)
  const commands = allowed(s, 'copy') ? i.commands : undefined
  const menu = byId('session-menu')
  for (const b of menu.querySelectorAll<HTMLButtonElement>('button[data-copy]')) {
    const command = b.dataset.copy === 'attach' ? commands?.attach : commands?.logs
    b.textContent = command ? `Copy ${command}` : ''
    b.hidden = !command
  }
  const notice = !!action && action !== 'read-only'
  byId('menu-readonly').hidden = action !== 'read-only'
  byId('menu-wake').hidden = !allowed(s, 'wake') || action === 'wake'
  paintBranchMenu(s)
  byId('session-more').hidden = !commands?.attach && action !== 'read-only'
  modelMenu.hidden = !allowed(s, 'model')
  if (!modelMenu.hidden) byId('session-more').hidden = false
  if (byId('session-more').hidden && menu.matches(':popover-open')) menu.hidePopover()
  byId('foot-text').textContent = notice ? '' : text
  byId('foot-text').classList.toggle('bad', c.tone === 'bad')
  byId('control-text').textContent = text
  button.dataset.action = action === 'wake' || action === 'release' ? action : 'control'
  busy(button, controlBusy.has(s.sid))
  const labels = [c.label ?? '', c.confirm ?? '']
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
  const managed = allowed(s, 'manage') && state.connected
  box.hidden = !managed
  if (!managed) return
  const removal = removals.get(s.sid)
  const button = byId<HTMLButtonElement>('manage-button')
  const force = byId<HTMLButtonElement>('force-button')
  const stop = !!i.running
  let pending = busyManage.get(s.sid)
  if (pending && (Date.now() - pending.at > 30000 || (pending.action === 'stop' && !stop))) {
    busyManage.delete(s.sid)
    pending = undefined
  }
  button.dataset.action = stop ? 'stop' : 'remove'
  const labels = stop ? ['Stop session', 'Confirm stop'] : ['Delete session', 'Confirm delete']
  const shown = armed.has(`${s.sid}:${button.dataset.action}`) ? 1 : 0
  busy(button, !!pending && !pending.force)
  busy(force, !!pending?.force)
  ;[...button.children].forEach((span, i) => {
    span.textContent = labels[i] ?? ''
    span.classList.toggle('off', i !== shown)
  })
  button.classList.toggle('danger', shown === 1)
  force.classList.toggle('danger', armed.has(`${s.sid}:force`))
  button.disabled = !!pending?.force
  force.disabled = !!pending && !pending.force
  const note = shown === 1 ? (stop ? 'Press Confirm stop to stop this session. It keeps the conversation; Wake brings it back.' : 'Press Confirm delete to delete this session and its worktree.') : armed.has(`${s.sid}:force`) && removal?.offer ? `Press the button again to ${removal.offer.flag === 'discard' ? 'discard' : 'remove the folder of'} ${removal.offer.value}.` : removal?.message || ''
  if (note) {
    byId('foot-text').textContent = note
    byId('foot-text').classList.remove('bad')
  }
  button.title = stop ? 'Stopping keeps the conversation; Wake brings it back.' : 'Deleting removes the session and its worktree. Claude Code refuses if the worktree has unpushed commits.'
  const offer = !stop && removal?.offer
  force.hidden = !offer
  byId('manage-cancel').hidden = !!pending || (shown !== 1 && !(offer && armed.has(`${s.sid}:force`)))
  if (offer && !pending?.force) force.textContent = armed.has(`${s.sid}:force`) ?`Confirm: ${offer.flag === 'discard' ? 'discard' : 'remove the folder of'} ${offer.value}` : offer.flag === 'discard' ? `Discard the unpushed work (${offer.value}) and delete` : `Delete the worktree folder anyway (${offer.value})`
}

function paintCheckpoint(s: Session) {
  const c = s.context
  const shown = !!c && !s.info.checkpointSaved && checkpointed.get(s.sid) !== c.level
  if (c && shown && byId('checkpoint').hidden) byId('checkpoint-said').textContent = c.said
  byId('checkpoint').hidden = !shown
  byId('checkpoint').classList.toggle('alert', !!c?.stop)
  if (!c) return
  byId('checkpoint-text').textContent = c.text
  ;(byId('checkpoint-button') as HTMLButtonElement).disabled = !state.connected || !allowed(s, 'checkpoint')
}

const stallDismissed = new Map<string, number>()

function paintStall(s: Session) {
  const at = s.info.stalledSince ?? 0
  const shown = state.connected && !!s.info.stalled && at > 0 && stallDismissed.get(s.sid) !== at
  const minutes = Math.floor((Date.now() - (at || Date.now())) / 60000)
  const since = at ? new Date(at).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' }) : ''
  if (shown && byId('stall').hidden) byId('stall-said').textContent = `No report from the session for ${minutes} ${minutes === 1 ? 'minute' : 'minutes'}.`
  byId('stall').hidden = !shown
  if (!shown) return
  const attach = allowed(s, 'copy') ? s.info.commands?.attach ?? '' : ''
  byId('stall-text').textContent = `${agentWord(s.info.agent)} is in its turn and hasn't reported anything for ${minutes} ${minutes === 1 ? 'minute' : 'minutes'}, since ${since}. This rests on the session's own reports, so it's only a hint: it may still be working, such as on a long command. ${attach ? 'Attach to look, or stop the turn.' : 'Stop the turn if it seems stuck.'}`
  byId('stall-copy').hidden = !attach
  byId('stall-copy').dataset.command = attach
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

function reviewsOf(s: Session): ReviewsView | null {
  const i = s.info
  if (!allowed(s, 'review')) return null
  const review = i.review && Array.isArray(i.review.roles) && i.review.commit ? i.review : null
  const gate = s.gate
  const offer = gate?.review
  const disabled = !state.connected ? 'Reviews start once the page is connected again.' : offer ? offer.reason : i.readOnly ?? ''
  return { sid: s.sid, review, disabled, readOnly: i.readOnly ?? '', asking: reviewAsking.has(s.sid), pushing: pushAsking.has(s.sid) || !!i.push?.running, pushed: pushedHead(s), pushSaid: pushSaid.get(s.sid) ?? null, reviewer, planned: Array.isArray(offer?.planned) ? offer.planned : [], gate: gate?.reviewers ?? [], allowed: gate?.allowed, missing: gate?.missing ?? [], head: gate?.head ?? '', testList: i.review?.testList ?? null, testAsking: testAsking.has(s.sid),
    linked: findingLinks(s.sid, review), flow: gate?.flow ?? null, catalog: gate?.catalog ? { state: gate.catalog, why: typeof gate.catalogWhy === 'string' ? gate.catalogWhy : '' } : undefined }
}

const findingChecks = new Map<string, { key: string; found: string[]; done: boolean }>()
const COMMIT = /^[0-9a-f]{40}$/

function findingLinks(sid: string, review: SessionReview | null): { commit: string; paths: string[] } | undefined {
  if (!review || review.running || !COMMIT.test(review.commit)) return undefined
  const paths = [...new Set(review.roles.flatMap(r => (Array.isArray(r.findings) ? r.findings : []).map(f => f?.path).filter((p): p is string => typeof p === 'string' && repoPath(p))))].slice(0, 20)
  if (!paths.length) return undefined
  const key = `${review.commit}\n${paths.join('\n')}`
  const had = findingChecks.get(sid)
  if (had?.key === key) return had.done ? { commit: review.commit, paths: had.found } : undefined
  const now = { key, found: [] as string[], done: false }
  findingChecks.set(sid, now)
  void api('/api/file-check', { sid, commit: review.commit, paths }).catch(() => null).then(reply).then(({ ok, body }) => {
    if (findingChecks.get(sid) !== now) return
    now.done = true
    if (ok && Array.isArray(body.found)) now.found = body.found.filter((p): p is string => typeof p === 'string' && paths.includes(p))
    else setTimeout(() => findingChecks.get(sid) === now && findingChecks.delete(sid), RECHECK_MS)
    paint()
  })
  return undefined
}

function pushPanelOf(s: Session): PushPanelView | null {
  const i = s.info
  if (!i.verified || !i.project) return null
  const push = i.push && Array.isArray(i.push.missing) ? i.push : null
  const gate = state.connected && s.gate?.head ? s.gate : null
  pushOutcome(s)
  return { sid: s.sid, push, disabled: i.readOnly || (state.connected ? '' : 'Answers go through once the page is connected again.'), answering: answerAsking.has(s.sid), flagged: gate?.flagged ?? [], flagsRead: gate ? gate.flagsRead !== false : true, checks: i.checks ?? [], typeChange: gate && push && gate.head === push.commit ? typeChangeOf(gate.typeChange) : null }
}

function withCoverage(view: PushPanelView | null, s: Session): PushPanelView | null {
  const commit = view?.push?.commit ?? ''
  if (!view || !commit) return view
  const coverage = reviewCoverage(s.info.review, commit)
  return coverage.length ? { ...view, coverage } : view
}

function forgePanelOf(s: Session): ForgePanelView | null {
  const i = s.info
  if (!i.verified || !Array.isArray(i.forge) || !i.forge.length) return null
  return { forge: i.forge, disabled: i.readOnly || (state.connected ? '' : 'Answers go through once the page is connected again.'), answering: forgeAsking.has(s.sid) }
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
  const disabled = !state.connected || !allowed(s, 'control')
  const required = (gate?.reviewers ?? []).filter(r => gate?.path?.required.includes(r.type))
  for (const { check, node } of syncChecks(required)) {
    const r = gate?.reviewers?.find(x => x.type === check.type) ?? null
    node.status = { gate: r, sid: s.sid }
  }
  const turn = { busy: state.connected && inTurn(s) }
  paintPath(byId('path'), gate?.path ?? null, byId('path-card'))
  byId('path-section').hidden = !s.gate || pickingSize(s)
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
  paintTeam(s, gate)
  size.disabled = disabled || !gate
  byId('size-note').textContent = gate?.sizeNote ?? ''
  byId('size-note').hidden = !gate?.sizeNote
  const doc = gate?.planDoc
  const plan = doc && gate?.effective === 'feature' ? doc : null
  byId<HTMLElementTagNameMap['sf-approval']>('approval').view = plan && { note: plan.note ?? '', path: plan.path, text: plan.text, hash: plan.hash, approved: !!plan.approved, changed: !!plan.changed, draft: turn.busy && !plan.approved, disabled: disabled || waiting(s.asked.plan, plan.hash), hold: gate?.holds?.approvePlan ?? '', refused: s.refused.plan?.hash === plan.hash ? s.refused.plan.text : '', commit: plan.commit ?? '', approvedAt: plan.approvedAt ?? '', reported: !!plan.reported, onBase: !!plan.onBase, status: plan.status ?? '' }
  const cause = doc && gate?.effective === 'incident' ? doc : null
  byId<HTMLElementTagNameMap['sf-approval']>('cause-approval').view = cause && { kind: 'root-cause', note: cause.note ?? '', path: cause.path, text: cause.text, hash: cause.hash, approved: !!cause.approved, changed: !!cause.changed, draft: turn.busy && !cause.approved, disabled: disabled || waiting(s.asked.plan, cause.hash), hold: gate?.holds?.approvePlan ?? '', refused: s.refused.plan?.hash === cause.hash ? s.refused.plan.text : '', commit: cause.commit ?? '', approvedAt: cause.approvedAt ?? '', reported: !!cause.reported, onBase: !!cause.onBase, status: cause.status ?? '' }
  byId<HTMLElementTagNameMap['sf-approval']>('approval').planReview = plan ? { run: s.info.review?.planReview ?? null, asking: planAsking.has(s.sid) } : null
  const reqs = gate?.size === 'feature' && gate.requirements?.hash ? gate.requirements : null
  byId<HTMLElementTagNameMap['sf-approval']>('req-approval').view = reqs && { kind: 'requirements', note: reqs.note ?? '', path: reqs.path, text: reqs.text, hash: reqs.hash, approved: !!reqs.approved, changed: !!reqs.changed, draft: turn.busy && !reqs.approved, disabled: disabled || waiting(s.asked.requirements, reqs.hash), hold: gate?.holds?.approveRequirements ?? '', refused: s.refused.requirements?.hash === reqs.hash ? s.refused.requirements.text : '', previous: reqs.previous ?? '', commit: reqs.commit ?? '', approvedAt: reqs.approvedAt ?? '', reported: !!reqs.reported, onBase: !!reqs.onBase, status: reqs.status ?? '' }
  paintExceptions(s)
  const project = projectFor(s)
  byId<HTMLElementTagNameMap['sf-project-agents']>('project-agents').view = gate ? { project: project?.path ?? '', rounds: project?.rounds ?? 0, roundsMax: project?.roundsMax ?? 0, prComment: !!project?.prComment, type: project?.type ?? '', types: types.map((t): [string, string] => [t.id, t.name]), autoFindings: project?.autoFindings ?? false, disabled, hold: project?.hold } : null
  byId<HTMLElementTagNameMap['sf-packs']>('packs').view = gate && project?.path ? { project: project.path, disabled } : null
  byId<HTMLElementTagNameMap['sf-reviewer-agents']>('reviewer-agents').view = gate && project?.path ? { project: project.path, disabled } : null
  reviewModels.view = gate && project?.path ? { project: project.path, disabled } : null
}

function projectFor(s: Session): Project | null {
  return projectList.find(p => p.name === s.info.project) ?? null
}

async function loadGate(s: Session, force = false) {
  const review = s.info.review
  const key = `${working(s)}:${review?.commit ?? ''}:${review?.running ?? ''}:${(review?.roles ?? []).map(r => r.state).join(',')}:${s.info.push?.at ?? ''}:${s.workflowSeq}:${s.info.running ?? ''}:${s.info.step ?? ''}:${waitingOf(s).length > 0}:${s.info.actions?.steps ?? ''}:${s.agents.map(a => a.status).join(',')}`
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

function setupLines(v: TeamView): HTMLElement[] {
  const setup = v.setup
  if (!setup) return []
  const own = setup.types.filter(t => !t.builtin)
  const items = [
    `Workflow: ${setup.adapter === 'knackpack' ? 'knackpack' : 'the built-in steps'}`,
    `Bug hunter: ${visible(setup.roles['bug-hunter'] ?? '')}; simplicity checker: ${visible(setup.roles.simplicity ?? '')}`,
    ...own.map(t => `Work type ${visible(t.name)}: ${t.steps.join(', ')}; ${t.push ? 'pushes' : "doesn't push"}; edits ${t.edits}${t.roles.length ? `; needs ${t.roles.join(' and ')}` : ''}`),
    ...(setup.defaultType ? [`Default work type: ${visible(setup.defaultType)}`] : []),
    ...(setup.agents.length ? [`Reviewers added: ${setup.agents.map(a => visible(a)).join(', ')}`] : []),
    ...setup.fileRules.map(f => [f.agent ? `Files ${visible(f.pattern)} need ${visible(f.agent)}` : '', f.instruction ? `Reviewers of files ${visible(f.pattern)} get the team's rule: ${visible(f.instruction)}` : ''].filter(Boolean).join('; ')),
    ...(setup.protect?.length ? [`Claude Code asks you before writing: ${setup.protect.map(p => visible(p)).join(', ')}`] : []),
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
  await whileBusy(byId<HTMLButtonElement>('team-open'), () => teamOp('read'))
}

function paintExceptions(s: Session) {
  const gate = s.gate
  const key = gate ? `${s.sid}:${gate.branch}:${gate.head}:${s.auditSeen}` : ''
  if (key && s.audit?.key !== key && state.connected && (allowed(s, 'control') || !!s.info.doubled)) {
    s.audit = { key, entries: s.audit?.entries ?? [], refusals: s.audit?.refusals ?? {}, history: s.audit?.history ?? [], friction: s.audit?.friction ?? [] }
    void api('/api/audit', { sid: s.sid, branch: gate?.branch ?? '' }).then(r => (r.ok ? r.json() : null)).then((v: { entries?: AuditEntry[]; refusals?: Record<string, number>; history?: unknown[]; friction?: unknown[] } | null) => {
      if (!v || s.audit?.key !== key) return
      s.audit = { key, entries: Array.isArray(v.entries) ? v.entries : [], refusals: v.refusals && typeof v.refusals === 'object' ? v.refusals : {}, history: Array.isArray(v.history) ? v.history.filter(isHistoryLine) : [], friction: Array.isArray(v.friction) ? v.friction.filter((t): t is string => typeof t === 'string') : [] }
      if (current() === s) paint()
    }, () => undefined)
  }
  const box = byId('exceptions')
  const items = (s.audit?.history ?? []).map(e => {
    const li = el('li', e.tone)
    const at = el('time', 'history-time', e.time ? when(e.time) : '')
    if (e.time) at.dateTime = new Date(e.time).toISOString()
    li.append(at, visible(e.text))
    return li
  })
  const tips = (s.audit?.friction ?? []).map(t => el('p', '', visible(t)))
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

function isHistoryLine(v: unknown): v is HistoryLine {
  return isRecord(v) && typeof v.text === 'string' && typeof v.tone === 'string' && typeof v.time === 'number'
}

function labelOfSid(sid: string): string {
  const x = sessions.get(sid)
  return x ? label(x.info) : ''
}

function projectOf(info: SessionInfo): Project | null {
  if (info.project) return projectList.find(p => p.name === info.project) ?? null
  const cwd = info.cwd || ''
  return projectList.filter(p => cwd === p.path || cwd.startsWith(`${p.path}/`)).sort((a, b) => b.path.length - a.path.length)[0] ?? null
}


let projectPaths: string[] = []
let projectList: Project[] = []
let commitIdentity: CommitIdentity | null | undefined

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
  const here = el('li')
  here.append(el('h1', '', visible(label(s.info))))
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
  if (body) byId('helper-notice').hidden = body.helper !== 'unreachable'
  if (body) {
    reviewer = body.reviewer ?? null
    starterNames = new Map((body.starters ?? []).map(a => [a.id, a.name]))
    setAgentNames(body.starters ?? [])
    setBranchStarters(body.starters)
    schedulePaint()
  }
  if (body) projectPaths = body.projects.map(p => p.path)
  if (body) projectList = body.projects.map(p => ({ ...p, agents: Array.isArray(p.agents) ? p.agents.filter(a => typeof a === 'string') : [] }))
}

function twinWords(i: SessionInfo): string {
  return i.agent ? agentName(i.agent) : ''
}

function sessionTwin(x: Session): { description: string; short: string } {
  const name = label(x.info)
  const twins = openTabs.flatMap(k => {
    const y = k.startsWith('s/') && k !== `s/${x.sid}` ? sessions.get(k.slice(2)) : undefined
    return y && label(y.info) === name ? [y] : []
  })
  if (!twins.length) return { description: '', short: '' }
  const words = twinWords(x.info)
  const sid = x.sid.slice(0, 8)
  if (twins.some(y => twinWords(y.info) === words)) return { description: [words, sid].filter(Boolean).join(', '), short: sid }
  return { description: words, short: words }
}

async function reply(res: Response | null): Promise<{ ok: boolean; body: Record<string, unknown> }> {
  const body = res ? ((await res.json().catch(() => ({}))) as Record<string, unknown>) : {}
  return { ok: !!res && res.ok, body }
}

async function loadChanges(s: Session, force = false) {
  const key = `${s.gate?.head ?? ''}:${s.gate?.branch ?? ''}:${working(s)}:${s.info.review?.commit ?? ''}:${s.info.review?.running ?? ''}:${s.info.push?.at ?? ''}:${s.compare.from}:${s.compare.commit}`
  if (!state.connected || (s.changesKey === key && !force)) return
  if (!force && Date.now() - s.changesAt < 5000) return void setTimeout(schedulePaint, 5100 - (Date.now() - s.changesAt))
  s.changesAt = Date.now()
  const asked = { ...s.compare }
  const res = await api('/api/changes', asked.from === 'fork' ? { sid: s.sid } : asked.from === 'commit' ? { sid: s.sid, from: 'commit', commit: asked.commit } : { sid: s.sid, from: asked.from }).catch(() => null)
  if (asked.from !== s.compare.from || asked.commit !== s.compare.commit) return
  if (res && res.status === 409 && asked.from !== 'fork') {
    const said = (await res.json().catch(() => null)) as { message?: unknown } | null
    toast({ id: `changes:${s.sid}`, title: 'That comparison isn’t available.', description: typeof said?.message === 'string' ? visible(said.message) : 'The Changes view went back to where the branch leaves main.', tone: 'warn' })
    s.compare = { from: 'fork', commit: '' }
    s.changesKey = ''
    return loadChanges(s, true)
  }
  const body = res && res.ok ? ((await res.json().catch(() => null)) as { changes?: Changes | null } | null) : null
  if (body) s.changesKey = key
  else setTimeout(schedulePaint, 5100)
  s.changes = body?.changes ?? null
  if (changesSession() === s) schedulePaint()
}

async function markViewed(s: Session | null, d: { path: string; blob: string; viewed: boolean }) {
  if (!s) return
  const flip = (on: boolean) => {
    if (!s.changes) return
    s.changes = { ...s.changes, files: s.changes.files.map(f => (f.path === d.path && f.blob === d.blob ? { ...f, viewed: on, stale: on ? false : f.stale } : f)) }
    if (changesSession() === s) byId<HTMLElementTagNameMap['sf-changes']>('changes').changes = s.changes
  }
  flip(d.viewed)
  const { ok, body } = await reply(await api('/api/viewed', { sid: s.sid, ...d }).catch(() => null))
  if (ok) return
  flip(!d.viewed)
  toast({ id: `viewed:${s.sid}`, title: d.viewed ? 'The file wasn’t marked viewed.' : 'The viewed mark wasn’t cleared.', description: typeof body.message === 'string' ? visible(body.message) : 'Check that the engine is running.', tone: 'bad' })
}

async function markSignIn() {
  if (await signInState(api)) void loadProjects(true)
}

function openSignIn() {
  void claudeLogin(api, title => toast({ title, tone: 'ok' }), () => void markSignIn(), reviewer?.signIn ?? null)
}

function openIdentity() {
  editIdentity(api, commitIdentity ?? null, id => {
    commitIdentity = id
    toast({ id: 'identity', title: 'Saved the name and email agents commit with.', description: `${visible(id.name)} <${visible(id.email)}>`, tone: 'ok' })
    void loadProjects(true)
  })
}

function identityAction(body: Record<string, unknown>): Toast['action'] {
  return body.error === 'no commit identity' ? { label: 'Set name and email', run: openIdentity } : undefined
}

async function markIdentity() {
  commitIdentity = await identityState(api)
  schedulePaint()
}

byId('conversations').addEventListener('sf-answer', async e => {
  const sid = (e.target as Element).closest('sf-conversation')?.dataset.sid
  schedulePaint()
  const { ok, body } = sid ? await reply(await api('/api/answer', { sid, ...e.detail }).catch(() => null)) : { ok: false, body: {} as Record<string, unknown> }
  if (!ok) {
    ;(e.target as CardHost).error = typeof body.message === 'string' && body.message ? visible(body.message) : 'That answer did not go through. It may already have been answered in the session itself.'
    return schedulePaint()
  }
})
byId('conversations').addEventListener('sf-settled', () => {
  if (!byId<HTMLTextAreaElement>('text').disabled) byId('text').focus()
})
byId('packs').addEventListener('sf-packs-changed', () => {
  const s = current()
  if (s) void loadGate(s, true)
})
byId('reviews').addEventListener('sf-review', e => void startReview(current(), e.detail.all))
byId('reviews').addEventListener('sf-test-list', () => void startTestList(current()))
byId('reviews').addEventListener('sf-explain', () => {
  const s = current()
  void startAside(s, '/api/explain', explainAsking, s?.info.review?.explain?.state === 'running', 'the explanation of the change', {})
})
byId('approval').addEventListener('sf-plan-review', e => {
  const s = current()
  void startAside(s, '/api/plan-review', planAsking, s?.info.review?.planReview?.state === 'running', 'the plan review', { hash: e.detail.hash })
})
byId('reviews').addEventListener('sf-walkthrough-check', e => void checkStep(current(), e.detail))
async function setExpect(agent: string, expect: Expect) {
  const { ok, body } = await reply(await api('/api/sign-in-expect', { agent, expect }).catch(() => null))
  if (!ok) toast({ id: `expect:${agent}`, title: "What you expect wasn't saved.", description: typeof body.message === 'string' ? visible(body.message) : 'Check that the engine is running.', tone: 'bad' })
  await loadProjects(true)
}
byId('reviews').addEventListener('sf-push-request', () => void startPush(current()))
const pushPanel = () => byId<HTMLElementTagNameMap['sf-push-panel']>('push-panel')
const reasonRefused = (refuse: (message: string) => boolean) => (said: Record<string, unknown>) => said.error === 'reason' && typeof said.message === 'string' && refuse(visible(said.message))
byId('push-panel').addEventListener('sf-push-answer', e => void answerPush(current(), e.detail, reasonRefused(m => pushPanel().refuse('reason', m))))
byId('session-facts').addEventListener('sf-commit-ask', () => once(current(), 'commit', () => whilePressed(() => ask('commit'))))
byId('forge-panel').addEventListener('sf-forge-answer', e => void answerForge(current(), e.detail))
byId('pull-follow').addEventListener('sf-session-deleted', e => forgetSession(e.detail.sid))
byId('push-panel').addEventListener('sf-send-findings', e => void sessionAsk(current(), '/api/send-findings', e.detail, "The findings weren't sent.", 'The blocking findings were sent to the session. The push stays held.'))
byId('push-panel').addEventListener('sf-handoff', e => void sessionAsk(current(), '/api/handoff', e.detail, "The push wasn't handed off.", 'The push was denied, and the session was asked for a handoff.'))
byId('push-panel').addEventListener('sf-allow-file', e => void sessionAsk(current(), '/api/allow-file', e.detail, "The file wasn't allowed.", 'You allowed that version of the file. A later change to it is flagged again.', reasonRefused(m => pushPanel().refuse(`flag:${e.detail.path}`, m))))
byId('push-panel').addEventListener('sf-remove-file', e => void sessionAsk(current(), '/api/flag-remove', { path: e.detail.path }, "The message wasn't sent.", e.detail.revert ? 'The session was asked to revert the change to the file.' : 'The session was asked to remove the file from the branch.'))
byId('push-panel').addEventListener('sf-allow-commit', e => void sessionAsk(current(), '/api/allow-commit', e.detail, "The commit wasn't allowed.", 'You allowed that field of that commit. Another commit with the same text is flagged again.', reasonRefused(m => pushPanel().refuse(`flag:${e.detail.commit}:${e.detail.field}`, m))))
byId('push-panel').addEventListener('sf-rewrite-commit', e => void sessionAsk(current(), '/api/commit-rewrite', e.detail, "The message wasn't sent.", 'The session was asked to rewrite the commit.'))
byId('changes').addEventListener('sf-line-comments', e => void send('', [], changesSession(), false, [], { part: { comments: e.detail.comments }, refused: e.detail.refused }).then(ok => e.detail.done(ok), () => e.detail.done(false)))
byId('changes').addEventListener('sf-compare-point', e => {
  const s = changesSession()
  if (!s) return
  s.compare = { from: e.detail.from, commit: e.detail.from === 'commit' ? e.detail.commit : '' }
  void loadChanges(s, true)
})
byId('changes').addEventListener('sf-viewed', e => void markViewed(changesSession(), e.detail))
byId('changes-open').addEventListener('click', () => {
  const s = current()
  if (s) openChanges(s)
})
byId('reviews').addEventListener('sf-override', e => void sessionAsk(current(), '/api/override', e.detail, "The verdict wasn't overridden.", 'You overrode the verdict for this commit. Press Push to push it.', reasonRefused(m => byId<HTMLElementTagNameMap['sf-reviews']>('reviews').refuse(`override-${e.detail.agent.replace(/[^A-Za-z0-9-]/g, '-')}`, m))))
byId('path').addEventListener('click', e => {
  const b = (e.target as HTMLElement).closest<HTMLButtonElement>('button.stage')
  if (!b || b.getAttribute('aria-disabled') === 'true') return
  const step = b.dataset.ask || ''
  return once(current(), step, () => whileBusy(b, () => ask(step)).finally(schedulePaint))
})
let teamPicks = ''

function paintTeamPicks(s: Session) {
  const types = s.gate?.types ?? []
  const own = types.filter(t => !t.builtin)
  const key = JSON.stringify(types.map(t => [t.id, t.name, t.steps]))
  if (key === teamPicks) return
  teamPicks = key
  const box = byId('size-pick').querySelector('.size-options')!
  for (const b of box.querySelectorAll<HTMLButtonElement>('button[data-size]:not([data-team])')) b.hidden = !!types.length && !types.some(t => t.id === b.dataset.size)
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
  if (!b || !s || byId('size-pick').querySelector('button[aria-busy="true"]')) return
  const picked = b.dataset.size ?? ''
  const res = await whileBusy(b, () => api('/api/size', { sid: s.sid, size: picked }).catch(() => null))
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
byId('team-approve').addEventListener('click', () => void whileBusy(byId<HTMLButtonElement>('team-approve'), () => teamOp('approve')))
byId('team-forget').addEventListener('click', () => void whileBusy(byId<HTMLButtonElement>('team-forget'), () => teamOp('forget')))
byId('req-approval').addEventListener('sf-approve', e => void whilePressed(() => approve('requirements', e.detail.hash)))
byId('req-approval').addEventListener('sf-revise', e => revise('req-approval', 'requirements', e.detail.note))
byId('approval').addEventListener('sf-approve', e => void whilePressed(() => approve('plan', e.detail.hash)))
byId('cause-approval').addEventListener('sf-approve', e => void whilePressed(() => approve('plan', e.detail.hash)))
byId('cause-approval').addEventListener('sf-revise', e => revise('cause-approval', 'root-cause', e.detail.note))
byId('approval').addEventListener('sf-revise', e => revise('approval', 'plan', e.detail.note))
function ask(step: string): Promise<boolean> {
  return send('', [], current(), false, [], { part: { ask: step } })
}

function revise(id: string, kind: 'requirements' | 'plan' | 'root-cause', note: string) {
  const s = current()
  if (!s) return
  const card = byId<HTMLElementTagNameMap['sf-approval']>(id)
  once(s, `revise-${kind}:${note}`, () => whilePressed(() => send('', [], s, false, [], { part: { revise: { kind, note } }, refused: (why, error) => card.refuse(why, error) }).then(ok => {
    if (ok) card.sent(note)
    return ok
  })))
}
byId('project-agents').addEventListener('sf-project-setting', async e => {
  const res = await api('/api/project', e.detail).catch(() => null)
  if (!res || !res.ok) {
    const body = res ? await res.json().catch(() => null) as { message?: unknown; field?: unknown } | null : null
    const why = typeof body?.message === 'string' ? body.message : ''
    const agents = byId<HTMLElementTagNameMap['sf-project-agents']>('project-agents')
    if (typeof body?.field === 'string' && agents.refuse(body.field, visible(why))) return
    agents.reset()
    return void toast({ title: 'The project’s setting did not change.', description: why || 'Try again.', tone: 'bad' })
  }
  await loadProjects(true)
  paint()
})
byId('checkpoint-button').addEventListener('click', () => once(current(), 'checkpoint', () => whileBusy(byId<HTMLButtonElement>('checkpoint-button'), () => ask('checkpoint'))))
byId('stall-stop').addEventListener('click', () => {
  const s = current()
  if (s) void whileBusy(byId<HTMLButtonElement>('stall-stop'), () => api('/api/interrupt', { sid: s.sid }).catch(() => null))
})
byId('stall-copy').addEventListener('click', () => {
  const command = byId('stall-copy').dataset.command
  if (command) void copy(command)
})
byId('stall-dismiss').addEventListener('click', () => {
  const s = current()
  if (!s) return
  stallDismissed.set(s.sid, s.info.stalledSince ?? 0)
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
function usageText(s: Session): string {
  const parts: string[] = []
  if (s.total) parts.push(`${s.total.toLocaleString()} tokens`)
  const cost = money(s.cost)
  if (cost) parts.push(`≈ ${cost} (the agent's estimate)`)
  const si = s.info.signIn
  const text = parts.length ? `This session: ${parts.join(', ')}` : ''
  return si ? [text, si.method === 'not-signed-in' ? 'not signed in' : `signed in with ${visible(si.words)}`].filter(Boolean).join(' · ') : text
}
function paintBranchMenu(s: Session) {
  byId('menu-bring-main').hidden = !(state.connected && allowed(s, 'bringMain'))
  byId('menu-fork').hidden = !forkable(s)
}

byId('menu-bring-main').addEventListener('click', () => {
  const s = current()
  byId('session-menu').hidePopover()
  byId('session-more').focus()
  if (s) void bringMain(s)
})
byId('menu-fork').addEventListener('click', () => {
  const s = current()
  byId('session-menu').hidePopover()
  const p = s && projectFor(s)
  if (!s || !p) return
  go('projects')
  projects.fork(p.path, s.sid)
})
byId('changes').addEventListener('sf-fork-commit', e => {
  const s = changesSession()
  const p = s && forkable(s) && projectFor(s)
  if (!s || !p) return
  go('projects')
  projects.fork(p.path, s.sid, e.detail.sha)
})
for (const id of ['split', 'compare']) {
  byId(id).addEventListener('sf-open-session', e => {
    if (sessions.has(e.detail.sid)) go(`s/${e.detail.sid}`)
  })
}
byId<HTMLElementTagNameMap['sf-split']>('split').start = (sid, card) => {
  const goal = sessions.get(sid)
  const name = starterNames.get(goal?.info.agent || 'claude') ?? 'The agent'
  return withConsent(name, 'the session', consent => api('/api/split', { op: 'start', sid, card, ...consent }))
}
byId<HTMLElementTagNameMap['sf-compare']>('compare').next = startSecond
byId('compare').addEventListener('sf-delete-session', async e => {
  const s = sessions.get(e.detail.sid)
  if (!s) return
  const problem = await whilePressed(() => manage(s, '/api/remove-session', {}))
  if (problem) toast({ id: `delete:${s.sid}`, title: `${label(s.info)} wasn't deleted`, description: problem, tone: 'bad', timeout: 0 })
})
byId('menu-wake').addEventListener('click', () => {
  const s = current()
  byId('session-menu').hidePopover()
  byId('session-more').focus()
  if (s && allowed(s, 'wake')) void wakeSession(s)
})
modelMenu.addEventListener('click', () => {
  byId('session-menu').hidePopover()
  sessionWork.focusModel()
})
byId('session-menu').addEventListener('click', async e => {
  const b = (e.target as Element).closest<HTMLButtonElement>('button[data-copy]')
  if (!b) return
  byId('session-menu').hidePopover()
  byId('session-more').focus()
  await copy((b.textContent || '').replace(/^Copy /, ''))
})

async function copy(command: string) {
  const ok = await copyText(command)
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
  if (!s || controlBusy.has(s.sid)) return
  if (byId('control-button').dataset.action === 'release') {
    const res = await controlBusyWhile(s, () => api('/api/release', { sid: s.sid }).catch(() => null))
    toast(res && res.ok ? { title: 'Ended both connections.', description: 'The real session reconnects by itself within a few seconds. If the other one comes back too, stop the program that runs it.', tone: 'info', timeout: 10000 } : { title: 'The connections could not be ended.', description: 'Try again, or restart the session.', tone: 'bad' })
    return
  }
  if (byId('control-button').dataset.action === 'wake') return void controlBusyWhile(s, () => wakeSession(s))
  if (s.info.controlled) {
    await controlBusyWhile(s, () => api('/api/control', { sid: s.sid, on: false, confirm: true }).catch(() => null))
    return
  }
  if (!confirmed(`${s.sid}:control`)) return
  const res = await controlBusyWhile(s, () => api('/api/control', { sid: s.sid, on: true, confirm: true }).catch(() => null))
  if (!res || !res.ok) toast({ title: 'The page could not take control of this session.', tone: 'bad' })
})
byId('use-tab').addEventListener('click', async () => {
  showGone('Signing in…', '', false, true)
  await trade()
  void connect(true)
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
  const s = changesSession()
  if (s) openFile(s.sid, e.detail.path, typeof e.detail.commit === 'string' && COMMIT.test(e.detail.commit) ? e.detail.commit : '', typeof e.detail.line === 'number' && Number.isInteger(e.detail.line) && e.detail.line > 0 ? e.detail.line : 0)
})
byId('file').addEventListener('sf-reload', () => reloadFile())
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

function markFilesSoon(e: Event) {
  const view = (e.target as Element).closest<HTMLElement>('#conversations > *')
  const s = [...sessions.values()].find(x => x.view === view)
  if (!view || !s) return
  clearTimeout(scanning.get(s.sid))
  scanning.set(s.sid, setTimeout(() => markFiles(view, s.sid), 600))
}
byId('conversations').addEventListener('sf-markdown-painted', markFilesSoon)
byId('conversations').addEventListener('sf-tool-path', markFilesSoon)
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
for (const id of ['projects', 'projects-side']) byId(id).addEventListener('click', e => {
  const link = (e.target as Element).closest<HTMLElement>('button[data-doc]')
  if (link) openDocs(link.dataset.doc!, { view: 'projects', label: 'Projects' })
})
byId('welcome').addEventListener('click', e => {
  const target = e.target as Element
  const walk = target.closest<HTMLElement>('[data-doc]')
  if (walk) return openDocs(walk.dataset.doc!, { view: 'overview', label: 'Welcome' })
  const start = target.closest<HTMLElement>('[data-start]')?.dataset.start
  if (start === 'docs') openDocs('')
  if (start === 'egress') openEgress(api)
  if (start === 'workspace') openWorkspace(api, labelOfSid, forgetSession)
  if (start === 'exceptions') openExceptions(api, labelOfSid)
  if (start === 'backup') openBackup(api, sendState)
  if (start === 'diagnostics') openDiagnostics(api)
  if (start === 'session') go('projects')
})
byId('account-identity').addEventListener('click', () => {
  byId('account').hidePopover()
  openIdentity()
})
const projects = byId<HTMLElementTagNameMap['sf-projects']>('projects')
async function changeRepo(detail: { op: 'add'; url: string } | { op: 'remove'; name: string }) {
  try {
    await changeRepoNow(detail)
  } finally {
    projects.settle('repo')
  }
}
const REPO_URL_REFUSALS = new Set(['url', 'exists', 'cloning', 'removing'])
async function changeRepoNow(detail: { op: 'add'; url: string } | { op: 'remove'; name: string }) {
  const { ok, body } = detail.op === 'add' ? await reply(await api('/api/add-repo', { url: detail.url }).catch(() => null)) : await reply(await api('/api/remove-repo', { name: detail.name }).catch(() => null))
  if (!ok) {
    const problem = typeof body.message === 'string' ? visible(body.message) : "That didn't work. Try again."
    return detail.op === 'add' && typeof body.error === 'string' && REPO_URL_REFUSALS.has(body.error) ? projects.addFailed(problem) : projects.say('', problem, true)
  }
  if (detail.op === 'add') projects.added()
  projects.say('', '')
  await loadProjects(true)
}
async function changeToken(detail: { op: 'save'; host: string; token: string } | { op: 'remove'; host: string }) {
  try {
    await changeTokenNow(detail)
  } finally {
    projects.settle('token')
  }
}
async function changeTokenNow(detail: { op: 'save'; host: string; token: string } | { op: 'remove'; host: string }) {
  const { ok, body } = detail.op === 'save' ? await reply(await api('/api/save-token', { host: detail.host, token: detail.token }).catch(() => null)) : await reply(await api('/api/remove-token', { host: detail.host }).catch(() => null))
  const where = visible(detail.host.replace(/^https:\/\//, ''))
  if (!ok && detail.op === 'save' && typeof body.message === 'string' && projects.tokenFailed(visible(body.message), body.error)) return
  if (!ok) {
    return void toast({ id: `token:${detail.host}`, title: detail.op === 'save' ? `The token for ${where} wasn't saved` : `The token for ${where} wasn't removed`, description: typeof body.message === 'string' ? visible(body.message) : "That didn't work. Try again.", tone: 'bad', timeout: 0 })
  }
  const saved = isRecord(body.host) && typeof body.host.host === 'string' ? body.host.host : detail.host
  if (typeof body.warning === 'string' && body.warning) toast({ id: `token:${saved}`, title: `Token saved for ${visible(saved)}, with a warning`, description: visible(body.warning), tone: 'warn', timeout: 0 })
  else toast({ id: `token:${detail.host}`, title: detail.op === 'save' ? 'Token saved.' : 'Token removed.', description: `For ${where}.`, tone: 'ok' })
  projects.tokenDone()
  await loadProjects(true)
}
projects.addEventListener('sf-repo', e => void changeRepo(e.detail))
projects.addEventListener('sf-setup-step', e => {
  if (e.detail === 'identity') openIdentity()
  else openSignIn()
})
projects.addEventListener('sf-expect', e => void setExpect(e.detail.agent, e.detail.expect))
projects.addEventListener('sf-token', e => void changeToken(e.detail))
projects.addEventListener('sf-session-action', async e => {
  const s = sessions.get(e.detail.sid)
  const project = s && projectOf(s.info)
  if (!s || !project) {
    toast({ id: `manage:${e.detail.sid}`, title: e.detail.action === 'stop' ? "The session wasn't stopped" : "The session wasn't deleted", description: 'This page lost track of it. Reload the page and try again.', tone: 'bad', timeout: 0 })
    return
  }
  const name = label(s.info)
  projects.say(project.path, e.detail.action === 'stop' ? `Stopping ${name}…` : `Deleting ${name}…`)
  const problem = await manage(s, e.detail.action === 'stop' ? '/api/stop-session' : '/api/remove-session', {})
  projects.say(project.path, problem, !!problem)
})
function capsRefused(body: Record<string, unknown>) {
  if (typeof body.capsRefused !== 'string' || !body.capsRefused) return
  const sid = typeof body.sid === 'string' ? body.sid : ''
  toast({ id: `caps:${sid}`, title: 'The session started without its caps', description: visible(body.capsRefused), tone: 'warn', timeout: 0 })
}
function refusedField(body: Record<string, unknown>): string {
  return typeof body.field === 'string' && body.field ? body.field : typeof body.error === 'string' ? body.error : ''
}
projects.addEventListener('sf-dispatch', async e => {
  const detail = e.detail
  const { path } = detail
  projects.starting(path)
  const name = starterNames.get(detail.agent) ?? 'The agent'
  const { ok, body } = await withConsent(name, 'the session', consent => api('/api/dispatch', { ...detail, ...consent }), true)
  if (!ok) {
    projects.failed()
    const text = typeof body.message === 'string' ? body.message : "The session didn't start. Try again."
    if (typeof body.message === 'string' && projects.startRefused(path, refusedField(body), visible(text))) return
    const trust = body.error === 'not trusted'
    toast({ id: `start:${path}`, title: trust ? `Claude Code doesn't trust ${baseName(path)} yet` : `No session started in ${baseName(path)}`, description: visible(text), tone: trust ? 'warn' : 'bad', timeout: 0, action: identityAction(body) })
    return void loadProjects(true)
  }
  byId<HTMLElementTagNameMap['sf-toasts']>('toasts').close(`start:${path}`)
  projects.started(path, typeof body.id === 'string' ? body.id : '')
  capsRefused(body)
})
async function startSecond(pair: string, agent: string): Promise<string> {
  const name = starterNames.get(agent) ?? 'The agent'
  const { ok, body } = await withConsent(name, 'the session', consent => api('/api/pair', { op: 'next', pair, ...consent }), true)
  if (!ok) {
    secondNotStarted(pair, body)
    return ''
  }
  capsRefused(body)
  return typeof body.sid === 'string' ? body.sid : ''
}
function secondNotStarted(pair: string, body: Record<string, unknown>) {
  const text = typeof body.message === 'string' ? body.message : "The second session didn't start."
  toast({ id: `pair:${pair}`, title: "The second session of the pair didn't start", description: `${visible(text)} Start it from the comparison in the first session.`, tone: 'bad', timeout: 0 })
}
projects.addEventListener('sf-pair', async e => {
  const detail = e.detail
  const { path } = detail
  if (detail.double && !(await confirmBoth())) return
  projects.starting(path)
  const name = starterNames.get(detail.agents[0].agent) ?? 'The agent'
  const { ok, body } = await withConsent(name, 'the session', consent => api('/api/pair', { op: 'start', ...detail, ...consent }), true)
  if (!ok) {
    projects.failed()
    const text = typeof body.message === 'string' ? body.message : "The session didn't start. Try again."
    if (typeof body.message === 'string' && projects.startRefused(path, refusedField(body), visible(text))) return
    toast({ id: `start:${path}`, title: `No session started in ${baseName(path)}`, description: visible(text), tone: 'bad', timeout: 0, action: identityAction(body) })
    return void loadProjects(true)
  }
  byId<HTMLElementTagNameMap['sf-toasts']>('toasts').close(`start:${path}`)
  projects.started(path, typeof body.id === 'string' ? body.id : '')
  capsRefused(body)
  if (body.next !== true || typeof body.pair !== 'string') return
  const refused = body.secondRefused && typeof body.secondRefused === 'object' ? (body.secondRefused as Record<string, unknown>) : {}
  if (refused.needs) await startSecond(body.pair, detail.agents[0].agent)
  else secondNotStarted(body.pair, refused)
})
byId('tabs').addEventListener('sf-close', e => closeTab(e.detail.id))
byId('tabs').addEventListener('sf-move', e => moveTab(e.detail.id, e.detail.index))
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

startLayout({ jumpLatest: () => composer.jump('latest') })

const { send, manage, answerForge, sessionAsk, startPush, answerPush, startReview, startTestList, startAside, checkStep, approve, wakeSession, withConsent, bringMain } = startSessionActions({ reply, loadGate, starterNames: () => starterNames, identityAction, copy, forgetSession })

const composer = startComposer({ send })
const { go, closeTab, pruneTabs, openChanges, openFile, loadDocs, openDocs } = startNavigation({ composer, reply })
const { connect, applySession } = startStream({ onEvent, markCheckpoint })
startTopBar()

byId('stop').addEventListener('click', () => {
  const s = current()
  if (s) void whileBusy(byId<HTMLButtonElement>('stop'), () => api('/api/interrupt', { sid: s.sid }).catch(() => null))
})
byId('rules').addEventListener('sf-rules-open', e => {
  const s = current()
  const p = s ? projectFor(s) : null
  if (e.detail.to === 'egress') return openEgress(api)
  if (e.detail.to === 'away') return openAway(true)
  if (e.detail.to === 'exceptions') return openExceptions(api, labelOfSid)
  if (e.detail.to === 'team') {
    if (!s) return
    const focusTeam = () => {
      byId('team-row').hidden = false
      byId('team-open').focus()
    }
    if (teamShown && teamFor === s.sid) return focusTeam()
    void teamOp('read').then(focusTeam)
    return
  }
  if (!p) return void toast({ title: 'Add this project first.', description: 'Saved allow rules are listed per project, under Projects.', tone: 'bad' })
  go('projects')
  projects.showPanel(p.path, 'rules')
})

startPalette({ reviewsOf, projectList: () => projectList, projects, labelOfSid, forgetSession, openIdentity, openSignIn, navigation: { go, openChanges, openDocs }, actions: { wakeSession, startReview, startPush } })
restoreTabs()
byId<HTMLElementTagNameMap['sf-recap']>('recap').post = api
onNotifyOpen(sid => {
  if (sessions.has(sid)) go(`s/${sid}`)
})
paint()
initPrefs(body => api('/api/prefs', body).catch(() => null))
initUpdates(api)
addEventListener('pagehide', () => {
  saveTabs()
  saveCheckpoints()
})
void exchange().then(() => connect())
