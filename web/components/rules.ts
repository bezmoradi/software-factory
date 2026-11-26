import { request, type EngineAnswer } from '../api.ts'
import { button, el, emit, say, shown, statusLine, visible, whileBusy } from '../lib/dom.ts'
import { shortSha } from '../lib/format.ts'
import { haltView } from './halt.ts'
import { when } from './usage.ts'
import { emptyState } from '../lib/page.ts'
import { CHEAP_EVERY_MS, whileOpen, type WhileOpen } from '../lib/refresh.ts'
import { toast } from './toasts.ts'

export type RulesLink = 'egress' | 'team' | 'away' | 'exceptions'

export interface RulesView {
  sid: string
  name: string
  claude: boolean
  repo: string
  path: string
  box: string
}

type Answer = EngineAnswer

const FIXED: { what: string; why: string; claude?: boolean }[] = [
  { what: 'Reach a git host Software Factory knows', why: 'Egress always refuses GitHub, githubusercontent.com, GitLab, Bitbucket, the hosts on its deny list and your projects’ hosts, and the engine refuses every grant and credential that would open one. A git host it can’t recognise, such as a mirror or another name for one, is reachable like any host you add under Network access.' },
  { what: 'Get the git host token', why: 'The token stays in the engine and never reaches the session’s container; the engine pushes with it, only through the gate. The agent can read its own container’s sign-in, a key you pasted for it included.' },
  { what: 'Run a push-shaped command', why: 'Claude Code’s plugin refuses it and tells the agent to call factory_push.', claude: true },
  { what: 'Hand the work to a vendor’s cloud', why: 'Commands such as claude --remote are refused, as the agent typed them; a script that runs one gets past this.', claude: true },
  { what: 'Reach another session', why: 'Each session’s container has its own network, home and workspace folder, and the engine ties every connection to one container.' },
  { what: 'Decide for you', why: 'Nothing from the session’s container counts as an answer to a card, a verdict or a push.' },
]

const NOT_CLAUDE = 'This session’s agent isn’t Claude Code, so the gate’s command and write checks (push-shaped commands, cloud hand-off commands, protected paths) don’t apply to it: only its pushes go through the gate.'

const text = (v: unknown) => (typeof v === 'string' ? v : '')
const strings = (v: unknown) => (Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [])
const records = (v: unknown) => (Array.isArray(v) ? (v as unknown[]).filter((x): x is Record<string, unknown> => !!x && typeof x === 'object' && !Array.isArray(x)) : [])
const record = (v: unknown) => (v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {})

const OUTSIDE: Answer = { ok: false, status: 0, error: '', field: '', message: 'This session isn’t in a project of your list.', body: {} }

function failed(a: Answer): HTMLElement {
  return el('p', 'small warn', a.message)
}

class SfRules extends HTMLElement {
  #view: RulesView | null = null
  #details = el('details', 'rules-panel')
  #body = el('div', 'rules-body')
  #status = statusLine('small muted')
  #removeSaid = statusLine('small')
  #poll: WhileOpen | null = null

