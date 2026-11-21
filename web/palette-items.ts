import { request, sendState } from './api.ts'
import { armed, current, offWhy, sessions, state, type Session } from './app-state.ts'
import { setSide } from './layout.ts'
import type { Navigation } from './navigation.ts'
import { reviewAsking, type SessionActions } from './session-actions.ts'
import { byId, shown, visible } from './lib/dom.ts'
import { label } from './lib/format.ts'
import { isMac, openPalette, openShortcuts, paletteKey, typing, type PaletteItem } from './components/palette.ts'
import { openEgress } from './components/egress-panel.ts'
import { openAway } from './components/halt.ts'
import { openWorkspace } from './components/workspace.ts'
import { openExceptions } from './components/exceptions.ts'
import { openBackup } from './components/backup.ts'
import { openDiagnostics } from './components/diagnostics.ts'
import type { Project, ReviewsView } from './types.ts'

interface PaletteDeps {
  reviewsOf: (s: Session) => ReviewsView | null
  projectList: () => readonly Project[]
  projects: HTMLElementTagNameMap['sf-projects']
  labelOfSid: (sid: string) => string
  forgetSession: (sid: string) => void
  openIdentity: () => void
  openSignIn: () => void
  navigation: Pick<Navigation, 'go' | 'openChanges' | 'openDocs'>
  actions: Pick<SessionActions, 'wakeSession' | 'startReview' | 'startPush'>
}

