import { api, events, forget, signedIn, trade } from './api.ts'
import { controllable, current, paint, say, schedulePaint, scroller, sessions, state, type Session } from './app-state.ts'
import { openTabs } from './navigation.ts'
import { byId, el } from './lib/dom.ts'
import { agentWord, attachCommand, composeCommand, isClaude } from './lib/format.ts'
import { isRecord } from './lib/json.ts'
import type { Step } from './lib/path.ts'
import { loadUpdate } from './components/update.ts'
import type { CatalogEntry, SessionEvent, SessionInfo, StreamEvent } from './types.ts'

interface StreamDeps {
  onEvent: (ev: StreamEvent) => void
  began: (s: Session) => void
  markCheckpoint: (sid: string, level: string) => void
}

interface Stream {
  connect: () => Promise<void>
  applySession: (s: Session, ev: SessionEvent) => void
}

let retry = 1000
let lost = 0

export const SIGN_IN_HINT = 'Press Sign in to use the page in this tab.'
let fresh = true

let goneNext: { head: string; detail: string } | null = null

function goneSay(head: string, detail: string) {
  for (const [id, text] of [['gone', head], ['gone-detail', detail]] as const) {
    const line = byId(id)
    if (line.textContent !== text) line.textContent = text
  }
}

export function showGone(head: string, detail: string, offerSignIn: boolean, busy = false) {
  byId('gone-progress').hidden = !busy
  byId('signin').hidden = !offerSignIn
  if (goneNext) {
    goneNext.head = head
    goneNext.detail = detail
    return
  }
  if (document.body.classList.contains('out')) return goneSay(head, detail)
  goneSay('', '')
  document.body.classList.add('out')
  const next = goneNext = { head, detail }
  requestAnimationFrame(() => requestAnimationFrame(() => {
    if (goneNext !== next) return
    goneNext = null
    goneSay(next.head, next.detail)
  }))
}

let generation = 0
let reading: ReadableStreamDefaultReader<string> | null = null

export function signOut(head: string, detail: string) {
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

async function restarted(): Promise<boolean> {
  const res = await fetch(import.meta.url, { cache: 'no-store' }).catch(() => null)
  return res?.status === 404
}

function stepOf(text: string): Step {
  const skill = /^\s*(?:<command-name>\s*)?\/?(?:knackpack:(challenge-requirements|plan-implementation)|speckit-(specify|plan|tasks))\b/.exec(text)
  return skill?.[1] === 'challenge-requirements' || skill?.[2] === 'specify' ? 'define' : skill?.[1] === 'plan-implementation' || skill?.[2] === 'plan' ? 'plan' : skill?.[2] === 'tasks' ? 'tasks' : ''
}

export function ensure(sid: string): Session {
  let s = sessions.get(sid)
  if (s) return s
  const view = el('sf-conversation')
  view.dataset.sid = sid
  view.away = true
  view.scroller = scroller
  byId('conversations').append(view)
  s = { sid, info: { sid, cwd: '', live: false }, view, epoch: '', seq: 0, replayAt: 0, rebase: false, gate: null, gateKey: '', gateAt: 0, workflow: null, workflowSeq: 0, planfile: null, agents: [], intent: null, catalog: null, total: 0, cost: 0, busy: false, step: '', compacting: '', changes: null, forkStart: null, changesKey: '', changesAt: 0, compare: { from: 'fork', commit: '' }, context: null, asked: {}, refused: {}, audit: null, auditSeen: 0 }
  sessions.set(sid, s)
  return s
}

export function requestReplay(s: Session, force = false) {
  if (!state.stream || !s.info.live || s.info.disputed) return
  if (!force && Date.now() - s.replayAt < 3000) return
  s.replayAt = Date.now()
  void api('/api/replay', { stream: state.stream, sid: s.sid, epoch: s.epoch, since: s.seq }).catch(() => null)
}

export function updateSessions(list: unknown) {
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
    if (Boolean(info.running) !== Boolean(was.running)) s.auditSeen++
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

function entries(list: unknown): CatalogEntry[] {
  return Array.isArray(list) ? (list as unknown[]).filter((e): e is CatalogEntry => isRecord(e) && typeof e.name === 'string' && typeof e.description === 'string').map(e => ({ name: e.name, description: e.description })) : []
}

export function startStream({ onEvent, began, markCheckpoint }: StreamDeps): Stream {
  function applySession(s: Session, ev: SessionEvent) {
    s.lastAt = typeof ev.ts === 'number' && Number.isFinite(ev.ts) && ev.ts > 0 ? Math.min(ev.ts, Date.now()) : Date.now()
    s.view.who = agentWord(s.info.agent)
    const attach = isClaude(s.info) && /^[0-9a-f]{8}$/.test(s.info.id || '') ? attachCommand(s.info.id!, s.info.box ?? '') : ''
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

  async function connect(): Promise<void> {
    if (!signedIn()) return signOut('You’re signed out', `The engine didn’t answer. Check that it’s running (${composeCommand('ps')}), then press Sign in.`)
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
      goneNext = null
      document.body.classList.remove('out')
      state.connected = true
      state.note = ''
      openedAt = Date.now()
      paint()
      void loadUpdate()
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

  return { connect, applySession }
}