  constructor() {
    super()
    const summary = el('summary', 'label', 'What this session may and may not do')
    summary.dataset.key = 'rules:summary'
    const head = el('div', 'row rules-head')
    head.append(el('p', 'small muted', 'What the engine holds for this session now. Remove a saved rule here; each other item you can change links to where it changes, and the fixed refusals have no such link.'))
    this.#details.append(summary, head, this.#status, this.#body)
    this.#details.addEventListener('toggle', () => {
      if (!this.#details.open) this.#stop()
      else if (!this.#poll?.live) this.#load()
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
    this.#stop()
    this.#body.replaceChildren()
    this.#status.textContent = ''
    if (v && this.#details.open) this.#load()
  }

  open() {
    this.#details.open = true
    if (!this.#poll?.live) this.#load()
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

  #stop() {
    this.#poll?.stop()
    this.#poll = null
  }

  #load() {
    const v = this.#view
    if (!v) return
    this.#stop()
    this.#status.textContent = 'Reading what the engine holds…'
    let team: Promise<Answer> | null = null
    const current = () => this.#view === v && this.#details.open
    this.#poll = whileOpen({
      root: this.#body,
      everyMs: CHEAP_EVERY_MS,
      open: () => this.#details.open && this.checkVisibility(),
      read: async () => {
        team ??= v.path ? request('/api/team', { path: v.path, op: 'read' }) : Promise.resolve(OUTSIDE)
        const [rules, egress, teamFile, exceptions] = await Promise.all([
          v.repo ? request('/api/allow-rules', { op: 'list', repo: v.repo }) : Promise.resolve(OUTSIDE),
          request('/api/egress', { op: 'read' }),
          team,
          request('/api/exceptions', {}),
        ])
        if (current()) this.#status.textContent = `Read at ${when(Date.now())}.`
        return { rules, egress, teamFile, exceptions }
      },
      show: a => this.#body.replaceChildren(this.#fixed(v), this.#saved(v, a.rules), this.#hosts(a.egress), this.#limits(v, a.teamFile), this.#away(v), this.#register(v, a.exceptions)),
    })
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
    const title = 'Saved “don’t ask again” rules'
    if (!a.ok) return this.#section(title, failed(a))
    const mine = records(a.body.rules).filter(r => text(r.rule) && (text(r.place) === 'clone' || (!!v.box && text(r.box) === v.box)))
    const ul = el('ul', 'list rules-list')
    if (!mine.length) ul.append(emptyState(a.body.idle === true ? 'No session of this project is running, so no session’s container was read; none was found in the clone folder.' : 'None saved.', { tag: 'li' }))
    for (const r of mine) {
      const li = el('li', 'row text-row')
      const what = el('span', 'small')
      what.append(el('code', '', visible(text(r.rule))), ' ', el('span', 'muted', `in ${visible(text(r.placeLabel) || text(r.place))}`))
      const wide = strings(r.wide)
      if (wide.length) what.append(' ', el('span', 'tag warn', 'wide'), el('span', 'small warn', ` ${wide.map(visible).join('; ')}`))
      const key = `rules:remove:${text(r.box)}:${text(r.place)}:${text(r.worktree)}:${text(r.rule)}`
      const remove = button('Remove', 'ghost small', () => void this.#remove(v, r, remove), 'button', key)
      remove.setAttribute('aria-label', `Remove the rule ${visible(text(r.rule))} from ${visible(text(r.path))}`)
      li.append(what, el('span', 'spacer'), remove)
      ul.append(li)
    }
    return this.#section(title, ul, this.#removeSaid)
  }

  async #remove(v: RulesView, r: Record<string, unknown>, b: HTMLButtonElement) {
    const body: Record<string, unknown> = { op: 'remove', repo: v.repo, box: text(r.box), place: text(r.place), rule: text(r.rule) }
    if (text(r.worktree)) body.worktree = text(r.worktree)
    say(this.#removeSaid, '')
    const a = await whileBusy(b, () => request('/api/allow-rules', body))
    if (!a.ok) return say(this.#removeSaid, a.message, 'warn')
    toast({ id: `allow-rules:${v.repo}`, title: `Removed ${visible(text(r.rule))} from ${visible(text(r.path))}. The next matching call shows a card again.`, tone: 'ok' })
    void this.#poll?.now()
  }

  #hosts(a: Answer): HTMLElement {
    const link = this.#link('Change under Network access', 'egress', 'the hosts sessions may reach')
    if (!a.ok) return this.#section('Hosts the session may reach', failed(a), link)
    const lists = record(a.body.lists)
    const allow = strings(record(lists.agents).allow)
    const now = Number(a.body.now) || Math.floor(Date.now() / 1000)
    const added = records(a.body.added).filter(h => text(h.host))
    const parts: Node[] = []
    const ul = el('ul', 'list rules-hosts')
    if (!allow.length) ul.append(emptyState('No host: every connection is refused.', { tag: 'li' }))
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
    if (text(gw.url)) parts.push(el('p', 'small warn', `A company LLM gateway, ${visible(text(gw.host))}, is set for the sessions.`))
    parts.push(el('p', 'small muted', 'Every session shares this list; egress tells sessions apart only by their container.'), link)
    return this.#section('Hosts the session may reach', ...parts)
  }

  #limits(v: RulesView, a: Answer): HTMLElement {
    const link = this.#link('Review the team file', 'team', 'the protect list in the team file on main')
    if (!a.ok) return this.#section('Paths it must ask before writing', failed(a), link)
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
      if (!protect.length) ul.append(emptyState('No path is protected.', { tag: 'li' }))
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
    parts.push(this.#link('Change with Away in the header', 'away', 'away mode'))
    return this.#section('Away mode', ...parts)
  }

  #register(v: RulesView, a: Answer): HTMLElement {
    const link = this.#link('Open the register of exceptions', 'exceptions', 'every exception you made')
    if (!a.ok) return this.#section('Exceptions you made for this session', failed(a), link)
    const mine = records(a.body.items).filter(i => text(i.sid) === v.sid)
    const KIND: Record<string, string> = { 'allow-push': 'Push allowed with items missing', override: 'Verdict overridden', 'allow-file': 'Flagged file allowed', 'allow-commit': 'Flagged commit allowed', 'cap-reached': 'Cap reached', 'cap-answer': 'Cap card answered', 'usage-answer': 'Kept running near a usage limit' }
    const ul = el('ul', 'list rules-list')
    if (!mine.length) ul.append(emptyState('None recorded for this session.', { tag: 'li' }))
    for (const i of mine.slice(0, 10)) {
      const li = el('li', 'small')
      const at = Number(i.time) || 0
      const freed = text(i.kind) === 'cap-answer' && text(i.verdict) === 'freed'
      li.append(el('strong', '', freed ? 'Disk cap cleared by the engine’s measurement' : KIND[text(i.kind)] ?? visible(text(i.kind))), ' ', el('span', 'muted', at ? new Date(at).toLocaleString() : ''), text(i.reason) ? el('span', '', ` · “${visible(text(i.reason))}”`) : '')
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
