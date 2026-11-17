import { api } from '../api.ts'
import { button, el, emit, shown, visible, whileBusy } from '../lib/dom.ts'
import { shortSha } from '../lib/format.ts'
import { haltView } from './halt.ts'
import { when } from './usage.ts'

export type RulesLink = 'allow-rules' | 'egress' | 'team' | 'away' | 'exceptions'

export interface RulesView {
  sid: string
  name: string
  claude: boolean
  repo: string
  path: string
  box: string
}

type Answer = { ok: boolean; body: Record<string, unknown> }

const FIXED: { what: string; why: string; claude?: boolean }[] = [
  { what: 'Reach a built-in git host or one on the deny list', why: 'Egress always refuses GitHub, githubusercontent.com, GitLab, Bitbucket and the hosts on its deny list. Another git host you add under Network access is reachable like any allowed host.' },
  { what: 'Get the git host token', why: 'The token stays in the engine and never reaches the session’s container; the engine pushes with it, only through the gate. The agent can read its own container’s sign-in, a key you pasted for it included.' },
  { what: 'Run a push-shaped command', why: 'Claude Code’s plugin refuses it and tells the agent to call factory_push.', claude: true },
  { what: 'Hand the work to a vendor’s cloud', why: 'Commands such as claude --remote and codex cloud are refused, as the agent typed them; a script that runs one gets past this.', claude: true },
  { what: 'Reach another session', why: 'Each session’s container has its own network, home and workspace folder, and the engine ties every connection to one container.' },
  { what: 'Decide for you', why: 'Nothing from the session’s container counts as an answer to a card, a verdict or a push.' },
]

const NOT_CLAUDE = 'This session’s agent isn’t Claude Code, so the gate’s command and write checks (push-shaped commands, cloud hand-off commands, protected paths) don’t apply to it: only its pushes go through the gate.'

const PLACES: Record<string, string> = { clone: 'the clone folder, shared by the project’s sessions', worktree: 'this session’s worktree', user: 'the agent’s user settings in this session’s container' }

const text = (v: unknown) => (typeof v === 'string' ? v : '')
const strings = (v: unknown) => (Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [])
const records = (v: unknown) => (Array.isArray(v) ? (v as unknown[]).filter((x): x is Record<string, unknown> => !!x && typeof x === 'object' && !Array.isArray(x)) : [])
const record = (v: unknown) => (v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {})

async function call(route: string, body: unknown): Promise<Answer> {
  const res = await api(route, body).catch(() => null)
  if (!res) return { ok: false, body: { message: 'The engine didn’t answer.' } }
  const parsed: unknown = await res.json().catch(() => ({}))
  return { ok: res.ok, body: record(parsed) }
}

function failed(a: Answer, fallback: string): HTMLElement {
  return el('p', 'small warn', text(a.body.message) ? visible(text(a.body.message)) : fallback)
}

class SfRules extends HTMLElement {
  #view: RulesView | null = null
  #details = el('details', 'rules-panel')
  #body = el('div', 'rules-body')
  #status = el('p', 'small muted')
  #loaded = ''
  #pending = ''
  #gen = 0

