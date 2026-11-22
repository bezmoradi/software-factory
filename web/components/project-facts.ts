import { request, type EngineAnswer } from '../api.ts'
import { busy, button, el, emit, focusedKey, refocus, replaceKeeping, say, statusLine, unspoofed, visible, visibleCommand } from '../lib/dom.ts'
import { shortSha } from '../lib/format.ts'
import type { WorkflowRun } from '../types.ts'
import { whileOpen, type WhileOpen } from '../lib/refresh.ts'
import { checkRow, emptyState, pageHead, type CheckState, type PageHeadOptions } from '../lib/page.ts'
import { toast } from './toasts.ts'

interface ProtectionRow {
  kind: string
  label: string
  state: CheckState
  value: string
  detail: string
  names: string[]
}

interface ReadinessLine {
  kind: string
  state: CheckState
  found: string[]
  value: string
  note: string
  fixIn: string
  commit: string
  at: number
  result: string
  tail: string
  label: string
  word: string
}

interface FactsView {
  readiness: ReadinessLine[]
  branch: string
  workflows: WorkflowRun[]
  workflowsProblem: string
  protection: ProtectionRow[]
}

function text(x: unknown): string {
  return typeof x === 'string' ? visible(unspoofed(x)) : ''
}

function strings(x: unknown): string[] {
  return Array.isArray(x) ? x.filter((s): s is string => typeof s === 'string').map(text) : []
}

function stateOf(x: unknown): CheckState {
  return x === 'found' || x === 'missing' ? x : 'unknown'
}

function readRun(x: unknown): WorkflowRun[] {
  if (!x || typeof x !== 'object') return []
  const w = x as Record<string, unknown>
  if (typeof w.path !== 'string') return []
  return [{ path: text(w.path), name: text(w.name), when: text(w.when), secrets: strings(w.secrets), idToken: w.idToken === true, trusted: strings(w.trusted), new: false, changed: false, problem: text(w.problem) }]
}

function readReadiness(x: unknown): ReadinessLine[] {
  if (!x || typeof x !== 'object') return []
  const r = x as Record<string, unknown>
  if (typeof r.kind !== 'string' || typeof r.label !== 'string') return []
  const commit = typeof r.commit === 'string' && /^[0-9a-f]{40}$/.test(r.commit) ? r.commit : ''
  const result = r.result === 'passed' || r.result === 'failed' || r.result === 'running' ? r.result : ''
  return [{ kind: r.kind, state: stateOf(r.state), found: strings(r.found), value: text(r.value), note: text(r.note), fixIn: text(r.fixIn), commit, at: Number.isSafeInteger(r.at) ? Number(r.at) : 0, result,
    tail: typeof r.tail === 'string' ? r.tail : '', label: text(r.label), word: text(r.word) }]
}

function readRow(x: unknown): ProtectionRow[] {
  if (!x || typeof x !== 'object') return []
  const r = x as Record<string, unknown>
  if (typeof r.label !== 'string') return []
  return [{ kind: text(r.kind), label: text(r.label), state: stateOf(r.state), value: text(r.value), detail: text(r.detail), names: strings(r.names) }]
}

function readView(body: unknown): FactsView | null {
  if (!body || typeof body !== 'object') return null
  const v = body as Record<string, unknown>
  const p = (v.protection && typeof v.protection === 'object' ? v.protection : {}) as Record<string, unknown>
  if (!Array.isArray(v.workflows)) return null
  return {
    readiness: Array.isArray(v.readiness) ? v.readiness.flatMap(readReadiness) : [],
    branch: text(v.branch), workflows: v.workflows.flatMap(readRun), workflowsProblem: text(v.workflowsProblem),
    protection: Array.isArray(p.rows) ? p.rows.flatMap(readRow) : [],
  }
}

function when(ms: number): string {
  return ms ? new Date(ms).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : ''
}

export type FactsSection = 'readiness' | 'workflows' | 'protection'

export interface FactsSource {
  readonly repo: string
  read(): Promise<EngineAnswer>
  again(): void
}

