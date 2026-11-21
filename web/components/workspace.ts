import type { EngineAnswer } from '../api.ts'
import { actionButton, around, busy, button, el, focusedKey, refocus, statusLine, visible, whileBusy } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { saidOf, tagClass, type Said } from '../lib/json.ts'
import { emptyState } from '../lib/page.ts'

type Post = (path: string, body: unknown) => Promise<EngineAnswer>

const ROUTE = '/api/workspace'

interface Row {
  sid: string
  name: string
  repo: string
  branch: string
  tree: string
  running: boolean
  size: number
  entries: number
  sizeMore: boolean
  sizeKnown: boolean
  state: string
  said: Said
  head: string
  deletable: boolean
  why: string
}

interface View {
  spaceKnown: boolean
  free: number
  total: number
  low: boolean
  lowBelow: number
  sessions: Row[]
}

const text = (v: unknown) => (typeof v === 'string' ? v : '')
const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) && v >= 0 ? v : 0)

export function bytes(n: number): string {
  const units = ['bytes', 'KiB', 'MiB', 'GiB', 'TiB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return i === 0 ? `${Math.round(v)} ${units[i]}` : `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`
}

function viewOf(body: Record<string, unknown>): View {
  const rows = Array.isArray(body.sessions) ? (body.sessions as unknown[]) : []
  return {
    spaceKnown: body.spaceKnown === true,
    free: num(body.free),
    total: num(body.total),
    low: body.low === true,
    lowBelow: num(body.lowBelow),
    sessions: rows.filter((r): r is Record<string, unknown> => typeof r === 'object' && r !== null && typeof (r as Record<string, unknown>).sid === 'string').map(r => ({
      sid: text(r.sid), name: text(r.name), repo: text(r.repo), branch: text(r.branch), tree: text(r.tree), running: r.running === true,
      size: num(r.size), entries: num(r.entries), sizeMore: r.sizeMore === true, sizeKnown: r.sizeKnown === true, state: text(r.state), said: saidOf(r.said), head: text(r.head),
      deletable: r.deletable === true, why: text(r.why),
    })),
  }
}

export function openWorkspace(post: Post, labelOf: (sid: string) => string, deleted: (sid: string) => void): void {
  const say = statusLine()
  const space = el('div', 'workspace-space')
  const list = el('ul', 'list workspace-list')
  const listHeading = el('h3', 'label', 'Sessions')
  listHeading.tabIndex = -1
  const deleting = new Set<string>()
  let order: string[] = []

  const paint = (v: View) => {
    const key = focusedKey(list)
    order = v.sessions.map(r => r.sid)
    space.replaceChildren()
    if (v.spaceKnown) {
      const used = v.total ? Math.round(((v.total - v.free) / v.total) * 100) : 0
      space.append(el('p', 'small', `${bytes(v.free)} free of ${bytes(v.total)} on the workspace volume (${used}% used).`))
      if (v.low) space.append(el('p', 'notice warn small', `Free space is low: under ${bytes(v.lowBelow)} or 5% of the volume. A clone, an install or a build can fail when it runs out. Delete sessions you no longer need below.`))
    } else space.append(el('p', 'small muted', 'The engine couldn’t read the workspace volume’s free space.'))
    list.replaceChildren()
    if (!v.sessions.length) list.append(emptyState('No sessions this engine started.', { tag: 'li' }))
    for (const r of v.sessions) {
      const li = el('li', 'workspace-row')
      const head = el('div', 'row')
      const name = labelOf(r.sid) || r.name || r.tree
      head.append(el('strong', '', visible(name)), el('span', 'small muted', `${visible(r.repo)} · ${visible(r.branch)}`))
      head.append(el('span', tagClass(r.said), r.said.label))
      if (r.running) head.append(el('span', 'tag', 'running'))
      head.append(el('span', 'spacer'))
      const size = r.sizeKnown ? `${r.sizeMore ? 'more than ' : ''}${bytes(r.size)} in ${r.sizeMore ? 'more than ' : ''}${r.entries.toLocaleString()} ${r.entries === 1 ? 'entry' : 'entries'}` : 'size not counted'
      head.append(el('span', 'small', size))
      li.append(head)
      const acts = el('div', 'row')
      const del = button('Delete session', 'ghost small', () => void whileBusy(del, () => remove(r)), 'button', `delete:${r.sid}`)
      del.setAttribute('aria-label', `Delete the session ${visible(name)} and its worktree`)
      if (!r.deletable) del.disabled = true
      else if (deleting.has(r.sid)) busy(del, true)
      acts.append(del)
      if (!r.deletable && r.why) acts.append(el('span', 'small muted', visible(r.why)))
      li.append(acts)
      list.append(li)
    }
    refocus(list, key)
  }

  const reload = async (keepSaid = false) => {
    if (!keepSaid) say.textContent = 'Counting…'
    const a = await post(ROUTE, { op: 'view' })
    if (!a.ok) {
      say.textContent = a.message
      return
    }
    if (!keepSaid) say.textContent = ''
    paint(viewOf(a.body))
  }

  const lost = () => {
    const a = document.activeElement
    return m.dialog.open && (!(a instanceof HTMLElement) || a === document.body || !a.isConnected)
  }

  const settle = (sid: string, before: string[], offer: HTMLButtonElement | null) => {
    if (!lost()) return
    if (offer?.isConnected) return offer.focus()
    refocus(list, `delete:${sid}`, () => around(before, sid).map(s => `delete:${s}`))
    if (lost()) listHeading.focus()
  }

  const remove = async (r: Row, offer: { flag: string; value: string } | null = null) => {
    if (deleting.has(r.sid)) return
    deleting.add(r.sid)
    const before = order
    say.textContent = 'Deleting…'
    const a = await post(ROUTE, offer ? { op: 'delete', sid: r.sid, flag: offer.flag, value: offer.value } : { op: 'delete', sid: r.sid })
    deleting.delete(r.sid)
    if (a.ok) {
      say.textContent = 'Deleted, with its worktree.'
      deleted(r.sid)
      await reload(true)
      return settle(r.sid, before, null)
    }
    const message = a.message
    const o = a.body.offer
    let anyway: HTMLButtonElement | null = null
    if (o && typeof o === 'object' && typeof (o as Record<string, unknown>).flag === 'string' && typeof (o as Record<string, unknown>).value === 'string') {
      const next = { flag: (o as Record<string, string>).flag!, value: (o as Record<string, string>).value! }
      anyway = actionButton('Delete anyway', 'ghost small', () => remove(r, next))
      say.replaceChildren(el('span', '', `${message} `), anyway)
    } else say.textContent = message
    await reload(true)
    settle(r.sid, before, anyway)
  }

  const m = modal({
    kind: 'workspace',
    title: 'Workspace',
    docs: 'working-in-a-session',
    body: [
      el('p', 'small', 'Clones and session worktrees share the workspace volume. Sizes count the files in each worktree without following links, and stop counting at 200,000 entries.'),
      space,
      listHeading,
      el('p', 'small muted', 'A stopped session can be deleted here when its branch is pushed or on main, so nothing is lost. A session with commits that aren’t pushed can be deleted only from its own menu.'),
      list, say,
    ],
    lead: [actionButton('Refresh', 'ghost small', () => reload())],
    actions: [button('Close', 'ghost small', () => m.close())],
  })
  void reload()
}
