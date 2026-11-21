import { request, savedTone } from '../api.ts'
import { button, el, focusedKey, labelError, refocus, say, statusLine, unspoofed, visible, whileBusy } from '../lib/dom.ts'
import { actions, field, form, showAnswer } from '../lib/form.ts'
import { emptyState } from '../lib/page.ts'

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
  for (let n = 1; n <= max; n++) {
    const o = el('option', '', n === 1 ? 'Once' : `${n} times`)
    o.value = String(n)
    s.append(o)
  }
  s.value = String(value)
}

function runSelect(id: string, value: number, max: number): HTMLSelectElement {
  const s = el('select', 'textbox')
  s.id = id
  runOptions(s, value, max)
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
  #extraRuns: HTMLSelectElement = runSelect('', 1, 1)
  #save = button('Save review settings', 'small', undefined, 'submit', 'depth-save')
  #said = statusLine()
  #form: HTMLFormElement | null = null
  #again = button('Read again', 'link small', () => {
    if (this.#reading) return
    say(this.#said, '')
    void whileBusy(this.#again, () => this.#load())
  }, 'button', 'depth-again')
  #reading = 0

  connectedCallback() {
    if (this.#built) {
      if (this.#path && this.#loaded !== this.#path) void this.#load()
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
    for (const input of [this.#files, this.#lines]) input.inputMode = 'numeric'
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
    if (this.#path && this.#loaded !== this.#path) void this.#load()
  }

  set path(value: string) {
    if (value === this.#path) return
    this.#path = value
    this.#loaded = ''
    if (this.isConnected) void this.#load()
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

  async #load() {
    const path = this.#path
    if (!path) return
    this.#reading++
    this.#again.setAttribute('aria-disabled', 'true')
    try {
      const a = await request('/api/review-depth', { op: 'read', path })
      if (path !== this.#path) return
      if (!a.ok) {
        say(this.#said, a.message, 'warn')
        return this.#retry(true)
      }
      const body: unknown = a.body
      if (!isDepth(body)) {
        say(this.#said, 'The engine\'s answer couldn\'t be read. Try again.', 'warn')
        return this.#retry(true)
      }
      this.#show(body)
      this.#loaded = path
      this.#retry(false)
    } finally {
      if (!--this.#reading) this.#again.removeAttribute('aria-disabled')
    }
  }

  #retry(on: boolean) {
    if (on === (this.#again.parentNode === this)) return
    if (on) return this.#said.after(this.#again)
    const focused = focusedKey(this)
    this.#again.remove()
    refocus(this, focused, () => ['depth-save'])
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
    const a = await whileBusy(this.#save, () => request('/api/review-depth', { op: 'set', path: this.#path, advisory, runs, maxFiles, maxLines, extra, extraRuns }))
    if (this.#form && !showAnswer(this.#form, a, this.#said, { tone: 'warn' })) return
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
