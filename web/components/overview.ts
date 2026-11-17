import { button, el, emit, field, focusedKey, refocus, remembered, statusLine, visible } from '../lib/dom.ts'
import { agentWord, label, plural, signInStep } from '../lib/format.ts'
import { bySessionOrder, isRunning, loginNeeded, sessionKey, sessionLink, sessionRow, sessionTable, sharing, titled } from './session-list.ts'
import type { AskEvent, CardEvent, OverviewSession, PermissionEvent, SessionInfo } from '../types.ts'

const CARD_WORDS: Record<string, ((c: CardEvent) => string) | undefined> = { permission: c => `wants to run ${(c as PermissionEvent).tool}`, ask: c => `asks: ${(c as AskEvent).questions?.[0]?.question ?? 'a question'}`, plan: () => 'has a plan ready' }

const SETTLED = new Set(['done', 'stopped', 'blocked', 'failed'])

export function warnings(s: SessionInfo, all: SessionInfo[]): string[] {
  const out: string[] = []
  if (s.verified && s.level === 'locked' && s.sandboxed === false) out.push(`Locked, not sandboxed: every check since this run started passed, but a check failed or couldn't run in an earlier run, so the session's work isn't counted as sandboxed and its pushes are held on the sandbox item.`)
  if (s.verified && s.level === 'held') out.push(`Held: every push from this session is held and no new session starts, because ${s.levelReason || 'the sandbox check hasn\'t passed'}.${s.levelDropped ? ' This dropped from Locked after the fact.' : ''}`)
  if (loginNeeded(s)) out.push(`Claude Code couldn't use this session's login: its last request failed authentication. Sign in again inside the session's container (${signInStep(s.agent, s.box)}); this page never signs in for you.`)
  if (s.signInWait) out.push(`Waiting for sign-in: ${agentWord(s.agent)} in this session's container isn't signed in. Press Wake to open the session's sign-in panel, sign in there, then continue.`)
  const others = sharing(s, all)
  if (others.length) out.push(`${others.join(', ')} ${others.length === 1 ? 'is' : 'are'} also in ${s.checkout}, with no worktree between them, and at least one of these sessions is working, so their edits can collide.`)
  return out
}

export type SessionFilter = 'running' | 'waiting' | 'held' | 'stopped' | 'done'

export const SESSION_FILTERS: { id: SessionFilter; label: string; test: (s: OverviewSession) => boolean }[] = [
  { id: 'running', label: 'Running', test: s => isRunning(s) },
  { id: 'waiting', label: 'Waiting for you', test: s => (s.waiting?.length ?? 0) > 0 || !!s.needs || !!s.signInWait },
  { id: 'held', label: 'Held push', test: s => s.push?.result === 'held' && !s.push.running },
  { id: 'stopped', label: 'Stopped', test: s => !isRunning(s) && s.state !== 'done' },
  { id: 'done', label: 'Done', test: s => !isRunning(s) && s.state === 'done' },
]

const MAX_QUERY = 200

export function sessionMatches(s: OverviewSession, query: string): boolean {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean)
  if (!words.length) return true
  const hay = [label(s), s.name, s.project, s.branch, s.lineage?.branch, s.push?.branch].filter(Boolean).join('\n').toLowerCase()
  return words.every(w => hay.includes(w))
}

function readFragment(): { query: string; states: Set<SessionFilter> } {
  const params = new URLSearchParams(location.hash.slice(1))
  const known = new Set(SESSION_FILTERS.map(f => f.id))
  const states = new Set((params.get('state') ?? '').split(',').filter((x): x is SessionFilter => known.has(x as SessionFilter)))
  return { query: (params.get('q') ?? '').slice(0, MAX_QUERY), states }
}

function writeFragment(query: string, states: Set<SessionFilter>) {
  const params = new URLSearchParams(location.hash.slice(1))
  if (query) params.set('q', query)
  else params.delete('q')
  if (states.size) params.set('state', SESSION_FILTERS.map(f => f.id).filter(id => states.has(id)).join(','))
  else params.delete('state')
  const hash = params.toString()
  const url = `${location.pathname}${location.search}${hash ? `#${hash}` : ''}`
  if (url !== `${location.pathname}${location.search}${location.hash}`) history.replaceState(history.state, '', url)
}

class SfOverview extends HTMLElement {
  #sessions: OverviewSession[] = []
  #supervisor = ''
  #open = new Map<string, boolean>()
  #key = ''
  #query = ''
  #states = new Set<SessionFilter>()
  #tools = el('div', 'session-filter')
  #search = el('input', 'textbox')
  #chips = el('div', 'filter-chips')
  #clear = button('Clear', 'ghost small', () => this.clear(), 'button', 'filter:clear')
  #shown = statusLine('small muted filter-count')
  #body = el('div')

