import { button, el, visible } from '../lib/dom.ts'
import type { Needs, SignIn } from '../types.ts'
import './accordion.ts'
import { icon } from './icon.ts'
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

function field(name: string, words: string, secret: boolean): [HTMLLabelElement, HTMLInputElement] {
  const label = el('label', 'small', words)
  const input = el('input', 'textbox')
  input.type = secret ? 'password' : 'text'
  input.name = name
  input.autocomplete = 'off'
  input.spellcheck = false
  input.required = true
  input.maxLength = 512
  label.append(input)
  return [label, input]
}

class Row {
  readonly node = el('section', 'login-row')
  private readonly status = el('p', 'small', 'Checking…')
  private readonly kinds = el('sf-accordion', 'login-kinds')
  private readonly say = el('p', 'small', '')
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

  constructor(post: Post, target: string, title: string) {
    this.post = post
    this.target = target
    this.say.setAttribute('role', 'status')
    this.status.setAttribute('role', 'status')
    this.link.target = '_blank'
    this.link.rel = 'noopener noreferrer'
    this.unready()
    const [codeLabel, code] = field('code', 'Code from claude.com', false)
    this.code = code
    const codeForm = this.form(codeLabel, () => void this.finishCode())
    this.subscription.append(
      el('p', 'small muted', 'Sign in opens claude.com in a new tab. Sign in with your Claude account there, copy the code it shows, paste it here and press Finish.'),
      this.link,
      codeForm,
    )
    const agents = target !== 'reviewers'
    const [keyLabel, key] = field('api-key', agents ? 'API key from the Claude Console, or your company gateway’s key' : 'API key from the Claude Console', true)
    this.key = key
    this.apiKey.append(
      el('p', 'small muted', `Billed per use. Software Factory saves the key in ${agents ? 'this session’s container' : 'the reviewers’ home'} for Claude Code and never shows it again. While a gateway is set for ${agents ? 'the agents' : 'the reviewers'} under Network access, Claude Code sends this key to the gateway; paste a gateway’s key there.`),
      this.form(keyLabel, () => void this.finishKey()),
    )
    this.kinds.addEventListener('sf-expand', e => {
      if (e.detail === 'subscription') void this.begin()
      else this.cancel()
    })
    this.paint()
    this.node.append(el('h3', 'label', title), this.status, this.kinds, this.say)
  }

