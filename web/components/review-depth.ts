import { api } from '../api.ts'
import { button, el, unspoofed, visible } from '../lib/dom.ts'

interface DepthView {
  advisory: string[]
  runs: Record<string, number>
  maxFiles: number
  maxLines: number
  extra: string[]
  extraRuns: number
  agents: string[]
  type: string
  runsTotal: number
  aboveRuns: number
  advisoryRuns: number
  maxRuns: number
}

const AGENT = /^[A-Za-z0-9][\w.:@-]{0,119}$/

let serial = 0

function text(value: string): string {
  return visible(unspoofed(value))
}

async function problemOf(res: Response | null): Promise<string> {
  if (!res) return 'The engine didn\'t answer. Try again.'
  const body = await res.json().catch(() => null) as { message?: unknown } | null
  return typeof body?.message === 'string' && body.message ? body.message : 'The engine refused the change. Try again.'
}

function isDepth(v: unknown): v is DepthView {
  const d = v as DepthView | null
  return !!d && Array.isArray(d.advisory) && Array.isArray(d.extra) && Array.isArray(d.agents) && typeof d.runs === 'object' && d.runs !== null &&
    typeof d.maxFiles === 'number' && typeof d.maxLines === 'number' && typeof d.extraRuns === 'number' && typeof d.runsTotal === 'number'
}

function names(value: string): string[] | null {
  const list = value.split(/[\s,]+/).map(s => s.trim()).filter(Boolean)
  return list.every(a => AGENT.test(a)) ? [...new Set(list)] : null
}

function runSelect(id: string, value: number, max: number): HTMLSelectElement {
  const s = el('select', 'textbox')
  s.id = id
  for (let n = 1; n <= max; n++) {
    const o = el('option', '', n === 1 ? 'Once' : `${n} times`)
    o.value = String(n)
    s.append(o)
  }
  s.value = String(Math.min(Math.max(value || 1, 1), max))
  return s
}

class SfReviewDepth extends HTMLElement {
  #path = ''
  #loaded = ''
  #built = false
  #n = 0
  #summary = el('p', 'small muted')
  #runs = el('div', 'depth-runs')
  #advisory = el('input', 'textbox')
  #files = el('input', 'textbox')
  #lines = el('input', 'textbox')
  #extra = el('input', 'textbox')
  #extraRuns: HTMLSelectElement = runSelect('', 1, 3)
  #save = button('Save review settings', 'small', () => void this.#store())
  #said = el('p', 'small')

