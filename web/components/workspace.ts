import { actionButton, busy, button, el, opener, visible, whileBusy } from '../lib/dom.ts'

type Post = (path: string, body: unknown) => Promise<Response>

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

type Answer = { ok: boolean; body: Record<string, unknown> }

const STATE: Record<string, { text: string; tone: string }> = {
  merged: { text: 'on main', tone: 'ok' },
  pushed: { text: 'pushed', tone: 'ok' },
  unpushed: { text: 'unpushed commits', tone: 'warn' },
  unknown: { text: 'not known', tone: '' },
}

async function call(post: Post, body: unknown): Promise<Answer> {
  const res = await post(ROUTE, body).catch(() => null)
  if (!res) return { ok: false, body: { message: 'The engine didn’t answer.' } }
  const parsed: unknown = await res.json().catch(() => ({}))
  return { ok: res.ok, body: parsed !== null && typeof parsed === 'object' ? (parsed as Record<string, unknown>) : {} }
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
      size: num(r.size), entries: num(r.entries), sizeMore: r.sizeMore === true, sizeKnown: r.sizeKnown === true, state: text(r.state), head: text(r.head),
      deletable: r.deletable === true, why: text(r.why),
    })),
  }
}

export function openWorkspace(post: Post, labelOf: (sid: string) => string, deleted: (sid: string) => void): void {
  const back = opener()
  const d = el('dialog', 'consent workspace surface')
  d.setAttribute('aria-label', 'Workspace')
  const close = () => {
    d.close()
    back()
    d.remove()
  }
  const say = el('p', 'small')
  say.setAttribute('role', 'status')
  const space = el('div', 'workspace-space')
  const list = el('ul', 'list workspace-list')
  const deleting = new Set<string>()

  const paint = (v: View) => {
    space.replaceChildren()
    if (v.spaceKnown) {
      const used = v.total ? Math.round(((v.total - v.free) / v.total) * 100) : 0
      space.append(el('p', 'small', `${bytes(v.free)} free of ${bytes(v.total)} on the workspace volume (${used}% used).`))
      if (v.low) space.append(el('p', 'notice warn small', `Free space is low: under ${bytes(v.lowBelow)} or 5% of the volume. A clone, an install or a build can fail when it runs out. Delete sessions you no longer need below.`))
    } else space.append(el('p', 'small muted', 'The engine couldn’t read the workspace volume’s free space.'))
    list.replaceChildren()
    if (!v.sessions.length) list.append(el('li', 'muted', 'No sessions this engine started.'))
    for (const r of v.sessions) {
      const li = el('li', 'workspace-row')
      const head = el('div', 'row')
      const name = labelOf(r.sid) || r.name || r.tree
      head.append(el('strong', '', visible(name)), el('span', 'small muted', `${visible(r.repo)} · ${visible(r.branch)}`))
      const st = STATE[r.state] ?? STATE.unknown!
      head.append(el('span', `tag ${st.tone}`.trim(), st.text))
      if (r.running) head.append(el('span', 'tag', 'running'))
      head.append(el('span', 'spacer'))
      const size = r.sizeKnown ? `${r.sizeMore ? 'more than ' : ''}${bytes(r.size)} in ${r.sizeMore ? 'more than ' : ''}${r.entries.toLocaleString()} ${r.entries === 1 ? 'entry' : 'entries'}` : 'size not counted'
      head.append(el('span', 'small', size))
      li.append(head)
      const acts = el('div', 'row')
      const del = button('Delete session', 'ghost small', () => void whileBusy(del, () => remove(r)))
      del.setAttribute('aria-label', `Delete the session ${visible(name)} and its worktree`)
      if (!r.deletable) del.disabled = true
      else if (deleting.has(r.sid)) busy(del, true)
      acts.append(del)
      if (!r.deletable && r.why) acts.append(el('span', 'small muted', visible(r.why)))
      li.append(acts)
      list.append(li)
    }
  }

  const reload = async () => {
    say.textContent = 'Counting…'
    const a = await call(post, { op: 'view' })
    if (!a.ok) {
      say.textContent = text(a.body.message) ? visible(text(a.body.message)) : 'The workspace status didn’t load.'
      return
    }
    say.textContent = ''
    paint(viewOf(a.body))
  }

  const remove = async (r: Row, offer: { flag: string; value: string } | null = null) => {
    if (deleting.has(r.sid)) return
    deleting.add(r.sid)
    say.textContent = 'Deleting…'
    const a = await call(post, offer ? { op: 'delete', sid: r.sid, flag: offer.flag, value: offer.value } : { op: 'delete', sid: r.sid })
    deleting.delete(r.sid)
    if (a.ok) {
      say.textContent = 'Deleted, with its worktree.'
      deleted(r.sid)
      return void reload()
    }
    const message = text(a.body.message) ? visible(text(a.body.message)) : 'The session wasn’t deleted.'
    const o = a.body.offer
    if (o && typeof o === 'object' && typeof (o as Record<string, unknown>).flag === 'string' && typeof (o as Record<string, unknown>).value === 'string') {
      const next = { flag: (o as Record<string, string>).flag!, value: (o as Record<string, string>).value! }
      say.replaceChildren(el('span', '', `${message} `), actionButton('Delete anyway', 'ghost small', () => remove(r, next)))
    } else say.textContent = message
    void reload()
  }

  const actions = el('div', 'row consent-actions')
  actions.append(actionButton('Refresh', 'ghost small', reload), el('span', 'spacer'), button('Close', 'ghost small', close))
  d.append(
    el('h2', 'label', 'Workspace'),
    el('p', 'small', 'Clones and session worktrees share the workspace volume. Sizes count the files in each worktree without following links, and stop counting at 200,000 entries.'),
    space,
    el('h3', 'label', 'Sessions'),
    el('p', 'small muted', 'A stopped session can be deleted here when its branch is pushed or on main, so nothing is lost. A session with commits that aren’t pushed can be deleted only from its own menu.'),
    list, say, actions,
  )
  d.addEventListener('cancel', e => {
    e.preventDefault()
    close()
  })
  document.body.append(d)
  d.showModal()
  void reload()
}
