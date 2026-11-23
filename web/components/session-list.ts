import { button, el, emit, say, shown, statusLine, visible } from '../lib/dom.ts'
import { placeMenu } from '../lib/menu.ts'
import { icon } from './icon.ts'
import { age, label, plural } from '../lib/format.ts'
import type { SessionInfo } from '../types.ts'


export function titled(text: string) {
  const name = el('span', 'name')
  name.append(el('span', 'title', text))
  return name
}

export const isRunning = (s: SessionInfo) => !!s.running || (s.live && !s.verified)

export function bySessionOrder(a: SessionInfo, b: SessionInfo): number {
  return Number(isRunning(b)) - Number(isRunning(a)) || Number(b.live) - Number(a.live) || (b.startedAt || 0) - (a.startedAt || 0)
}

export function sessionKey(list: SessionInfo[], now = Date.now()): string {
  return JSON.stringify(list.map(s => [s.sid, s.name, s.cwd, s.kind, s.running, s.live, s.verified, s.disputed, s.outdated, s.controlled, s.mode, s.state, s.status, s.waitingFor, s.queued, s.busy, s.needs, s.login, s.lineage?.group, s.lineage?.groupKind, s.summary?.word, s.summary?.tone, s.modeLabel, s.actions?.manage, age(s.startedAt, now)]))
}

function groupOf(s: SessionInfo, all: SessionInfo[]): string {
  const l = s.lineage
  if (l?.group && l.groupKind === 'goal') return `goal:${l.group}`
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

export function sessionLink(sid: string, key: string, parts: (Node | string)[]) {
  const a = el('a', 'boxlist-row')
  a.href = '/'
  a.dataset.view = `s/${sid}`
  a.dataset.key = key
  a.append(...parts)
  const li = el('li')
  li.append(a)
  return li
}

function sessionMenu(s: SessionInfo, li: HTMLLIElement): HTMLElement[] {
  if (s.actions?.manage !== '') return [el('span')]
  const action = s.running ? 'stop' : 'remove'
  const words = action === 'stop' ? ['Stop session', 'Confirm stop'] : ['Delete session', 'Confirm delete']
  const armedSaid = action === 'stop' ? 'Press Confirm stop to stop this session. It keeps the conversation; Wake brings it back.' : 'Press Confirm delete to delete this session and its worktree.'
  const said = statusLine('sr-only', 'span')
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
      say(said, armedSaid)
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
      say(said, '')
      return
    }
    placeMenu(more, menu)
    item.focus()
  })
  return [more, menu, said]
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
  const name = titled(visible(label(s)))
  if (s.running && !s.live) name.append(el('span', 'tag', 'not connected'))
  else if (s.live && !s.controlled) name.append(el('span', 'tag', 'read-only'))
  if (s.mode === 'bypassPermissions') name.append(el('span', 'tag bad', 'bypass permissions'))
  const group = groupTag(s, all)
  if (group) name.append(group)
  const tone = s.summary?.tone ?? ''
  const mode = el('span', 'mode secondary', s.mode ? `${s.modeLabel || s.mode} mode` : '')
  mode.title = mode.textContent ?? ''
  const open = el('a', 'button primary small session-open', 'Open')
  open.href = '/'
  open.dataset.view = `s/${s.sid}`
  open.dataset.key = key
  open.setAttribute('aria-label', `Open ${visible(label(s))}`)
  const cells = el('div', 'boxlist-row session-cells')
  cells.title = s.cwd || ''
  cells.append(name, el('span', `what secondary${tone ? ` ${tone}` : ''}`, shown(s.summary?.word ?? '')), mode, el('span', 'when secondary', age(s.startedAt)), open)
  const row = el('li')
  row.append(cells)
  if (menu) {
    row.classList.add('with-menu')
    row.append(...sessionMenu(s, row))
  }
  return row
}