  private form(label: HTMLLabelElement, done: () => void): HTMLFormElement {
    const form = el('form', 'row login-code')
    form.append(label, button('Finish', 'primary small', undefined, 'submit'))
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

  pending(s: SignIn) {
    this.status.textContent = `${s.stale ? notCurrent(s) : describe(s)} Checking again…`
  }

  show(s: SignIn | null, busy = false) {
    this.signIn = s
    this.current = !stale(s, busy)
    this.status.textContent = describe(s, busy)
    if (s?.method === 'gateway' && this.current) this.status.textContent += ` ${GATEWAY_WARNING}.`
    this.paint()
  }

  private unready() {
    this.link.removeAttribute('href')
    this.link.setAttribute('aria-disabled', 'true')
  }

  private note(text: string, bad = false) {
    this.say.textContent = text
    this.say.classList.toggle('bad', bad)
  }

  private async begin() {
    const mine = ++this.starting
    this.unready()
    this.note('')
    const a = await call(this.post, { step: 'start', target: this.target })
    if (mine !== this.starting) return
    const link = safeLink(a.body.url)
    if (!a.ok || !link) return this.note(typeof a.body.message === 'string' ? a.body.message : 'The login didn’t start. Close Subscription and open it again to retry.', true)
    this.waiting = true
    this.link.href = link
    this.link.removeAttribute('aria-disabled')
    this.note('')
  }

  private async finishCode() {
    const code = this.code.value.trim()
    if (!code || !this.waiting) return
    this.code.disabled = true
    this.note('Checking the code…')
    const a = await call(this.post, { step: 'code', target: this.target, code })
    this.code.value = ''
    this.code.disabled = false
    this.waiting = false
    this.unready()
    if (!a.ok) {
      this.note(typeof a.body.message === 'string' ? a.body.message : 'That didn’t work.', true)
      if (this.kinds.open === 'subscription') void this.begin()
      return
    }
    this.note('Signed in with your subscription.')
    this.show(signInOf(a.body.signIn))
  }

  private async finishKey() {
    const key = this.key.value.trim()
    if (!key) return
    this.key.disabled = true
    this.note('Saving the key…')
    const a = await call(this.post, { step: 'key', target: this.target, key })
    this.key.value = ''
    this.key.disabled = false
    if (!a.ok) return this.note(typeof a.body.message === 'string' ? a.body.message : 'The key wasn’t saved. Try again.', true)
    this.note('Signed in with your API key.')
    this.show(signInOf(a.body.signIn))
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

export async function claudeLogin(post: Post, closed?: () => void, known?: SignIn | null): Promise<void> {
  const d = el('dialog', 'consent login surface')
  d.setAttribute('aria-label', 'Sign Claude Code in for the reviewers')
  const reviewers = new Row(post, 'reviewers', 'Claude Code for the reviewers')
  const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
  let open = true
  let again = 0
  const close = () => {
    open = false
    clearTimeout(again)
    reviewers.cancel()
    d.close()
    d.remove()
    closed?.()
    if (opener?.isConnected) opener.focus()
  }
  const actions = el('div', 'row consent-actions')
  actions.append(el('span', 'spacer'), button('Close', 'ghost small', close))
  const heading = el('h2', 'label', 'Sign Claude Code in for the reviewers')
  heading.tabIndex = -1
  d.append(
    heading,
    el('p', 'small', 'The reviewers run their own Claude Code, which signs in once, with a subscription or an API key. Reviews then start only on the kind you signed in with here. Each session signs in in its own container when you start it.'),
    reviewers.node,
    actions,
  )
  d.addEventListener('cancel', e => {
    e.preventDefault()
    close()
  })
  document.body.append(d)
  d.showModal()
  heading.focus()
  if (known) reviewers.pending(known)
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

function commandBlock(command: string): HTMLElement {
  const block = el('div', 'setup-code')
  const copy = button('', 'setup-copy', () => {
    void navigator.clipboard.writeText(command).then(() => {
      copy.replaceChildren(icon('check'))
      copy.setAttribute('aria-label', 'Copied')
      copy.title = 'Copied'
      setTimeout(() => {
        copy.replaceChildren(icon('copy'))
        copy.setAttribute('aria-label', 'Copy the command')
        copy.title = 'Copy'
      }, 1500)
    }, () => { copy.title = 'Couldn’t copy: select the command and copy it' })
  })
  copy.append(icon('copy'))
  copy.setAttribute('aria-label', 'Copy the command')
  copy.title = 'Copy'
  block.append(el('code', '', visible(command)), copy)
  return block
}

const BOX = /^[0-9a-f]{16}$/

export function boxSignIn(post: Post, needs: Needs, name: string): Promise<boolean> {
  const box = needs.box ?? ''
  if (!BOX.test(box)) return Promise.resolve(false)
  const target = `box:${box}`
  return new Promise(resolve => {
    const d = el('dialog', 'consent login surface')
    d.setAttribute('aria-label', `Sign ${visible(name)} in for this session`)
    const status = el('p', 'small', describe(needs.signIn ?? null))
    status.setAttribute('role', 'status')
    const row = needs.code ? new Row(post, target, `${visible(name)} in this session’s container`) : null
    const done = (answer: boolean) => {
      row?.cancel()
      d.close()
      d.remove()
      resolve(answer)
    }
    const check = async () => {
      status.textContent = 'Checking…'
      const a = await call(post, { step: 'status', target })
      const each = Array.isArray(a.body.each) ? a.body.each as unknown[] : []
      const first = each[0] !== null && typeof each[0] === 'object' ? each[0] as Record<string, unknown> : {}
      const s = signInOf(first.signIn)
      status.textContent = a.ok ? (row ? '' : describe(s)) : typeof a.body.message === 'string' ? visible(a.body.message) : 'The engine didn’t answer.'
      row?.show(s)
    }
    const parts: Node[] = [
      el('h2', 'label', `Sign ${visible(name)} in for this session`),
      el('p', 'small', `Each session runs in its own container, with its own sign-in. ${visible(name)} there needs one before the session starts. The container stays up while you sign in.`),
    ]
    if (row) {
      status.textContent = ''
      row.show(needs.signIn ?? null)
      parts.push(row.node, status)
    } else {
      parts.push(status)
      if (needs.step) parts.push(el('p', 'small muted', 'Run this in a terminal, then open the link it prints and enter the code it shows.'), commandBlock(needs.step))
    }
    const actions = el('div', 'row consent-actions')
    actions.append(button('Check again', 'ghost small', () => void check()), el('span', 'spacer'), button('Cancel', 'ghost small', () => done(false)), button('Continue', 'primary small', () => done(true)))
    parts.push(actions)
    d.append(...parts)
    d.addEventListener('cancel', e => {
      e.preventDefault()
      done(false)
    })
    document.body.append(d)
    d.showModal()
  })
}
