import { request, type EngineAnswer } from './api.ts'
import { allowed, armed, current, paint, say, schedulePaint, state, toast, waking, type Session } from './app-state.ts'
import { byId, pressed, visible, whileBusy } from './lib/dom.ts'
import { agentWord, label } from './lib/format.ts'
import { boxSignIn } from './components/login.ts'
import { approveHomeSettings, approveTrustFiles, askTrust, consentFor } from './components/consent.ts'
import type { Toast } from './components/toasts.ts'
import type { Consent, LineCommentBody, Needs } from './types.ts'

export type ComposedMessage = { part: { revise: { kind: 'requirements' | 'plan' | 'root-cause'; note: string } } | { comments: LineCommentBody[] } | { ask: string }; refused?: (why: string, field: string) => void }

interface SessionActionsDeps {
  loadGate: (s: Session, force?: boolean) => Promise<void>
  starterNames: () => ReadonlyMap<string, string>
  identityAction: (a: EngineAnswer) => Toast['action']
  signInAction: (a: EngineAnswer) => Toast['action']
  copy: (command: string) => Promise<void>
  forgetSession: (sid: string) => void
}

export interface SessionActions {
  send: (text: string, images?: string[], s?: Session | null, pressed?: boolean, files?: string[], composed?: ComposedMessage) => Promise<boolean>
  manage: (s: Session, path: string, body: Record<string, unknown>) => Promise<string>
  answerForge: (s: Session | null, answer: { card: string; approve: boolean; draft?: boolean; confirmed?: boolean }) => Promise<void>
  sessionAsk: (s: Session | null, route: string, body: Record<string, unknown>, title: string, done: string, refused?: (a: EngineAnswer) => boolean) => Promise<void>
  startPush: (s: Session | null) => Promise<void>
  answerPush: (s: Session | null, answer: { card: string; commit: string; allow: boolean; reason: string }, refused?: (a: EngineAnswer) => boolean) => Promise<void>
  startReview: (s: Session | null, all: boolean) => Promise<void>
  startTestList: (s: Session | null) => Promise<void>
  startAside: (s: Session | null, route: string, asking: Set<string>, running: boolean, what: string, body: Record<string, unknown>) => Promise<void>
  checkStep: (s: Session | null, d: { commit: string; step: number; done: boolean; settle: (ok: boolean) => void }) => Promise<void>
  approve: (kind: 'requirements' | 'plan', hash: string) => Promise<void>
  wakeSession: (s: Session, quiet?: boolean) => Promise<boolean>
  withConsent: (name: string, what: string, send: (c: Consent) => Promise<EngineAnswer>, resumable?: boolean, sid?: string) => Promise<EngineAnswer>
  bringMain: (s: Session) => Promise<void>
}

export const removals = new Map<string, { message: string; offer: { flag: string; value: string } | null }>()
export const busyManage = new Map<string, { action: string; at: number; force: boolean }>()
export const footAsk = new Map<string, 'stop' | 'remove'>()

export function askToManage(s: Session) {
  footAsk.set(s.sid, s.info.running ? 'stop' : 'remove')
  paint()
  const go = byId<HTMLButtonElement>('ask-confirm')
  if (!go.closest('[hidden]')) go.focus()
}
export const controlBusy = new Set<string>()
export const reviewAsking = new Set<string>()
export const pushAsking = new Set<string>()
const pushWatched = new Map<string, number>()
export const pushSaid = new Map<string, { text: string; tone: string }>()
export const answerAsking = new Set<string>()

const ASKED_MS = 30000

export const stepping = new Map<string, number>()