  constructor() {
    super()
    const summary = el('summary', 'label', 'What this session may and may not do')
    summary.dataset.key = 'rules:summary'
    this.#status.setAttribute('role', 'status')
    const refresh = button('Refresh', 'ghost small', () => void whileBusy(refresh, () => this.#load(true)), 'button', 'rules:refresh')
    const head = el('div', 'row rules-head')
    head.append(el('p', 'small muted', 'What the engine holds for this session now, read only. Each item you can change links to where it changes; the fixed refusals have no such link.'), refresh)
    this.#details.append(summary, head, this.#status, this.#body)
    this.#details.addEventListener('toggle', () => {
      if (this.#details.open) void this.#load(false)
    })
  }

  connectedCallback() {
    if (!this.#details.isConnected) this.append(this.#details)
  }

  set view(v: RulesView | null) {
    const same = v && this.#view && v.sid === this.#view.sid && v.repo === this.#view.repo && v.box === this.#view.box && v.claude === this.#view.claude
    this.#view = v
    this.hidden = !v
    if (same) return
    this.#gen++
    this.#pending = ''
    this.#loaded = ''
    this.#body.replaceChildren()
    this.#status.textContent = ''
    if (v && this.#details.open) void this.#load(false)
  }

  open() {
    this.#details.open = true
    void this.#load(false)
    this.scrollIntoView({ block: 'start' })
    this.querySelector<HTMLElement>('summary')?.focus()
  }

  #link(words: string, to: RulesLink, what: string): HTMLButtonElement {
    const b = button(words, 'link small', () => emit(this, 'sf-rules-open', { to }), 'button', `rules:link:${to}`)
    b.setAttribute('aria-label', `${words}: ${what}`)
    return b
  }

  #section(title: string, ...nodes: Node[]): HTMLElement {
    const s = el('section', 'rules-section')
    s.append(el('h3', 'small rules-title', title), ...nodes)
    return s
  }

  async #load(force: boolean) {
    const v = this.#view
    if (!v || (!force && (this.#loaded === v.sid || this.#pending === v.sid))) return
    const gen = ++this.#gen
    this.#pending = v.sid
    this.#status.textContent = 'Reading what the engine holds…'
    const [rules, egress, team, exceptions] = await Promise.all([
      v.repo ? call('/api/allow-rules', { op: 'list', repo: v.repo }) : Promise.resolve<Answer>({ ok: false, body: { message: 'This session isn’t in a project of your list.' } }),
      call('/api/egress', { op: 'read' }),
      v.path ? call('/api/team', { path: v.path, op: 'read' }) : Promise.resolve<Answer>({ ok: false, body: { message: 'This session isn’t in a project of your list.' } }),
      call('/api/exceptions', {}),
    ])
    if (gen !== this.#gen || this.#view?.sid !== v.sid) return
    this.#pending = ''
    this.#loaded = v.sid
    this.#status.textContent = `Read at ${when(Date.now())}.`
    this.#body.replaceChildren(this.#fixed(v), this.#saved(v, rules), this.#hosts(egress), this.#limits(v, team), this.#away(v), this.#register(v, exceptions))
  }

  #fixed(v: RulesView): HTMLElement {
    const ul = el('ul', 'list rules-list')
    for (const f of FIXED) {
      if (f.claude && !v.claude) continue
      const li = el('li')
      li.append(el('strong', 'small', `Never: ${f.what}.`), ' ', el('span', 'small muted', f.why))
      ul.append(li)
    }
    const parts: Node[] = [ul]
    if (!v.claude) parts.push(el('p', 'small warn', NOT_CLAUDE))
    parts.push(el('p', 'small muted', 'These are part of how Software Factory works, so the page offers no way to change them.'))
    return this.#section('Fixed refusals', ...parts)
  }

  #saved(v: RulesView, a: Answer): HTMLElement {
    const link = this.#link('Change on Projects', 'allow-rules', 'the saved allow rules of this project')
    if (!a.ok) return this.#section('Saved “don’t ask again” rules', failed(a, 'The saved rules didn’t load.'), link)
    const mine = records(a.body.rules).filter(r => text(r.rule) && (text(r.place) === 'clone' || (!!v.box && text(r.box) === v.box)))
    const ul = el('ul', 'list rules-list')
    if (!mine.length) ul.append(el('li', 'small muted', a.body.idle === true ? 'No session of this project is running, so no session’s container was read; none was found in the clone folder.' : 'None saved.'))
    for (const r of mine) {
      const li = el('li', 'small')
      li.append(el('code', '', visible(text(r.rule))), ' ', el('span', 'muted', `in ${PLACES[text(r.place)] ?? visible(text(r.place))}`))
      const wide = strings(r.wide)
      if (wide.length) li.append(' ', el('span', 'tag warn', 'wide'), el('span', 'small warn', ` ${wide.map(visible).join('; ')}`))
      ul.append(li)
    }
    return this.#section('Saved “don’t ask again” rules', ul, link)
  }

  #hosts(a: Answer): HTMLElement {
    const link = this.#link('Change under Network access', 'egress', 'the hosts sessions may reach')
    if (!a.ok) return this.#section('Hosts the session may reach', failed(a, 'Network access didn’t load.'), link)
    const lists = record(a.body.lists)
    const allow = strings(record(lists.agents).allow)
    const now = Number(a.body.now) || Math.floor(Date.now() / 1000)
    const added = records(a.body.added).filter(h => text(h.host))
    const parts: Node[] = []
    const ul = el('ul', 'list rules-hosts')
    if (!allow.length) ul.append(el('li', 'small muted', 'No host: every connection is refused.'))
    for (const h of allow) {
      const extra = added.find(x => text(x.host) === h)
      const li = el('li', 'small')
      li.append(el('code', '', visible(h)))
      if (extra) {
        const expires = Number(extra.expires) || 0
        li.append(' ', el('span', 'tag', 'added by you'), ' ', el('span', 'muted', !expires ? 'no expiry' : expires <= now ? 'expired' : `until ${new Date(expires * 1000).toLocaleString()}`))
      }
      ul.append(li)
    }
    parts.push(ul)
    const gw = record(a.body.gateway)
    if (text(gw.url)) parts.push(el('p', 'small warn', `A company LLM gateway, ${visible(text(gw.host))}, is set${gw.agents === true ? ' for the sessions' : ''}.`))
    parts.push(el('p', 'small muted', 'Every session shares this list; egress tells sessions apart only by their container.'), link)
    return this.#section('Hosts the session may reach', ...parts)
  }

  #limits(v: RulesView, a: Answer): HTMLElement {
    const link = this.#link('Review the team file', 'team', 'the protect list in the team file on main')
    if (!a.ok) return this.#section('Paths it must ask before writing', failed(a, 'The team file didn’t load.'), link)
    const setup = record(a.body.setup)
    const protect = strings(setup.protect)
    const approved = record(a.body.approved)
    const head = text(a.body.head)
    const parts: Node[] = []
    if (typeof a.body.text !== 'string') parts.push(el('p', 'small muted', 'The main branch has no software-factory.json, so no path is protected.'))
    else {
      const applies = !!a.body.approved && approved.same === true
      parts.push(el('p', `small ${applies ? 'muted' : 'warn'}`, `From software-factory.json on main${head ? ` at ${shortSha(head)}` : ''}. ${applies ? 'You approved this version, so it applies.' : !a.body.approved ? 'You haven’t approved it, so it doesn’t apply yet.' : 'It changed since you approved it, so it applies once you approve this version.'}`))
      const ul = el('ul', 'list rules-list')
      if (!protect.length) ul.append(el('li', 'small muted', 'No path is protected.'))
      for (const p of protect) ul.append(el('li', 'small mono', shown(p)))
      parts.push(ul, el('p', `small ${v.claude ? 'muted' : 'warn'}`, v.claude ? 'Claude Code shows a card before each write to these paths, with no “don’t ask again”. A push is never held by path.' : 'This session’s agent isn’t Claude Code, so it writes these paths without a card. A push is never held by path.'))
    }
    parts.push(link)
    return this.#section('Paths it must ask before writing', ...parts)
  }

