import { busy, button, el, field, say, statusLine, visible, whileBusy } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import type { Needs, SignIn } from '../types.ts'
import './accordion.ts'
import { secret } from './secret.ts'
import { commandBlock } from './copy.ts'
import { GATEWAY_WARNING } from './egress-panel.ts'
import { when } from './usage.ts'

type Post = (path: string, body: unknown) => Promise<Response>

interface Answer {
  ok: boolean
  body: Record<string, unknown>
}

const ROUTE = '/api/claude-login'

async function call(post: Post, body: unknown): Promise<Answer> {
  const res = await post(ROUTE, body).catch(() => null)
  if (!res) return { ok: false, body: { message: 'The engine didn’t answer.' } }
  const parsed: unknown = await res.json().catch(() => ({}))
  const record = parsed !== null && typeof parsed === 'object' ? parsed as Record<string, unknown> : {}
  return { ok: res.ok, body: record }
}

function signInOf(v: unknown): SignIn | null {
  if (v === null || typeof v !== 'object') return null
  const s = v as Record<string, unknown>
  return typeof s.method === 'string' && typeof s.words === 'string' ? s as unknown as SignIn : null
}

function safeLink(v: unknown): string {
  if (typeof v !== 'string') return ''
  try {
    const u = new URL(v)
    return u.protocol === 'https:' && (u.hostname === 'claude.com' || u.hostname === 'claude.ai') && !u.username && !u.password && !u.port ? u.href : ''
  } catch {
    return ''
  }
}

const RECHECK_MS = 15000
const BUSY = 'All 8 reviewer containers are in use, so the sign-in wasn’t checked again; it checks again shortly.'

function stale(s: SignIn | null, busy = false): boolean {
  return !!s?.stale && (!!s.busy || busy)
}

function notCurrent(s: SignIn): string {
  const before = s.method === 'not-signed-in' ? 'not signed in' : `signed in: ${visible(s.words)}`
  return `Not current: the sign-in or key changed after the last check${s.checkedAt ? `, checked ${when(s.checkedAt)}` : ''}, which found it ${before}.`
}

function describe(s: SignIn | null, busy = false): string {
  if (!s) return busy ? `The reviewers are busy. ${BUSY}` : 'Not checked yet.'
  if (stale(s, busy)) return `${notCurrent(s)} ${BUSY}`
  const said = s.method === 'not-signed-in' ? 'Not signed in.' : `Signed in: ${visible(s.words)}.`
  const at = s.checkedAt ? `${said} Checked ${when(s.checkedAt)}.` : said
  return s.busy || busy ? `${at} ${BUSY}` : at
}

const PER_USE = ['api-key', 'gateway', 'bedrock', 'vertex']

function secretField(name: string, words: string, what: string, autocomplete: AutoFill): [HTMLElement, HTMLInputElement] {
  const input = el('input', 'textbox')
  input.type = 'password'
  input.name = name
  input.autocomplete = autocomplete
  input.spellcheck = false
  input.required = true
  input.maxLength = 512
  return [field(words, secret(input, what), undefined, 'login-field'), input]
}

function busyButton(text: string): { node: HTMLButtonElement; busy: (on: boolean) => void } {
  const node = button(text, 'primary small', undefined, 'submit')
  return { node, busy: on => busy(node, on) }
}

class Row {
  readonly node = el('section', 'login-row')
  private readonly status = statusLine('small status-slot')
  private readonly kinds = el('sf-accordion', 'login-kinds')
  private readonly subscriptionSaid = statusLine('small status-slot')
  private readonly keySaid = statusLine('small status-slot')
  private readonly codeGo = busyButton('Complete sign-in')
  private readonly keyGo = busyButton('Save key')
  private readonly link = el('a', 'button primary small', 'Sign in')
  private readonly code: HTMLInputElement
  private readonly key: HTMLInputElement
  private readonly subscription = el('div', 'login-panel')
  private readonly apiKey = el('div', 'login-panel')
  private waiting = false
  private starting = 0
  private signIn: SignIn | null = null
  private current = true
  private readonly post: Post
  private readonly target: string
  private readonly signedIn: (how: string) => void

