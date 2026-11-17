import { api } from './api.ts'
import { armed, controllable, current, paint, say, schedulePaint, sleeping, state, toast, type Session } from './app-state.ts'
import { busy, byId, pressed, visible, whileBusy } from './lib/dom.ts'
import { agentWord, label } from './lib/format.ts'
import { boxSignIn } from './components/login.ts'
import { approveTrustFiles, askTrust, consentFor } from './components/consent.ts'
import { agentName } from './components/lineage.ts'
import type { Toast } from './components/toasts.ts'
import type { Consent, Needs } from './types.ts'

type Reply = { ok: boolean; body: Record<string, unknown> }

interface SessionActionsDeps {
  reply: (res: Response | null) => Promise<Reply>
  loadGate: (s: Session, force?: boolean) => Promise<void>
  loadProjects: (force?: boolean) => Promise<void>
  starterNames: () => ReadonlyMap<string, string>
  identityAction: (body: Record<string, unknown>) => Toast['action']
  markCheckpoint: (sid: string, level: string) => void
  copy: (command: string) => Promise<void>
  forgetSession: (sid: string) => void
}

export interface SessionActions {
  send: (text: string, images?: string[], s?: Session | null, pressed?: boolean, files?: string[]) => Promise<boolean>
  manage: (s: Session, path: string, body: Record<string, unknown>) => Promise<string>
  answerForge: (s: Session | null, answer: { card: string; approve: boolean; draft?: boolean; confirmed?: boolean }) => Promise<void>
  sessionAsk: (s: Session | null, route: string, body: Record<string, unknown>, title: string, done: string) => Promise<void>
  startPush: (s: Session | null) => Promise<void>
  answerPush: (s: Session | null, answer: { card: string; commit: string; allow: boolean; reason: string }) => Promise<void>
  startReview: (s: Session | null, all: boolean) => Promise<void>
  startTestList: (s: Session | null) => Promise<void>
  startAside: (s: Session | null, route: string, asking: Set<string>, running: boolean, what: string, body: Record<string, unknown>) => Promise<void>
  checkStep: (s: Session | null, d: { commit: string; step: number; done: boolean; settle: (ok: boolean) => void }) => Promise<void>
  approve: (kind: 'requirements' | 'plan', hash: string) => Promise<void>
  wakeSession: (s: Session) => Promise<void>
  withConsent: (name: string, what: string, send: (c: Consent) => Promise<Response>, resumable?: boolean) => Promise<Reply>
  handOver: (s: Session, agent: string) => Promise<void>
  bringMain: (s: Session) => Promise<void>
}

export const removals = new Map<string, { message: string; offer: { flag: string; value: string } | null }>()
export const busyManage = new Map<string, { action: string; at: number; force: boolean }>()
export const controlBusy = new Set<string>()
export const reviewAsking = new Set<string>()
export const pushAsking = new Set<string>()
const pushWatched = new Map<string, number>()
export const pushSaid = new Map<string, { text: string; tone: string }>()
const PUSH_SAID: Record<string, { text: string; tone: string }> = { held: { text: 'Held', tone: 'warn' }, denied: { text: 'Denied', tone: 'bad' }, refused: { text: 'Not pushed', tone: 'bad' } }
export const answerAsking = new Set<string>()

const ASKED_MS = 30000

export const stepping = new Map<string, number>()

export function holdBusy(b: HTMLButtonElement, on: boolean) {
  if (on !== (b.getAttribute('aria-busy') === 'true')) busy(b, on)
}

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
  const p = s.info.push
  const head = s.gate?.head ?? ''
  return !!p && !p.running && !pushAsking.has(s.sid) && (p.result === 'pushed' || p.result === 'up-to-date') && !!head && p.commit === head
}

