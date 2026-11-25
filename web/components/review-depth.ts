import { request, savedTone, UNREAD, type EngineAnswer } from '../api.ts'
import { button, el, labelError, say, statusLine, unspoofed, visible, whileBusy } from '../lib/dom.ts'
import { actions, field, fieldRow, form, showAnswer } from '../lib/form.ts'
import { emptyState, pageHead } from '../lib/page.ts'
import { dropdown, option, textbox } from '../lib/controls.ts'
import { CHEAP_EVERY_MS, whileOpen, type WhileOpen } from '../lib/refresh.ts'
import { icon } from './icon.ts'
import { toast } from './toasts.ts'

type Mode = 'always' | 'big' | 'warn'

const MODES: ReadonlyArray<readonly [Mode, string]> = [['always', 'Always'], ['big', 'Big changes only'], ['warn', 'Warn only']]

interface DepthView {
  reviewers: Record<string, string>
  maxLines: number
  twice: boolean
  type: string
  runsTotal: number
  aboveRuns: number
  advisoryRuns: number
  available: string[]
  required: string[]
}

let serial = 0

function text(value: string): string {
  return visible(unspoofed(value))
}

function isDepth(v: unknown): v is DepthView {
  const d = v as DepthView | null
  return !!d && typeof d.reviewers === 'object' && d.reviewers !== null && Array.isArray(d.available) && Array.isArray(d.required) &&
    typeof d.maxLines === 'number' && typeof d.twice === 'boolean' && typeof d.runsTotal === 'number'
}

function isMode(v: string): v is Mode {
  return MODES.some(([m]) => m === v)
}