  constructor(post: Post, target: string, signedIn: (how: string) => void) {
    this.post = post
    this.target = target
    this.signedIn = signedIn
    this.link.target = '_blank'
    this.link.rel = 'noopener noreferrer'
    this.unready()
    const [codeLabel, code] = secretField('code', 'Code from claude.com', 'code', 'one-time-code')
    this.code = code
    const codeForm = this.form(codeLabel, this.codeGo.node, () => void this.finishCode())
    this.subscription.append(
      el('p', 'small muted', 'Sign in opens claude.com in a new tab. Sign in with your Claude account there, copy the code it shows, paste it here and press Complete sign-in.'),
      this.link,
      codeForm,
      this.subscriptionSaid,
    )
    const agents = target !== 'reviewers'
    const [keyLabel, key] = secretField('api-key', agents ? 'API key from the Claude Console, or your company gateway’s key' : 'API key from the Claude Console', 'API key', 'off')
    this.key = key
    this.apiKey.append(
      el('p', 'small muted', `Billed per use. Software Factory saves the key in ${agents ? 'this session’s container' : 'the reviewers’ home'} for Claude Code and never shows it again. While a gateway is set for ${agents ? 'the agents' : 'the reviewers'} under Network access, Claude Code sends this key to the gateway; paste a gateway’s key there.`),
      this.form(keyLabel, this.keyGo.node, () => void this.finishKey()),
      this.keySaid,
    )
    this.kinds.addEventListener('sf-expand', e => {
      if (e.detail === 'subscription') void this.begin()
      else this.cancel()
    })
    this.paint()
    this.node.append(this.status, this.kinds)
  }

  private form(label: HTMLElement, go: HTMLButtonElement, done: () => void): HTMLFormElement {
    const form = el('form', 'row login-code')
    form.append(label, go)
    form.addEventListener('submit', e => {
      e.preventDefault()
      done()
    })
    return form
  }

  private paint() {
    const m = this.current ? this.signIn?.method ?? '' : ''
    this.kinds.items = [
      { id: 'subscription', title: 'Subscription', note: 'Claude Pro, Max or Ultra', state: m === 'subscription' ? 'In use' : '', good: true, panel: this.subscription },
      { id: 'api-key', title: 'API key', note: this.target === 'reviewers' ? 'Claude Console' : 'Claude Console or company gateway', state: PER_USE.includes(m) ? 'In use' : '', good: true, panel: this.apiKey },
    ]
  }

  show(s: SignIn | null, busy = false) {
    this.signIn = s
    this.current = !stale(s, busy)
    const worth = stale(s, busy) || !!s?.busy || busy
    const gateway = s?.method === 'gateway' && this.current ? `${GATEWAY_WARNING}.` : ''
    say(this.status, [worth ? describe(s, busy) : '', gateway].filter(Boolean).join(' '))
    this.paint()
  }

  private unready() {
    this.link.removeAttribute('href')
    this.link.setAttribute('aria-disabled', 'true')
  }

  private note(said: HTMLElement, text: string, bad = false) {
    say(said, text, bad ? 'bad' : undefined)
  }

  private async begin() {
    const mine = ++this.starting
    this.unready()
    this.note(this.subscriptionSaid, '')
    const a = await call(this.post, { step: 'start', target: this.target })
    if (mine !== this.starting) return
    const link = safeLink(a.body.url)
    if (!a.ok || !link) return this.note(this.subscriptionSaid, typeof a.body.message === 'string' ? a.body.message : 'The login didn’t start. Close Subscription and open it again to retry.', true)
    this.waiting = true
    this.link.href = link
    this.link.removeAttribute('aria-disabled')
    this.note(this.subscriptionSaid, '')
  }

  private async finishCode() {
    const code = this.code.value.trim()
    if (!code || !this.waiting) return
    this.code.disabled = true
    this.codeGo.busy(true)
    this.note(this.subscriptionSaid, '')
    const a = await call(this.post, { step: 'code', target: this.target, code })
    this.code.value = ''
    this.code.type = 'password'
    this.code.disabled = false
    this.codeGo.busy(false)
    this.waiting = false
    this.unready()
    if (!a.ok) {
      this.note(this.subscriptionSaid, typeof a.body.message === 'string' ? a.body.message : 'That didn’t work.', true)
      if (this.kinds.open === 'subscription') void this.begin()
      return
    }
    this.show(signInOf(a.body.signIn))
    this.signedIn('your subscription')
  }

  private async finishKey() {
    const key = this.key.value.trim()
    if (!key) return
    this.key.disabled = true
    this.keyGo.busy(true)
    this.note(this.keySaid, '')
    const a = await call(this.post, { step: 'key', target: this.target, key })
    this.key.value = ''
    this.key.type = 'password'
    this.key.disabled = false
    this.keyGo.busy(false)
    if (!a.ok) return this.note(this.keySaid, typeof a.body.message === 'string' ? a.body.message : 'The key wasn’t saved. Try again.', true)
    this.show(signInOf(a.body.signIn))
    this.signedIn('your API key')
  }

  cancel() {
    this.starting++
    this.unready()
    if (this.waiting) void call(this.post, { step: 'cancel', target: this.target })
    this.waiting = false
  }
}

export interface AgentLogin {
  id: string
  name: string
  step: string
  signedIn: boolean | null
}

