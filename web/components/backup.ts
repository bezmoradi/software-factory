import { actionButton, button, el, sayWhenShown, statusLine, visible, whileBusy } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { saveFile } from '../lib/download.ts'

type Post = (path: string, body: unknown) => Promise<Response>
type Send = (file: Blob) => Promise<Response>

const VIEW = '/api/backup'
const EXPORT = '/api/export'
const CLONE = '/api/clone-again'
const MAX_IMPORT = 256 * 1024 * 1024

interface Dropped {
  store: string
  count: number
  since: number
  last: number
}

interface Report {
  state: string
  why: string
  at: number
  engine: string
  exportedAt: number
  stores: { file: string; class: string; version: number; label: string }[]
  off: string[]
  keptOwn: string[]
  leftOut: string[]
  projects: number
  sessions: number
}

interface View {
  drops: Dropped[]
  copies: { version: string; files: string[]; at: number }[]
  last: Report | null
  needClone: string[]
  canImport: boolean
  why: string
}

const text = (v: unknown) => (typeof v === 'string' ? v : '')
const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) && v >= 0 ? v : 0)
const records = (v: unknown) => (Array.isArray(v) ? (v as unknown[]).filter((x): x is Record<string, unknown> => typeof x === 'object' && x !== null) : [])
const texts = (v: unknown) => (Array.isArray(v) ? (v as unknown[]).filter((x): x is string => typeof x === 'string') : [])

const STORE: Record<string, string> = {
  'reviews.json': 'review records',
  'audit.jsonl': 'audit lines',
  'walkthroughs.json': 'manual test lists',
  'notes.json': 'notes to agents',
  'recap-seen.json': 'recap marks',
  'prfollow.json': 'pull request follows',
}

const CLASS: Record<string, string> = {
  kept: 'kept as it was',
  display: 'kept for display only, counting for nothing',
  off: 'kept but off',
  dropped: 'dropped',
}

function reportOf(v: unknown): Report | null {
  if (v === null || typeof v !== 'object') return null
  const r = v as Record<string, unknown>
  return {
    state: text(r.state), why: text(r.why), at: num(r.at), engine: text(r.engine), exportedAt: num(r.exportedAt),
    stores: records(r.stores).map(s => ({ file: text(s.file), class: text(s.class), version: num(s.version), label: text(s.label) })),
    off: texts(r.off), keptOwn: texts(r.keptOwn), leftOut: texts(r.leftOut), projects: num(r.projects), sessions: num(r.sessions),
  }
}

function viewOf(v: Record<string, unknown>): View {
  return {
    drops: records(v.drops).map(d => ({ store: text(d.store), count: num(d.count), since: num(d.since), last: num(d.last) })),
    copies: records(v.copies).map(c => ({ version: text(c.version), files: texts(c.files), at: num(c.at) })),
    last: reportOf(v.last),
    needClone: texts(v.needClone),
    canImport: v.canImport === true,
    why: text(v.why),
  }
}

async function load(post: Post): Promise<{ view: View | null; message: string }> {
  const res = await post(VIEW, {}).catch(() => null)
  const parsed: unknown = res ? await res.json().catch(() => ({})) : {}
  const v = parsed !== null && typeof parsed === 'object' ? (parsed as Record<string, unknown>) : {}
  if (!res || !res.ok) return { view: null, message: text(v.message) ? visible(text(v.message)) : 'The engine didn’t answer.' }
  return { view: viewOf(v), message: '' }
}

function day(ms: number): string {
  return ms ? new Date(ms).toLocaleString() : 'an unknown time'
}

export function droppedLine(d: Dropped): string {
  const what = STORE[d.store] ?? `records of ${visible(d.store)}`
  return `${visible(d.store)}: ${d.count.toLocaleString()} old ${what} dropped since ${day(d.since)}, the last at ${day(d.last)}.`
}