export function once(s: Session | null | undefined, what: string, run: () => Promise<boolean>) {
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

export function confirmed(key: string, prompt = ''): boolean {
  if (armed.delete(key)) {
    if (prompt) say('')
    return true
  }
  armed.add(key)
  if (prompt) say(prompt)
  schedulePaint()
  return false
}

export const forgeAsking = new Set<string>()

export function whilePressed<T>(work: () => Promise<T>): Promise<T> {
  const b = pressed()
  return b ? whileBusy(b, work) : work()
}

export function pushedHead(s: Session): boolean {
  return s.gate?.pushed === true && !s.info.push?.running && !pushAsking.has(s.sid)
}

export function pushOutcome(s: Session) {
  const since = pushWatched.get(s.sid)
  const p = s.info.push
  if (since === undefined || !p || p.running || pushAsking.has(s.sid) || p.at === since) return
  pushWatched.delete(s.sid)
  const said = p.resultSaid ? { text: p.resultSaid.label, tone: p.resultSaid.tone } : null
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

export const testAsking = new Set<string>()

export const explainAsking = new Set<string>()
export const planAsking = new Set<string>()

const ASK_WAIT_MS = 15000

export function waiting(asked: { hash: string; at: number } | undefined, hash: string): boolean {
  return asked?.hash === hash && Date.now() - asked.at < ASK_WAIT_MS
}

function notThrough(a: EngineAnswer | null, title: string) {
  if (a?.ok) return
  toast({ title, description: a ? a.message : 'The text changed since the page showed it. Read it again, then approve it.', tone: 'bad' })
}

export function forkable(s: Session): boolean {
  return state.connected && allowed(s, 'fork')
}

export async function controlBusyWhile<T>(s: Session, work: () => Promise<T>): Promise<T> {
  controlBusy.add(s.sid)
  paint()
  try {
    return await work()
  } finally {
    controlBusy.delete(s.sid)
    paint()
  }
}

export function startSessionActions({ loadGate, starterNames, identityAction, signInAction, copy, forgetSession }: SessionActionsDeps): SessionActions {
  async function manage(s: Session, path: string, body: Record<string, unknown>): Promise<string> {
    busyManage.set(s.sid, { action: path === '/api/stop-session' ? 'stop' : 'remove', at: Date.now(), force: 'flag' in body })
    schedulePaint()
    setTimeout(schedulePaint, 30500)
    const { ok, body: out, message: said } = await request(path, { sid: s.sid, ...body })
    if (!ok || path !== '/api/stop-session') busyManage.delete(s.sid)
    if (ok) {
      removals.delete(s.sid)
      if (path === '/api/remove-session') forgetSession(s.sid)
      schedulePaint()
      return ''
    }
    const offer = out.offer && typeof out.offer === 'object' ? (out.offer as { flag: string; value: string }) : null
    const message = said
    removals.set(s.sid, { message, offer })
    schedulePaint()
    return offer ? `${message} Open the session to choose what to do with it.` : message
  }

  async function answerForge(s: Session | null, answer: { card: string; approve: boolean; draft?: boolean; confirmed?: boolean }) {
    if (!s || forgeAsking.has(s.sid)) return
    forgeAsking.add(s.sid)
    schedulePaint()
    const { ok, message } = await request('/api/forge-answer', { sid: s.sid, ...answer })
    forgeAsking.delete(s.sid)
    schedulePaint()
    const comment = s.info.forge?.find(f => f.card === answer.card)?.kind === 'comment'
    if (!ok) toast({ id: `forge-answer:${s.sid}`, title: !answer.approve ? "It wasn't denied." : comment ? "It wasn't posted." : "It wasn't opened.", description: message, tone: 'bad' })
  }

  async function sessionAsk(s: Session | null, route: string, body: Record<string, unknown>, title: string, done: string, refused?: (a: EngineAnswer) => boolean) {
    if (!s) return
    const a = await whilePressed(() => request(route, { sid: s.sid, ...body }))
    if (!a.ok && refused?.(a)) return
    if (!a.ok) return void toast({ id: `${route}:${s.sid}`, title, description: a.message, tone: 'bad' })
    toast({ title: done, tone: 'ok' })
    s.auditSeen++
    void loadGate(s, true)
    schedulePaint()
  }

  async function startPush(s: Session | null) {
    if (!s || pushAsking.has(s.sid) || s.info.push?.running || pushedHead(s)) return
    pushAsking.add(s.sid)
    pushSaid.delete(s.sid)
    pushWatched.set(s.sid, s.info.push?.at ?? 0)
    schedulePaint()
    const { ok, message } = await request('/api/push', { sid: s.sid })
    pushAsking.delete(s.sid)
    if (!ok) pushWatched.delete(s.sid)
    schedulePaint()
    if (!ok) toast({ id: `push:${s.sid}`, title: "The push didn't start.", description: message, tone: 'bad' })
  }

  async function answerPush(s: Session | null, answer: { card: string; commit: string; allow: boolean; reason: string }, refused?: (a: EngineAnswer) => boolean) {
    if (!s || answerAsking.has(s.sid)) return
    answerAsking.add(s.sid)
    schedulePaint()
    const a = await request('/api/push-answer', { sid: s.sid, ...answer })
    answerAsking.delete(s.sid)
    schedulePaint()
    if (!a.ok && refused?.(a)) return
    if (!a.ok) toast({ id: `push-answer:${s.sid}`, title: answer.allow ? "The push wasn't allowed." : "The push wasn't denied.", description: a.message, tone: 'bad' })
  }

  async function startReview(s: Session | null, all: boolean) {
    if (!s || reviewAsking.has(s.sid) || s.info.review?.running) return
    reviewAsking.add(s.sid)
    schedulePaint()
    const asked = all ? { sid: s.sid, all: true } : { sid: s.sid }
    let answer = await request('/api/review', asked)
    while (!answer.ok) {
      const more = await consentFor(answer.body.needs as Needs | undefined, "The reviewers' Claude Code", 'the review')
      if (!more?.costOk) break
      answer = await request('/api/review', { ...asked, costOk: true })
    }
    const { ok, body, error } = answer
    reviewAsking.delete(s.sid)
    schedulePaint()
    const message = answer.message
    if (!ok && error === 'reviewed') toast({ id: `review:${s.sid}`, title: 'Every review is done.', description: message, tone: 'info' })
    else if (!ok) toast({ id: `review:${s.sid}`, title: "The review didn't start.", description: message, tone: 'bad', timeout: body.needs ? 0 : undefined, action: signInAction(answer) })
  }

  async function startTestList(s: Session | null) {
    if (!s || testAsking.has(s.sid) || s.info.review?.testList?.state === 'running') return
    testAsking.add(s.sid)
    schedulePaint()
    let answer = await request('/api/test-list', { sid: s.sid })
    while (!answer.ok) {
      const more = await consentFor(answer.body.needs as Needs | undefined, "The reviewers' Claude Code", 'the manual test list')
      if (!more?.costOk) break
      answer = await request('/api/test-list', { sid: s.sid, costOk: true })
    }
    const { ok, body, message } = answer
    testAsking.delete(s.sid)
    schedulePaint()
    if (!ok) toast({ id: `test-list:${s.sid}`, title: "The test list wasn't started.", description: message, tone: 'bad', timeout: body.needs ? 0 : undefined, action: signInAction(answer) })
  }

  async function startAside(s: Session | null, route: string, asking: Set<string>, running: boolean, what: string, body: Record<string, unknown>) {
    if (!s || asking.has(s.sid) || running) return
    asking.add(s.sid)
    schedulePaint()
    let answer = await request(route, { sid: s.sid, ...body })
    while (!answer.ok) {
      const more = await consentFor(answer.body.needs as Needs | undefined, "The reviewers' Claude Code", what)
      if (!more?.costOk) break
      answer = await request(route, { sid: s.sid, ...body, costOk: true })
    }
    const { ok, body: said, message } = answer
    asking.delete(s.sid)
    schedulePaint()
    if (!ok) toast({ id: `${route}:${s.sid}`, title: `${what[0]!.toUpperCase()}${what.slice(1)} wasn't started.`, description: message, tone: 'bad', timeout: said.needs ? 0 : undefined, action: signInAction(answer) })
    if (!ok) void loadGate(s, true)
  }

  async function send(text: string, images: string[] = [], s = current(), pressed = false, files: string[] = [], composed?: ComposedMessage) {
    if (!s) return false
    const asleep = allowed(s, 'wake')
    const bypass = s.info.mode === 'bypassPermissions'
    if (bypass && !pressed && !confirmed(`${s.sid}:bypass`, 'This session runs with bypassPermissions: Claude runs every tool without asking. Press Enter again to send anyway.')) return false
    say('')
    const body: { sid: string; text?: string; revise?: { kind: string; note: string }; comments?: LineCommentBody[]; ask?: string; images?: string[]; files?: string[]; confirm?: boolean } = composed ? { sid: s.sid, ...composed.part } : { sid: s.sid, text }
    if (images.length) body.images = images
    if (files.length) body.files = files
    if (bypass) body.confirm = true
    const name = starterNames().get(s.info.agent ?? '') ?? 'The agent'
    if (asleep) startWaking(s.sid)
    const answer = asleep ? await withConsent(name, 'the session', consent => request('/api/message', { ...body, ...consent }), false, s.sid) : await request('/api/message', body)
    if (!answer.ok) {
      if (asleep) stopWaking(s.sid)
      const line = typeof answer.body.line === 'string' ? answer.body.line : ''
      if (line) toast({ id: 'shell-line', title: "The page doesn't run shell lines.", description: answer.message, tone: 'warn', action: { label: 'Copy the command', run: () => void copy(line) } })
      const said = answer.message
      if (composed?.refused) composed.refused(said, answer.field)
      else say(said)
      const action = identityAction(answer)
      if (action) toast({ id: 'identity-needed', title: 'The session didn’t wake.', description: 'Set the name and email agents commit with first.', tone: 'bad', timeout: 0, action })
      return false
    }
    if (asleep) {
      say(s.info.live ? '' : `Waking the session. The message goes to ${agentWord(s.info.agent)} once it reconnects, if that happens within 2 minutes.`)
    }
    return true
  }

  async function checkStep(s: Session | null, d: { commit: string; step: number; done: boolean; settle: (ok: boolean) => void }) {
    if (!s) return d.settle(false)
    const { ok, message } = await request('/api/walkthrough-check', { sid: s.sid, commit: d.commit, step: d.step, done: d.done })
    d.settle(ok)
    if (!ok) toast({ id: `walkthrough:${s.sid}`, title: "The step wasn't checked off.", description: message, tone: 'bad' })
  }

  async function approve(kind: 'requirements' | 'plan', hash: string) {
    const s = current()
    if (!s) return
    s.asked[kind] = { hash, at: Date.now() }
    delete s.refused[kind]
    schedulePaint()
    setTimeout(schedulePaint, ASK_WAIT_MS)
    const doc = kind === 'plan' ? s.gate?.planDoc : s.gate?.requirements
    const a = doc?.hash === hash ? await request(kind === 'plan' ? '/api/approve-plan' : '/api/approve-requirements', { sid: s.sid, hash, text: doc.text }) : null
    delete s.asked[kind]
    void loadGate(s, true)
    schedulePaint()
    notThrough(a, 'The approval did not go through.')
  }

  function startWaking(sid: string) {
    waking.set(sid, Date.now())
    schedulePaint()
  }

  function stopWaking(sid: string) {
    waking.delete(sid)
    schedulePaint()
  }

  async function wakeSession(s: Session, quiet = false): Promise<boolean> {
    const name = starterNames().get(s.info.agent ?? '') ?? 'The agent'
    startWaking(s.sid)
    const a = await withConsent(name, 'the session', consent => request('/api/wake', { sid: s.sid, ...consent }), false, s.sid)
    const { ok, body, message } = a
    if (!ok) stopWaking(s.sid)
    if (body.refused === true) return false
    if (ok && (quiet || current() === s)) return true
    toast(ok ? { id: `wake:${s.sid}`, title: `Waking ${visible(label(s.info))}…`, description: 'It reconnects in a few seconds.', tone: 'info', timeout: 30000 } : { id: `wake:${s.sid}`, title: 'The session could not be woken.', description: message, tone: 'bad', timeout: body.needs || identityAction(a) ? 0 : undefined, action: identityAction(a) })
    return ok
  }

  async function answerTrust(path: string): Promise<EngineAnswer | null> {
    let approve: string | undefined
    for (;;) {
      toast({ id: 'trust', title: 'Answering Claude Code’s trust question…', description: 'This takes a few seconds.', tone: 'info', timeout: 60000 })
      const trusted = await request('/api/claude-trust', approve ? { path, approve } : { path })
      const needs = trusted.body.needs as Needs | undefined
      const files = needs?.next === 'approve' ? needs.approval : undefined
      if (!trusted.ok && trusted.error === 'changing') {
        toast({ id: 'trust', title: 'Claude Code’s trust question wasn’t answered.', description: trusted.message, tone: 'warn', timeout: 0 })
        return null
      }
      if (trusted.ok || !files) return trusted
      const said = trusted.message
      toast(approve ? { id: 'trust', title: 'The files changed since you approved them.', description: said, tone: 'warn', timeout: 0 } : { id: 'trust', title: 'Claude Code’s trust question waits for your approval.', tone: 'info', timeout: 5000 })
      if (!(await approveTrustFiles(files, starterNames().get('claude') ?? 'Claude Code'))) {
        toast({ id: 'trust', title: 'Claude Code’s trust question wasn’t answered.', description: 'The folder stays untrusted.', tone: 'warn', timeout: 5000 })
        return null
      }
      approve = files.hash
    }
  }

  async function refuseWake(sid: string): Promise<EngineAnswer> {
    const refused = await request('/api/wake', { sid, refuse: true })
    toast(refused.ok ? { id: `wake:${sid}`, title: 'The session stays stopped.', description: 'Its container was stopped. Wake it again to see the same list, or delete it.', tone: 'info' } : { id: `wake:${sid}`, title: 'The session’s container wasn’t stopped.', description: refused.message, tone: 'warn', timeout: 0 })
    return { ok: false, status: 0, error: '', field: '', message: 'The session stays stopped, so nothing was sent.', body: { refused: true } }
  }

  async function withConsent(name: string, what: string, send: (c: Consent) => Promise<EngineAnswer>, resumable = false, sid?: string): Promise<EngineAnswer> {
    let consent: Consent = {}
    let answer = await send(consent)
    while (!answer.ok) {
      const needs = answer.body.needs as Needs | undefined
      const next = needs?.next ?? ''
      if (!needs || !next) break
      if (next === 'home' && needs.home) {
        if (!(await approveHomeSettings(needs.home, name))) return sid ? refuseWake(sid) : answer
        consent = { ...consent, home: needs.home.digest }
        answer = await send(consent)
        continue
      }
      if (resumable && needs.box) consent = { ...consent, box: needs.box }
      if (next === 'sign-in') {
        if (!(await boxSignIn(request, needs, name, title => toast({ title, tone: 'ok' })))) break
        answer = await send(consent)
        continue
      }
      if (next === 'trust' && needs.trust) {
        if (!(await askTrust())) break
        const trusted = await answerTrust(needs.trust)
        if (!trusted) break
        if (!trusted.ok) {
          byId<HTMLElementTagNameMap['sf-toasts']>('toasts').close('trust')
          return trusted
        }
        toast({ id: 'trust', title: 'Claude Code now trusts this project.', tone: 'info', timeout: 5000 })
        answer = await send(consent)
        continue
      }
      const more = await consentFor(needs, name, what)
      if (!more) break
      consent = { ...consent, ...more }
      answer = await send(consent)
    }
    return answer
  }

  async function bringMain(s: Session) {
    toast({ id: `main:${s.sid}`, title: 'Fetching the latest main…', tone: 'info', timeout: 60000 })
    const { ok, body, message } = await request('/api/bring-main', { sid: s.sid })
    const commit = typeof body.commit === 'string' && /^[0-9a-f]{40}$/.test(body.commit) ? body.commit.slice(0, 7) : ''
    toast(ok ? { id: `main:${s.sid}`, title: `Main is now at ${commit} as origin/main in the clone.`, description: 'The engine asked the agent to merge it into its branch and resolve any conflicts.', tone: 'ok' } : { id: `main:${s.sid}`, title: 'The latest main wasn’t brought in.', description: message, tone: 'bad', timeout: 0 })
  }

  return { send, manage, answerForge, sessionAsk, startPush, answerPush, startReview, startTestList, startAside, checkStep, approve, wakeSession, withConsent, bringMain }
}
