import { button, el, emit, focusedKey, refocus, remembered, shown, visible } from '../lib/dom.ts'
import { icon } from './icon.ts'
import { age, agentWord, label, modeLabel, plural, signInStep } from '../lib/format.ts'
import type { AskEvent, CardEvent, OverviewSession, PermissionEvent, SessionInfo } from '../types.ts'

const CARD_WORDS: Record<string, ((c: CardEvent) => string) | undefined> = { permission: c => `wants to run ${(c as PermissionEvent).tool}`, ask: c => `asks: ${(c as AskEvent).questions?.[0]?.question ?? 'a question'}`, plan: () => 'has a plan ready' }

const SETTLED = new Set(['done', 'stopped', 'blocked', 'failed'])

const LEVELS: Record<string, { word: string; tone: string } | undefined> = { locked: { word: 'Locked', tone: '' }, held: { word: 'Held', tone: 'warn' }, stopped: { word: 'Stopped', tone: 'bad' } }

export function sessionState(s: SessionInfo): { dot: string; word: string } {
  if (s.disputed) return { dot: 'dot off', word: 'two connections claim this session' }
  if (!s.verified) return { dot: 'dot', word: 'not started by this engine' }
  if (loginNeeded(s)) return { dot: 'dot off', word: 'needs you to sign in again' }
  if (s.signInWait) return { dot: 'dot need', word: 'Waiting for sign-in' }
  if (!s.running && s.queued) return { dot: 'dot busy', word: `waking, ${s.queued} ${plural(s.queued, 'message')} waiting` }
  if (!s.running && s.levelLost) return { dot: 'dot off', word: 'stopped: process lost' }
  if (!s.running && s.stopReason) return { dot: 'dot off', word: `stopped: ${shown(s.stopReason)}` }
  if (!s.running && s.state === 'done') return { dot: 'dot done', word: 'done' }
  if (!s.running) return { dot: 'dot', word: !s.state || s.state === 'stopped' ? 'stopped' : s.state === 'blocked' ? 'stopped, waiting for your answer' : `stopped, was ${shown(s.state)}` }
  if (!s.live) return { dot: 'dot', word: shown(s.waitingFor ? `waiting for ${s.waitingFor}` : s.status || s.state || 'running') }
  if (s.outdated) return { dot: 'dot off', word: 'older plugin version' }
  if (s.needs) return { dot: 'dot need', word: 'needs you' }
  if (s.busy) return { dot: 'dot busy', word: 'working' }
  return { dot: 'dot', word: 'idle' }
}

export const loginNeeded = (s: SessionInfo) => !!s.running && s.live && !!s.login
const active = (s: SessionInfo) => !!s.running || s.live

const inTurn = (s: SessionInfo) => !!s.busy || !!s.needs || s.status === 'busy' || s.state === 'working' || s.state === 'blocked'

export function sharing(s: SessionInfo, all: SessionInfo[]): string[] {
  if (!s.checkout || !active(s)) return []
  const others = all.filter(o => o.sid !== s.sid && active(o) && o.checkout === s.checkout)
  return inTurn(s) || others.some(inTurn) ? others.map(o => label(o)) : []
}

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

function titled(text: string) {
  const name = el('span', 'name')
  name.append(el('span', 'title', text))
  return name
}

export const isRunning = (s: SessionInfo) => !!s.running || (s.live && !s.verified)

export function bySessionOrder(a: SessionInfo, b: SessionInfo): number {
  return Number(isRunning(b)) - Number(isRunning(a)) || Number(b.live) - Number(a.live) || (b.startedAt || 0) - (a.startedAt || 0)
}

export function sessionKey(list: SessionInfo[], now = Date.now()): string {
  return JSON.stringify(list.map(s => [s.sid, s.name, s.cwd, s.kind, s.running, s.live, s.verified, s.disputed, s.outdated, s.controlled, s.mode, s.checkout, s.state, s.status, s.waitingFor, s.queued, s.busy, s.needs, s.login, s.level, s.levelReason, s.sandboxed, s.lineage?.group, s.lineage?.groupKind, age(s.startedAt, now)]))
}

