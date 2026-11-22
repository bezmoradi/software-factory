import { request } from '../api.ts'
import { busy, button, el, focusedKey, refocus, replaceKeeping, say, statusLine, unspoofed, visible, visibleCommand } from '../lib/dom.ts'
import { field, form } from '../lib/form.ts'
import { shortSha } from '../lib/format.ts'
import type { WorkflowRun } from '../types.ts'
import { checkbox } from '../lib/controls.ts'
import { whileOpen, type WhileOpen } from '../lib/refresh.ts'

interface Protection {
  state: string
  reviews: string
  approvals: number
  checks: string
  checkNames: string[]
  notes: string[]
  tones: Record<string, string>
}

interface ReadinessLine {
  kind: string
  state: string
  found: string[]
  note: string
  commit: string
  at: number
  result: string
  code: number
  tail: string
  label: string
  tone: string
}


interface FactsView {
  readiness: ReadinessLine[]
  branch: string
  commit: string
  workflows: WorkflowRun[]
  workflowsProblem: string
  protection: Protection
  lookups: boolean
}

function text(x: unknown): string {
  return typeof x === 'string' ? visible(unspoofed(x)) : ''
}

function strings(x: unknown): string[] {
  return Array.isArray(x) ? x.filter((s): s is string => typeof s === 'string').map(text) : []
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
  const state = r.state === 'found' || r.state === 'missing' ? r.state : 'unknown'
  const commit = typeof r.commit === 'string' && /^[0-9a-f]{40}$/.test(r.commit) ? r.commit : ''
  const result = r.result === 'passed' || r.result === 'failed' || r.result === 'running' ? r.result : ''
  return [{ kind: r.kind, state, found: strings(r.found), note: text(r.note), commit, at: Number.isSafeInteger(r.at) ? Number(r.at) : 0, result, code: Number.isSafeInteger(r.code) ? Number(r.code) : 0, tail: typeof r.tail === 'string' ? r.tail : '', label: r.label, tone: typeof r.tone === 'string' ? r.tone : '' }]
}

function readView(body: unknown): FactsView | null {
  if (!body || typeof body !== 'object') return null
  const v = body as Record<string, unknown>
  const p = (v.protection && typeof v.protection === 'object' ? v.protection : {}) as Record<string, unknown>
  if (!Array.isArray(v.workflows)) return null
  return {
    readiness: Array.isArray(v.readiness) ? v.readiness.flatMap(readReadiness) : [],
    branch: text(v.branch), commit: text(v.commit), workflows: v.workflows.flatMap(readRun), workflowsProblem: text(v.workflowsProblem), lookups: v.lookups === true,
    protection: { state: text(p.state) || 'unknown', reviews: text(p.reviews) || 'unknown', approvals: Number(p.approvals) || 0, checks: text(p.checks) || 'unknown', checkNames: strings(p.checkNames), notes: strings(p.notes), tones: p.tones && typeof p.tones === 'object' ? p.tones as Record<string, string> : {} },
  }
}

class SfProjectFacts extends HTMLElement {
  #repo = ''
  #view: FactsView | null = null
  #busy = false
  #op = ''
  #gen = 0
  #status = statusLine()
  #poll: WhileOpen | null = null
  #opened = ''

  connectedCallback() {
    if (this.#repo && this.#opened !== this.#repo) this.#watch()
  }

  get repo(): string {
    return this.#repo
  }

  set repo(value: string) {
    if (value === this.#repo) return
    this.#repo = value
    this.#gen++
    this.#busy = false
    this.#view = null
    this.#status.textContent = ''
    this.#paint()
    this.#watch()
  }

  #watch() {
    this.#poll?.stop()
    this.#poll = null
    const repo = this.#repo
    if (!repo || !this.isConnected) return
    this.#opened = repo
    const gen = this.#gen
    this.#poll = whileOpen({ root: this, read: () => request('/api/project-facts', { op: 'read', repo }), show: a => {
      if (gen !== this.#gen) return
      if (!a.ok) say(this.#status, a.message, 'warn')
      const v = a.ok ? readView(a.body) : null
      if (v) this.#view = v
      this.#paint()
    } })
  }