  #away(v: RulesView): HTMLElement {
    const h = haltView()
    const on = h.awayAll || h.away.includes(v.sid)
    const parts: Node[] = [el('p', `small ${on ? 'warn' : ''}`, on ? `Away mode is on${h.awayAll ? ' for every session' : ' for this session'}: each new permission card is denied with a note to the agent.` : 'Away mode is off: permission cards wait for you.')]
    if (h.mode === 'stopped') parts.push(el('p', 'small bad', 'Stop everything is on: this session can’t start, wake or take messages until Release.'))
    else if (h.mode === 'paused') parts.push(el('p', 'small warn', 'Pause all is on: no new turn starts and no automatic review or push runs until Release.'))
    parts.push(this.#link('Change with Away in the header', 'away', 'away mode'))
    return this.#section('Away mode', ...parts)
  }

  #register(v: RulesView, a: Answer): HTMLElement {
    const link = this.#link('Open the register of exceptions', 'exceptions', 'every exception you made')
    if (!a.ok) return this.#section('Exceptions you made for this session', failed(a, 'The register didn’t load.'), link)
    const mine = records(a.body.items).filter(i => text(i.sid) === v.sid)
    const KIND: Record<string, string> = { 'allow-push': 'Push allowed with items missing', override: 'Verdict overridden', 'allow-file': 'Flagged file allowed', 'allow-commit': 'Flagged commit allowed', 'cap-reached': 'Cap reached', 'cap-answer': 'Cap card answered', 'usage-answer': 'Kept running near a usage limit' }
    const ul = el('ul', 'list rules-list')
    if (!mine.length) ul.append(el('li', 'small muted', 'None recorded for this session.'))
    for (const i of mine.slice(0, 10)) {
      const li = el('li', 'small')
      const at = Number(i.time) || 0
      li.append(el('strong', '', KIND[text(i.kind)] ?? visible(text(i.kind))), ' ', el('span', 'muted', at ? new Date(at).toLocaleString() : ''), text(i.reason) ? el('span', '', ` · “${visible(text(i.reason))}”`) : '')
      ul.append(li)
    }
    if (mine.length > 10) ul.append(el('li', 'small muted', `And ${mine.length - 10} more in the register.`))
    return this.#section('Exceptions you made for this session', ul, link)
  }
}

customElements.define('sf-rules', SfRules)

declare global {
  interface HTMLElementTagNameMap {
    'sf-rules': SfRules
  }
  interface HTMLElementEventMap {
    'sf-rules-open': CustomEvent<{ to: RulesLink }>
  }
}