export function factsSource(repo: string): FactsSource {
  let answer: Promise<EngineAnswer> | null = null
  return {
    repo,
    read: () => answer ??= request('/api/project-facts', { op: 'read', repo }),
    again: () => {
      answer = null
    },
  }
}

const HEADS: Record<FactsSection, PageHeadOptions & { reading: string }> = {
  readiness: { title: 'Ready for agents', intro: 'What main’s latest commit gives the agents, read without running anything.', docs: { doc: 'ready-for-agents', key: 'readiness-docs' }, reading: 'Reading main’s latest commit…' },
  workflows: { title: 'CI workflows on the agents’ pushes', intro: 'The workflows on main that start, with the repository’s secrets, when an agent pushes a session branch.', docs: { doc: 'ci-workflows-on-the-agents-pushes', key: 'workflows-docs' }, reading: 'Reading main’s workflows…' },
  protection: { title: 'Protection of main', intro: 'Whether the git host protects this branch and asks for reviews and status checks before a merge.', docs: { doc: 'protection-of-main', key: 'protection-docs' }, reading: 'Reading main’s protection…' },
}

class SfProjectFacts extends HTMLElement {
  #source: FactsSource | null = null
  #section: FactsSection = 'readiness'
  #view: FactsView | null = null
  #busy = false
  #gen = 0
  #status = statusLine()
  #poll: WhileOpen | null = null
  #opened: FactsSource | null = null
  #head = this.#heading()

