import { busy, button, el, emit, field, focusedKey, pageCrumb, refocus, replaceKeeping, rovingKeys, sayIfChanged, sayWhenShown, statusLine, visible } from '../lib/dom.ts'
import { icon } from './icon.ts'
import { secret } from './secret.ts'
import { effortOptions } from './session-work.ts'
import './allow-rules.ts'
import './project-facts.ts'
import './team-rules.ts'
import './caps.ts'
import './issue-picker.ts'
import './usage.ts'
import './project-commands.ts'
import './metrics.ts'
import './decision-log.ts'
import './review-depth.ts'
import './lessons.ts'
import { bySessionOrder, grouped, isRunning, sessionKey, sessionRow, sessionTable } from './session-list.ts'
import { placeMenu } from '../lib/menu.ts'
import { BenchmarkField, PairField, StartFromField } from './start-from.ts'
import { developmentBuild } from './update.ts'
import type { Cloning, Expect, PairAgent, Project, ProjectsView, SessionInfo, Starter, TokenHost } from '../types.ts'

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
const GITLAB = 'https://gitlab.com'
const DEV_HOST = 'https://gitserver.test:8443'
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
    scopes: 'The link opens GitLab with the right scopes already chosen. Set an expiry date, create the token, then paste it below. Pull requests and issues work only on GitHub.',
  },
}

function tokenShape(host: string, value: string): string {
  const v = value.trim()
  if (!v) return ''
  if (host === GITHUB && !/^(github_pat_|gh[pousr]_)/.test(v)) return 'GitHub tokens start with github_pat_ or ghp_. Check that you pasted the token itself.'
  if (host === GITLAB && !v.startsWith('glpat-')) return 'GitLab personal access tokens start with glpat-. Check that you pasted the token itself.'
  return ''
}

function blank(...inputs: HTMLInputElement[]) {
  for (const input of inputs) {
    input.value = ''
    input.type = 'password'
  }
}

function actions(...buttons: HTMLElement[]): HTMLElement[] {
  if (!buttons.length) return []
  const row = el('div', 'form-actions')
  row.append(...buttons)
  return [row]
}

function stacked(...children: HTMLElement[]): HTMLFormElement {
  const form = el('form', 'stacked')
  form.autocomplete = 'off'
  form.append(...children)
  return form
}

function webAddress(url: string): string {
  try {
    const parsed = new URL(url)
    return parsed.protocol === 'https:' || parsed.protocol === 'http:' ? parsed.href : ''
  } catch {
    return ''
  }
}