export interface SignInState {
  agents: AgentLogin[]
  reviewers: boolean | null
}

function signedIn(v: unknown): boolean | null {
  const s = signInOf(v)
  return s && !s.stale ? s.method !== 'not-signed-in' : null
}

export async function signInState(post: Post): Promise<SignInState | null> {
  const a = await call(post, { step: 'status' })
  if (!a.ok) return null
  const each = Array.isArray(a.body.each) ? (a.body.each as unknown[]) : []
  const agents: AgentLogin[] = []
  for (const x of each) {
    if (x === null || typeof x !== 'object') continue
    const r = x as Record<string, unknown>
    if (typeof r.id !== 'string' || typeof r.name !== 'string') continue
    agents.push({ id: r.id, name: r.name, step: typeof r.step === 'string' ? r.step : '', signedIn: signedIn(r.signIn) })
  }
  return { agents, reviewers: a.body.reviewer === true ? signedIn(a.body.reviewers) : true }
}

export async function claudeLogin(post: Post, tell: (title: string) => void, closed?: () => void, known?: SignIn | null): Promise<void> {
  const reviewers = new Row(post, 'reviewers', how => {
    m.close()
    tell(`Claude Code for the reviewers is signed in with ${how}.`)
  })
  let open = true
  let again = 0
  const m = modal({
    kind: 'login',
    title: 'Sign Claude Code in for the reviewers',
    body: [
      el('p', 'small', 'The reviewers run their own Claude Code, which signs in once, with a subscription or an API key. Reviews then start only on the kind you signed in with here. Each session signs in in its own container when you start it.'),
      reviewers.node,
    ],
    actions: [button('Close', 'ghost small', () => m.close())],
    focus: 'heading',
  })
  m.onClose(() => {
    open = false
    clearTimeout(again)
    reviewers.cancel()
    closed?.()
  })
  if (known) reviewers.show(known)
  const refresh = async (): Promise<void> => {
    const a = await call(post, { step: 'status' })
    if (!open) return
    if (a.body.reviewer !== true) return void reviewers.node.remove()
    const busy = a.body.reviewersBusy === true
    reviewers.show(signInOf(a.body.reviewers), busy)
    if (busy) again = window.setTimeout(() => void refresh(), RECHECK_MS)
  }
  await refresh()
}

const BOX = /^[0-9a-f]{16}$/

export function boxSignIn(post: Post, needs: Needs, name: string, tell: (title: string) => void): Promise<boolean> {
  const box = needs.box ?? ''
  if (!BOX.test(box)) return Promise.resolve(false)
  const target = `box:${box}`
  return new Promise(resolve => {
    const status = statusLine()
    if (!needs.code) say(status, describe(needs.signIn ?? null))
    let finished = false
    let answer = false
    const done = (yes: boolean) => {
      if (finished) return
      finished = true
      answer = yes
      m.close()
    }
    const row = needs.code ? new Row(post, target, how => {
      done(true)
      tell(`${visible(name)} is signed in for this session with ${how}.`)
    }) : null
    const check = async (): Promise<boolean | null> => {
      say(status, 'Checking…')
      const a = await call(post, { step: 'status', target })
      if (!a.ok) {
        say(status, typeof a.body.message === 'string' ? visible(a.body.message) : 'The engine didn’t answer.')
        return null
      }
      const each = Array.isArray(a.body.each) ? a.body.each as unknown[] : []
      const first = each[0] !== null && typeof each[0] === 'object' ? each[0] as Record<string, unknown> : {}
      const s = signInOf(first.signIn)
      row?.show(s)
      const ready = signedIn(first.signIn)
      if (ready === false) say(status, row ? 'Not signed in yet. Sign in above, then press Continue.' : `${describe(s)} Run the command below, then press Continue.`)
      else say(status, row ? '' : describe(s))
      return ready
    }
    const proceed = async () => {
      if (await check() !== false) done(true)
    }
    const parts: Node[] = [
      el('p', 'small', `Each session runs in its own container, with its own sign-in. ${visible(name)} there needs one before the session starts. The container stays up while you sign in.`),
    ]
    if (row) {
      row.show(needs.signIn ?? null)
      parts.push(row.node, status)
    } else {
      parts.push(status)
      if (needs.step) parts.push(el('p', 'small muted', 'Run this in a terminal, then open the link it prints and enter the code it shows.'), commandBlock(needs.step))
    }
    const go = button('Continue', 'primary small', () => void whileBusy(go, proceed))
    const m = modal({
      kind: 'login',
      title: `Sign ${visible(name)} in for this session`,
      body: parts,
      actions: [button('Cancel', 'ghost small', () => done(false)), go],
    })
    m.onClose(() => {
      finished = true
      row?.cancel()
      resolve(answer)
    })
  })
}
