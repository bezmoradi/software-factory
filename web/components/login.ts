import { UNREAD, type EngineAnswer } from '../api.ts'
import { busy, button, el, labelError, say, sayIfChanged, statusLine, visible, whileBusy } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { actions, field, form, showAnswer } from '../lib/form.ts'
import type { Needs, SignIn } from '../types.ts'
import './accordion.ts'
import { secret } from './secret.ts'
import { commandBlock } from './copy.ts'
import { GATEWAY_WARNING } from './egress-panel.ts'
import { when } from './usage.ts'

type Post = (path: string, body: unknown) => Promise<EngineAnswer>

const ROUTE = '/api/claude-login'

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
  input.setAttribute('aria-required', 'true')
  input.addEventListener('input', () => labelError(input, ''))
  return [field(words, secret(input, what), { cls: 'login-field' }), input]
}

function sending(input: HTMLInputElement, on: boolean) {
  input.readOnly = on
  if (on) input.setAttribute('aria-disabled', 'true')
  else input.removeAttribute('aria-disabled')
}

class Row {
  readonly node = el('section', 'login-row')
  private readonly status = statusLine('small status-slot')
  private readonly kinds = el('sf-accordion', 'login-kinds')
  private readonly subscriptionSaid = statusLine('small status-slot')
  private readonly keySaid = statusLine('small status-slot')
  private readonly codeGo = button('Complete sign-in', 'primary small', undefined, 'submit')
  private readonly keyGo = button('Save key', 'primary small', undefined, 'submit')
  private readonly link = el('a', 'button primary small', 'Sign in')
  private readonly linkSaid = statusLine('small muted status-slot login-link-said')
  private readonly code: HTMLInputElement
  private readonly key: HTMLInputElement
  private readonly codeForm: HTMLFormElement
  private readonly keyForm: HTMLFormElement
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
    const [codeLabel, code] = secretField('code', 'Code from claude.com', 'code', 'one-time-code')
    this.code = code
    this.codeForm = this.form(codeLabel, this.codeGo, () => void this.finishCode())
    this.subscription.append(
      el('p', 'small muted', 'Sign in opens claude.com in a new tab. Sign in with your Claude account there, copy the code it shows, paste it here and press Complete sign-in.'),
      this.linkSaid,
      this.codeForm,
      this.subscriptionSaid,
    )
    const agents = target !== 'reviewers'
    const [keyLabel, key] = secretField('key', agents ? 'API key from the Claude Console, or your company gateway’s key' : 'API key from the Claude Console', 'API key', 'off')
    this.key = key
    this.keyForm = this.form(keyLabel, this.keyGo, () => void this.finishKey())
    this.apiKey.append(
      el('p', 'small muted', `Billed per use. Software Factory saves the key in ${agents ? 'this session’s container' : 'the reviewers’ home'} for Claude Code and never shows it again. While a gateway is set for ${agents ? 'the agents' : 'the reviewers'} under Network access, Claude Code sends this key to the gateway; paste a gateway’s key there.`),
      this.keyForm,
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
    return form({ inline: true, cls: 'login-code', children: [label, actions([go])], onSubmit: done })
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

  private unready(said = '') {
    sayIfChanged(this.linkSaid, said)
    this.link.removeAttribute('href')
    if (!this.link.parentNode) return
    const focused = document.activeElement === this.link
    this.link.remove()
    if (focused) this.code.focus({ preventScroll: true })
  }

  private ready(href: string, quiet: boolean) {
    sayIfChanged(this.linkSaid, quiet ? '' : 'Sign-in link ready.')
    this.link.href = href
    if (!this.link.parentNode) this.linkSaid.after(this.link)
  }

  private note(said: HTMLElement, text: string, bad = false) {
    say(said, text, bad ? 'bad' : undefined)
  }

  private async begin(keepNote = false) {
    const mine = ++this.starting
    this.unready(keepNote ? '' : 'Getting the sign-in link…')
    if (!keepNote) this.note(this.subscriptionSaid, '')
    const a = await this.post(ROUTE, { step: 'start', target: this.target })
    if (mine !== this.starting) return
    const link = safeLink(a.body.url)
    if (!a.ok || !link) {
      sayIfChanged(this.linkSaid, '')
      return this.note(this.subscriptionSaid, a.ok ? UNREAD : a.message, true)
    }
    this.waiting = true
    this.ready(link, keepNote)
    if (!keepNote) this.note(this.subscriptionSaid, '')
  }

  private async finishCode() {
    const code = this.code.value.trim()
    if (!this.waiting || this.code.readOnly) return
    sending(this.code, true)
    busy(this.codeGo, true)
    this.note(this.subscriptionSaid, '')
    labelError(this.code, '')
    const a = await this.post(ROUTE, { step: 'code', target: this.target, code })
    if (!a.ok && a.field === 'code') {
      sending(this.code, false)
      busy(this.codeGo, false)
      showAnswer(this.codeForm, a, this.subscriptionSaid)
      return
    }
    this.code.value = ''
    this.code.type = 'password'
    sending(this.code, false)
    busy(this.codeGo, false)
    this.waiting = false
    this.unready()
    if (!showAnswer(this.codeForm, a, this.subscriptionSaid)) {
      if (this.kinds.open === 'subscription') void this.begin(true)
      return
    }
    this.show(signInOf(a.body.signIn))
    this.signedIn('your subscription')
  }

  private async finishKey() {
    const key = this.key.value.trim()
    if (this.key.readOnly) return
    sending(this.key, true)
    busy(this.keyGo, true)
    this.note(this.keySaid, '')
    labelError(this.key, '')
    const a = await this.post(ROUTE, { step: 'key', target: this.target, key })
    this.key.value = ''
    this.key.type = 'password'
    sending(this.key, false)
    busy(this.keyGo, false)
    if (!showAnswer(this.keyForm, a, this.keySaid)) return this.key.focus()
    this.show(signInOf(a.body.signIn))
    this.signedIn('your API key')
  }

  cancel() {
    this.starting++
    this.unready()
    if (this.waiting) void this.post(ROUTE, { step: 'cancel', target: this.target })
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
  const a = await post(ROUTE, { step: 'status' })
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
    docs: 'run-it-with-docker',
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
    const a = await post(ROUTE, { step: 'status' })
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
      const a = await post(ROUTE, { step: 'status', target })
      if (!a.ok) {
        say(status, a.message)
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
      docs: 'run-it-with-docker',
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