  async #call(body: Record<string, unknown>): Promise<boolean> {
    this.#poll?.stop()
    const gen = ++this.#gen
    const current = () => gen === this.#gen
    this.#busy = true
    this.#op = typeof body.op === 'string' ? body.op : ''
    this.#paint()
    const a = await request('/api/project-facts', body)
    if (!current()) return false
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

  #readiness(v: FactsView): HTMLElement {
    const box = el('div')
    box.append(el('h4', 'label', 'What the repository gives the agents'))
    box.append(el('p', 'small muted', 'Agents work much better with an instruction file, tests, CI, a linter and a check command. The engine reads these files from main with its own reader and runs nothing, except the check command on main\'s latest commit when you press Run on main, in a verification container with no network. Display only: nothing here holds or allows anything, and a run on main counts for no session\'s push.'))
    if (!v.readiness.length) {
      box.append(el('p', 'small muted', 'The engine sent no readiness report.'))
      return box
    }
    const ul = el('ul', 'small list')
    for (const r of v.readiness) {
      const li = el('li', r.tone === 'warn' ? 'warn' : '')
      li.append(el('span', r.tone ? `tag ${r.tone}` : 'tag', r.state), ` ${r.label}`)
      if (r.found.length) li.append(el('span', 'muted', `: ${r.found.join(', ')}`))
      if (r.note) li.append(el('p', 'small muted', r.note))
      if (r.kind === 'verified') li.append(...this.#verified(r, v))
      ul.append(li)
    }
    box.append(ul)
    return box
  }