  constructor() {
    super()
    const from = readFragment()
    this.#query = from.query
    this.#states = from.states
    this.#search.type = 'search'
    this.#search.id = 'session-search'
    this.#search.maxLength = MAX_QUERY
    this.#search.autocomplete = 'off'
    this.#search.spellcheck = false
    this.#search.placeholder = 'Name, branch or project'
    this.#search.value = this.#query
    this.#search.dataset.key = 'filter:search'
    this.#search.addEventListener('input', () => this.#set(this.#search.value, this.#states))
    this.#search.addEventListener('keydown', e => {
      if (e.key === 'Escape' && this.#search.value) {
        e.preventDefault()
        e.stopPropagation()
        this.#set('', this.#states)
      }
    })
    const search = field('Search sessions', this.#search, undefined, 'filter-search')
    this.#chips.setAttribute('role', 'group')
    this.#chips.setAttribute('aria-label', 'Show sessions that are')
    this.#tools.setAttribute('role', 'search')
    this.#tools.setAttribute('aria-label', 'Sessions')
    const row = el('div', 'filter-row')
    row.append(this.#chips, this.#clear)
    this.#tools.append(search, row, this.#shown)
  }

  #followHash = () => {
    const next = readFragment()
    if (next.query === this.#query && [...next.states].join() === [...this.#states].join()) return
    this.#query = next.query
    this.#states = next.states
    this.#search.value = next.query
    this.#key = ''
    this.#render()
  }

  connectedCallback() {
    addEventListener('hashchange', this.#followHash)
    this.#followHash()
  }

  disconnectedCallback() {
    removeEventListener('hashchange', this.#followHash)
  }

  get filtered(): boolean {
    return !!this.#query.trim() || this.#states.size > 0
  }

  focusSearch() {
    this.#search.focus()
    this.#search.select()
  }

  clear() {
    this.#set('', new Set())
    this.#search.focus()
  }

  #set(query: string, states: Set<SessionFilter>) {
    this.#query = query.slice(0, MAX_QUERY)
    this.#states = states
    if (this.#search.value !== this.#query) this.#search.value = this.#query
    writeFragment(this.#query, this.#states)
    this.#key = ''
    this.#render()
  }

  set supervisor(value: string) {
    this.#supervisor = value || ''
  }

  set sessions(value: OverviewSession[]) {
    this.#sessions = value || []
    const now = Date.now()
    const key = JSON.stringify([
      this.#supervisor,
      sessionKey(this.#sessions, now),
      this.#sessions.map(s => [s.project, s.branch, s.lineage?.branch, s.push?.result, s.push?.running, s.signInWait, (s.waiting || []).map(c => [c.id, c.type, c.agentId, c.readOnly, c.ts, c.type === 'permission' ? c.tool : c.type === 'ask' ? c.questions?.[0]?.question : ''])]),
    ])
    if (key === this.#key) return
    this.#key = key
    this.#render()
  }

  #paintTools(all: OverviewSession[], matching: OverviewSession[]) {
    const focused = document.activeElement instanceof HTMLElement && this.#chips.contains(document.activeElement) ? document.activeElement.dataset.key ?? '' : ''
    this.#chips.replaceChildren(...SESSION_FILTERS.map(f => {
      const n = all.filter(s => sessionMatches(s, this.#query) && f.test(s)).length
      const on = this.#states.has(f.id)
      const chip = button('', `ghost small chip${on ? ' on' : ''}`, () => {
        const next = new Set(this.#states)
        if (on) next.delete(f.id)
        else next.add(f.id)
        this.#set(this.#query, next)
      }, 'button', `filter:${f.id}`)
      chip.setAttribute('aria-pressed', String(on))
      chip.append(el('span', '', f.label), el('span', 'chip-count', String(n)))
      chip.title = `${n} ${plural(n, 'session')} ${f.label.toLowerCase()}`
      return chip
    }))
    if (focused) this.#chips.querySelector<HTMLElement>(`[data-key="${CSS.escape(focused)}"]`)?.focus()
    this.#clear.hidden = !this.filtered
    this.#shown.textContent = this.filtered ? `Showing ${matching.length} of ${all.length} ${plural(all.length, 'session')}.` : ''
    this.#tools.hidden = !all.length
  }

  #render() {
    const focused = focusedKey(this)
    const all = this.#sessions
    const visibleSessions = all.filter(s => sessionMatches(s, this.#query) && (!this.#states.size || SESSION_FILTERS.some(f => this.#states.has(f.id) && f.test(s))))
    this.#paintTools(all, visibleSessions)
    const shownSet = new Set(visibleSessions.map(s => s.sid))
    const cards = all.flatMap(s => (s.waiting || []).map(c => ({ s, c }))).sort((a, b) => (a.c.ts || 0) - (b.c.ts || 0))
    const shownCards = cards.filter(x => shownSet.has(x.s.sid))
    const running = all.filter(isRunning).sort(bySessionOrder)
    const other = all.filter(s => !running.includes(s)).sort((a, b) => (b.startedAt || 0) - (a.startedAt || 0))
    const unfinished = other.filter(s => s.kind === 'background' && s.state && !SETTLED.has(s.state)).length
    const parts: HTMLElement[] = []
    if (this.#supervisor === 'stopped' && unfinished) parts.push(el('p', 'notice warn', `Claude Code's background supervisor isn't running in the agent container, and ${unfinished === 1 ? 'one Claude Code session' : `${unfinished} Claude Code sessions`} stopped before finishing. Open one and send it a message, which wakes it.`))
    else if (this.#supervisor === 'stopped') parts.push(el('p', 'small muted', "Claude Code's background supervisor isn't running in the agent container. That's normal: it starts with the next Claude Code session."))
    if (shownCards.length) {
      const ul = el('ul', 'boxlist sessions')
      for (const { s, c } of shownCards) ul.append(sessionLink(s.sid, `wait:${s.sid}:${c.id}`, [el('span', 'dot need'), titled(visible(label(s))), el('span', 'what secondary', visible(`${c.agentId ? 'A subagent' : agentWord(s.agent)} ${(CARD_WORDS[c.type] || (() => 'needs you'))(c)}`)), el('span', 'when secondary', c.readOnly ? 'read-only' : '')]))
      parts.push(el('h2', 'label', `Waiting on you (${shownCards.length})`), ul)
    }
    const hiddenWaiting = new Set(cards.filter(x => !shownSet.has(x.s.sid)).map(x => x.s.sid)).size
    if (hiddenWaiting) {
      const more = el('p', 'small warn', `${hiddenWaiting} more ${plural(hiddenWaiting, 'session')} the search or filters hide ${hiddenWaiting === 1 ? 'is' : 'are'} waiting for you. `)
      more.append(button('Clear the search and filters', 'link', () => this.clear()))
      parts.push(more)
    }
    const runningShown = running.filter(s => shownSet.has(s.sid))
    const otherShown = other.filter(s => shownSet.has(s.sid) && s.state !== 'done')
    const doneShown = other.filter(s => shownSet.has(s.sid) && s.state === 'done')
    if (runningShown.length) parts.push(el('h2', 'label', `Running (${runningShown.length})`), sessionTable(runningShown.map(s => sessionRow(s, all, `run:${s.sid}`)), '', 'Running sessions'))
    else if (!this.filtered) {
      const projects = el('a', 'link', 'Projects')
      projects.href = '/'
      projects.dataset.view = 'projects'
      const none = el('p', 'small muted', 'No session is running. Start one from a project in ')
      none.append(projects, '.')
      parts.push(none)
    }
    if (otherShown.length) {
      const ul = sessionTable(otherShown.map(s => sessionRow(s, all, `stop:${s.sid}`)), '', 'Stopped sessions')
      const summary = el('summary', 'label', `Stopped (${otherShown.length})`)
      summary.dataset.key = 'summary:stopped'
      const d = remembered(this.#open, 'stopped', false, summary, ul)
      if (this.filtered) d.open = true
      d.className = 'ended'
      parts.push(d)
    }
    if (doneShown.length) {
      const ul = sessionTable(doneShown.map(s => sessionRow(s, all, `done:${s.sid}`)), '', 'Done sessions')
      const summary = el('summary', 'label', `Done (${doneShown.length})`)
      summary.dataset.key = 'summary:done'
      const d = remembered(this.#open, 'done', false, summary, ul)
      if (this.filtered) d.open = true
      d.className = 'ended'
      parts.push(d)
    }
    if (this.filtered && !visibleSessions.length) {
      const none = el('p', 'small muted', 'No session matches. ')
      none.append(button('Clear the search and filters', 'link', () => this.clear()))
      parts.push(none)
    }
    this.#body.replaceChildren(...parts)
    if (this.firstChild !== this.#tools) this.replaceChildren(this.#tools, this.#body)
    const waiting = new Set(cards.map(x => x.s.sid))
    emit(this, 'sf-counts', { waiting: waiting.size, working: running.filter(s => s.busy && !waiting.has(s.sid)).length, stopped: unfinished })
    refocus(this, focused)
  }
}

customElements.define('sf-overview', SfOverview)

declare global {
  interface HTMLElementTagNameMap {
    'sf-overview': SfOverview
  }
}
