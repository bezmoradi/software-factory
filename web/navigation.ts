import { request, UNREAD } from './api.ts'
import { armed, current, paint, scroller, sessions, state, type Session } from './app-state.ts'
import { byId, settleFocus } from './lib/dom.ts'
import { baseName, shortSha } from './lib/format.ts'
import type { Spot } from './components/conversation.ts'
import type { FileView } from './types.ts'

interface NavigationDeps {
  composer: {
    swapDraft: (from: string, to: string) => void
    dropDraft: (key: string) => void
    grow: () => void
    paintJump: () => void
  }
}

export interface Navigation {
  go: (key: string) => void
  closeTab: (key: string) => void
  pruneTabs: () => void
  openChanges: (s: Session) => void
  openFile: (sid: string, path: string, commit?: string, line?: number) => void
  loadDocs: () => Promise<void>
  openDocs: (id: string, back?: { view: string; label: string } | null) => void
}

export let openTabs = ['overview']
export let recent: string[] = []
const scrolls = new Map<string, number>()
const spots = new Map<string, Spot | null>()

const TABS_KEY = 'sf-tabs'
const MAX_TAB_LINE = 10_000_000
const TAB_KEY = /^(?:projects|docs|s\/[\w-]{1,100}|c:[\w-]{1,100}|f:[\w-]{1,100}:[^\0\n]{1,4000}(?:\0[0-9a-f]{40})?)$/

export function saveTabs() {
  try {
    const lines = Object.fromEntries(openTabs.flatMap(k => {
      const line = k.startsWith('f:') ? files.get(k)?.line ?? 0 : 0
      return line > 0 ? [[k, line]] : []
    }))
    sessionStorage.setItem(TABS_KEY, JSON.stringify({ tabs: openTabs.slice(1), active: current.key, lines }))
  } catch {}
}

export function restoreTabs() {
  let saved: { tabs?: unknown; active?: unknown; lines?: unknown } | null = null
  try {
    saved = JSON.parse(sessionStorage.getItem(TABS_KEY) || 'null')
  } catch {}
  const tabs = Array.isArray(saved?.tabs) ? [...new Set((saved.tabs as unknown[]).filter((k): k is string => typeof k === 'string' && TAB_KEY.test(k)))].slice(0, 50) : []
  const lines = saved?.lines && typeof saved.lines === 'object' ? (saved.lines as Record<string, unknown>) : {}
  for (const k of tabs) {
    if (!k.startsWith('f:')) continue
    const { path, commit } = fileKey(k)
    const line = lines[k]
    const at = typeof line === 'number' && Number.isInteger(line) && line > 0 && line <= MAX_TAB_LINE ? line : 0
    files.set(k, { path, ...(commit ? { commit } : {}), ...(at ? { line: at } : {}) })
  }
  openTabs = ['overview', ...tabs]
  const active = typeof saved?.active === 'string' && openTabs.includes(saved.active) ? saved.active : 'overview'
  current.key = active
  recent = [active, ...[...openTabs].reverse().filter(k => k !== active && k !== 'overview')].filter(k => k !== 'overview')
}

export function keepTab(key: string) {
  if (!openTabs.includes(key)) openTabs = [...openTabs, key]
}

export function moveTab(id: string, index: number) {
  const from = openTabs.indexOf(id)
  if (from < 1) return
  const rest = openTabs.filter(k => k !== id)
  const to = Math.max(1, Math.min(rest.length, index > from ? index - 1 : index))
  if (to === from) return
  rest.splice(to, 0, id)
  openTabs = rest
  paint()
}

export const files = new Map<string, FileView>()

export function fileLabel(key: string): string {
  const path = files.get(key)?.path ?? key
  const commit = files.get(key)?.commit
  const name = baseName(path)
  const twin = openTabs.some(k => k !== key && k.startsWith('f:') && baseName(files.get(k)?.path ?? k) === name)
  const parts = path.split('/').filter(Boolean)
  const label = twin && parts.length > 1 ? `${name} · ${parts[parts.length - 2]}` : name
  return commit ? `${label} @ ${shortSha(commit)}` : label
}

function fileKey(key: string): { sid: string; path: string; commit: string } {
  const at = key.indexOf(':', 2)
  const rest = key.slice(at + 1)
  const cut = rest.indexOf('\0')
  return { sid: key.slice(2, at), path: cut < 0 ? rest : rest.slice(0, cut), commit: cut < 0 ? '' : rest.slice(cut + 1) }
}

const fileLoads = new Set<string>()
let focusFile = ''