export async function stateNotices(post: Post, target: HTMLElement): Promise<void> {
  const { view } = await load(post)
  if (!view) return
  const lines = view.drops.map(droppedLine)
  if (view.last?.state === 'applied' && view.needClone.length) lines.push(`${view.needClone.length === 1 ? '1 imported project needs' : `${view.needClone.length} imported projects need`} a clone: open Export and import to clone ${view.needClone.length === 1 ? 'it' : 'them'} again.`)
  if (view.last?.state === 'refused' && view.last.why) lines.push(`The last import was refused: ${visible(view.last.why)}`)
  const said = lines.map(() => el('p', 'small warn'))
  target.replaceChildren(...said)
  lines.forEach((l, i) => sayWhenShown(said[i]!, l, 'warn'))
  target.hidden = lines.length === 0
}

function list(title: string, items: string[]): Node[] {
  if (!items.length) return []
  const ul = el('ul', 'list')
  for (const i of items) ul.append(el('li', 'small', visible(i)))
  return [el('h4', 'small', title), ul]
}

function reportNodes(r: Report): Node[] {
  const head = r.state === 'applied' ? `Imported at ${day(r.at)}` : r.state === 'staged' ? 'Imported; the engine restarts to finish' : `Import refused at ${day(r.at)}`
  const parts: Node[] = [el('h3', 'label', head)]
  if (r.why) parts.push(el('p', 'small bad', visible(r.why)))
  parts.push(el('p', 'small muted', `From engine ${visible(r.engine)}, exported ${day(r.exportedAt)}: ${r.projects} ${r.projects === 1 ? 'project' : 'projects'} and ${r.sessions} ${r.sessions === 1 ? 'session' : 'sessions'}. Sessions come back stopped and as history only; Wake is refused, since their worktrees stayed on the old engine.`))
  const stores = el('ul', 'list')
  for (const s of r.stores) stores.append(el('li', 'small', `${visible(s.file)}${s.version ? ` (format ${s.version})` : ''}: ${CLASS[s.class] ?? visible(s.class)}, ${visible(s.label)}`))
  if (r.stores.length) parts.push(el('h4', 'small', 'What came back'), stores)
  parts.push(...list('Off until you turn each on again', r.off), ...list('Settings this engine already had, kept', r.keptOwn), ...list('Left out', r.leftOut))
  return parts
}

function fileName(res: Response): string {
  const m = /filename="(software-factory-state-[0-9-]+\.tar\.gz)"/.exec(res.headers.get('Content-Disposition') ?? '')
  return m?.[1] ?? 'software-factory-state.tar.gz'
}