function groupOf(s: SessionInfo, all: SessionInfo[]): string {
  const l = s.lineage
  if (l?.group && (l.groupKind === 'pair' || l.groupKind === 'goal')) return `${l.groupKind}:${l.group}`
  return all.some(o => o.lineage?.groupKind === 'goal' && o.lineage.group === s.sid) ? `goal:${s.sid}` : ''
}

export function grouped(list: SessionInfo[]): SessionInfo[] {
  const out: SessionInfo[] = []
  const seen = new Set<string>()
  const groups = new Map(list.map(s => [s.sid, groupOf(s, list)]))
  for (const s of list) {
    if (seen.has(s.sid)) continue
    const g = groups.get(s.sid) ?? ''
    const members = g ? list.filter(o => groups.get(o.sid) === g) : [s]
    if (g.startsWith('goal:')) members.sort((a, b) => Number(b.sid === g.slice(5)) - Number(a.sid === g.slice(5)))
    for (const m of members) {
      if (seen.has(m.sid)) continue
      seen.add(m.sid)
      out.push(m)
    }
  }
  return out
}

function groupTag(s: SessionInfo, all: SessionInfo[]): HTMLElement | null {
  const l = s.lineage
  if (l?.groupKind === 'pair' && l.group) {
    const others = all.filter(o => o.sid !== s.sid && o.lineage?.groupKind === 'pair' && o.lineage.group === l.group).map(label)
    const tag = el('span', 'tag', 'pair')
    tag.title = visible(others.length ? `Started on the same task as ${others.join(', ')}` : 'Started as one of a pair, or on a merged pull request as a benchmark')
    return tag
  }
  if (l?.groupKind === 'goal' && l.group) {
    const goal = all.find(o => o.sid === l.group)
    const tag = el('span', 'tag', 'task')
    tag.title = visible(goal ? `A task split from the goal ${label(goal)}` : 'A task split from a goal session')
    return tag
  }
  const tasks = all.filter(o => o.lineage?.groupKind === 'goal' && o.lineage.group === s.sid).length
  if (!tasks) return null
  const tag = el('span', 'tag', 'goal')
  tag.title = `Split into ${tasks} ${plural(tasks, 'task')}`
  return tag
}

function sessionLink(sid: string, key: string, parts: (Node | string)[]) {
  const a = el('a', 'boxlist-row')
  a.href = '/'
  a.dataset.view = `s/${sid}`
  a.dataset.key = key
  a.append(...parts)
  const li = el('li')
  li.append(a)
  return li
}

export function placeMenu(anchor: HTMLElement, menu: HTMLElement) {
  const r = anchor.getBoundingClientRect()
  const w = menu.offsetWidth
  const h = menu.offsetHeight
  const top = r.bottom + 4 + h > innerHeight - 8 ? Math.max(8, r.top - 4 - h) : r.bottom + 4
  menu.style.top = `${Math.round(top)}px`
  menu.style.left = `${Math.round(Math.max(8, Math.min(r.right - w, innerWidth - w - 8)))}px`
}

function sessionMenu(s: SessionInfo, li: HTMLLIElement): HTMLElement[] {
  if (s.kind !== 'background' || !s.verified || !/^[0-9a-f]{8}$/.test(s.id || '')) return [el('span')]
  const action = s.running ? 'stop' : 'remove'
  const words = action === 'stop' ? ['Stop session', 'Confirm stop'] : ['Delete session', 'Confirm delete']
  const more = button('', 'ghost icon-button', undefined, 'button', `more:${s.sid}`)
  more.setAttribute('aria-label', `More actions for ${visible(label(s))}`)
  more.setAttribute('aria-haspopup', 'menu')
  more.append(icon('more'))
  const menu = el('div', 'menu row-menu surface')
  menu.popover = 'auto'
  menu.setAttribute('role', 'menu')
  menu.setAttribute('aria-label', `Actions for ${visible(label(s))}`)
  more.popoverTargetElement = menu
  const text = el('span', '', words[0])
  const item = button('', '', () => {
    if (item.dataset.armed !== 'yes') {
      item.dataset.armed = 'yes'
      item.classList.add('danger')
      text.textContent = words[1] ?? ''
      return
    }
    menu.hidePopover()
    more.focus()
    emit(li, 'sf-session-action', { sid: s.sid, action })
  })
  item.setAttribute('role', 'menuitem')
  const mark = el('span', 'mark')
  mark.append(icon(action === 'stop' ? 'stop' : 'trash'))
  item.append(mark, text)
  menu.append(item)
  menu.addEventListener('toggle', e => {
    if ((e as ToggleEvent).newState !== 'open') {
      delete item.dataset.armed
      item.classList.remove('danger')
      text.textContent = words[0] ?? ''
      return
    }
    placeMenu(more, menu)
    item.focus()
  })
  return [more, menu]
}