  connectedCallback() {
    if (this.#built) {
      if (this.#path && this.#loaded !== this.#path) void this.#load()
      return
    }
    this.#built = true
    this.#n = ++serial
    this.#said.setAttribute('role', 'status')
    this.#summary.setAttribute('aria-live', 'polite')
    const ids: [HTMLInputElement, string, string][] = [[this.#advisory, 'advisory', 'knackpack:security-reviewer'], [this.#files, 'files', '0'], [this.#lines, 'lines', '0'], [this.#extra, 'extra', 'knackpack:architecture-reviewer']]
    for (const [input, id, holder] of ids) {
      input.id = `depth-${id}-${this.#n}`
      input.placeholder = holder
      input.spellcheck = false
      input.autocomplete = 'off'
    }
    for (const input of [this.#files, this.#lines]) {
      input.inputMode = 'numeric'
      input.maxLength = 8
    }
    this.#advisory.maxLength = 700
    this.#extra.maxLength = 700
    this.#extraRuns.id = `depth-extra-runs-${this.#n}`
    const field = (label: string, input: HTMLElement, help: string) => {
      const box = el('div', 'field')
      const name = el('label', 'small', label)
      name.htmlFor = input.id
      const note = el('p', 'small muted', help)
      note.id = `${input.id}-help`
      input.setAttribute('aria-describedby', note.id)
      box.append(name, input, note)
      return box
    }
    this.replaceChildren(
      el('h3', 'label', 'Review depth'),
      el('p', 'small muted', 'These settings come only from this page, never from the repository or the team file, and they only add reviews: a work type\'s required roles always run. Each run uses the reviewers\' sign-in.'),
      this.#summary,
      el('h4', 'label', 'Runs per commit'),
      el('p', 'small muted', 'A role run more than once reviews the same commit separately each time. Their findings are pooled: one blocking run blocks the push, and an override clears every blocking run of that role on that commit.'),
      this.#runs,
      field('Advisory roles', this.#advisory, 'Agents that review with the required ones but only warn: their block shows as a warning and never holds the push, the push hold or an automatic findings send. Name agents from an installed plugin or your own agents folder, separated by commas, at most 5.'),
      el('h4', 'label', 'Deeper review for larger changes'),
      field('Above this many changed files', this.#files, 'Counted as the push hold counts them: the files the branch changes against the remote\'s main branch. 0 is off.'),
      field('Or above this many changed lines', this.#lines, 'Added and removed lines in that change. 0 is off. A change the engine can\'t count gets the deeper review.'),
      field('Extra roles above the size', this.#extra, 'Required for a change above the size, filled the way required roles are, separated by commas, at most 5.'),
      field('Every required role above the size runs', this.#extraRuns, 'Repeated runs for every required role of a larger change.'),
      this.#save,
      this.#said,
    )
    if (this.#path && this.#loaded !== this.#path) void this.#load()
  }

  set path(value: string) {
    if (value === this.#path) return
    this.#path = value
    this.#loaded = ''
    if (this.isConnected) void this.#load()
  }

  #show(v: DepthView) {
    const max = Math.max(1, Math.min(v.maxRuns || 3, 3))
    const kind = v.type ? text(v.type) : 'review'
    const parts = [`On this project a ${kind}'s review now takes ${v.runsTotal} reviewer ${v.runsTotal === 1 ? 'run' : 'runs'}`]
    if (v.advisoryRuns) parts.push(`plus ${v.advisoryRuns} advisory ${v.advisoryRuns === 1 ? 'run' : 'runs'}`)
    let said = parts.join(', ')
    if (v.aboveRuns) said += `; a change above the size takes ${v.aboveRuns} required ${v.aboveRuns === 1 ? 'run' : 'runs'}`
    this.#summary.textContent = `${said}, plus any the changed files call for. Each one is a run in the reviewer container.`
    this.#runs.replaceChildren()
    v.agents.forEach((agent, i) => {
      const s = runSelect(`depth-run-${this.#n}-${i}`, v.runs[agent] ?? 1, max)
      s.dataset.agent = agent
      const box = el('div', 'row field-row')
      const label = el('label', 'small', text(agent))
      label.htmlFor = s.id
      box.append(label, el('span', 'spacer'), s)
      this.#runs.append(box)
    })
    if (!v.agents.length) this.#runs.append(el('p', 'small muted', 'This project names no review roles yet.'))
    this.#advisory.value = v.advisory.join(', ')
    this.#extra.value = v.extra.join(', ')
    this.#files.value = String(v.maxFiles)
    this.#lines.value = String(v.maxLines)
    this.#extraRuns.value = String(Math.min(Math.max(v.extraRuns || 1, 1), max))
  }

  async #load() {
    const path = this.#path
    if (!path) return
    const res = await api('/api/review-depth', { op: 'read', path }).catch(() => null)
    if (path !== this.#path) return
    if (!res || !res.ok) {
      this.#said.className = 'small warn'
      this.#said.textContent = await problemOf(res)
      return
    }
    const body: unknown = await res.json().catch(() => null)
    if (!isDepth(body)) {
      this.#said.className = 'small warn'
      this.#said.textContent = 'The engine\'s answer couldn\'t be read. Try again.'
      return
    }
    this.#show(body)
    this.#loaded = path
  }

  async #store() {
    const advisory = names(this.#advisory.value)
    const extra = names(this.#extra.value)
    const count = (input: HTMLInputElement) => {
      const t = input.value.trim()
      return t === '' ? 0 : /^\d{1,8}$/.test(t) ? Number(t) : -1
    }
    const maxFiles = count(this.#files)
    const maxLines = count(this.#lines)
    if (!advisory || !extra || advisory.length > 5 || extra.length > 5 || maxFiles < 0 || maxLines < 0) {
      this.#said.className = 'small warn'
      this.#said.textContent = 'Name at most 5 agents in each list, separated by commas, and give each size as a whole number, 0 to switch it off.'
      return
    }
    const runs: Record<string, number> = {}
    for (const s of this.#runs.querySelectorAll<HTMLSelectElement>('select[data-agent]')) {
      const n = Number(s.value)
      if (s.dataset.agent && n > 1) runs[s.dataset.agent] = n
    }
    const extraRuns = Number(this.#extraRuns.value) > 1 ? Number(this.#extraRuns.value) : 0
    this.#save.disabled = true
    const res = await api('/api/review-depth', { op: 'set', path: this.#path, advisory, runs, maxFiles, maxLines, extra, extraRuns }).catch(() => null)
    this.#save.disabled = false
    if (!res || !res.ok) {
      this.#said.className = 'small warn'
      this.#said.textContent = await problemOf(res)
      return
    }
    const body: unknown = await res.json().catch(() => null)
    if (isDepth(body)) this.#show(body)
    this.#said.className = 'small muted'
    this.#said.textContent = 'Saved. The next review uses them.'
  }
}

customElements.define('sf-review-depth', SfReviewDepth)

declare global {
  interface HTMLElementTagNameMap {
    'sf-review-depth': SfReviewDepth
  }
}
