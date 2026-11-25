import { download, type EngineAnswer } from '../api.ts'
import { actionButton, button, el, sayWhenShown, statusLine, visible, whileBusy } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { dialogDocs } from '../lib/dialog-docs.ts'
import { saveFile } from '../lib/download.ts'
import { emptyState } from '../lib/page.ts'
import { filePicker } from '../lib/controls.ts'
import { actions } from '../lib/form.ts'
import { CHEAP_EVERY_MS, whileOpen } from '../lib/refresh.ts'

type Post = (path: string, body: unknown) => Promise<EngineAnswer>
type Send = (file: Blob) => Promise<EngineAnswer>

const VIEW = '/api/backup'
const EXPORT = '/api/export'
const CLONE = '/api/clone-again'

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
  const a = await post(VIEW, {})
  if (!a.ok) return { view: null, message: a.message }
  return { view: viewOf(a.body), message: '' }
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

function fileName(disposition: string): string {
  const m = /filename="(software-factory-state-[0-9-]+\.tar\.gz)"/.exec(disposition)
  return m?.[1] ?? 'software-factory-state.tar.gz'
}

export function openBackup(post: Post, send: Send): void {
  const cloning = new Set<string>()
  let readSaid = ''
  const say = statusLine()
  const body = el('div', 'backup-body')
  const exportButton = button('Export', 'primary small')
  const picker = filePicker({ accept: '.gz,application/gzip', hidden: true })
  const importButton = button('Import…', 'ghost small', () => picker.click())
  const importWhy = el('p', 'small muted')

  const paint = (v: View) => {
    const parts: Node[] = []
    importButton.disabled = !v.canImport
    importWhy.textContent = v.canImport ? 'Import needs an engine with no projects or sessions, and restarts it.' : visible(v.why)
    if (v.needClone.length) {
      parts.push(el('h3', 'label', 'Projects that need a clone'), el('p', 'small muted', 'Save the git host’s token first if the repository is private, then clone each again from its recorded address.'))
      const ul = el('ul', 'list')
      for (const name of v.needClone) {
        const li = el('li', 'row')
        const b = actionButton('Clone again', 'ghost small', async () => {
          const a = await post(CLONE, { name })
          say.textContent = a.ok ? `Cloning ${visible(name)}… It shows on Projects when it’s done.` : a.message
          if (a.ok) {
            cloning.add(name)
            b.disabled = true
          }
        })
        if (cloning.has(name)) b.disabled = true
        li.append(el('code', '', visible(name)), el('span', 'spacer'), b)
        ul.append(li)
      }
      parts.push(ul)
    }
    parts.push(el('h3', 'label', 'Dropped records'))
    if (!v.drops.length) parts.push(emptyState('No store has dropped old records.'))
    for (const x of v.drops) parts.push(el('p', 'small warn', droppedLine(x)))
    parts.push(el('h3', 'label', 'Copies from before a format change'))
    if (!v.copies.length) parts.push(emptyState('None yet.'))
    for (const c of v.copies) parts.push(el('p', 'small', `backup/${visible(c.version)}: ${c.files.map(visible).join(', ') || 'empty'} (${day(c.at)})`))
    if (v.last) parts.push(...reportNodes(v.last))
    body.replaceChildren(...parts)
  }

  const show = ({ view, message }: { view: View | null; message: string }) => {
    if (!say.textContent || say.textContent === readSaid) say.textContent = readSaid = message
    if (view) paint(view)
  }

  exportButton.addEventListener('click', () => void whileBusy(exportButton, async () => {
    say.textContent = 'Exporting…'
    const got = await download(EXPORT)
    if (!got.answer.ok) {
      say.textContent = got.answer.message
      return
    }
    if (!got.file) {
      say.textContent = 'The export was cut off. Try again.'
      return
    }
    const name = fileName(got.disposition)
    saveFile(got.file, name)
    say.textContent = `Saved ${name} (${Math.ceil(got.file.size / 1024).toLocaleString()} KiB).`
  }))

  picker.addEventListener('change', () => {
    const file = picker.files?.[0]
    picker.value = ''
    if (!file) return
    say.textContent = `Importing ${visible(file.name)}…`
    void whileBusy(importButton, () => send(file).then(a => {
      if (!a.ok) {
        say.textContent = a.message
        return
      }
      const report = reportOf(a.body.report)
      refresh.stop()
      say.textContent = 'Imported. The engine restarts to finish; the page reconnects once it’s back.'
      importButton.disabled = true
      if (report) body.replaceChildren(...reportNodes(report))
    }))
  })

  const transfer = actions([importButton, exportButton])
  const m = modal({
    kind: 'backup',
    ...dialogDocs('backup'),
    body: [
      el('p', 'small', 'One file with the engine’s projects, sessions, review records, settings and audit. It holds your commit name and email and every reason you typed, so keep it private.'),
      importWhy, transfer, picker, say, body,
    ],
    actions: [button('Close', 'ghost small', () => m.close())],
  })
  say.textContent = readSaid = 'Loading…'
  const refresh = whileOpen({ root: body, read: () => load(post), show, everyMs: CHEAP_EVERY_MS })
  m.onClose(() => refresh.stop())
}
