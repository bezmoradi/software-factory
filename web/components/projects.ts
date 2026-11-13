import { button, el, emit, focusedKey, pageCrumb, refocus, rovingKeys } from '../lib/dom.ts'
import { icon } from './icon.ts'
import { bySessionOrder, isRunning, sessionKey, sessionRow, sessionTable } from './overview.ts'
import type { Expect, Project, ProjectsView, SessionInfo, Starter, TokenHost } from '../types.ts'

const MODELS: [string, string][] = [['', 'Default model'], ['fable', 'Fable'], ['opus', 'Opus'], ['sonnet', 'Sonnet'], ['haiku', 'Haiku']]
function options(select: HTMLSelectElement, list: [string, string][]) {
  for (const [value, text] of list) {
    const o = el('option', '', text)
    o.value = value
    select.append(o)
  }
}


function savedAt(ms: number): string {
  if (!ms) return ''
  const at = new Date(ms)
  return at.toLocaleString(undefined, { month: 'short', day: 'numeric', ...(at.getFullYear() === new Date().getFullYear() ? {} : { year: 'numeric' }), hour: 'numeric', minute: '2-digit' })
}

function hostName(host: string): string {
  return host.replace(/^https?:\/\//i, '').replace(/\/+$/, '')
}

const GITHUB = 'https://github.com'
const TOKEN_HELP = 'https://github.com/settings/personal-access-tokens/new'

function field(label: string, input: HTMLInputElement): HTMLElement {
  const box = el('div', 'field')
  const name = el('label', 'small', label)
  name.htmlFor = input.id
  box.append(name, input)
  return box
}

function stacked(...children: HTMLElement[]): HTMLFormElement {
  const form = el('form', 'stacked')
  form.autocomplete = 'off'
  form.append(...children)
  return form
}

function urlText(url: string): HTMLElement {
  const span = el('span', 'project-path secondary')
  span.title = url
  const text = el('bdi', '', url)
  text.dir = 'ltr'
  span.append(text)
  return span
}

function nearby(key: string): string[] {
  const row = /^row:(.*):[a-z]+$/.exec(key)
  if (row) return ['confirm', 'keep', 'toggle', 'count', 'new', 'more', 'remove'].map(a => `row:${row[1]}:${a}`)
  const host = /^host:(.*):[a-z]+$/.exec(key)
  if (host) return [`host:${host[1]}:replace`, 'token-more', 'token-save']
  if (key.startsWith('token-')) return ['token-more', 'token-save']
  if (key.startsWith('add')) return ['add-more', 'add']
  return []
}

const COMMANDS: Record<string, string> = { plugin: 'claude --bg', codex: 'codex app-server', acp: 'its ACP server' }

class DispatchForm {
  form = el('form', 'dispatch')
  agent = el('select', 'textbox')
  why = el('div', 'small muted')
  name = el('input', 'textbox')
  model = el('select', 'textbox')
  kind = el('select', 'textbox')
  expect = el('select', 'textbox')
  start = button('', 'primary small stack', undefined, 'submit')
  idle = el('span', '', 'Start')
  working = el('span', '', 'Starting…')

  constructor(p: Project, starters: Starter[], onCancel: () => void) {
    const path = p.path
    this.form.autocomplete = 'off'
    this.working.className = 'off'
    this.start.replaceChildren(this.idle, this.working)
    this.name.name = 'name'
    this.model.name = 'model'
    this.name.placeholder = 'Name (optional)'
    this.name.maxLength = 80
    this.name.setAttribute('aria-label', 'Session name')
    this.model.setAttribute('aria-label', 'Model')
    options(this.model, MODELS)
    this.kind.name = 'type'
    this.kind.setAttribute('aria-label', 'Work type')
    options(this.kind, [['', p.type ? `Work type: the project's default (${p.type})` : "Work type: pick before the first message"], ['feature', 'Feature'], ['fix', 'Fix'], ['docs', 'Docs only'], ['explore', 'Exploring']])
    this.agent.name = 'agent'
    this.agent.setAttribute('aria-label', 'Agent')
    const list = starters.length ? starters : [{ id: 'claude', name: 'Claude Code', method: 'plugin', enabled: true }]
    for (const a of list) {
      const o = el('option', '', a.enabled ? a.name : `${a.name} (not available)`)
      o.value = a.id
      o.disabled = !a.enabled
      if (a.off) o.title = a.off
      this.agent.append(o)
    }
    const first = list.find(a => a.enabled)
    if (first) this.agent.value = first.id
    this.expect.setAttribute('aria-label', 'The sign-in you expect of this agent')
    this.expect.title = 'The engine checks the agent\'s own sign-in before it starts, and refuses a start that differs from what you expect'
    options(this.expect, [['any', 'Expect any sign-in'], ['subscription', 'Expect a subscription'], ['api-key', 'Expect an API key']])
    this.expect.addEventListener('change', () => {
      const chosen = list.find(a => a.id === this.agent.value)
      if (chosen) chosen.expect = this.expect.value as Expect
      emit(this.form, 'sf-expect', { agent: this.agent.value, expect: this.expect.value as Expect })
    })
    const row = el('div', 'opts field-row')
    row.append(this.agent, this.name, this.model, this.kind, this.expect, this.start, button('Cancel', 'ghost small', onCancel))
    const note = el('p', 'small muted')
    const command = el('code', '')
    note.append('Fetches ', el('code', '', p.branch || 'the main branch'), ' from ', el('code', '', p.url || p.name), ', then runs ', command, " in a new worktree that starts there, once you've approved the repository files that agent loads at start and its own sign-in check passed. The session shows in this project's list when it connects; open it there to write its first message. This page controls it from the start. It starts in the agent's own asking mode, so it asks before it acts; auto and bypass modes can't be chosen here.")
    const off = list.filter(a => !a.enabled && a.off && /install/i.test(a.off))
    this.why.replaceChildren(...off.map(a => el('p', '', `${a.name}: ${a.off}`)))
    this.why.hidden = !off.length
    const sync = () => {
      const chosen = list.find(a => a.id === this.agent.value)
      command.textContent = COMMANDS[chosen?.method ?? 'plugin'] ?? chosen?.id ?? ''
      const plugin = chosen?.method === 'plugin'
      this.model.hidden = !plugin
      if (!plugin) this.model.value = ''
      this.start.disabled = !chosen?.enabled
      this.expect.value = chosen?.expect ?? 'any'
      this.expect.hidden = plugin
    }
    this.agent.addEventListener('change', sync)
    sync()
    this.form.append(row, note, this.why)
    this.form.addEventListener('submit', e => {
      e.preventDefault()
      if (this.start.disabled) return
      emit(this.form, 'sf-dispatch', { path, agent: this.agent.value, name: this.name.value, model: this.model.value, mode: '', type: this.kind.value })
    })
  }

  busy(on: boolean) {
    this.start.disabled = on || !!this.agent.selectedOptions[0]?.disabled
    this.idle.classList.toggle('off', on)
    this.working.classList.toggle('off', !on)
  }
}

class SfProjects extends HTMLElement {
  #view: ProjectsView = { projects: [], cloning: [], hosts: [] }
  #forms = new Map<string, DispatchForm>()
  #removing = ''
  #message: { path: string; text: string; bad: boolean } = { path: '', text: '', bad: false }
  #busy = false
  #add = el('input', 'textbox')
  #tokenHost = el('input', 'textbox')
  #tokenValue = el('input', 'textbox')
  #replacing = ''
  #replaceValue = el('input', 'textbox')
  #forgetting = ''
  #tokenNote = ''
  #tokenFrom = ''
  #addingHost = false
  #hostEdited = false
  #addingRepo = false
  #sessions: SessionInfo[] = []
  #sessionsKey = ''
  #expanded = new Map<string, boolean>()
  #menuOpen = ''
  #slot: HTMLElement | null = null
  #stale = false
  #starting: { path: string; before: Set<string>; id: string; late: boolean } | null = null
  #startTimer: ReturnType<typeof setTimeout> | undefined

  constructor() {
    super()
    const settle = () => setTimeout(() => {
      if (!this.#stale || this.#choosing()) return
      this.#stale = false
      this.#render()
    })
    this.addEventListener('focusout', settle)
    this.addEventListener('toggle', settle, true)
    this.addEventListener('change', e => {
      if (e.target instanceof HTMLSelectElement && this.#stale) {
        this.#stale = false
        this.#render()
      }
    })
    this.#add.type = 'url'
    this.#add.id = 'repo-url'
    this.#add.placeholder = 'https://git.example.com/team/project.git'
    this.#add.spellcheck = false
    this.#add.name = 'url'
    this.#add.maxLength = 2048
    this.#tokenHost.id = 'token-host'
    this.#tokenHost.placeholder = 'https://git.example.com'
    this.#tokenHost.spellcheck = false
    this.#tokenHost.name = 'host'
    this.#tokenHost.value = GITHUB
    this.#tokenHost.addEventListener('input', () => {
      this.#hostEdited = true
    })
    for (const input of [this.#tokenValue, this.#replaceValue]) {
      input.type = 'password'
      input.autocomplete = 'off'
      input.spellcheck = false
      input.maxLength = 4096
      input.name = 'token'
    }
    this.#tokenValue.id = 'token-value'
    this.#replaceValue.id = 'token-replace'
  }

  #viewKey = ''

  set view(value: ProjectsView) {
    const key = JSON.stringify(value)
    if (key === this.#viewKey) return
    this.#viewKey = key
    this.#view = value
    for (const path of this.#forms.keys()) if (!value.projects.some(p => p.path === path)) this.#forms.delete(path)
    this.#render()
  }

  set sideSlot(value: HTMLElement | null) {
    if (value === this.#slot) return
    this.#slot?.replaceChildren()
    this.#slot = value
    this.#render()
  }

  set sessions(value: SessionInfo[]) {
    const key = sessionKey(value || [])
    this.#sessions = value || []
    if (this.#arrived()) {
      this.#sessionsKey = key
      return
    }
    if (key === this.#sessionsKey) return
    this.#sessionsKey = key
    if (this.#choosing()) this.#stale = true
    else this.#render()
  }

  #choosing(): boolean {
    const a = document.activeElement
    return (a instanceof HTMLSelectElement && this.contains(a)) || !!this.querySelector('.menu:popover-open')
  }

  reveal(path: string) {
    this.#expanded.set(path, true)
    this.#render()
    this.querySelector<HTMLElement>(`[data-key="${CSS.escape(`row:${path}:toggle`)}"]`)?.scrollIntoView({ block: 'nearest' })
  }

  #projectName(path: string): string {
    return this.#view.projects.find(p => p.path === path)?.name ?? ''
  }

  #sessionsIn(path: string): SessionInfo[] {
    const name = this.#projectName(path)
    return this.#sessions.filter(s => s.project ? s.project === name : !!s.cwd && (s.cwd === path || s.cwd.startsWith(`${path}/`))).sort(bySessionOrder)
  }

  #setBusy(value: boolean) {
    this.#busy = value
    for (const f of this.#forms.values()) f.busy(value)
  }

  starting(path: string) {
    clearTimeout(this.#startTimer)
    this.#starting = { path, before: new Set(this.#sessionsIn(path).map(s => s.sid)), id: '', late: false }
    this.#setBusy(true)
    this.say(path, '')
  }

  failed() {
    clearTimeout(this.#startTimer)
    this.#starting = null
    this.#setBusy(false)
  }

  say(path: string, text: string, bad = false) {
    this.#message = { path, text, bad }
    this.#render()
  }

  started(path: string, id: string) {
    if (this.#starting?.path !== path) return
    this.#starting.id = id
    this.#startTimer = setTimeout(() => this.#late(), 30000)
    this.#arrived()
  }

  #arrived(): boolean {
    const s = this.#starting
    if (!s?.id || !this.#sessions.some(x => x.id === s.id && x.live)) return false
    clearTimeout(this.#startTimer)
    this.#starting = null
    if (!s.late) this.#setBusy(false)
    this.#forms.delete(s.path)
    this.#expanded.set(s.path, true)
    if (s.late && this.#message.path === s.path) this.#message = { path: '', text: '', bad: false }
    this.#render()
    return true
  }

  #late() {
    const s = this.#starting
    if (!s) return
    s.late = true
    this.#setBusy(false)
    this.#forms.delete(s.path)
    this.say(s.path, `Started ${s.id || 'the session'}, but it hasn't connected yet. It shows here when it does.`)
  }

  added() {
    this.#add.value = ''
    this.#addingRepo = false
  }

  tokenSaid(text: string, bad = false) {
    if (bad) {
      this.#tokenNote = text
      return this.#render()
    }
    if (this.#tokenFrom === '') {
      this.#addingHost = false
      this.#hostEdited = false
      this.#tokenHost.value = ''
    }
    this.#replacing = ''
    this.#forgetting = ''
    this.#tokenNote = ''
    this.#tokenFrom = ''
    this.#render()
  }

  #tokenSend(from: string, detail: { op: 'save'; host: string; token: string } | { op: 'remove'; host: string }) {
    this.#tokenFrom = from
    this.#tokenNote = ''
    emit(this, 'sf-token', detail)
  }

  #focus(key: string) {
    for (const root of [this.#slot, this]) root?.querySelector<HTMLElement>(`[data-key="${CSS.escape(key)}"]`)?.focus()
  }

  #tokenError(from: string): HTMLElement[] {
    if (!this.#tokenNote || this.#tokenFrom !== from) return []
    const note = el('p', 'small bad', this.#tokenNote)
    note.setAttribute('role', 'alert')
    return [note]
  }

  #who(title: HTMLElement, p: Project): Element[] {
    const who = el('div', 'project-id')
    who.append(title, urlText(p.url || p.path))
    return [icon('folder', 'icon folder'), who]
  }

  #menu(p: Project): HTMLElement[] {
    const more = button('', 'ghost icon-button', undefined, 'button', `row:${p.path}:more`)
    more.setAttribute('aria-label', `More actions for ${p.name}`)
    more.setAttribute('aria-haspopup', 'menu')
    more.append(icon('more'))
    const menu = el('div', 'menu row-menu surface')
    menu.popover = 'auto'
    menu.setAttribute('role', 'menu')
    menu.setAttribute('aria-label', `Actions for ${p.name}`)
    more.popoverTargetElement = menu
    const item = (text: string, run: () => void, glyph: string) => {
      const b = button('', '', () => {
        this.#menuOpen = ''
        menu.hidePopover()
        more.focus()
        run()
      })
      b.setAttribute('role', 'menuitem')
      const mark = el('span', 'mark')
      mark.append(icon(glyph))
      b.append(mark, el('span', '', text))
      menu.append(b)
    }
    item('Remove', () => {
      this.#removing = p.path
      this.#render()
    }, 'trash')
    menu.addEventListener('toggle', e => {
      if ((e as ToggleEvent).newState !== 'open') {
        if (this.#menuOpen === p.path && menu.isConnected) this.#menuOpen = ''
        return
      }
      this.#menuOpen = p.path
      this.#place(more, menu)
      menu.querySelector<HTMLElement>('button')?.focus()
    })
    menu.addEventListener('keydown', e => {
      const items = [...menu.querySelectorAll<HTMLElement>('button')]
      rovingKeys(e, items, items.indexOf(document.activeElement as HTMLElement), 'vertical')
    })
    return [more, menu]
  }

  #place(anchor: HTMLElement, menu: HTMLElement) {
    const r = anchor.getBoundingClientRect()
    const w = menu.offsetWidth
    const h = menu.offsetHeight
    const top = r.bottom + 4 + h > innerHeight - 8 ? Math.max(8, r.top - 4 - h) : r.bottom + 4
    menu.style.top = `${Math.round(top)}px`
    menu.style.left = `${Math.round(Math.max(8, Math.min(r.right - w, innerWidth - w - 8)))}px`
  }

  #row(p: Project) {
    const li = el('li', 'boxlist-row')
    const head = el('div', 'row')
    const name = el('span', 'name')
    name.append(el('span', 'title', p.name))
    const wait = this.#starting
    const list = this.#sessionsIn(p.path).filter(s => !wait || wait.late || wait.path !== p.path || !this.#forms.has(p.path) || wait.before.has(s.sid))
    const count = list.filter(isRunning).length
    const open = list.length > 0 && (this.#expanded.get(p.path) ?? count > 0)
    const toggle = () => {
      this.#expanded.set(p.path, !open)
      this.#render()
    }
    if (list.length) {
      const chevron = button('', 'ghost project-toggle', toggle, 'button', `row:${p.path}:toggle`)
      chevron.append(icon('chevron'))
      chevron.setAttribute('aria-expanded', String(open))
      chevron.setAttribute('aria-label', `${open ? 'Hide' : 'Show'} the sessions in ${p.name}`)
      head.append(chevron)
    } else head.append(el('span', 'project-toggle-space'))
    head.append(...this.#who(name, p))
    if (list.length && this.#removing !== p.path) {
      const counted = button(count ? `${count} running` : `${list.length} stopped`, 'link project-count secondary', toggle, 'button', `row:${p.path}:count`)
      counted.setAttribute('aria-expanded', String(open))
      head.append(counted)
    }
    const acts = el('span', 'acts')
    if (this.#removing === p.path && list.length) {
      acts.append(el('span', 'small muted', `Delete its ${list.length === 1 ? 'session' : `${list.length} sessions`} first: open each, stop it, then delete it.`), button('OK', 'ghost small', () => {
        this.#removing = ''
        this.#render()
      }, 'button', `row:${p.path}:keep`))
    } else if (this.#removing === p.path) {
      acts.append(el('span', 'small muted', 'Remove from the list? Its clone stays in the workspace.'), button('Remove', 'ghost small danger', () => {
        this.#removing = ''
        emit(this, 'sf-repo', { op: 'remove', name: p.name })
      }, 'button', `row:${p.path}:confirm`), button('Cancel', 'ghost small', () => {
        this.#removing = ''
        this.#render()
      }, 'button', `row:${p.path}:keep`))
    } else {
      if (!this.#forms.has(p.path)) acts.append(button('New session', 'ghost small', () => this.#open(p.path), 'button', `row:${p.path}:new`))
      acts.append(...this.#menu(p))
    }
    head.append(acts)
    li.append(head)
    if (open) {
      li.append(sessionTable(list.map(s => sessionRow(s, this.#sessions, `proj:${p.path}:${s.sid}`, true)), 'project-sessions with-menu', `Sessions in ${p.name}`))
    }
    if (this.#message.path === p.path && this.#message.text) li.append(el('p', `small ${this.#message.bad ? 'bad' : 'muted'}`, this.#message.text))
    const form = this.#forms.get(p.path)
    if (form) li.append(form.form)
    return li
  }

  #open(path: string) {
    const p = this.#view.projects.find(x => x.path === path)
    if (!p) return
    const form = new DispatchForm(p, this.#view.starters ?? [], () => {
      this.#forms.delete(path)
      this.#render()
    })
    form.busy(this.#busy)
    this.#forms.set(path, form)
    if (this.#message.path === path) this.#message = { path: '', text: '', bad: false }
    this.#render()
    form.name.focus()
  }

  #cloning(): HTMLElement[] {
    const list = this.#view.cloning ?? []
    if (!list.length) return []
    const ul = el('ul', 'boxlist projects cloning')
    for (const k of list) {
      const li = el('li', 'boxlist-row')
      const row = el('div', 'row')
      const who = el('div', 'project-id')
      const name = el('span', 'name')
      name.append(el('span', 'title', k.name))
      who.append(name, urlText(k.url))
      const status = el('span', 'acts small muted', 'Cloning…')
      status.setAttribute('role', 'status')
      row.append(el('span', 'project-toggle-space'), icon('folder', 'icon folder'), who, status)
      li.append(row)
      ul.append(li)
    }
    return [ul]
  }

  #hostRow(h: TokenHost): HTMLElement {
    const li = el('li')
    const row = el('div', 'host-row')
    const who = el('div', 'host-who')
    const name = el('span', 'host-name')
    name.title = h.host
    const text = el('bdi', '', hostName(h.host))
    text.dir = 'ltr'
    name.append(text)
    who.append(name, el('span', 'host-saved', h.savedAt ? `Saved ${savedAt(h.savedAt)}` : 'Saved'))
    row.append(who)
    if (this.#forgetting !== h.host && this.#replacing !== h.host) {
      const acts = el('span', 'host-acts')
      acts.append(button('Replace', 'link', () => {
        this.#replacing = h.host
        this.#forgetting = ''
        this.#addingHost = false
        this.#tokenNote = ''
        this.#replaceValue.value = ''
        this.#render()
        this.#replaceValue.focus()
      }, 'button', `host:${h.host}:replace`), button('Remove', 'link', () => {
        this.#forgetting = h.host
        this.#replacing = ''
        this.#addingHost = false
        this.#tokenNote = ''
        this.#render()
        this.#focus(`host:${h.host}:confirm`)
      }, 'button', `host:${h.host}:remove`))
      row.append(acts)
    }
    li.append(row)
    if (this.#forgetting === h.host) {
      const confirm = el('div', 'stacked')
      confirm.append(el('p', 'small muted', 'Remove this token? Clones and fetches from this host go without one.'), button('Remove token', 'danger', () => {
        this.#tokenSend(h.host, { op: 'remove', host: h.host })
      }, 'button', `host:${h.host}:confirm`), button('Cancel', 'link small', () => {
        this.#forgetting = ''
        this.#tokenNote = ''
        this.#render()
      }, 'button', `host:${h.host}:keep`), ...this.#tokenError(h.host))
      li.append(confirm)
    }
    if (this.#replacing === h.host) {
      const form = stacked(field(`New token for ${hostName(h.host)}`, this.#replaceValue), button('Save token', 'primary', undefined, 'submit', `host:${h.host}:save`), button('Cancel', 'link small', () => {
        this.#replacing = ''
        this.#replaceValue.value = ''
        this.#tokenNote = ''
        this.#render()
      }, 'button', `host:${h.host}:cancel`), ...this.#tokenError(h.host))
      form.addEventListener('submit', e => {
        e.preventDefault()
        const token = this.#replaceValue.value
        this.#replaceValue.value = ''
        if (token) this.#tokenSend(h.host, { op: 'save', host: h.host, token })
      })
      li.append(form)
    }
    return li
  }

  #tokens(): HTMLElement[] {
    const hosts = this.#view.hosts ?? []
    const title = el('h2', 'label', 'Git host tokens')
    const about = el('p', 'small token-about', 'One token per git host. Software Factory uses it to clone, fetch, push and open pull requests; agents never see it.')
    const help = el('a', 'small token-help', 'How to create one on GitHub')
    help.href = TOKEN_HELP
    help.target = '_blank'
    help.rel = 'noopener noreferrer'
    const scopes = el('p', 'small muted token-scopes', 'Fine-grained, limited to your repositories: Contents (read and write), Issues and Pull requests (read and write), Actions and Commit statuses (read). Add Workflows (read and write) if sessions change .github/workflows.')
    const parts: HTMLElement[] = [title, about, help, scopes]
    if (hosts.length) {
      const ul = el('ul', 'host-list')
      ul.setAttribute('aria-label', 'Saved git host tokens')
      for (const h of hosts) ul.append(this.#hostRow(h))
      parts.push(ul)
    }
    if (hosts.length && !this.#addingHost) {
      if (this.#replacing || this.#forgetting) return parts
      const more = button('+ Add another host', 'link small add-more', () => {
        this.#addingHost = true
        this.#replacing = ''
        this.#forgetting = ''
        this.#tokenNote = ''
        this.#tokenHost.value = ''
        this.#tokenValue.value = ''
        this.#render()
        this.#tokenHost.focus()
      }, 'button', 'token-more')
      parts.push(more)
      return parts
    }
    if (!hosts.length && !this.#hostEdited && !this.#tokenHost.value) this.#tokenHost.value = GITHUB
    const cancel = hosts.length ? [button('Cancel', 'link small', () => {
      this.#addingHost = false
      this.#tokenValue.value = ''
      if (this.#tokenFrom === '') this.#tokenNote = ''
      this.#render()
    }, 'button', 'token-cancel')] : []
    const form = stacked(field('Host', this.#tokenHost), field('Token', this.#tokenValue), button('Save token', 'primary', undefined, 'submit', 'token-save'), ...cancel, ...this.#tokenError(''))
    form.addEventListener('submit', e => {
      e.preventDefault()
      const host = this.#tokenHost.value.trim()
      const token = this.#tokenValue.value
      if (!host) return this.#tokenHost.focus()
      if (!token) return this.#tokenValue.focus()
      this.#tokenValue.value = ''
      this.#tokenSend('', { op: 'save', host, token })
    })
    parts.push(form)
    return parts
  }

  #render() {
    this.#stale = false
    const focused = document.activeElement
    const focusKey = focusedKey(this)
    const sideKey = this.#slot ? focusedKey(this.#slot) : ''
    const { projects } = this.#view
    const empty = !projects.length && !(this.#view.cloning ?? []).length
    const addControl = (collapsible: boolean): HTMLElement => {
      const cancel = collapsible ? [button('Cancel', 'link small', () => {
        this.#addingRepo = false
        this.#add.value = ''
        if (this.#message.path === '' && this.#message.bad) this.#message = { path: '', text: '', bad: false }
        this.#render()
      }, 'button', 'add-cancel')] : []
      const add = stacked(field('Repository', this.#add), button('Add repository', 'primary', undefined, 'submit', 'add'), ...cancel)
      add.classList.add('repo-add')
      add.addEventListener('submit', e => {
        e.preventDefault()
        const url = this.#add.value.trim()
        if (!url) return this.#add.focus()
        emit(this, 'sf-repo', { op: 'add', url })
      })
      return add
    }
    const head = el('div', 'projects-head')
    const docs = button('View in docs', 'link small docs-link', undefined, 'button', 'docs')
    docs.dataset.doc = 'projects-and-new-sessions'
    docs.prepend(icon('link'))
    head.append(pageCrumb('Projects'), docs)
    const sub = el('div', 'projects-sub')
    sub.append(el('p', 'small muted', 'Repositories the engine cloned by their https address. Each new session works in its own worktree, started from the main branch fetched at that moment.'))
    if (!empty && !this.#addingRepo) sub.append(button('+ Add repository', 'link small add-more', () => {
      this.#addingRepo = true
      this.#render()
      this.#add.focus()
    }, 'button', 'add-more'))
    const parts: HTMLElement[] = [head, sub]
    if (!empty && this.#addingRepo) parts.push(addControl(true))
    if (this.#message.path === '' && this.#message.text) {
      const note = el('p', `small ${this.#message.bad ? 'bad' : 'muted'}`, this.#message.text)
      note.setAttribute('role', 'status')
      parts.push(note)
    }
    if (empty) {
      const box = el('div', 'projects-empty')
      const media = el('div', 'projects-empty-icon')
      media.append(icon('folder', ''))
      box.append(media, el('h3', '', 'No projects yet'), el('p', 'small muted', "Add a repository by its https address. If it's private, save a token for its git host first."), addControl(false))
      parts.push(box)
    }
    parts.push(...this.#cloning())
    if (projects.length) {
      const ul = el('ul', 'boxlist projects')
      for (const p of projects) ul.append(this.#row(p))
      parts.push(ul)
    }
    const side = this.#tokens()
    if (this.#slot) this.#slot.replaceChildren(...side)
    else parts.push(...side)
    this.replaceChildren(...parts)
    const open = this.#menuOpen
    if (open) {
      const more = [...this.querySelectorAll<HTMLButtonElement>('button.icon-button')].find(b => b.dataset.key === `row:${open}:more`)
      const menu = more?.popoverTargetElement
      if (more && menu instanceof HTMLElement) {
        menu.showPopover()
        return
      }
      this.#menuOpen = ''
    }
    if (focused instanceof HTMLElement && focused !== document.activeElement && (this.contains(focused) || this.#slot?.contains(focused))) focused.focus()
    else if (sideKey && this.#slot) refocus(this.#slot, sideKey, nearby)
    else refocus(this, focusKey, nearby)
  }

}

customElements.define('sf-projects', SfProjects)

declare global {
  interface HTMLElementTagNameMap {
    'sf-projects': SfProjects
  }
}