  #verified(r: ReadinessLine, v: FactsView): HTMLElement[] {
    const out: HTMLElement[] = []
    const at = r.at ? new Date(r.at).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : ''
    if (r.commit) out.push(el('p', 'small muted', `This line's commit: ${shortSha(r.commit)}${at ? `, ${r.result === 'running' ? 'started' : 'ended'} ${at}` : ''}${v.commit && v.commit !== r.commit ? `; the rest of the report read main at ${shortSha(v.commit)}` : ''}.`))
    if (r.result === 'failed' && r.tail) {
      const pre = el('pre', 'small command-tail', visibleCommand(r.tail))
      pre.tabIndex = 0
      pre.setAttribute('role', 'region')
      pre.setAttribute('aria-label', `End of the output of the run on main's commit ${shortSha(r.commit)}`)
      out.push(pre)
    }
    const why = r.result === 'running' ? 'A run on main is running.' : ''
    const go = button('Run on main', 'small', () => {
      if (!why && !this.#busy) void this.#runMain()
    }, 'button', 'verify-main')
    go.setAttribute('aria-disabled', String(!!why || this.#busy))
    if (this.#busy && this.#op === 'run-main') busy(go, true)
    out.push(go)
    return out
  }

  async #runMain(): Promise<void> {
    this.#poll?.stop()
    const gen = ++this.#gen
    const repo = this.#repo
    this.#busy = true
    this.#op = 'run-main'
    this.#status.textContent = ''
    this.#paint()
    const a = await request('/api/verify', { op: 'start', repo, kind: 'check' })
    if (gen !== this.#gen) return
    if (!a.ok) {
      this.#busy = false
      say(this.#status, a.message, 'warn')
      this.#paint()
      return
    }
    if (await this.#call({ op: 'read', repo })) say(this.#status, 'The run on main started.')
  }

  refresh() {
    if (this.#repo && !this.#busy) void this.#call({ op: 'read', repo: this.#repo })
  }

  #workflows(v: FactsView): HTMLElement {
    const box = el('div')
    box.append(el('h4', 'label', 'CI workflows on the agents’ pushes'))
    box.append(el('p', 'small muted', `Every push of a session branch (sf/<id>) starts the workflows below with the repository's secrets, running the branch's own Makefile, tests and scripts. GitHub runs them as they are on the pushed branch, so the push card lists them for each commit. Where one shouldn't run on the agents' branches, add branches-ignore: ['sf/**'] under its push: trigger on the main branch.`))
    if (v.workflowsProblem) {
      box.append(el('p', 'small warn', v.workflowsProblem))
      return box
    }
    if (!v.workflows.length) {
      box.append(el('p', 'small muted', 'None: no workflow in .github/workflows/ on the main branch starts on a push to a session branch.'))
      return box
    }
    const ul = el('ul', 'small list')
    for (const w of v.workflows) {
      const li = el('li', w.secrets.length || w.idToken || w.trusted.length || w.problem ? 'warn' : '')
      li.append(el('code', '', w.path))
      if (w.name) li.append(` (${w.name})`)
      if (w.problem) li.append(' ', el('span', 'tag warn', "couldn't read"), el('span', 'muted', ` ${w.problem}`))
      else {
        li.append(el('span', 'muted', ` runs ${w.when}`))
        if (w.secrets.length) li.append(' ', el('span', 'tag warn', `uses secrets: ${w.secrets.join(', ')}`))
        if (w.idToken) li.append(' ', el('span', 'tag warn', 'id-token: write'))
        if (w.trusted.length) li.append(' ', el('span', 'tag warn', `also ${w.trusted.join(' and ')}, which run with secrets`))
      }
      ul.append(li)
    }
    box.append(ul)
    return box
  }

  #protection(p: Protection, branch: string): HTMLElement {
    const box = el('div')
    box.append(el('h4', 'label', `Protection of ${branch || 'the main branch'}`))
    box.append(el('p', 'small muted', 'The push gate stops at the session branch; whether a merge into main is safe rests on the git host\'s branch protection. The engine reads it with the saved token, reads only.'))
    const ul = el('ul', 'small list')
    const tone = (key: string) => typeof p.tones[key] === 'string' ? p.tones[key] : ''
    ul.append(el('li', tone('state'), `Protection: ${p.state}`))
    ul.append(el('li', tone('reviews'), `Reviews: ${p.reviews}${p.reviews === 'required' ? `, ${p.approvals} ${p.approvals === 1 ? 'approval' : 'approvals'}` : ''}`))
    ul.append(el('li', tone('checks'), `Status checks: ${p.checks}${p.checkNames.length ? ` (${p.checkNames.join(', ')})` : ''}`))
    box.append(ul)
    for (const n of p.notes) box.append(el('p', 'small muted', n))
    return box
  }

  #lookups(v: FactsView): HTMLElement {
    const box = checkbox({ checked: v.lookups })
    box.id = `lookups-${this.#repo}`
    if (this.#busy) box.setAttribute('aria-disabled', 'true')
    box.dataset.key = 'lookups'
    box.addEventListener('click', e => {
      if (this.#busy) e.preventDefault()
    })
    box.addEventListener('change', () => {
      if (this.#busy) return
      this.#status.textContent = ''
      void this.#call({ op: 'lookups', repo: this.#repo, on: box.checked })
    })
    const note = el('p', 'small muted', 'Off by default. When on, the engine asks each registry from its own network whether each package a push adds exists and when it was first published, and marks missing and very young ones, since attackers register names agents invent. Only package names are sent.')
    return form({ children: [field('Look up new packages on their public registry (npm, PyPI, proxy.golang.org, crates.io) for the push card', box, { kind: 'check' }), note] })
  }

  #paint() {
    if (!this.#repo) return this.replaceChildren()
    const focused = focusedKey(this)
    const parts: HTMLElement[] = [el('h3', 'label', 'CI workflows and main’s protection')]
    const v = this.#view
    if (!v) parts.push(el('p', 'small muted', 'Reading the workflows and the main branch\'s protection…'))
    else parts.push(this.#readiness(v), this.#workflows(v), this.#protection(v.protection, v.branch), this.#lookups(v))
    parts.push(this.#status)
    replaceKeeping(this, parts)
    refocus(this, focused)
  }
}

customElements.define('sf-project-facts', SfProjectFacts)

declare global {
  interface HTMLElementTagNameMap {
    'sf-project-facts': SfProjectFacts
  }
}