export function openBackup(post: Post, send: Send): void {
  let generation = 0
  const say = statusLine()
  const body = el('div', 'backup-body')
  const exportButton = button('Export', 'primary small')
  const picker = el('input')
  picker.type = 'file'
  picker.accept = '.gz,application/gzip'
  picker.hidden = true
  const importButton = button('Import…', 'ghost small', () => picker.click())
  const importWhy = el('p', 'small muted')

  const paint = (v: View) => {
    const parts: Node[] = []
    importButton.disabled = !v.canImport
    importWhy.textContent = v.canImport ? 'An import goes only into an engine with no projects and no sessions, and the engine restarts to finish it.' : visible(v.why)
    if (v.needClone.length) {
      parts.push(el('h3', 'label', 'Projects that need a clone'), el('p', 'small muted', 'Save the git host’s token first if the repository is private, then clone each again from its recorded address.'))
      const ul = el('ul', 'list')
      for (const name of v.needClone) {
        const li = el('li', 'row')
        const b = actionButton('Clone again', 'ghost small', async () => {
          const res = await post(CLONE, { name }).catch(() => null)
          if (!res) {
            say.textContent = 'The engine didn’t answer.'
            return
          }
          const parsed: unknown = await res.json().catch(() => ({}))
          const r = parsed !== null && typeof parsed === 'object' ? (parsed as Record<string, unknown>) : {}
          say.textContent = res.ok ? `Cloning ${visible(name)}… It shows on Projects when it’s done.` : text(r.message) ? visible(text(r.message)) : 'The clone didn’t start.'
          if (res.ok) b.disabled = true
        })
        li.append(el('code', '', visible(name)), el('span', 'spacer'), b)
        ul.append(li)
      }
      parts.push(ul)
    }
    parts.push(el('h3', 'label', 'Dropped records'))
    if (!v.drops.length) parts.push(el('p', 'small muted', 'No store has dropped old records.'))
    for (const x of v.drops) parts.push(el('p', 'small warn', droppedLine(x)))
    parts.push(el('h3', 'label', 'Copies from before a format change'))
    if (!v.copies.length) parts.push(el('p', 'small muted', 'None yet. Before an engine writes a store in a newer format, it copies the old file here, so the engine before it can read it after a rollback.'))
    for (const c of v.copies) parts.push(el('p', 'small', `backup/${visible(c.version)}: ${c.files.map(visible).join(', ') || 'empty'} (${day(c.at)})`))
    if (v.last) parts.push(...reportNodes(v.last))
    body.replaceChildren(...parts)
  }

  const reload = async () => {
    const mine = ++generation
    say.textContent = 'Loading…'
    const { view, message } = await load(post)
    if (mine !== generation) return
    say.textContent = message
    if (view) paint(view)
  }

  exportButton.addEventListener('click', () => void whileBusy(exportButton, () => {
    say.textContent = 'Exporting…'
    return post(EXPORT, {}).then(async res => {
      if (!res.ok) {
        const parsed: unknown = await res.json().catch(() => ({}))
        const r = parsed !== null && typeof parsed === 'object' ? (parsed as Record<string, unknown>) : {}
        say.textContent = text(r.message) ? visible(text(r.message)) : 'The export didn’t finish.'
        return
      }
      const blob = await res.blob()
      saveFile(blob, fileName(res))
      say.textContent = `Saved ${fileName(res)} (${Math.ceil(blob.size / 1024).toLocaleString()} KiB).`
    }).catch(() => {
      say.textContent = 'The export was cut off. Try again.'
    })
  }))

  picker.addEventListener('change', () => {
    const file = picker.files?.[0]
    picker.value = ''
    if (!file) return
    if (file.size > MAX_IMPORT) {
      say.textContent = 'That file is larger than 256 MiB, more than an import takes.'
      return
    }
    say.textContent = `Importing ${visible(file.name)}…`
    void whileBusy(importButton, () => send(file).then(async res => {
      const parsed: unknown = await res.json().catch(() => ({}))
      const r = parsed !== null && typeof parsed === 'object' ? (parsed as Record<string, unknown>) : {}
      if (!res.ok) {
        say.textContent = text(r.message) ? visible(text(r.message)) : res.status === 413 ? 'That file is larger than an import takes.' : 'The import didn’t finish.'
        return
      }
      const report = reportOf(r.report)
      generation++
      say.textContent = 'Imported. The engine restarts to finish; the page reconnects once it’s back.'
      importButton.disabled = true
      if (report) body.replaceChildren(...reportNodes(report))
    }).catch(() => {
      say.textContent = 'The import was cut off. Try again.'
    }))
  })

  const transfer = el('div', 'row')
  transfer.append(exportButton, importButton, picker)
  const m = modal({
    kind: 'backup',
    title: 'Export and import',
    body: [
      el('p', 'small', 'Export saves the engine’s state to one file: projects, sessions, review records, settings and the audit. It holds no git host token, but it does hold the name and email agents commit with and every reason you typed, so keep it private.'),
      el('p', 'small muted', 'An imported file counts as outside data. Review passes and approvals come back for display only and count for nothing until you approve again; added hosts, commands, reviewer agent files, automatic sends and review depth come back off; projects need a clone, and sessions are history only.'),
      transfer, importWhy, say, body,
    ],
    lead: [actionButton('Refresh', 'ghost small', reload)],
    actions: [button('Close', 'ghost small', () => m.close())],
  })
  m.onClose(() => {
    generation++
  })
  void reload()
}