function urlText(url: string): HTMLElement {
  const href = webAddress(url)
  const text = el('bdi', '', url)
  text.dir = 'ltr'
  if (!href) {
    const span = el('span', 'project-path secondary')
    span.title = url
    span.append(text)
    return span
  }
  const a = el('a', 'project-path project-link')
  a.href = href
  a.target = '_blank'
  a.rel = 'noopener noreferrer'
  a.title = `Open ${url} in a new tab`
  a.append(text)
  return a
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

const COMMANDS: Record<string, string> = { plugin: 'claude --bg', acp: 'its ACP server' }

class DispatchForm {
  form = el('form', 'dispatch')
  agent = el('select', 'textbox')
  why = el('div', 'small muted')
  name = el('input', 'textbox')
  model = el('select', 'textbox')
  effort = el('select', 'textbox')
  method = ''
  kind = el('select', 'textbox')
  expect = el('select', 'textbox')
  caps = el('sf-caps-fields', 'dispatch-caps')
  advanced = el('details', 'advanced-settings')
  issue = el('sf-issue-picker', 'dispatch-issue')
  start = button('Start', 'primary small', undefined, 'submit')
  from: StartFromField
  pair: PairField
  bench: BenchmarkField

  constructor(p: Project, starters: Starter[], onCancel: () => void, sessions: SessionInfo[] = [], fork = '', forkAt = '') {
    const path = p.path
    this.form.autocomplete = 'off'
    this.name.name = 'name'
    this.model.name = 'model'
    this.name.placeholder = 'Name (optional)'
    this.name.maxLength = 80
    this.name.setAttribute('aria-label', 'Session name')
    this.model.setAttribute('aria-label', 'Model')
    options(this.model, MODELS)
    this.effort.name = 'effort'
    this.effort.setAttribute('aria-label', 'Effort')
    this.effort.title = 'How hard the agent thinks. The default sends no effort, as before.'
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
    this.agent.hidden = list.length < 2
    this.expect.setAttribute('aria-label', 'The sign-in you expect of this agent')
    this.expect.title = 'The engine checks the agent\'s own sign-in before it starts, and refuses a start that differs from what you expect'
    options(this.expect, [['any', 'Expect any sign-in'], ['subscription', 'Expect a subscription'], ['api-key', 'Expect an API key']])
    this.expect.addEventListener('change', () => {
      const chosen = list.find(a => a.id === this.agent.value)
      if (chosen) chosen.expect = this.expect.value as Expect
      emit(this.form, 'sf-expect', { agent: this.agent.value, expect: this.expect.value as Expect })
    })
    const row = el('div', 'opts field-row')
    const picks = el('span', 'dispatch-picks')
    picks.append(this.agent, this.model)
    const go = el('span', 'dispatch-actions')
    go.append(button('Cancel', 'ghost small', onCancel), this.start)
    row.append(this.name, picks, go)
    const more = el('div', 'opts field-row')
    more.append(this.effort, this.kind, this.expect)
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
      this.choices(chosen?.method ?? '')
      this.start.disabled = !chosen?.enabled
      this.expect.value = chosen?.expect ?? 'any'
      this.expect.hidden = plugin
      this.pair.follow(this.agent.value)
    }
    this.agent.addEventListener('change', sync)
    this.caps.project = p.name
    this.from = new StartFromField(path, p.branch ?? '', sessions, fork, forkAt)
    this.issue.path = path
    this.pair = new PairField(list, MODELS)
    sync()
    this.bench = new BenchmarkField(path)
    this.advanced.append(el('summary', 'small', 'Advanced settings'), more, this.from.box, this.caps, note)
    this.advanced.open = !!this.from.value()
    this.form.append(row, this.why, this.advanced)
    this.form.append(this.issue)
    this.form.append(this.pair.box, this.bench.box)
    this.form.addEventListener('submit', e => {
      e.preventDefault()
      if (this.start.disabled) return
      const caps = this.caps.checked()
      if (caps === 'invalid') {
        this.advanced.open = true
        return
      }
      const from = this.from.value()
      const issue = this.issue.chosen()
      if (issue === 'refused') return
      const bench = this.bench.chosen()
      if (bench === 'refused') return
      const pair = this.pair.value(!!bench)
      if (pair === 'invalid') return
      if (pair || bench) {
        if (from) this.advanced.open = true
        if (from || issue) return void this.pair.say('Start with both and a benchmark start from main, or from the merged pull request’s base, without an issue. Set Start from back to main and clear the issue first.')
        const first: PairAgent = { agent: this.agent.value, ...(this.model.value ? { model: this.model.value } : {}), ...(this.effort.value ? { effort: this.effort.value } : {}) }
        const agents: PairAgent[] = pair ? [first, { agent: first.agent, ...(pair.model ? { model: pair.model } : {}) }] : [first]
        return void emit(this.form, 'sf-pair', { path, name: this.name.value, type: this.kind.value, agents, ...(pair?.message ? { message: pair.message } : {}), ...(bench ? { benchmark: bench } : {}), ...(caps ? { caps } : {}) })
      }
      emit(this.form, 'sf-dispatch', { path, agent: this.agent.value, name: this.name.value, model: this.model.value, mode: '', type: this.kind.value, ...(caps ? { caps } : {}), ...(from ? { from } : {}), ...(issue ?? {}), ...(this.effort.value ? { effort: this.effort.value } : {}) })
    })
    this.model.addEventListener('change', () => this.efforts())
  }

  choices(method: string) {
    this.method = method
    this.effort.hidden = method !== 'plugin'
    if (method === 'plugin') {
      const keep = this.model.value
      this.model.replaceChildren()
      options(this.model, MODELS)
      if (MODELS.some(([v]) => v === keep)) this.model.value = keep
    }
    this.efforts()
  }

  efforts() {
    if (this.method !== 'plugin') return this.effort.replaceChildren()
    const keep = this.effort.value
    effortOptions(this.effort, 'Default effort')
    if ([...this.effort.options].some(o => o.value === keep)) this.effort.value = keep
  }

  busy(on: boolean) {
    this.start.disabled = !!this.agent.selectedOptions[0]?.disabled
    busy(this.start, on)
  }
}

