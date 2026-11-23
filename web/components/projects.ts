import { busy, button, el, emit, focusedKey, labelError, refocus, replaceKeeping, rovingKeys, sayIfChanged, statusLine, visible } from '../lib/dom.ts'
import { request } from '../api.ts'
import type { EngineAnswer } from '../api.ts'
import { actions as actionsRow, answerControl, field, fieldRow, form, showAnswer } from '../lib/form.ts'
import { emptyState, pageHead } from '../lib/page.ts'
import { icon } from './icon.ts'
import { secret } from './secret.ts'
import './issue-picker.ts'
import './usage.ts'
import './progress.ts'
import { bySessionOrder, grouped, isRunning, sessionKey, sessionRow, sessionTable } from './session-list.ts'
import { placeMenu } from '../lib/menu.ts'
import { StartFromField } from './start-from.ts'
import type { CloneProgress, Cloning, Expect, Project, ProjectsView, SessionInfo, Starter, TokenHost } from '../types.ts'
import { textbox, dropdown, option } from '../lib/controls.ts'

function modelChoices(starter: Starter | undefined): [string, string][] {
  return [['', 'Default model'], ...(starter?.models ?? []).map((m): [string, string] => [m.id, m.name])]
}
function options(select: HTMLSelectElement, list: [string, string][]) {
  for (const [value, text] of list) select.append(option(text, value))
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
const GITLAB = 'https://gitlab.com'
const TOKEN_NAME = 'name=Software+Factory&description=Clone%2C+fetch+and+push+for+Software+Factory'

interface TokenHelp {
  label: string
  link: string
  linkText: string
  scopes: string
}

const TOKEN_HOSTS: Record<string, TokenHelp> = {
  [GITHUB]: {
    label: 'GitHub',
    link: `${GITHUB}/settings/personal-access-tokens/new?${TOKEN_NAME}&expires_in=90&contents=write&issues=write&pull_requests=write&actions=read&statuses=read`,
    linkText: 'Create a GitHub token',
    scopes: 'The link opens GitHub with the right permissions already chosen. Pick your repositories, generate the token, then paste it below.',
  },
  [GITLAB]: {
    label: 'GitLab',
    link: `${GITLAB}/-/user_settings/personal_access_tokens?${TOKEN_NAME}&scopes=read_repository,write_repository`,
    linkText: 'Create a GitLab token',
    scopes: 'The link opens GitLab with the right scopes already chosen. Set an expiry date, create the token, then paste it below.',
  },
}

function tokenHostHelp(host: string): TokenHelp | undefined {
  return Object.hasOwn(TOKEN_HOSTS, host) ? TOKEN_HOSTS[host] : undefined
}

function blank(...inputs: HTMLInputElement[]) {
  for (const input of inputs) {
    input.value = ''
    input.type = 'password'
    labelError(input, '')
  }
}

function actions(...buttons: HTMLElement[]): HTMLElement[] {
  return buttons.length ? [actionsRow(buttons)] : []
}

function stacked(...children: HTMLElement[]): HTMLFormElement {
  return form({ cls: 'stacked', autocomplete: 'off', children })
}

function nearby(key: string): string[] {
  const row = /^row:(.*):[a-z]+$/.exec(key)
  if (row) return ['confirm', 'keep', 'count', 'new', 'more', 'remove'].map(a => `row:${row[1]}:${a}`)
  const host = /^host:(.*):[a-z]+$/.exec(key)
  if (host) return [`host:${host[1]}:replace`, 'token-more', 'token-save']
  if (key.startsWith('token-')) return ['token-more', 'token-save']
  if (key.startsWith('add')) return ['add-more', 'add']
  return []
}


let dispatchSerial = 0

class DispatchForm {
  form = form({ cls: 'dispatch', autocomplete: 'off' })
  agent = dropdown({ name: 'agent' })
  why = el('div', 'small muted')
  name = textbox({ name: 'name' })
  model = dropdown({ name: 'model' })
  effort = dropdown({ name: 'effort' })
  starter: Starter | undefined
  expect = dropdown()
  task = dropdown()
  issue = el('sf-issue-picker')
  start = button('Start', 'primary small', undefined, 'submit')
  from: StartFromField
  #agentField: HTMLDivElement
  #modelField: HTMLDivElement
  #effortField: HTMLDivElement
  #expectField: HTMLDivElement

  constructor(p: Project, starters: Starter[], onCancel: () => void, sessions: SessionInfo[] = [], fork = '', forkAt = '') {
    const path = p.path
    const n = ++dispatchSerial
    this.name.id = `dispatch-name-${n}`
    this.name.addEventListener('input', () => labelError(this.name, ''))
    const list = starters
    for (const a of list) {
      const o = option(a.enabled ? a.name : `${a.name} (not available)`, a.id, { disabled: !a.enabled })
      if (a.off) o.title = a.off
      this.agent.append(o)
    }
    const first = list.find(a => a.default)
    if (first) this.agent.value = first.id
    options(this.expect, [['any', 'Any sign-in'], ['subscription', 'A subscription'], ['api-key', 'An API key']])
    this.expect.addEventListener('change', () => {
      const chosen = list.find(a => a.id === this.agent.value)
      if (chosen) chosen.expect = this.expect.value as Expect
      emit(this.form, 'sf-expect', { agent: this.agent.value, expect: this.expect.value as Expect })
    })
    this.#agentField = field('Agent', this.agent)
    this.#agentField.hidden = list.length < 2
    this.#modelField = field('Model', this.model)
    this.#effortField = field('Effort', this.effort)
    this.#expectField = field('Expected sign-in', this.expect)
    const off = list.filter(a => a.install && a.off)
    this.why.replaceChildren(...off.map(a => el('p', '', `${a.name}: ${a.off}`)))
    this.why.hidden = !off.length
    const sync = () => {
      const chosen = list.find(a => a.id === this.agent.value)
      const model = chosen?.fields?.model === true
      this.#modelField.hidden = !model
      if (!model) this.model.value = ''
      this.choices(chosen)
      this.start.disabled = !chosen?.enabled
      this.expect.value = chosen?.expect ?? 'any'
      this.#expectField.hidden = chosen?.fields?.expect !== true
    }
    this.agent.addEventListener('change', sync)
    this.from = new StartFromField(path, p.branch ?? '', sessions, fork, forkAt)
    this.issue.path = path
    sync()
    options(this.task, [['', 'A blank session'], ['issue', 'An issue']])
    this.task.addEventListener('change', () => this.#showIssue())
    this.issue.hidden = true
    this.issue.numberField.hidden = true
    this.form.append(
      fieldRow([
        this.from.box, this.#agentField, this.#modelField, this.#effortField, this.#expectField,
        field('Session name', this.name, { optional: true, cls: 'line-start' }), field('Begin with', this.task), this.issue.numberField,
        this.issue,
      ], 'columns'),
      this.why,
      actionsRow([button('Cancel', 'ghost small', onCancel), this.start]),
    )
    this.form.addEventListener('submit', () => {
      if (this.start.disabled) return
      this.unrefuse()
      const from = this.from.value()
      const issue = this.issue.hidden ? null : this.issue.chosen()
      emit(this.form, 'sf-dispatch', { path, agent: this.agent.value, name: this.name.value, model: this.model.value, mode: '', ...(from ? { from } : {}), ...(issue ?? {}), ...(this.effort.value ? { effort: this.effort.value } : {}) })
    })
    this.model.addEventListener('change', () => this.efforts())
  }

  #showIssue() {
    const on = this.task.value === 'issue'
    this.issue.hidden = !on
    this.issue.numberField.hidden = !on
    if (on) this.issue.opened()
    else this.issue.reset()
  }

  refuse(a: EngineAnswer): boolean {
    if (!a.message) return false
    if (a.field === 'issue') {
      this.task.value = 'issue'
      this.#showIssue()
      return this.issue.refuse(a.message)
    }
    const input = answerControl(this.form, a)
    if (!input) return false
    showAnswer(this.form, a)
    return true
  }

  unrefuse() {
    for (const input of [this.name, this.from.select]) labelError(input, '')
  }

  choices(starter: Starter | undefined) {
    const fresh = this.starter !== starter
    this.starter = starter
    this.#effortField.hidden = starter?.fields?.effort !== true
    if (starter?.fields?.model === true) {
      const keep = fresh ? starter.defaultModel ?? '' : this.model.value
      const models = modelChoices(starter)
      this.model.replaceChildren()
      options(this.model, models)
      if (models.some(([v]) => v === keep)) this.model.value = keep
    }
    this.efforts(fresh)
  }

  efforts(fresh = false) {
    if (this.starter?.fields?.effort !== true) return this.effort.replaceChildren()
    const keep = fresh ? this.starter.defaultEffort ?? '' : this.effort.value
    this.effort.replaceChildren()
    options(this.effort, [['', 'Default effort'], ...(this.starter?.efforts ?? []).map((e): [string, string] => [e, visible(e)])])
    if ([...this.effort.options].some(o => o.value === keep)) this.effort.value = keep
  }

  busy(on: boolean) {
    this.start.disabled = !!this.agent.selectedOptions[0]?.disabled
    busy(this.start, on)
  }

  async suggestName(open: () => boolean) {
    const a = await request('/api/session-name')
    const name = a.ok && typeof a.body.name === 'string' ? a.body.name : ''
    if (name && open() && !this.name.value) this.name.value = name
  }
}

class SfProjects extends HTMLElement {
  #view: ProjectsView = { projects: [], cloning: [], hosts: [] }
  #forms = new Map<string, DispatchForm>()
  #removing = ''
  #message: { path: string; text: string; bad: boolean } = { path: '', text: '', bad: false }
  #messageLine = statusLine('small status-slot')
  #cloning = new Map<string, { url: string; li: HTMLLIElement; row: HTMLDivElement; bar: HTMLElementTagNameMap['sf-progress'] }>()
  #list = el('ul', 'boxlist projects')
  #rows = new Map<string, HTMLLIElement>()
  #said = new Map<string, HTMLParagraphElement>()
  #busy = false
  #add = textbox({ type: 'url', placeholder: 'https://git.example.com/team/project.git', spellcheck: false, name: 'url' })
  #pending = new Map<'repo' | 'token', { key: string; after?: () => void }>()
  #addError = ''
  #tokenHost = dropdown({ name: 'host' })
  #tokenValue = textbox({ type: 'password', autocomplete: 'off', spellcheck: false, name: 'token' })
  #replacing = ''
  #replaceValue = textbox({ type: 'password', autocomplete: 'off', spellcheck: false, name: 'token' })
  #hostField: HTMLElement
  #tokenField: HTMLElement
  #replaceField: HTMLElement
  #replaceWords = document.createTextNode('New token')
  #tokenForm = stacked()
  #replaceForm = stacked()
  #hostList = el('ul', 'host-list')
  #hostRows = new Map<string, HTMLLIElement>()
  #forgetting = ''
  #tokenFrom = ''
  #addingHost = false
  #hostEdited = false
  #addingRepo = false
  #sessions: SessionInfo[] = []
  #sessionsKey = ''
  #expanded = new Map<string, boolean>()
  #expiry = new Map<string, HTMLElementTagNameMap['sf-token-expiry']>()
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
    this.#add.id = 'repo-url'
    this.#add.setAttribute('aria-required', 'true')
    this.#add.addEventListener('input', () => {
      if (!this.#addError) return
      this.#addError = ''
      this.#render()
    })
    this.#tokenHost.id = 'token-host'
    this.#tokenHost.addEventListener('change', () => {
      labelError(this.#tokenHost, '')
      this.#hostEdited = true
      this.#render()
    })
    for (const input of [this.#tokenValue, this.#replaceValue]) input.setAttribute('aria-required', 'true')
    this.#tokenValue.id = 'token-value'
    this.#replaceValue.id = 'token-replace'
    for (const input of [this.#tokenValue, this.#replaceValue]) {
      input.addEventListener('input', () => labelError(input, ''))
    }
    this.#hostField = field('Host', this.#tokenHost)
    this.#tokenField = field('Token', secret(this.#tokenValue, 'token'))
    this.#replaceField = field(this.#replaceWords, secret(this.#replaceValue, 'token'))
    this.#hostList.setAttribute('aria-label', 'Saved git host tokens')
    this.#tokenForm.addEventListener('submit', () => {
      const host = this.#tokenHost.value.trim()
      const token = this.#tokenValue.value
      this.#tokenSend('', { op: 'save', host, token }, 'token-save')
    })
    this.#replaceForm.addEventListener('submit', () => {
      const host = this.#replacing
      const token = this.#replaceValue.value
      if (host) this.#tokenSend(host, { op: 'save', host, token }, `host:${host}:save`)
    })
  }

  connectedCallback() {
    if (this.#starting?.id && !this.#starting.late && this.#startTimer === undefined) this.#startTimer = setTimeout(() => this.#late(), 30000)
  }

  disconnectedCallback() {
    clearTimeout(this.#startTimer)
    this.#startTimer = undefined
  }

  #viewKey = ''

  set view(value: ProjectsView) {
    const key = JSON.stringify(value)
    if (key === this.#viewKey) return
    this.#viewKey = key
    this.#view = value
    const kept = new Set(value.projects.map(p => p.path))
    for (const m of [this.#rows, this.#said, this.#forms, this.#expanded, this.#expiry]) for (const path of m.keys()) if (!kept.has(path)) m.delete(path)
    this.#render()
  }

  get needsIdentity(): boolean {
    return this.#needs('identity')
  }

  #needs(id: string): boolean {
    return Array.isArray(this.#view.readiness) && this.#view.readiness.some(n => n && n.id === id)
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

  startIn(path: string) {
    if (!this.#view.projects.some(x => x.path === path)) return
    this.#expanded.set(path, true)
    if (this.#forms.has(path)) {
      this.#render()
      this.#forms.get(path)?.from.select.focus()
      return
    }
    this.#open(path)
  }

  reveal(path: string, focus = false) {
    this.#expanded.set(path, true)
    this.#render()
    const row = ['toggle', 'count', 'new', 'more'].map(a => this.querySelector<HTMLElement>(`[data-key="${CSS.escape(`row:${path}:${a}`)}"]`)).find(Boolean)
    row?.scrollIntoView({ block: 'nearest' })
    if (focus) row?.focus({ preventScroll: true })
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

  startRefused(path: string, a: EngineAnswer): boolean {
    const form = this.#forms.get(path)
    return !!form?.form.isConnected && !!a.field && form.refuse(a)
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
    this.#addError = ''
  }

  addFailed(text: string) {
    this.#addError = text
    this.#render()
    this.#add.focus()
  }

  tokenFailed(text: string, field: string): boolean {
    const fresh = this.#tokenFrom === ''
    const input = field === 'token' ? (fresh ? this.#tokenValue : this.#replaceValue) : field === 'host' && fresh ? this.#tokenHost : null
    if (!input) return false
    labelError(input, text)
    input.focus()
    return true
  }

  tokenDone() {
    if (this.#tokenFrom === '') {
      this.#addingHost = false
      this.#hostEdited = false
      this.#tokenHost.value = ''
      blank(this.#tokenValue)
    } else blank(this.#replaceValue)
    this.#replacing = ''
    this.#forgetting = ''
    this.#tokenFrom = ''
    this.#render()
  }

  #tokenSend(from: string, detail: { op: 'save'; host: string; token: string } | { op: 'remove'; host: string }, key: string) {
    for (const input of [this.#tokenHost, this.#tokenValue, this.#replaceValue]) labelError(input, '')
    this.#tokenFrom = from
    this.#hold('token', key)
    emit(this, 'sf-token', detail)
  }

  #hold(kind: 'repo' | 'token', key: string, after?: () => void) {
    this.#pending.set(kind, { key, after })
    this.#applyPending()
  }

  settle(kind: 'repo' | 'token') {
    const held = this.#pending.get(kind)
    if (!held) return
    this.#pending.delete(kind)
    held.after?.()
    this.#render()
  }

  #applyPending() {
    for (const { key } of this.#pending.values()) {
      for (const root of [this, this.#slot]) {
        const b = root?.querySelector<HTMLButtonElement>(`button[data-key="${CSS.escape(key)}"]`)
        if (b) busy(b, true)
      }
    }
  }

  #focus(key: string) {
    for (const root of [this.#slot, this]) root?.querySelector<HTMLElement>(`[data-key="${CSS.escape(key)}"]`)?.focus()
  }

  #who(title: HTMLElement): Element[] {
    const who = el('div', 'project-id')
    who.append(title)
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
    item('Project settings', () => emit(this, 'sf-project-page', { name: p.name }), 'settings')
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
      placeMenu(more, menu)
      menu.querySelector<HTMLElement>('button')?.focus()
    })
    menu.addEventListener('keydown', e => {
      const items = [...menu.querySelectorAll<HTMLElement>('button')]
      rovingKeys(e, items, items.indexOf(document.activeElement as HTMLElement), 'vertical')
    })
    return [more, menu]
  }

  #row(p: Project) {
    const li = this.#rows.get(p.path) ?? el('li', 'boxlist-row')
    this.#rows.set(p.path, li)
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
    head.append(...this.#who(name))
    if (list.length && this.#removing !== p.path) {
      const counted = button(count ? `${count} running` : `${list.length} stopped`, 'link project-count secondary', toggle, 'button', `row:${p.path}:count`)
      counted.append(icon('chevron', 'icon count-chevron'))
      counted.setAttribute('aria-expanded', String(open))
      counted.setAttribute('aria-label', `${count ? `${count} running` : `${list.length} stopped`}: ${open ? 'hide' : 'show'} the sessions in ${p.name}`)
      head.append(counted)
    }
    const acts = el('span', 'acts')
    if (this.#removing === p.path) {
      acts.append(el('span', 'small muted', 'Remove it and delete its clone from the workspace?'), button('Cancel', 'ghost small', () => {
        this.#removing = ''
        this.#render()
      }, 'button', `row:${p.path}:keep`), button('Remove', 'ghost small danger', () => {
        this.#hold('repo', `row:${p.path}:confirm`, () => {
          this.#removing = ''
        })
        emit(this, 'sf-repo', { op: 'remove', name: p.name })
      }, 'button', `row:${p.path}:confirm`))
    } else {
      if (!this.#forms.has(p.path)) {
        const start = button('New session', 'ghost small', () => this.#open(p.path), 'button', `row:${p.path}:new`)
        start.setAttribute('aria-label', `New session in ${visible(p.name)}`)
        acts.append(start)
      }
      acts.append(...this.#menu(p))
    }
    head.append(acts)
    const parts: HTMLElement[] = [head]
    if (open) {
      parts.push(sessionTable(grouped(list).map(s => sessionRow(s, this.#sessions, `proj:${p.path}:${s.sid}`, true)), 'project-sessions with-menu', `Sessions in ${p.name}`))
    }
    const said = this.#said.get(p.path) ?? statusLine('small status-slot')
    this.#said.set(p.path, said)
    const mine = this.#message.path === p.path
    sayIfChanged(said, mine ? this.#message.text : '', mine && this.#message.bad ? 'bad' : 'muted')
    parts.push(said)
    const form = this.#forms.get(p.path)
    if (form && this.#removing !== p.path) parts.push(form.form)
    let expiry = this.#expiry.get(p.path)
    if (!expiry) {
      expiry = el('sf-token-expiry', 'project-expiry')
      this.#expiry.set(p.path, expiry)
    }
    expiry.url = p.url ?? ''
    parts.push(expiry)
    replaceKeeping(li, parts)
    return li
  }

  fork(path: string, sid: string, commit = '') {
    this.#forms.delete(path)
    this.#expanded.set(path, true)
    this.#open(path, sid, commit)
    this.#rows.get(path)?.scrollIntoView({ block: 'nearest' })
  }

  #open(path: string, fork = '', forkAt = '') {
    const p = this.#view.projects.find(x => x.path === path)
    if (!p) return
    const form = new DispatchForm(p, this.#view.starters ?? [], () => {
      this.#forms.delete(path)
      this.#render()
    }, this.#sessionsIn(path), fork, forkAt)
    form.busy(this.#busy)
    this.#forms.set(path, form)
    void form.suggestName(() => this.#forms.get(path) === form)
    if (this.#message.path === path) this.#message = { path: '', text: '', bad: false }
    this.#render()
    form.from.select.focus()
  }

  #cloningRow(k: Cloning): HTMLElement {
    const kept = this.#cloning.get(k.name)
    if (kept?.url === k.url) {
      drawProgress(kept.bar, k.progress)
      return kept.li
    }
    const c = kept ?? { url: k.url, li: el('li', 'boxlist-row'), row: el('div', 'row'), bar: el('sf-progress', 'acts small muted') }
    c.url = k.url
    c.bar.name = k.name
    drawProgress(c.bar, k.progress)
    const who = el('div', 'project-id')
    const name = el('span', 'name')
    name.append(el('span', 'title', k.name))
    who.append(name)
    replaceKeeping(c.row, [icon('folder', 'icon folder'), who, c.bar])
    if (!kept) {
      c.li.append(c.row)
      this.#cloning.set(k.name, c)
    }
    return c.li
  }

  cloneProgress(url: string, name: string, progress: CloneProgress) {
    const k = (this.#view.cloning ?? []).find(c => c.url === url && c.name === name)
    if (!k) return false
    k.progress = progress
    const row = this.#cloning.get(name)
    if (row?.url === url) drawProgress(row.bar, progress)
    return true
  }

  #hostRow(h: TokenHost): HTMLElement {
    const li = this.#hostRows.get(h.host) ?? el('li')
    this.#hostRows.set(h.host, li)
    const label = tokenHostHelp(h.host)?.label ?? hostName(h.host)
    const shownHost = visible(label)
    const row = el('div', 'host-row')
    const who = el('div', 'host-who')
    const name = el('span', 'host-name')
    name.title = h.host
    const text = el('bdi', '', label)
    text.dir = 'ltr'
    name.append(text)
    who.append(name, el('span', 'host-saved', h.savedAt ? `Saved ${savedAt(h.savedAt)}` : 'Saved'))
    row.append(who)
    if (this.#forgetting !== h.host && this.#replacing !== h.host) {
      const acts = el('span', 'host-acts')
      const replace = button('Replace', 'link', () => {
        this.#replacing = h.host
        this.#forgetting = ''
        this.#addingHost = false
        blank(this.#tokenValue, this.#replaceValue)
        this.#render()
        this.#replaceValue.focus()
      }, 'button', `host:${h.host}:replace`)
      replace.setAttribute('aria-label', `Replace the token for ${shownHost}`)
      const remove = button('Remove', 'link', () => {
        this.#forgetting = h.host
        this.#replacing = ''
        this.#addingHost = false
        blank(this.#tokenValue, this.#replaceValue)
        this.#render()
        this.#focus(`host:${h.host}:confirm`)
      }, 'button', `host:${h.host}:remove`)
      remove.setAttribute('aria-label', `Remove the token for ${shownHost}`)
      acts.append(replace, remove)
      row.append(acts)
    }
    const parts: HTMLElement[] = [row]
    if (this.#forgetting === h.host) {
      const confirm = stacked(el('p', 'small muted', 'Remove this token? Clones and fetches from this host go without one.'), ...actions(button('Cancel', 'ghost', () => {
        this.#forgetting = ''
        this.#render()
      }, 'button', `host:${h.host}:keep`), button('Remove token', 'danger', () => {
        this.#tokenSend(h.host, { op: 'remove', host: h.host }, `host:${h.host}:confirm`)
      }, 'button', `host:${h.host}:confirm`)))
      parts.push(confirm)
    }
    if (this.#replacing === h.host) {
      this.#replaceWords.data = `New token for ${label}`
      replaceKeeping(this.#replaceForm, [this.#replaceField, ...actions(button('Cancel', 'ghost', () => {
        this.#replacing = ''
        blank(this.#replaceValue)
        this.#render()
      }, 'button', `host:${h.host}:cancel`), button('Save token', 'primary', undefined, 'submit', `host:${h.host}:save`))])
      parts.push(this.#replaceForm)
    }
    replaceKeeping(li, parts)
    return li
  }

  #tokenHelp(host: string): HTMLElement[] {
    const help = tokenHostHelp(host)
    if (!help) return []
    const link = el('a', 'small token-help', help.linkText)
    link.href = help.link
    link.target = '_blank'
    link.rel = 'noopener noreferrer'
    const note = this.#view.hostNotes?.[host]
    return [el('p', 'small muted token-scopes', typeof note === 'string' && note ? `${help.scopes} ${visible(note)}` : help.scopes), link]
  }

  #tokens(): HTMLElement[] {
    const hosts = this.#view.hosts ?? []
    const parts: HTMLElement[] = pageHead({ title: 'Git host tokens', headingClass: 'label', intro: 'One per git host, to clone, push and open pull requests. Agents never see it.', docs: { doc: 'git-host-tokens', key: 'token-docs' } })
    const offered = this.#view.offerHosts ?? []
    if (this.#replacing) parts.push(...this.#tokenHelp(this.#replacing))
    for (const host of this.#hostRows.keys()) if (!hosts.some(h => h.host === host)) this.#hostRows.delete(host)
    replaceKeeping(this.#hostList, hosts.map(h => this.#hostRow(h)))
    if (hosts.length) parts.push(this.#hostList)
    if (hosts.length && !this.#addingHost) {
      if (this.#replacing || this.#forgetting || !offered.length) return parts
      const more = el('div', 'projects-actions')
      more.append(button('+ Add another host', 'link small add-more', () => {
        this.#addingHost = true
        this.#replacing = ''
        this.#forgetting = ''
        this.#hostEdited = false
        blank(this.#tokenValue)
        this.#render()
        this.#tokenHost.focus()
      }, 'button', 'token-more'))
      parts.push(more)
      return parts
    }
    if (!offered.length) return parts
    const picked = this.#hostEdited && offered.includes(this.#tokenHost.value) ? this.#tokenHost.value : ''
    this.#tokenHost.replaceChildren(option('Select a git host', '', { disabled: true }), ...offered.map(h => option(tokenHostHelp(h)?.label ?? 'Development git server', h)))
    this.#tokenHost.value = picked
    const cancel = hosts.length ? [button('Cancel', 'ghost', () => {
      this.#addingHost = false
      blank(this.#tokenValue)
      this.#render()
    }, 'button', 'token-cancel')] : []
    const save = picked ? [button('Save token', 'primary', undefined, 'submit', 'token-save')] : []
    const steps = picked ? [...this.#tokenHelp(picked), this.#tokenField] : []
    replaceKeeping(this.#tokenForm, [this.#hostField, ...steps, ...actions(...cancel, ...save)])
    parts.push(this.#tokenForm)
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
      const cancel = collapsible ? [button('Cancel', 'ghost', () => {
        this.#addingRepo = false
        this.#add.value = ''
        this.#addError = ''
        this.#render()
      }, 'button', 'add-cancel')] : []
      const box = field('Repository', this.#add)
      labelError(this.#add, this.#addError)
      const add = stacked(box, ...actions(...cancel, button('Add repository', 'primary', undefined, 'submit', 'add')))
      add.classList.add('repo-add')
      add.addEventListener('submit', () => {
        const url = this.#add.value.trim()
        this.#addError = ''
        labelError(this.#add, '')
        this.#hold('repo', 'add')
        emit(this, 'sf-repo', { op: 'add', url })
      })
      if (!collapsible) return add
      const card = el('div', 'repo-add-card')
      card.append(add)
      return card
    }
    const parts: HTMLElement[] = [
      ...pageHead({ title: 'Projects', crumb: true, intro: 'Your repositories. Each session works on its own branch, started from the latest main.', docs: { doc: 'projects', key: 'docs' } }),
    ]
    if (!empty && this.#addingRepo) parts.push(addControl(true))
    const message = this.#message.path === '' ? this.#message.text : ''
    sayIfChanged(this.#messageLine, message, this.#message.bad ? 'bad' : 'muted')
    parts.push(this.#messageLine)
    if (empty) {
      parts.push(emptyState("Add a repository by its https address. If it's private, save a token for its git host first.", { title: 'No projects yet', icon: 'folder', action: addControl(false) }))
    }
    if (!empty && !this.#addingRepo) {
      const actions = el('div', 'projects-actions')
      actions.append(button('+ Add repository', 'link small add-more', () => {
        this.#addingRepo = true
        this.#render()
        this.#add.focus()
      }, 'button', 'add-more'))
      parts.push(actions)
    }
    const cloning = this.#view.cloning ?? []
    for (const name of this.#cloning.keys()) if (!cloning.some(k => k.name === name)) this.#cloning.delete(name)
    if (projects.length || cloning.length) {
      const rows = [...projects.map(p => ({ name: p.name, row: () => this.#row(p) })), ...cloning.map(k => ({ name: k.name, row: () => this.#cloningRow(k) }))]
      rows.sort((a, b) => a.name < b.name ? -1 : a.name > b.name ? 1 : 0)
      replaceKeeping(this.#list, rows.map(r => r.row()))
      parts.push(this.#list)
    } else this.#list.replaceChildren()
    const side = this.#tokens()
    if (this.#slot) replaceKeeping(this.#slot, side)
    else parts.push(...side)
    replaceKeeping(this, parts)
    this.#applyPending()
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

function drawProgress(bar: HTMLElementTagNameMap['sf-progress'], p: CloneProgress | undefined) {
  bar.hidden = !p
  bar.percent = typeof p?.percent === 'number' ? p.percent : null
  bar.label = typeof p?.said === 'string' ? p.said : ''
}

customElements.define('sf-projects', SfProjects)

declare global {
  interface HTMLElementTagNameMap {
    'sf-projects': SfProjects
  }
}