export function pushOutcome(s: Session) {
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

export const testAsking = new Set<string>()

export const explainAsking = new Set<string>()
export const planAsking = new Set<string>()

const ASK_WAIT_MS = 15000

export function waiting(asked: { hash: string; at: number } | undefined, hash: string): boolean {
  return asked?.hash === hash && Date.now() - asked.at < ASK_WAIT_MS
}

async function notThrough(res: Response | null, title: string) {
  if (res?.ok) return
  const why = res ? await res.json().then((b: { message?: unknown }) => (typeof b.message === 'string' ? b.message : ''), () => '') : ''
  toast({ title, description: why ? visible(why) : res ? 'Try again.' : 'Check that the engine is running.', tone: 'bad' })
}

export function forkable(s: Session): boolean {
  return !!(state.connected && s.info.verified && s.info.kind === 'background' && s.info.project)
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

export function startSessionActions({ reply, loadGate, loadProjects, starterNames, identityAction, markCheckpoint, copy, forgetSession }: SessionActionsDeps): SessionActions {
  async function manage(s: Session, path: string, body: Record<string, unknown>): Promise<string> {
    busyManage.set(s.sid, { action: path === '/api/stop-session' ? 'stop' : 'remove', at: Date.now(), force: 'flag' in body })
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

  async function answerForge(s: Session | null, answer: { card: string; approve: boolean; draft?: boolean; confirmed?: boolean }) {
    if (!s || forgeAsking.has(s.sid)) return
    forgeAsking.add(s.sid)
    schedulePaint()
    const { ok, body } = await reply(await api('/api/forge-answer', { sid: s.sid, ...answer }).catch(() => null))
    forgeAsking.delete(s.sid)
    schedulePaint()
    const comment = s.info.forge?.find(f => f.card === answer.card)?.kind === 'comment'
    if (!ok) toast({ id: `forge-answer:${s.sid}`, title: !answer.approve ? "It wasn't denied." : comment ? "It wasn't posted." : "It wasn't opened.", description: typeof body.message === 'string' ? visible(body.message) : 'Check that the engine is running.', tone: 'bad' })
  }

  async function sessionAsk(s: Session | null, route: string, body: Record<string, unknown>, title: string, done: string) {
    if (!s) return
    const { ok, body: said } = await whilePressed(async () => reply(await api(route, { sid: s.sid, ...body }).catch(() => null)))
    if (!ok) return void toast({ id: `${route}:${s.sid}`, title, description: typeof said.message === 'string' ? visible(said.message) : 'Check that the engine is running.', tone: 'bad' })
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

  async function startReview(s: Session | null, all: boolean) {
    if (!s || reviewAsking.has(s.sid) || s.info.review?.running) return
    reviewAsking.add(s.sid)
    schedulePaint()
    const asked = all ? { sid: s.sid, all: true } : { sid: s.sid }
    let consent: Consent = {}
    let answer = await reply(await api('/api/review', asked).catch(() => null))
    for (let round = 0; !answer.ok && round < 2; round++) {
      const more = await consentFor(answer.body.needs as Needs | undefined, "The reviewers' Claude Code", 'the review')
      if (!more?.costOk) break
      consent = { costOk: true }
      answer = await reply(await api('/api/review', { ...asked, ...consent }).catch(() => null))
    }
    const { ok, body } = answer
    reviewAsking.delete(s.sid)
    schedulePaint()
    const message = typeof body.message === 'string' ? visible(body.message) : 'Check that the engine is running.'
    if (!ok && body.error === 'reviewed') toast({ id: `review:${s.sid}`, title: 'Every review is done.', description: message, tone: 'info' })
    else if (!ok) toast({ id: `review:${s.sid}`, title: "The review didn't start.", description: message, tone: 'bad', timeout: body.needs ? 0 : undefined })
  }

  async function startTestList(s: Session | null) {
    if (!s || testAsking.has(s.sid) || s.info.review?.testList?.state === 'running') return
    testAsking.add(s.sid)
    schedulePaint()
    let answer = await reply(await api('/api/test-list', { sid: s.sid }).catch(() => null))
    for (let round = 0; !answer.ok && round < 2; round++) {
      const more = await consentFor(answer.body.needs as Needs | undefined, "The reviewers' Claude Code", 'the manual test list')
      if (!more?.costOk) break
      answer = await reply(await api('/api/test-list', { sid: s.sid, costOk: true }).catch(() => null))
    }
    const { ok, body } = answer
    testAsking.delete(s.sid)
    schedulePaint()
    if (!ok) toast({ id: `test-list:${s.sid}`, title: "The test list wasn't started.", description: typeof body.message === 'string' ? visible(body.message) : 'Check that the engine is running.', tone: 'bad', timeout: body.needs ? 0 : undefined })
  }

  async function startAside(s: Session | null, route: string, asking: Set<string>, running: boolean, what: string, body: Record<string, unknown>) {
    if (!s || asking.has(s.sid) || running) return
    asking.add(s.sid)
    schedulePaint()
    let answer = await reply(await api(route, { sid: s.sid, ...body }).catch(() => null))
    for (let round = 0; !answer.ok && round < 2; round++) {
      const more = await consentFor(answer.body.needs as Needs | undefined, "The reviewers' Claude Code", what)
      if (!more?.costOk) break
      answer = await reply(await api(route, { sid: s.sid, ...body, costOk: true }).catch(() => null))
    }
    const { ok, body: said } = answer
    asking.delete(s.sid)
    schedulePaint()
    if (!ok) toast({ id: `${route}:${s.sid}`, title: `${what[0]!.toUpperCase()}${what.slice(1)} wasn't started.`, description: typeof said.message === 'string' ? visible(said.message) : 'Check that the engine is running.', tone: 'bad', timeout: said.needs ? 0 : undefined })
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
    const name = starterNames().get(s.info.agent ?? '') ?? 'The agent'
    const answer = asleep ? await withConsent(name, 'the session', consent => api('/api/message', { ...body, ...consent })) : await reply(await api('/api/message', body).catch(() => null))
    if (!answer.ok) {
      const why = answer.body.message
      say(typeof why === 'string' && why ? visible(why) : 'The message did not go through. Check that the session is still running.')
      const action = identityAction(answer.body)
      if (action) toast({ id: 'identity-needed', title: 'The session didn’t wake.', description: 'Set the name and email agents commit with first.', tone: 'bad', timeout: 0, action })
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

  async function checkStep(s: Session | null, d: { commit: string; step: number; done: boolean; settle: (ok: boolean) => void }) {
    if (!s) return d.settle(false)
    const { ok, body } = await reply(await api('/api/walkthrough-check', { sid: s.sid, commit: d.commit, step: d.step, done: d.done }).catch(() => null))
    d.settle(ok)
    if (!ok) toast({ id: `walkthrough:${s.sid}`, title: "The step wasn't checked off.", description: typeof body.message === 'string' ? visible(body.message) : 'Check that the engine is running.', tone: 'bad' })
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

  async function wakeSession(s: Session) {
    const name = starterNames().get(s.info.agent ?? '') ?? 'The agent'
    const { ok, body } = await withConsent(name, 'the session', consent => api('/api/wake', { sid: s.sid, ...consent }))
    toast(ok ? { id: `wake:${s.sid}`, title: `Waking ${visible(label(s.info))}…`, description: 'It reconnects in a few seconds.', tone: 'info', timeout: 30000 } : { id: `wake:${s.sid}`, title: 'The session could not be woken.', description: typeof body.message === 'string' ? visible(body.message) : 'Try again in a moment.', tone: 'bad', timeout: body.needs || identityAction(body) ? 0 : undefined, action: identityAction(body) })
  }

  async function answerTrust(path: string): Promise<{ ok: boolean; body: Record<string, unknown> } | null> {
    const rounds = 3
    let approve: string | undefined
    for (let round = 1; round <= rounds; round++) {
      toast({ id: 'trust', title: 'Answering Claude Code’s trust question…', description: 'This takes a few seconds.', tone: 'info', timeout: 60000 })
      const trusted = await reply(await api('/api/claude-trust', approve ? { path, approve } : { path }).catch(() => null))
      const files = (trusted.body.needs as Needs | undefined)?.approval
      if (trusted.ok || !files) return trusted
      if (round === rounds) {
        toast({ id: 'trust', title: 'Claude Code’s trust question wasn’t answered.', description: 'The files in the clone folder kept changing while you read them, so the folder stays untrusted. Try again once they stop changing.', tone: 'warn', timeout: 0 })
        return null
      }
      const said = typeof trusted.body.message === 'string' ? visible(trusted.body.message) : undefined
      toast(approve ? { id: 'trust', title: 'The files changed since you approved them.', description: said, tone: 'warn', timeout: 0 } : { id: 'trust', title: 'Claude Code’s trust question waits for your approval.', tone: 'info', timeout: 5000 })
      if (!(await approveTrustFiles(files, starterNames().get('claude') ?? 'Claude Code'))) {
        toast({ id: 'trust', title: 'Claude Code’s trust question wasn’t answered.', description: 'The folder stays untrusted.', tone: 'warn', timeout: 5000 })
        return null
      }
      approve = files.hash
    }
    return null
  }

  async function withConsent(name: string, what: string, send: (c: Consent) => Promise<Response>, resumable = false): Promise<{ ok: boolean; body: Record<string, unknown> }> {
    let consent: Consent = {}
    let answer = await reply(await send(consent).catch(() => null))
    for (let round = 0; !answer.ok && round < 5; round++) {
      const needs = answer.body.needs as Needs | undefined
      if (resumable && needs?.box) consent = { ...consent, box: needs.box }
      if (needs?.signIn && needs.box && !needs.cost) {
        if (!(await boxSignIn(api, needs, name, title => toast({ title, tone: 'ok' })))) break
        answer = await reply(await send(consent).catch(() => null))
        continue
      }
      if (needs?.trust) {
        if (!(await askTrust(needs.trust))) break
        const trusted = await answerTrust(needs.trust)
        if (!trusted) break
        if (!trusted.ok) {
          byId<HTMLElementTagNameMap['sf-toasts']>('toasts').close('trust')
          return trusted
        }
        toast({ id: 'trust', title: 'Claude Code now trusts this project.', tone: 'info', timeout: 5000 })
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

  async function handOver(s: Session, agent: string) {
    const name = agentName(agent)
    if (!confirmed(`${s.sid}:handover:${agent}`, `Choose Continue with ${name} again to stop this session and go on with ${name} on the same branch, with the approvals you gave.`)) return
    toast({ id: `handover:${s.sid}`, title: `Handing ${visible(label(s.info))} over to ${name}…`, description: 'The engine starts the other agent on the same branch in a container of its own, then stops this session and its container.', tone: 'info', timeout: 150000 })
    const { ok, body } = await withConsent(name, 'the session', consent => api('/api/handover', { sid: s.sid, agent, ...consent }))
    toast(ok ? { id: `handover:${s.sid}`, title: `${name} continues the branch.`, description: "It shows in the project's list when it connects; open it there to write its first message. The engine tells it where the branch stands.", tone: 'ok', timeout: 8000 } : { id: `handover:${s.sid}`, title: 'The branch wasn’t handed over.', description: typeof body.message === 'string' ? visible(body.message) : 'Try again in a moment.', tone: 'bad', timeout: 0, action: identityAction(body) })
    if (ok) void loadProjects(true)
  }

  async function bringMain(s: Session) {
    toast({ id: `main:${s.sid}`, title: 'Fetching the latest main…', tone: 'info', timeout: 60000 })
    const { ok, body } = await reply(await api('/api/bring-main', { sid: s.sid }).catch(() => null))
    const commit = typeof body.commit === 'string' && /^[0-9a-f]{40}$/.test(body.commit) ? body.commit.slice(0, 7) : ''
    toast(ok ? { id: `main:${s.sid}`, title: `Main is now at ${commit} as origin/main in the clone.`, description: 'The engine asked the agent to merge it into its branch and resolve any conflicts.', tone: 'ok' } : { id: `main:${s.sid}`, title: 'The latest main wasn’t brought in.', description: typeof body.message === 'string' ? visible(body.message) : 'Try again in a moment.', tone: 'bad', timeout: 0 })
  }

  return { send, manage, answerForge, sessionAsk, startPush, answerPush, startReview, startTestList, startAside, checkStep, approve, wakeSession, withConsent, handOver, bringMain }
}
