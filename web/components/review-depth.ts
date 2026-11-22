import { request, savedTone, UNREAD, type EngineAnswer } from '../api.ts'
import { button, el, labelError, say, statusLine, unspoofed, visible, whileBusy } from '../lib/dom.ts'
import { actions, field, form, showAnswer } from '../lib/form.ts'
import { emptyState } from '../lib/page.ts'
import { textbox, dropdown, option } from '../lib/controls.ts'
import { CHEAP_EVERY_MS, whileOpen, type WhileOpen } from '../lib/refresh.ts'

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

let serial = 0

function text(value: string): string {
  return visible(unspoofed(value))
}


function isDepth(v: unknown): v is DepthView {
  const d = v as DepthView | null
  return !!d && Array.isArray(d.advisory) && Array.isArray(d.extra) && Array.isArray(d.agents) && typeof d.runs === 'object' && d.runs !== null &&
    typeof d.maxFiles === 'number' && typeof d.maxLines === 'number' && typeof d.extraRuns === 'number' && typeof d.runsTotal === 'number'
}

function runOptions(s: HTMLSelectElement, value: number, max: number) {
  s.replaceChildren()
  for (let n = 1; n <= max; n++) s.append(option(n === 1 ? 'Once' : `${n} times`, String(n)))
  s.value = String(value)
}

function runSelect(id: string, value: number, max: number): HTMLSelectElement {
  const s = dropdown()
  s.id = id
  runOptions(s, value, max)
  return s
}

class SfReviewDepth extends HTMLElement {
  #path = ''
  #built = false
  #n = 0
  #summary = el('p', 'small muted')
  #runs = el('div', 'depth-runs')
  #advisory = textbox()
  #files = textbox({ inputMode: 'numeric' })
  #lines = textbox({ inputMode: 'numeric' })
  #extra = textbox()
  #extraRuns: HTMLSelectElement = runSelect('', 1, 1)
  #save = button('Save review settings', 'small', undefined, 'submit', 'depth-save')
  #said = statusLine()
  #form: HTMLFormElement | null = null
  #poll: WhileOpen | null = null
  #failed = false
  #dirty = false
  #saving = false