function loadFile(key: string) {
  const { sid, path, commit } = fileKey(key)
  if (fileLoads.has(key) || !sessions.has(sid)) return
  fileLoads.add(key)
  void request('/api/file', commit ? { sid, path, commit } : { sid, path }).then(a => {
    const body = a.body
    fileLoads.delete(key)
    if (!openTabs.includes(key)) return
    const text = a.ok && typeof body.text === 'string' ? body.text : undefined
    const shown = typeof body.path === 'string' ? body.path : path
    const old = files.get(key)
    if (text !== undefined && old?.text === text && old.path === shown) return
    const at = { ...(commit ? { commit } : {}), ...(old?.line ? { line: old.line } : {}) }
    files.set(key, text !== undefined ? { path: shown, text, ...at } : { path, ...at, note: a.ok ? UNREAD : a.message })
    const file = byId<HTMLElementTagNameMap['sf-file']>('file')
    const refocus = current.key === key && (focusFile === key || file.contains(document.activeElement) || document.activeElement === document.body)
    paint()
    if (refocus) file.focusContent()
  })
}

export function reloadFile() {
  if (!current.key.startsWith('f:')) return
  focusFile = current.key
  loadFile(current.key)
}

let docsAt = 0

export function startNavigation({ composer }: NavigationDeps): Navigation {
  function go(key: string) {
    if (key === current.key) return
    scrolls.set(current.key, scroller.scrollTop)
    const leaving = current()
    if (leaving) spots.set(current.key, leaving.view.spot())
    const from = current.key
    current.key = key
    composer.swapDraft(from, key)
    armed.clear()
    if (!openTabs.includes(key)) openTabs = [...openTabs, key]
    recent = [key, ...recent.filter(k => k !== key)]
    if (key !== focusFile) focusFile = ''
    if (key.startsWith('f:')) loadFile(key)
    paint()
    composer.grow()
    const view = current()?.view
    scroller.scrollTop = view?.pinned ? scroller.scrollHeight : scrolls.get(key) ?? (key.startsWith('s/') ? scroller.scrollHeight : 0)
    if (view && !view.pinned) view.restore(spots.get(key))
    composer.paintJump()
    settleFocus()
  }

  function pruneTabs() {
    const sidOf = (k: string) => (k.startsWith('s/') || k.startsWith('c:') ? k.slice(2) : k.startsWith('f:') ? k.slice(2, k.indexOf(':', 2)) : '')
    for (const k of openTabs) if (sidOf(k) && !sessions.has(sidOf(k))) closeTab(k)
    if (current.key.startsWith('f:') && !files.get(current.key)?.text) loadFile(current.key)
  }

  function closeTab(key: string) {
    if (key === 'overview') return
    openTabs = openTabs.filter(k => k !== key)
    files.delete(key)
    recent = recent.filter(k => k !== key && openTabs.includes(k))
    scrolls.delete(key)
    composer.dropDraft(key)
    const owner = key.startsWith('c:') ? `s/${key.slice(2)}` : key.startsWith('f:') ? `s/${key.slice(2, key.indexOf(':', 2))}` : ''
    if (key === current.key) go(owner && openTabs.includes(owner) ? owner : recent[0] || 'overview')
    else paint()
  }

  function openChanges(s: Session) {
    go(`c:${s.sid}`)
    requestAnimationFrame(() => byId<HTMLElementTagNameMap['sf-changes']>('changes').openDiff())
  }

  function openFile(sid: string, path: string, commit = '', line = 0) {
    const key = commit ? `f:${sid}:${path}\0${commit}` : `f:${sid}:${path}`
    const old = files.get(key)
    if (!old) files.set(key, { path, ...(commit ? { commit } : {}), ...(line ? { line } : {}) })
    else if (commit) {
      const next: FileView = { ...old }
      if (line) next.line = line
      else delete next.line
      files.set(key, next)
    }
    focusFile = key
    if (key === current.key) paint()
    go(key)
    byId<HTMLElementTagNameMap['sf-file']>('file').focusContent()
  }

  async function loadDocs() {
    const docs = byId<HTMLElementTagNameMap['sf-docs']>('docs')
    if (docs.loaded || !state.connected || Date.now() - docsAt < 10000) return
    docsAt = Date.now()
    const a = await request('/api/docs')
    docs.text = a.ok && typeof a.body.text === 'string' ? a.body.text : ''
  }

  function openDocs(id: string, back: { view: string; label: string } | null = null) {
    go('docs')
    byId<HTMLElementTagNameMap['sf-docs']>('docs').show(id, back)
  }

  return { go, closeTab, pruneTabs, openChanges, openFile, loadDocs, openDocs }
}