export function sessionTable(rows: HTMLLIElement[], cls = '', name = ''): HTMLDivElement {
  const head = el('div', 'sessions-head secondary')
  head.setAttribute('aria-hidden', 'true')
  head.append(el('span', '', 'Session'), el('span', '', 'Status'), el('span', '', 'Mode'), el('span', 'when', 'Age'), el('span'))
  const ul = el('ul', 'sessions')
  if (name) ul.setAttribute('aria-label', name)
  ul.append(...rows)
  const table = el('div', `session-table${cls ? ` ${cls}` : ''}`)
  table.append(head, ul)
  return table
}

export function sessionRow(s: SessionInfo, all: SessionInfo[], key: string, menu = false): HTMLLIElement {
  const st = sessionState(s)
  const name = titled(visible(label(s)))
  const done = !s.running && s.state === 'done'
  const level = done ? { word: 'Done', tone: '' } : s.level === 'locked' && s.sandboxed === false ? { word: 'Locked, not sandboxed', tone: 'warn' } : LEVELS[s.level ?? '']
  if (level) {
    const tag = el('span', level.tone ? `tag ${level.tone}` : 'tag', level.word)
    tag.title = visible(`${s.levelReason ?? ''}${s.levelDropped ? ' This dropped from Locked after the fact.' : ''}`)
    name.append(tag)
  }
  if (s.running && !s.live) name.append(el('span', 'tag', 'not connected'))
  else if (s.live && !s.controlled) name.append(el('span', 'tag', 'read-only'))
  if (s.mode === 'bypassPermissions') name.append(el('span', 'tag bad', 'bypass permissions'))
  const group = groupTag(s, all)
  if (group) name.append(group)
  const usage = el('sf-usage-line', 'usage-line')
  usage.sid = s.sid
  name.append(usage)
  const shared = sharing(s, all)
  if (shared.length) {
    const tag = el('span', 'tag warn', 'same checkout')
    tag.title = shown(`Also in ${s.checkout}, with a session working: ${shared.join(', ')}`)
    name.append(tag)
  }
  const open = el('span', 'open-cta', 'Open')
  open.setAttribute('aria-hidden', 'true')
  const tone = st.dot.split(' ')[1] ?? ''
  const mode = el('span', 'mode secondary', s.mode ? `${modeLabel(s.mode)} mode` : '')
  mode.title = mode.textContent ?? ''
  const row = sessionLink(s.sid, key, [name, el('span', `what secondary${tone ? ` ${tone}` : ''}`, st.word), mode, el('span', 'when secondary', age(s.startedAt)), open])
  ;(row.firstChild as HTMLAnchorElement).title = s.cwd || ''
  if (menu) {
    row.classList.add('with-menu')
    row.append(...sessionMenu(s, row))
  }
  return row
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
  #shown = el('p', 'small muted filter-count')
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
    const field = el('div', 'filter-search')
    const name = el('label', 'small', 'Search sessions')
    name.htmlFor = this.#search.id
    field.append(name, this.#search)
    this.#chips.setAttribute('role', 'group')
    this.#chips.setAttribute('aria-label', 'Show sessions that are')
    this.#shown.setAttribute('role', 'status')
    this.#tools.setAttribute('role', 'search')
    this.#tools.setAttribute('aria-label', 'Sessions')
    const row = el('div', 'filter-row')
    row.append(this.#chips, this.#clear)
    this.#tools.append(field, row, this.#shown)
    addEventListener('hashchange', () => {
      const next = readFragment()
      if (next.query === this.#query && [...next.states].join() === [...this.#states].join()) return
      this.#query = next.query
      this.#states = next.states
      this.#search.value = next.query
      this.#key = ''
      this.#render()
    })
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