  connectedCallback() {
    if (this.#source && this.#opened !== this.#source) this.#watch()
  }

  get repo(): string {
    return this.#source?.repo ?? ''
  }

  get section(): FactsSection {
    return this.#section
  }

  set section(value: FactsSection) {
    if (value === this.#section) return
    this.#section = value
    this.#head = this.#heading()
    this.#paint()
  }

  set source(value: FactsSource | null) {
    if (value === this.#source) return
    this.#source = value
    this.#gen++
    this.#busy = false
    this.#view = null
    this.#status.textContent = ''
    this.#paint()
    this.#watch()
  }

  #heading(): HTMLElement[] {
    const { title, intro, docs } = HEADS[this.#section]
    return pageHead({ title, level: 'h2', headingClass: 'label', intro, docs })
  }

  #watch() {
    this.#poll?.stop()
    this.#poll = null
    const source = this.#source
    if (!source || !this.isConnected) return
    this.#opened = source
    const gen = this.#gen
    this.#poll = whileOpen({ root: this, read: () => source.read(), show: a => {
      if (gen !== this.#gen) return
      if (!a.ok) say(this.#status, a.message, 'warn')
      const v = a.ok ? readView(a.body) : null
      if (v) this.#view = v
      this.#paint()
    } })
  }

  async #read(): Promise<boolean> {
    const source = this.#source
    if (!source) return false
    this.#poll?.stop()
    const gen = ++this.#gen
    const a = await source.read()
    if (gen !== this.#gen) return false
    this.#busy = false
    if (!a.ok) {
      say(this.#status, a.message, 'warn')
      this.#paint()
      return false
    }
    const v = readView(a.body)
    if (v) this.#view = v
    this.#paint()
    return true
  }

  #fix(note: string, part: string): Node {
    if (!part) return el('span', 'muted', note)
    return button(note, 'link', () => emit(this, 'sf-project-jump', { part }), 'button', `fix:${part}`)
  }

  #readiness(v: FactsView): HTMLElement {
    if (!v.readiness.length) return emptyState('The engine sent no readiness report.')
    const ul = el('ul', 'small list check-list')
    for (const r of v.readiness) {
      if (r.kind === 'verified') {
        ul.append(this.#verified(r))
        continue
      }
      const listed = r.found.join(', ')
      ul.append(checkRow({ state: r.state, word: r.word, label: r.label, value: r.value, tip: listed !== r.value ? listed : '', detail: r.note ? this.#fix(r.note, r.fixIn) : undefined }))
    }
    return ul
  }

  #verified(r: ReadinessLine): HTMLLIElement {
    const detail = el('span', 'muted')
    const at = when(r.at)
    const facts = [r.commit && r.result ? shortSha(r.commit) : '', at].filter(Boolean).join(', ')
    if (r.note) detail.append(this.#fix(r.note, r.fixIn))
    if (r.note && facts) detail.append(' · ')
    if (facts) detail.append(facts)
    const below: HTMLElement[] = []
    if (r.result === 'failed' && r.tail) {
      const pre = el('pre', 'small command-tail', visibleCommand(r.tail))
      pre.tabIndex = 0
      pre.setAttribute('role', 'region')
      pre.setAttribute('aria-label', `End of the output of the run on main's commit ${shortSha(r.commit)}`)
      below.push(pre)
    }
    const running = r.result === 'running'
    const go = button('Run on main', 'small', () => {
      if (!running && !this.#busy) void this.#runMain()
    }, 'button', 'verify-main')
    go.setAttribute('aria-disabled', String(running || this.#busy))
    if (this.#busy) busy(go, true)
    return checkRow({ state: r.state, word: r.word, label: r.label, value: r.value, detail: detail.childNodes.length ? detail : undefined, action: go, below })
  }

  async #runMain(): Promise<void> {
    const source = this.#source
    if (!source) return
    this.#poll?.stop()
    const gen = ++this.#gen
    this.#busy = true
    this.#status.textContent = ''
    this.#paint()
    const a = await request('/api/verify', { op: 'start', repo: source.repo, kind: 'check' })
    if (gen !== this.#gen) return
    if (!a.ok) {
      this.#busy = false
      say(this.#status, a.message, 'warn')
      this.#paint()
      return
    }
    source.again()
    if (await this.#read()) toast({ id: `project-facts:${source.repo}`, title: 'The run on main started.', tone: 'ok' })
  }

  refresh() {
    if (this.#source && !this.#busy) void this.#read()
  }

  #workflows(v: FactsView): HTMLElement {
    if (v.workflowsProblem) return el('p', 'small warn', v.workflowsProblem)
    if (!v.workflows.length) return emptyState('None of main’s workflows run on the agents’ pushes.')
    const ul = el('ul', 'small list workflow-list')
    for (const w of v.workflows) {
      const li = el('li')
      if (w.name) li.append(w.name, ' ', el('code', 'muted', w.path))
      else li.append(el('code', '', w.path))
      if (w.problem) li.append(' ', el('span', 'tag warn', "couldn't read"), el('span', 'muted', ` ${w.problem}`))
      if (w.secrets.length) li.append(' ', el('span', 'tag warn', `uses secrets: ${w.secrets.join(', ')}`))
      if (w.idToken) li.append(' ', el('span', 'tag warn', 'id-token: write'))
      if (w.trusted.length) li.append(' ', el('span', 'tag warn', `also ${w.trusted.join(' and ')}, which run with secrets`))
      ul.append(li)
    }
    return ul
  }

  #protection(v: FactsView): HTMLElement {
    const ul = el('ul', 'small list check-list')
    for (const r of v.protection) ul.append(checkRow({ state: r.state, label: r.label, value: r.value, tip: r.names.join(', '), detail: r.detail || undefined }))
    return ul
  }

  #body(v: FactsView): HTMLElement {
    if (this.#section === 'workflows') return this.#workflows(v)
    if (this.#section === 'protection') return this.#protection(v)
    return this.#readiness(v)
  }

  #paint() {
    if (!this.#source) return this.replaceChildren()
    const focused = focusedKey(this)
    const v = this.#view
    const title = this.#head[0]?.firstElementChild
    if (this.#section === 'protection' && title) title.textContent = `Protection of ${v?.branch || 'main'}`
    replaceKeeping(this, [...this.#head, v ? this.#body(v) : el('p', 'small muted', HEADS[this.#section].reading), this.#status])
    refocus(this, focused)
  }
}

customElements.define('sf-project-facts', SfProjectFacts)

declare global {
  interface HTMLElementTagNameMap {
    'sf-project-facts': SfProjectFacts
  }
}