export function startPalette({ reviewsOf, projectList, projects, labelOfSid, forgetSession, openIdentity, openSignIn, navigation: { go, openChanges, openDocs }, actions: { wakeSession, startReview, startPush } }: PaletteDeps) {
  function openSessionPanel(which: 'changes' | 'rules') {
    const s = current()
    if (which === 'changes' && s) return openChanges(s)
    setSide(true)
    if (!document.body.classList.contains('side-full') && !document.body.classList.contains('wide')) {
      byId('side-toggle').setAttribute('aria-expanded', 'true')
      byId('side').classList.add('open')
    }
    requestAnimationFrame(() => {
      if (which === 'changes') byId<HTMLElementTagNameMap['sf-changes']>('changes').openDiff()
      else byId<HTMLElementTagNameMap['sf-rules']>('rules').open()
    })
  }

  function sessionActions(s: Session): PaletteItem[] {
    const name = visible(label(s.info))
    const group = `This session: ${name}`
    const offline = !state.connected ? 'the page isn’t connected to the engine' : ''
    const rv = reviewsOf(s)
    const noReview = !rv ? offWhy(s, 'review') : rv.disabled
    const out: PaletteItem[] = []
    out.push({ id: 'wake', group, label: 'Wake the session', detail: 'Asks for any sign-in or cost confirmation, as Wake does', off: offline || offWhy(s, 'wake'), run: () => void wakeSession(s) })
    out.push({ id: 'turn', group, label: 'Stop the turn', off: offline || offWhy(s, 'stop'), run: () => byId('stop').click() })
    const manageButton = byId<HTMLButtonElement>('manage-button')
    const stopping = manageButton.dataset.action !== 'remove'
    out.push({ id: 'manage', group, label: stopping ? 'Stop the session' : 'Delete the session', detail: 'Then press Confirm, as with its button', off: offline || offWhy(s, 'manage') || (manageButton.disabled || manageButton.getAttribute('aria-busy') === 'true' ? 'it is already being stopped or deleted' : ''), run: () => {
      for (const action of ['stop', 'remove', 'force']) armed.delete(`${s.sid}:${action}`)
      manageButton.click()
      manageButton.focus()
    } })
    out.push({ id: 'review', group, label: 'Review', detail: 'Runs the required reviewers, asking for cost confirmation when the sign-in is per use', off: offline || noReview || (s.info.review?.running || reviewAsking.has(s.sid) ? 'a review is running' : ''), run: () => void startReview(s, false) })
    out.push({ id: 'push', group, label: 'Push', detail: 'The gate checks the reviews first; a held push waits on its card', off: offline || noReview || (rv?.pushing ? 'a push is being decided' : rv?.pushed ? 'the latest commit is already pushed' : ''), run: () => void startPush(s) })
    out.push({ id: 'changes', group, label: 'Open Changes', detail: 'The diff, its comparison points and viewed marks', off: !s.changes ? 'the engine hasn’t sent this session’s changes yet' : '', run: () => openSessionPanel('changes') })
    out.push({ id: 'rules', group, label: 'What this session may and may not do', off: offWhy(s, 'rules'), run: () => openSessionPanel('rules') })
    return out
  }

  function paletteItems(): PaletteItem[] {
    const items: PaletteItem[] = []
    const s = current()
    if (s) items.push(...sessionActions(s))
    for (const x of [...sessions.values()].sort((a, b) => (b.info.startedAt || 0) - (a.info.startedAt || 0))) {
      items.push({ id: `s:${x.sid}`, group: 'Sessions', label: visible(label(x.info)), detail: [shown(x.info.summary?.word ?? ''), x.info.project, x.info.lineage?.branch].filter(Boolean).map(t => visible(String(t))).join(' · '), run: () => go(`s/${x.sid}`) })
    }
    for (const p of projectList()) {
      items.push({ id: `p:${p.path}`, group: 'Projects', label: visible(p.name), detail: 'Open the project', run: () => {
        go('projects')
        projects.reveal(p.path, true)
      } })
      items.push({ id: `start:${p.path}`, group: 'Projects', label: `Start a session in ${visible(p.name)}`, detail: 'Opens the new-session form', off: !state.connected ? 'the page isn’t connected to the engine' : '', run: () => {
        go('projects')
        projects.startIn(p.path)
      } })
    }
    const panel = (id: string, title: string, run: () => void, off = '') => items.push({ id, group: 'Views and panels', label: title, off, run })
    const needsEngine = !state.connected ? 'the page isn’t connected to the engine' : ''
    panel('v:welcome', 'Welcome', () => go('overview'))
    panel('v:search', 'Search and filter sessions', () => {
      go('overview')
      byId<HTMLElementTagNameMap['sf-overview']>('overview').focusSearch()
    })
    panel('v:projects', 'Projects', () => go('projects'))
    panel('v:docs', 'Docs', () => openDocs(''))
    panel('v:egress', 'Network access', () => openEgress(request), needsEngine)
    panel('v:workspace', 'Workspace and disk space', () => openWorkspace(request, labelOfSid, forgetSession), needsEngine)
    panel('v:exceptions', 'Register of exceptions', () => openExceptions(request, labelOfSid), needsEngine)
    panel('v:backup', 'Export and import', () => openBackup(request, sendState), needsEngine)
    panel('v:diagnostics', 'Diagnostics', () => openDiagnostics(request), needsEngine)
    panel('v:identity', 'Git name and email', () => openIdentity(), needsEngine)
    panel('v:signin', 'Reviewers’ sign-in', () => openSignIn(), needsEngine)
    panel('v:away', 'Away mode', () => openAway(), needsEngine)
    panel('v:prefs', 'Settings', () => byId('prefs').showPopover())
    items.push({ id: 'v:keys', group: 'Views and panels', label: 'Keyboard shortcuts', keys: document.documentElement.dataset.keys === 'off' ? undefined : ['?'], run: () => showShortcuts() })
    return items
  }

  function showShortcuts() {
    const root = document.documentElement
    openShortcuts(root.dataset.keys !== 'off', on => {
      root.dataset.keys = on ? 'on' : 'off'
      void request('/api/prefs', { keys: root.dataset.keys })
    })
  }

  const paletteButton = byId('palette-open')
  if (isMac) {
    paletteButton.setAttribute('aria-keyshortcuts', 'Meta+K')
    paletteButton.title = 'Command palette (⌘K)'
  }
  paletteButton.addEventListener('click', () => openPalette(paletteItems))
  byId('shortcuts-open').addEventListener('click', () => {
    byId('prefs').hidePopover()
    showShortcuts()
  })
  document.addEventListener('keydown', e => {
    if (e.defaultPrevented || e.isComposing) return
    const modal = document.querySelector('dialog[open]:not(.palette)')
    if (paletteKey(e)) {
      if (modal) return
      e.preventDefault()
      openPalette(paletteItems)
      return
    }
    if (e.key !== '?' || e.ctrlKey || e.metaKey || e.altKey || modal || document.querySelector('dialog.palette[open]')) return
    if (document.documentElement.dataset.keys === 'off' || typing(e.target)) return
    e.preventDefault()
    showShortcuts()
  })
}