class SfReviewDepth extends HTMLElement {
  #path = ''
  #built = false
  #n = 0
  #summary = el('span')
  #list = el('ul', 'list depth-list')
  #add = dropdown({ name: 'reviewers' })
  #lines = textbox({ inputMode: 'numeric', name: 'maxLines' })
  #twice = dropdown({ name: 'twice', options: [option('Once', 'once'), option('Twice', 'twice')] })
  #save = button('Save', 'primary small', undefined, 'submit', 'depth-save')
  #said = statusLine()
  #form: HTMLFormElement | null = null
  #poll: WhileOpen | null = null
  #view: DepthView | null = null
  #picked = new Map<string, Mode>()
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
    this.#add.id = `depth-add-${this.#n}`
    this.#lines.id = `depth-lines-${this.#n}`
    this.#lines.placeholder = '0'
    this.#lines.spellcheck = false
    this.#lines.autocomplete = 'off'
    this.#twice.id = `depth-twice-${this.#n}`
    this.#add.addEventListener('change', () => this.#added())
    this.#lines.addEventListener('input', () => {
      labelError(this.#lines, '')
      this.#sized()
    })
    this.#twice.addEventListener('change', () => {
      labelError(this.#twice, '')
      this.#sized()
    })
    const head = pageHead({ title: 'Review depth', level: 'h2', headingClass: 'label', intro: 'Reviews this project adds to the ones its work types require.', docs: { doc: 'review-depth', key: 'depth-docs' } })
    head.at(-1)?.append(' ', this.#summary)
    this.replaceChildren(
      ...head,
      this.#form = form({
        children: [
          fieldRow([
            field('A big change has more than this many changed lines (0 is off)', this.#lines),
            field('Big changes are reviewed', this.#twice),
          ], 'halves'),
          ...pageHead({ title: 'Reviewers', level: 'h3', headingClass: 'label', intro: 'The reviewers that check this project’s changes. The work type’s own are locked; each one you add reviews always, only big changes, or only warns.', docs: { doc: 'reviewers', key: 'reviewers-docs' } }),
          this.#list,
          field('Add reviewer', this.#add),
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
    this.#view = v
    const kind = v.type ? text(v.type) : 'review'
    const parts = [`On this project a ${kind}'s review now takes ${v.runsTotal} reviewer ${v.runsTotal === 1 ? 'run' : 'runs'}`]
    if (v.advisoryRuns) parts.push(`plus ${v.advisoryRuns} advisory ${v.advisoryRuns === 1 ? 'run' : 'runs'}`)
    let said = parts.join(', ')
    if (v.aboveRuns) said += `; a big change takes ${v.aboveRuns} required ${v.aboveRuns === 1 ? 'run' : 'runs'}`
    this.#summary.textContent = `${said}, plus any the changed files call for. Each one is a run in a reviewer container of its own.`
    this.#picked = new Map(Object.entries(v.reviewers).map(([agent, mode]) => [agent, isMode(mode) ? mode : 'warn']))
    this.#lines.value = String(v.maxLines)
    this.#twice.querySelector('option[value=""]')?.remove()
    this.#twice.value = v.twice ? 'twice' : 'once'
    this.#rows()
  }

  #rows(focus = '') {
    const v = this.#view
    if (!v) return
    this.#list.replaceChildren()
    for (const agent of v.required) {
      if (this.#picked.has(agent)) continue
      const li = el('li', 'row text-row')
      li.append(el('span', 'depth-name', text(agent)), el('span', 'spacer'), el('span', 'small muted', 'Always (required)'))
      this.#list.append(li)
    }
    for (const [agent, mode] of this.#picked) {
      const name = text(agent)
      const li = el('li', 'row text-row')
      const label = el('span', 'depth-name', name)
      const note = v.required.includes(agent) ? 'already required' : v.available.includes(agent) ? '' : 'not installed'
      if (note) label.append(' ', el('span', 'small muted', `(${note})`))
      const when = dropdown({ options: MODES.map(([m, words]) => option(words, m)) })
      when.value = mode
      when.setAttribute('aria-label', `When ${name} reviews`)
      when.addEventListener('change', () => {
        if (isMode(when.value)) this.#picked.set(agent, when.value)
        this.#sized()
      })
      const remove = button('', 'ghost icon-button', () => this.#removed(agent))
      remove.setAttribute('aria-label', `Remove ${name}`)
      remove.append(icon('close'))
      when.dataset.agent = agent
      label.append(el('span', 'small muted depth-unsized', ' (never runs: no big-change size)'))
      li.append(label, el('span', 'spacer'), when, remove)
      this.#list.append(li)
      if (agent === focus) queueMicrotask(() => when.focus())
    }
    if (!this.#list.childElementCount) this.#list.append(emptyState('This project names no reviewers yet.', { tag: 'li' }))
    const left = v.available.filter(a => !v.required.includes(a) && !this.#picked.has(a))
    this.#add.replaceChildren(option(left.length ? 'Choose a reviewer' : 'No more reviewers to add', ''), ...left.map(a => option(text(a), a)))
    this.#add.value = ''
    this.#sized()
  }

  #sized() {
    const typed = this.#lines.value.trim()
    const unsized = typed === '' || Number(typed) === 0
    const why = 'set a size first'
    for (const when of this.#list.querySelectorAll<HTMLSelectElement>('select[data-agent]')) {
      const big = when.querySelector<HTMLOptionElement>('option[value="big"]')
      const picked = when.value === 'big'
      if (big) {
        big.disabled = unsized && !picked
        big.textContent = unsized && !picked ? `Big changes only (${why})` : 'Big changes only'
      }
      const note = when.closest('li')?.querySelector<HTMLElement>('.depth-unsized')
      if (note) note.hidden = !(unsized && picked)
    }
    const off = unsized
    const first = this.#twice.querySelector<HTMLOptionElement>('option[value=""]')
    if (off && !first) {
      const hint = option('Set a size first', '')
      hint.disabled = true
      this.#twice.prepend(hint)
      this.#twice.value = ''
    } else if (!off && first) {
      first.remove()
      this.#twice.value = 'once'
    }
    this.#twice.disabled = off
  }

  #added() {
    const agent = this.#add.value
    if (!agent) return
    labelError(this.#add, '')
    this.#picked.set(agent, 'warn')
    this.#rows(agent)
  }

  #removed(agent: string) {
    this.#dirty = true
    labelError(this.#add, '')
    this.#picked.delete(agent)
    this.#rows()
    this.#add.focus()
  }

  async #store() {
    const reviewers = Object.fromEntries(this.#picked)
    const maxLines = this.#lines.value
    const twice = this.#twice.value === 'twice'
    say(this.#said, '')
    this.#saving = true
    const a = await whileBusy(this.#save, () => request('/api/review-depth', { op: 'set', path: this.#path, reviewers, maxLines, twice }))
    this.#saving = false
    if (this.#form && !showAnswer(this.#form, a, this.#said, { tone: 'warn' })) return
    this.#failed = false
    this.#dirty = false
    const body: unknown = a.body
    if (isDepth(body)) this.#show(body)
    toast({ id: `review-depth:${this.#path}`, title: a.message, tone: savedTone(a) })
  }
}

customElements.define('sf-review-depth', SfReviewDepth)

declare global {
  interface HTMLElementTagNameMap {
    'sf-review-depth': SfReviewDepth
  }
}