  connectedCallback() {
    if (this.#built) {
      if (!this.#poll?.live) this.#watch()
      return
    }
    this.#built = true
    this.#n = ++serial
    this.#summary.setAttribute('aria-live', 'polite')
    const ids: [HTMLInputElement, string, string, string][] = [[this.#advisory, 'advisory', 'advisory', 'knackpack:security-reviewer'], [this.#files, 'files', 'maxFiles', '0'], [this.#lines, 'lines', 'maxLines', '0'], [this.#extra, 'extra', 'extra', 'knackpack:architecture-reviewer']]
    for (const [input, id, name, holder] of ids) {
      input.id = `depth-${id}-${this.#n}`
      input.name = name
      input.placeholder = holder
      input.spellcheck = false
      input.autocomplete = 'off'
    }
    this.#extraRuns.id = `depth-extra-runs-${this.#n}`
    this.#extraRuns.name = 'extraRuns'
    for (const input of [this.#advisory, this.#files, this.#lines, this.#extra]) input.addEventListener('input', () => labelError(input, ''))
    this.#extraRuns.addEventListener('change', () => labelError(this.#extraRuns, ''))
    this.replaceChildren(
      el('h3', 'label', 'Review depth'),
      el('p', 'small muted', 'These settings come only from this page, never from the repository or the team file, and they only add reviews: a work type\'s required roles always run. Each run uses the reviewers\' sign-in.'),
      this.#summary,
      el('h4', 'label', 'Runs per commit'),
      el('p', 'small muted', 'A role run more than once reviews the same commit separately each time. Their findings are pooled: one blocking run blocks the push, and an override clears every blocking run of that role on that commit.'),
      this.#form = form({
        children: [
          this.#runs,
          field('Advisory roles', this.#advisory, { help: 'Agents that review with the required ones but only warn: their block shows as a warning and never holds the push, the push hold or an automatic findings send. Name agents from an installed plugin or your own agents folder, separated by commas, at most 5.' }),
          el('h4', 'label', 'Deeper review for larger changes'),
          field('Above this many changed files', this.#files, { help: 'Counted as the push hold counts them: the files the branch changes against the remote\'s main branch. 0 is off.' }),
          field('Or above this many changed lines', this.#lines, { help: 'Added and removed lines in that change. 0 is off. A change the engine can\'t count gets the deeper review.' }),
          field('Extra roles above the size', this.#extra, { help: 'Required for a change above the size, filled the way required roles are, separated by commas, at most 5.' }),
          field('Every required role above the size runs', this.#extraRuns, { help: 'Repeated runs for every required role of a larger change.' }),
          actions([this.#save]),
        ],
        onSubmit: () => void this.#store(),
      }),
      this.#said,
    )
    for (const kind of ['input', 'change']) this.#form?.addEventListener(kind, () => {
      this.#dirty = true
    })
    this.#watch()
  }

  set path(value: string) {
    if (value === this.#path) return
    this.#path = value
    this.#dirty = false
    this.#watch()
  }

  #watch() {
    this.#poll?.stop()
    this.#poll = null
    const path = this.#path
    if (!path || !this.isConnected || !this.#built) return
    this.#poll = whileOpen({ root: this, everyMs: CHEAP_EVERY_MS, hold: () => this.#typing(), read: () => request('/api/review-depth', { op: 'read', path }), show: a => this.#took(a), near: () => ['depth-save'] })
  }

  #typing(): boolean {
    const a = document.activeElement
    return this.#dirty || this.#saving || (a instanceof HTMLElement && !!this.#form?.contains(a) && a.matches('input, textarea, select'))
  }

  #took(a: EngineAnswer) {
    const body: unknown = a.body
    if (!a.ok || !isDepth(body)) {
      this.#failed = true
      say(this.#said, a.ok ? UNREAD : a.message, 'warn')
      return
    }
    if (this.#failed) say(this.#said, '')
    this.#failed = false
    this.#show(body)
  }

  #show(v: DepthView) {
    const max = v.maxRuns
    const kind = v.type ? text(v.type) : 'review'
    const parts = [`On this project a ${kind}'s review now takes ${v.runsTotal} reviewer ${v.runsTotal === 1 ? 'run' : 'runs'}`]
    if (v.advisoryRuns) parts.push(`plus ${v.advisoryRuns} advisory ${v.advisoryRuns === 1 ? 'run' : 'runs'}`)
    let said = parts.join(', ')
    if (v.aboveRuns) said += `; a change above the size takes ${v.aboveRuns} required ${v.aboveRuns === 1 ? 'run' : 'runs'}`
    this.#summary.textContent = `${said}, plus any the changed files call for. Each one is a run in a reviewer container of its own.`
    this.#runs.replaceChildren()
    v.agents.forEach((agent, i) => {
      const s = runSelect(`depth-run-${this.#n}-${i}`, v.runs[agent] ?? 1, max)
      s.dataset.agent = agent
      this.#runs.append(field(text(agent), s, { kind: 'inline', cls: 'field-row depth-run' }))
    })
    if (!v.agents.length) this.#runs.append(emptyState('This project names no review roles yet.'))
    this.#advisory.value = v.advisory.join(', ')
    this.#extra.value = v.extra.join(', ')
    this.#files.value = String(v.maxFiles)
    this.#lines.value = String(v.maxLines)
    runOptions(this.#extraRuns, v.extraRuns || 1, max)
  }

  async #store() {
    const advisory = this.#advisory.value
    const extra = this.#extra.value
    const maxFiles = this.#files.value
    const maxLines = this.#lines.value
    const runs: Record<string, string> = {}
    for (const s of this.#runs.querySelectorAll<HTMLSelectElement>('select[data-agent]')) {
      if (s.dataset.agent) runs[s.dataset.agent] = s.value
    }
    const extraRuns = this.#extraRuns.value
    say(this.#said, '')
    this.#saving = true
    const a = await whileBusy(this.#save, () => request('/api/review-depth', { op: 'set', path: this.#path, advisory, runs, maxFiles, maxLines, extra, extraRuns }))
    this.#saving = false
    if (this.#form && !showAnswer(this.#form, a, this.#said, { tone: 'warn' })) return
    this.#failed = false
    this.#dirty = false
    const body: unknown = a.body
    if (isDepth(body)) this.#show(body)
    say(this.#said, a.message, savedTone(a))
  }
}

customElements.define('sf-review-depth', SfReviewDepth)

declare global {
  interface HTMLElementTagNameMap {
    'sf-review-depth': SfReviewDepth
  }
}
