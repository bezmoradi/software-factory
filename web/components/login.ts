import { button, el, visible } from '../lib/dom.ts'
import type { SignIn } from '../types.ts'
import './accordion.ts'

type Post = (path: string, body: unknown) => Promise<Response>
type Target = 'agents' | 'reviewers'

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

function describe(s: SignIn | null): string {
  if (!s) return 'Not checked yet.'
  return s.method === 'not-signed-in' ? 'Not signed in.' : `Signed in: ${visible(s.words)}.`
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
  private readonly post: Post
  private readonly target: Target

  constructor(post: Post, target: Target, title: string) {
    this.post = post
    this.target = target
    this.say.setAttribute('role', 'status')
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
    const [keyLabel, key] = field('api-key', 'API key from the Claude Console', true)
    this.key = key
    this.apiKey.append(
      el('p', 'small muted', `Billed per use. Software Factory saves the key in the ${target === 'agents' ? 'agent' : 'reviewer'} container for Claude Code and never shows it again.`),
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
    const m = this.signIn?.method ?? ''
    this.kinds.items = [
      { id: 'subscription', title: 'Subscription', note: 'Claude Pro, Max or Ultra', state: m === 'subscription' ? 'In use' : '', good: true, panel: this.subscription },
      { id: 'api-key', title: 'API key', note: 'Claude Console', state: PER_USE.includes(m) ? 'In use' : '', good: true, panel: this.apiKey },
    ]
  }

  show(s: SignIn | null) {
    this.signIn = s
    this.status.textContent = describe(s)
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
  return s ? s.method !== 'not-signed-in' : null
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

export async function claudeLogin(post: Post, closed?: () => void): Promise<void> {
  const d = el('dialog', 'consent login surface')
  d.setAttribute('aria-label', 'Sign Claude Code in')
  const agents = new Row(post, 'agents', 'Claude Code for the agents')
  const reviewers = new Row(post, 'reviewers', 'Claude Code for the reviewers')
  const close = () => {
    agents.cancel()
    reviewers.cancel()
    d.close()
    d.remove()
    closed?.()
  }
  const actions = el('div', 'row consent-actions')
  actions.append(el('span', 'spacer'), button('Close', 'ghost small', close))
  d.append(
    el('h2', 'label', 'Sign Claude Code in'),
    el('p', 'small', 'The agents and the reviewers each run their own Claude Code, so each signs in once, with a subscription or an API key. Sessions and reviews then start only on the kind you signed in with here.'),
    agents.node,
    reviewers.node,
    actions,
  )
  d.addEventListener('cancel', e => {
    e.preventDefault()
    close()
  })
  document.body.append(d)
  d.showModal()
  const a = await call(post, { step: 'status' })
  agents.show(signInOf(a.body.agents))
  if (a.body.reviewer === true) reviewers.show(signInOf(a.body.reviewers))
  else reviewers.node.remove()
}
