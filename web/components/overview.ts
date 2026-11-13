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
  if (!s.running && s.queued) return { dot: 'dot busy', word: `waking, ${s.queued} ${plural(s.queued, 'message')} waiting` }
  if (!s.running) return { dot: 'dot', word: !s.state || s.state === 'done' || s.state === 'stopped' ? 'stopped' : s.state === 'blocked' ? 'stopped, waiting for your answer' : `stopped, was ${shown(s.state)}` }
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
  if (s.verified && s.level === 'held') out.push(`Held: every push from this session is held and no new session starts, because ${s.levelReason || 'the sandbox check hasn\'t passed'}.${s.levelDropped ? ' This dropped from Locked after the fact.' : ''}`)
  if (loginNeeded(s)) out.push(`Claude Code couldn't use this session's login: its last request failed authentication. Sign in again inside the agent container (${signInStep(s.agent)}); this page never signs in for you.`)
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
  return JSON.stringify(list.map(s => [s.sid, s.name, s.cwd, s.kind, s.running, s.live, s.verified, s.disputed, s.outdated, s.controlled, s.mode, s.checkout, s.state, s.status, s.waitingFor, s.queued, s.busy, s.needs, s.login, s.level, s.levelReason, age(s.startedAt, now)]))
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
  const level = LEVELS[s.level ?? '']
  if (level) {
    const tag = el('span', level.tone ? `tag ${level.tone}` : 'tag', level.word)
    tag.title = visible(`${s.levelReason ?? ''}${s.levelDropped ? ' This dropped from Locked after the fact.' : ''}`)
    name.append(tag)
  }
  if (s.running && !s.live) name.append(el('span', 'tag', 'not connected'))
  else if (s.live && !s.controlled) name.append(el('span', 'tag', 'read-only'))
  if (s.mode === 'bypassPermissions') name.append(el('span', 'tag bad', 'bypass permissions'))
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

class SfOverview extends HTMLElement {
  #sessions: OverviewSession[] = []
  #supervisor = ''
  #open = new Map<string, boolean>()
  #key = ''

  set supervisor(value: string) {
    this.#supervisor = value || ''
  }

  set sessions(value: OverviewSession[]) {
    this.#sessions = value || []
    const now = Date.now()
    const key = JSON.stringify([
      this.#supervisor,
      sessionKey(this.#sessions, now),
      this.#sessions.map(s => (s.waiting || []).map(c => [c.id, c.type, c.agentId, c.readOnly, c.ts, c.type === 'permission' ? c.tool : c.type === 'ask' ? c.questions?.[0]?.question : ''])),
    ])
    if (key === this.#key) return
    this.#key = key
    this.#render()
  }

  #render() {
    const focused = focusedKey(this)
    const cards = this.#sessions.flatMap(s => (s.waiting || []).map(c => ({ s, c }))).sort((a, b) => (a.c.ts || 0) - (b.c.ts || 0))
    const running = this.#sessions.filter(isRunning).sort(bySessionOrder)
    const other = this.#sessions.filter(s => !running.includes(s)).sort((a, b) => (b.startedAt || 0) - (a.startedAt || 0))
    const unfinished = other.filter(s => s.kind === 'background' && s.state && !SETTLED.has(s.state)).length
    const parts: HTMLElement[] = []
    if (this.#supervisor === 'stopped' && unfinished) parts.push(el('p', 'notice warn', `Claude Code's background supervisor isn't running in the agent container, and ${unfinished === 1 ? 'one Claude Code session' : `${unfinished} Claude Code sessions`} stopped before finishing. Open one and send it a message, which wakes it.`))
    else if (this.#supervisor === 'stopped') parts.push(el('p', 'small muted', "Claude Code's background supervisor isn't running in the agent container. That's normal: it starts with the next Claude Code session."))
    if (cards.length) {
      const ul = el('ul', 'boxlist sessions')
      for (const { s, c } of cards) ul.append(sessionLink(s.sid, `wait:${s.sid}:${c.id}`, [el('span', 'dot need'), titled(visible(label(s))), el('span', 'what secondary', visible(`${c.agentId ? 'A subagent' : agentWord(s.agent)} ${(CARD_WORDS[c.type] || (() => 'needs you'))(c)}`)), el('span', 'when secondary', c.readOnly ? 'read-only' : '')]))
      parts.push(el('h2', 'label', `Waiting on you (${cards.length})`), ul)
    }
    const list = sessionTable(running.map(s => sessionRow(s, this.#sessions, `run:${s.sid}`)))
    if (running.length) parts.push(el('h2', 'label', `Running (${running.length})`), list)
    else {
      const projects = el('a', 'link', 'Projects')
      projects.href = '/'
      projects.dataset.view = 'projects'
      const none = el('p', 'small muted', 'No session is running. Start one from a project in ')
      none.append(projects, '.')
      parts.push(none)
    }
    if (other.length) {
      const ul = sessionTable(other.map(s => sessionRow(s, this.#sessions, `stop:${s.sid}`)))
      const summary = el('summary', 'label', `Stopped (${other.length})`)
      summary.dataset.key = 'summary:stopped'
      const d = remembered(this.#open, 'stopped', false, summary, ul)
      d.className = 'ended'
      parts.push(d)
    }
    this.replaceChildren(...parts)
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