class SfProjects extends HTMLElement {
  #view: ProjectsView = { projects: [], cloning: [], hosts: [] }
  #forms = new Map<string, DispatchForm>()
  #removing = ''
  #message: { path: string; text: string; bad: boolean } = { path: '', text: '', bad: false }
  #messageLine = statusLine('small status-slot')
  #cloning = new Map<string, { url: string; li: HTMLLIElement; row: HTMLDivElement; said: HTMLSpanElement }>()
  #list = el('ul', 'boxlist projects')
  #rows = new Map<string, HTMLLIElement>()
  #said = new Map<string, HTMLParagraphElement>()
  #busy = false
  #add = el('input', 'textbox')
  #pending = new Map<'repo' | 'token', { key: string; after?: () => void }>()
  #addError = ''
  #addAlert = false
  #tokenHost = el('select', 'textbox')
  #tokenValue = el('input', 'textbox')
  #replacing = ''
  #replaceValue = el('input', 'textbox')
  #tokenHint = statusLine('small warn field-error status-slot')
  #replaceHint = statusLine('small warn field-error status-slot')
  #hostField: HTMLElement
  #tokenField: HTMLElement
  #replaceField: HTMLElement
  #replaceLabel: HTMLLabelElement
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
  #rules = new Map<string, HTMLElementTagNameMap['sf-allow-rules']>()
  #facts = new Map<string, HTMLElementTagNameMap['sf-project-facts']>()
  #caps = new Map<string, HTMLElementTagNameMap['sf-project-caps']>()
  #team = new Map<string, HTMLElementTagNameMap['sf-team-rules']>()
  #expiry = new Map<string, HTMLElementTagNameMap['sf-token-expiry']>()
  #commands = new Map<string, HTMLElementTagNameMap['sf-project-commands']>()
  #metrics = new Map<string, HTMLElementTagNameMap['sf-project-metrics']>()
  #decisions = new Map<string, HTMLElementTagNameMap['sf-decision-log']>()
  #depth = new Map<string, HTMLElementTagNameMap['sf-review-depth']>()
  #lessons = new Map<string, HTMLElementTagNameMap['sf-lessons']>()
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
    this.#add.addEventListener('input', () => {
      if (!this.#addError) return
      this.#addError = ''
      this.#render()
    })
    this.#tokenHost.id = 'token-host'
    this.#tokenHost.name = 'host'
    this.#tokenHost.addEventListener('change', () => {
      this.#hostEdited = true
      this.#render()
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
    this.#tokenHint.id = 'token-value-hint'
    this.#replaceHint.id = 'token-replace-hint'
    this.#tokenValue.addEventListener('input', () => this.#hint())
    this.#replaceValue.addEventListener('input', () => this.#hint())
    this.#hostField = field('Host', this.#tokenHost)
    this.#tokenField = field('Token', secret(this.#tokenValue, 'token'))
    this.#replaceField = field('New token', secret(this.#replaceValue, 'token'))
    this.#replaceLabel = this.#replaceField.querySelector('label')!
    this.#hostList.setAttribute('aria-label', 'Saved git host tokens')
    this.#tokenForm.addEventListener('submit', e => {
      e.preventDefault()
      const host = this.#tokenHost.value.trim()
      const token = this.#tokenValue.value
      if (!host) return this.#tokenHost.focus()
      if (!token) return this.#tokenValue.focus()
      this.#tokenSend('', { op: 'save', host, token }, 'token-save')
    })
    this.#replaceForm.addEventListener('submit', e => {
      e.preventDefault()
      const host = this.#replacing
      const token = this.#replaceValue.value
      if (host && token) this.#tokenSend(host, { op: 'save', host, token }, `host:${host}:save`)
    })
  }

  connectedCallback() {
    if (this.#starting?.id && !this.#starting.late && this.#startTimer === undefined) this.#startTimer = setTimeout(() => this.#late(), 30000)
  }

  disconnectedCallback() {
    clearTimeout(this.#startTimer)
    this.#startTimer = undefined
  }

  #hint() {
    for (const [input, hint, host] of [[this.#tokenValue, this.#tokenHint, this.#tokenHost.value], [this.#replaceValue, this.#replaceHint, this.#replacing]] as const) {
      const text = tokenShape(host, input.value)
      sayIfChanged(hint, text, 'warn')
      if (text) input.setAttribute('aria-describedby', hint.id)
      else input.removeAttribute('aria-describedby')
    }
  }

  #viewKey = ''

  set view(value: ProjectsView) {
    const key = JSON.stringify(value)
    if (key === this.#viewKey) return
    this.#viewKey = key
    this.#view = value
    const kept = new Set(value.projects.map(p => p.path))
    for (const m of [this.#rows, this.#said, this.#forms, this.#expanded, this.#rules, this.#facts, this.#caps, this.#team, this.#expiry, this.#commands, this.#metrics, this.#decisions, this.#depth, this.#lessons]) for (const path of m.keys()) if (!kept.has(path)) m.delete(path)
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

  showPanel(path: string, which: 'rules' | 'team') {
    const p = this.#view.projects.find(x => x.path === path)
    if (!p) return
    if (which === 'rules' && !this.#rules.has(path)) {
      const panel = el('sf-allow-rules', 'project-rules')
      panel.repo = p.name
      this.#rules.set(path, panel)
    }
    if (which === 'team' && !this.#team.has(path)) {
      const panel = el('sf-team-rules', 'project-rules')
      panel.path = p.path
      this.#team.set(path, panel)
    }
    this.reveal(path)
    const panel = which === 'rules' ? this.#rules.get(path) : this.#team.get(path)
    panel?.scrollIntoView({ block: 'nearest' })
    if (panel) {
      panel.tabIndex = -1
      panel.focus()
    }
  }

  startIn(path: string) {
    if (!this.#view.projects.some(x => x.path === path)) return
    this.#expanded.set(path, true)
    if (this.#forms.has(path)) {
      this.#render()
      this.#forms.get(path)?.name.focus()
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
    this.#addAlert = true
    this.#render()
    this.#add.focus()
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
    item(this.#rules.has(p.path) ? 'Hide saved allow rules' : 'Saved allow rules', () => {
      if (this.#rules.has(p.path)) this.#rules.delete(p.path)
      else {
        const panel = el('sf-allow-rules', 'project-rules')
        panel.repo = p.name
        this.#rules.set(p.path, panel)
      }
      this.#render()
    }, 'warning')
    item(this.#facts.has(p.path) ? 'Hide CI workflows and main\'s protection' : 'CI workflows and main\'s protection', () => {
      if (this.#facts.has(p.path)) this.#facts.delete(p.path)
      else {
        const panel = el('sf-project-facts', 'project-rules')
        panel.repo = p.name
        this.#facts.set(p.path, panel)
      }
      this.#render()
    }, 'info')
    item(this.#team.has(p.path) ? 'Hide team rules from main' : 'Team rules from main', () => {
      if (this.#team.has(p.path)) this.#team.delete(p.path)
      else {
        const panel = el('sf-team-rules', 'project-rules')
        panel.path = p.path
        this.#team.set(p.path, panel)
      }
      this.#render()
    }, 'info')
    item(this.#caps.has(p.path) ? 'Hide caps' : 'Caps', () => {
      if (this.#caps.has(p.path)) this.#caps.delete(p.path)
      else {
        const panel = el('sf-project-caps', 'project-caps')
        panel.project = p.name
        this.#caps.set(p.path, panel)
      }
      this.#render()
    }, 'warning')
    item(this.#commands.has(p.path) ? 'Hide check and setup commands' : 'Check and setup commands', () => {
      if (this.#commands.has(p.path)) this.#commands.delete(p.path)
      else {
        const panel = el('sf-project-commands', 'project-caps')
        panel.project = p.name
        this.#commands.set(p.path, panel)
      }
      this.#render()
    }, 'info')
    item(this.#metrics.has(p.path) ? 'Hide delivery and reviewer numbers' : 'Delivery and reviewer numbers', () => {
      if (this.#metrics.has(p.path)) this.#metrics.delete(p.path)
      else {
        const panel = el('sf-project-metrics', 'project-caps')
        panel.project = p.name
        this.#metrics.set(p.path, panel)
      }
      this.#render()
    }, 'info')
    item(this.#decisions.has(p.path) ? 'Hide decision log' : 'Decision log', () => {
      if (this.#decisions.has(p.path)) this.#decisions.delete(p.path)
      else {
        const panel = el('sf-decision-log', 'project-caps')
        panel.project = p.name
        this.#decisions.set(p.path, panel)
      }
      this.#render()
    }, 'info')
    item(this.#depth.has(p.path) ? 'Hide review depth' : 'Review depth', () => {
      if (this.#depth.has(p.path)) this.#depth.delete(p.path)
      else {
        const panel = el('sf-review-depth', 'project-caps')
        panel.path = p.path
        this.#depth.set(p.path, panel)
      }
      this.#render()
    }, 'info')
    item(this.#lessons.has(p.path) ? 'Hide lessons from reviews' : 'Lessons from reviews', () => {
      if (this.#lessons.has(p.path)) this.#lessons.delete(p.path)
      else {
        const panel = el('sf-lessons', 'project-caps')
        panel.project = p.name
        this.#lessons.set(p.path, panel)
      }
      this.#render()
    }, 'info')
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
    if (form) {
      form.caps.project = p.name
      parts.push(form.form)
    }
    for (const m of [this.#rules, this.#facts, this.#caps, this.#team, this.#commands, this.#metrics, this.#decisions, this.#depth, this.#lessons]) {
      const panel = m.get(p.path)
      if (panel) parts.push(panel)
    }
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
    this.querySelector<HTMLElement>(`[data-key="${CSS.escape(`row:${path}:toggle`)}"]`)?.scrollIntoView({ block: 'nearest' })
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
    if (this.#message.path === path) this.#message = { path: '', text: '', bad: false }
    this.#render()
    form.name.focus()
  }

  #cloningRow(k: Cloning): HTMLElement {
    const kept = this.#cloning.get(k.name)
    if (kept?.url === k.url) return kept.li
    const c = kept ?? { url: k.url, li: el('li', 'boxlist-row'), row: el('div', 'row'), said: statusLine('acts small muted status-slot', 'span') }
    c.url = k.url
    const who = el('div', 'project-id')
    const name = el('span', 'name')
    name.append(el('span', 'title', k.name))
    who.append(name, urlText(k.url))
    replaceKeeping(c.row, [el('span', 'project-toggle-space'), icon('folder', 'icon folder'), who, c.said])
    if (!kept) {
      c.li.append(c.row)
      this.#cloning.set(k.name, c)
      sayWhenShown(c.said, 'Cloning…', 'muted')
    }
    return c.li
  }

  #hostRow(h: TokenHost): HTMLElement {
    const li = this.#hostRows.get(h.host) ?? el('li')
    this.#hostRows.set(h.host, li)
    const shownHost = visible(hostName(h.host))
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
      const confirm = el('div', 'stacked')
      confirm.append(el('p', 'small muted', 'Remove this token? Clones and fetches from this host go without one.'), ...actions(button('Cancel', 'ghost', () => {
        this.#forgetting = ''
        this.#render()
      }, 'button', `host:${h.host}:keep`), button('Remove token', 'danger', () => {
        this.#tokenSend(h.host, { op: 'remove', host: h.host }, `host:${h.host}:confirm`)
      }, 'button', `host:${h.host}:confirm`)))
      parts.push(confirm)
    }
    if (this.#replacing === h.host) {
      this.#replaceLabel.textContent = `New token for ${hostName(h.host)}`
      replaceKeeping(this.#replaceForm, [this.#replaceField, this.#replaceHint, ...actions(button('Cancel', 'ghost', () => {
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
    const help = TOKEN_HOSTS[host]
    if (!help) return []
    const link = el('a', 'small token-help', help.linkText)
    link.href = help.link
    link.target = '_blank'
    link.rel = 'noopener noreferrer'
    return [el('p', 'small muted token-scopes', help.scopes), link]
  }

  #tokens(): HTMLElement[] {
    this.#hint()
    const hosts = this.#view.hosts ?? []
    const title = el('div', 'page-head')
    const docs = button('View in docs', 'link small docs-link', undefined, 'button', 'token-docs')
    docs.dataset.doc = 'git-host-tokens'
    docs.prepend(icon('link'))
    title.append(el('h2', 'label', 'Tokens'), docs)
    const about = el('p', 'small muted page-intro', 'One per git host, to clone, push and open pull requests. Agents never see it.')
    const parts: HTMLElement[] = [title, about]
    const offered = [GITHUB, GITLAB, ...(developmentBuild() ? [DEV_HOST] : [])].filter(h => !hosts.some(saved => saved.host === h))
    if (this.#replacing) parts.push(...this.#tokenHelp(this.#replacing))
    for (const host of this.#hostRows.keys()) if (!hosts.some(h => h.host === host)) this.#hostRows.delete(host)
    replaceKeeping(this.#hostList, hosts.map(h => this.#hostRow(h)))
    if (hosts.length) parts.push(this.#hostList)
    if (hosts.length && !this.#addingHost) {
      if (this.#replacing || this.#forgetting || !offered.length) return parts
      const more = button('Add another host', 'primary add-host', () => {
        this.#addingHost = true
        this.#replacing = ''
        this.#forgetting = ''
        this.#hostEdited = false
        blank(this.#tokenValue)
        this.#render()
        this.#tokenHost.focus()
      }, 'button', 'token-more')
      parts.push(more)
      return parts
    }
    if (!offered.length) return parts
    const picked = this.#hostEdited && offered.includes(this.#tokenHost.value) ? this.#tokenHost.value : ''
    const none = el('option', '', 'Select a git host')
    none.value = ''
    none.disabled = true
    this.#tokenHost.replaceChildren(none, ...offered.map(h => {
      const o = el('option', '', TOKEN_HOSTS[h]?.label ?? 'Development git server')
      o.value = h
      return o
    }))
    this.#tokenHost.value = picked
    const cancel = hosts.length ? [button('Cancel', 'ghost', () => {
      this.#addingHost = false
      blank(this.#tokenValue)
      this.#render()
    }, 'button', 'token-cancel')] : []
    const save = picked ? [button('Save token', 'primary', undefined, 'submit', 'token-save')] : []
    const steps = picked ? [...this.#tokenHelp(picked), this.#tokenField, this.#tokenHint] : []
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
      if (this.#addError) {
        const said = el('p', 'small bad field-error', this.#addError)
        said.id = 'repo-url-error'
        if (this.#addAlert) said.setAttribute('role', 'alert')
        this.#addAlert = false
        box.append(said)
        this.#add.setAttribute('aria-invalid', 'true')
        this.#add.setAttribute('aria-describedby', said.id)
      } else {
        this.#add.removeAttribute('aria-invalid')
        this.#add.removeAttribute('aria-describedby')
      }
      const add = stacked(box, ...actions(...cancel, button('Add repository', 'primary', undefined, 'submit', 'add')))
      add.classList.add('repo-add')
      if (collapsible) add.classList.add('repo-add-card')
      add.addEventListener('submit', e => {
        e.preventDefault()
        const url = this.#add.value.trim()
        if (!url) return this.#add.focus()
        this.#hold('repo', 'add')
        emit(this, 'sf-repo', { op: 'add', url })
      })
      return add
    }
    const head = el('div', 'page-head')
    const docs = button('View in docs', 'link small docs-link', undefined, 'button', 'docs')
    docs.dataset.doc = 'projects-and-new-sessions'
    docs.prepend(icon('link'))
    head.append(pageCrumb('Projects'), docs)
    const sub = el('p', 'small muted page-intro', 'Your repositories. Each session works on its own branch, started from the latest main.')
    const parts: HTMLElement[] = [head, sub]
    if (!empty && this.#addingRepo) parts.push(addControl(true))
    const message = this.#message.path === '' ? this.#message.text : ''
    sayIfChanged(this.#messageLine, message, this.#message.bad ? 'bad' : 'muted')
    parts.push(this.#messageLine)
    if (empty) {
      const box = el('div', 'projects-empty')
      const media = el('div', 'projects-empty-icon')
      media.append(icon('folder', ''))
      box.append(media, el('h3', '', 'No projects yet'), el('p', 'small muted', "Add a repository by its https address. If it's private, save a token for its git host first."), addControl(false))
      parts.push(box)
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

customElements.define('sf-projects', SfProjects)

declare global {
  interface HTMLElementTagNameMap {
    'sf-projects': SfProjects
  }
}
