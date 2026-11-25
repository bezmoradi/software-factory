import { request, savedTone, type EngineAnswer } from '../api.ts'
import { around, busy, button, el, focusedKey, labelError, refocus, replaceKeeping, say, shown, statusLine } from '../lib/dom.ts'
import { actions, answerControl, field, form } from '../lib/form.ts'
import { emptyState, pageHead } from '../lib/page.ts'
import { CHEAP_EVERY_MS, whileOpen, type WhileOpen } from '../lib/refresh.ts'
import { textbox, textarea, checkbox } from '../lib/controls.ts'
import { toast } from './toasts.ts'


interface Entry {
  id: string
  kind: string
  label: string
  at: number
  line: string
  reason: string
  withheld: boolean
  name: string
  text: string
  hidden: boolean
  added: boolean
  edited: boolean
}

interface LogView {
  entries: Entry[]
  more: number
  note: string
  left: number
  auditSince: number
  orphans: number
  problems: string[]
}

let serial = 0

function str(x: unknown): string {
  return typeof x === 'string' ? x : ''
}

function readEntry(x: unknown): Entry[] {
  if (!x || typeof x !== 'object') return []
  const e = x as Record<string, unknown>
  if (typeof e.id !== 'string' || !/^[0-9a-f]{16}$/.test(e.id) || typeof e.kind !== 'string' || typeof e.label !== 'string') return []
  return [{ id: e.id, kind: e.kind, label: e.label, at: Number(e.at) || 0, line: shown(str(e.line)), reason: shown(str(e.reason)), withheld: e.withheld === true, name: str(e.name), text: str(e.text), hidden: e.hidden === true, added: e.added === true, edited: e.edited === true }]
}

function readView(body: unknown): LogView | null {
  if (!body || typeof body !== 'object') return null
  const v = body as Record<string, unknown>
  if (!Array.isArray(v.entries)) return null
  return {
    entries: v.entries.flatMap(readEntry), more: Number(v.more) || 0, note: str(v.note), left: Number(v.left) || 0, auditSince: Number(v.auditSince) || 0, orphans: Number(v.orphans) || 0,
    problems: Array.isArray(v.problems) ? v.problems.filter((s): s is string => typeof s === 'string').map(shown) : [],
  }
}

function day(ms: number): string {
  return ms ? new Date(ms).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' }) : ''
}

class SfDecisionLog extends HTMLElement {
  #project = ''
  #view: LogView | null = null
  #busy = false
  #doing = ''
  #editing = ''
  #status = statusLine()
  #adding = textarea({ rows: 2, name: 'text', placeholder: 'A decision for the next sessions, such as "Keep the public API stable until 2.0"' })
  #name = textbox({ name: 'name', placeholder: 'What it was, such as "Sign-in by email"' })
  #text = textarea({ rows: 3, name: 'text', placeholder: 'Your own wording; empty keeps the engine\'s line' })
  #hidden = checkbox()
  #n = ++serial
  #order: string[] = []
  #formFor = ''
  #refused = ''
  #bad: { input: HTMLElement; text: string } | null = null
  #poll: WhileOpen | null = null
  #failed = false
  #previewOpen = false
  #head = pageHead({ title: 'Decision log', level: 'h2', headingClass: 'label', intro: 'Your decisions on this project, which each new session gets with its first message.', docs: { doc: 'decision-log', key: 'decisions-docs' } })

  constructor() {
    super()
    this.#adding.id = `decision-add-${this.#n}`
    this.#name.id = `decision-name-${this.#n}`
    this.#text.id = `decision-text-${this.#n}`
    this.#hidden.id = `decision-hidden-${this.#n}`
    this.#adding.dataset.key = 'decision-adding'
    this.#name.dataset.key = 'decision-name'
    this.#text.dataset.key = 'decision-text'
    this.#hidden.dataset.key = 'decision-hidden'
    for (const input of [this.#adding, this.#name, this.#text]) input.addEventListener('input', () => {
      if (this.#bad?.input !== input) return
      this.#bad = null
      labelError(input, '')
    })
  }

  connectedCallback() {
    if (!this.#poll?.live) this.#watch()
  }

  set project(value: string) {
    if (value === this.#project) return
    this.#project = value
    this.#view = null
    this.#editing = ''
    this.#status.textContent = ''
    this.#paint()
    this.#watch()
  }

  #watch() {
    this.#poll?.stop()
    this.#poll = null
    const repo = this.#project
    if (!repo || !this.isConnected) return
    this.#poll = whileOpen({ root: this, everyMs: CHEAP_EVERY_MS, hold: () => this.#typing(), read: () => request('/api/decision-log', { op: 'read', repo }), show: a => this.#took(a), near: key => this.#near(key) })
  }

  #typing(): boolean {
    const a = document.activeElement
    return this.#busy || !!this.#editing || (a instanceof HTMLElement && this.contains(a) && a.matches('input, textarea, select'))
  }

  #took(a: EngineAnswer) {
    if (!a.ok) {
      this.#failed = true
      say(this.#status, a.message, 'warn')
    } else {
      if (this.#failed) this.#status.textContent = ''
      this.#failed = false
      const v = readView(a.body)
      if (v) this.#view = v
    }
    this.#paint()
  }

  #hold(b: HTMLButtonElement): void {
    if (this.#busy && this.#doing === b.dataset.key) busy(b, true)
    else if (this.#busy) b.setAttribute('aria-disabled', 'true')
  }

  async #call(body: Record<string, unknown>, key: string): Promise<EngineAnswer | null> {
    const project = this.#project
    this.#busy = true
    this.#doing = key
    this.#bad = null
    this.#paint()
    const a = await request('/api/decision-log', body)
    if (project !== this.#project) return null
    this.#busy = false
    this.#refused = a.field
    if (!a.ok) {
      const place = body.op === 'add' ? this.#adding.form : body.op === 'set' ? this.#name.form : null
      const input = place && a.message ? answerControl(place, a) : null
      this.#bad = input && { input, text: a.message }
      say(this.#status, input ? '' : a.message, 'warn')
      this.#paint()
      return a
    }
    const v = readView(a.body)
    if (v) this.#view = v
    this.#paint()
    void this.#poll?.now()
    return a
  }

  async #add() {
    const text = this.#adding.value
    if (this.#busy) return
    if ((await this.#call({ op: 'add', repo: this.#project, text }, 'decision-add'))?.ok) {
      this.#adding.value = ''
      say(this.#status, '')
      toast({ id: `decisions:${this.#project}`, title: 'Added. New sessions get it with their first message.', tone: 'ok' })
    } else this.#adding.focus()
  }

  #edit(e: Entry) {
    if (this.#busy) return
    this.#editing = e.id
    this.#name.value = e.name
    this.#text.value = e.text
    this.#hidden.checked = e.hidden
    this.#status.textContent = ''
    this.#paint()
    this.#name.focus()
  }

  async #save(e: Entry) {
    if (this.#busy) return
    const name = this.#name.value
    const text = this.#text.value
    const a = await this.#call({ op: 'set', repo: this.#project, id: e.id, name, text, hidden: this.#hidden.checked }, `decision-save:${e.id}`)
    if (a?.ok) {
      this.#editing = ''
      say(this.#status, '')
      toast({ id: `decisions:${this.#project}`, title: a.message, tone: savedTone(a) })
      this.#paint()
    } else (this.#refused === 'name' ? this.#name : this.#text).focus()
  }

  #form(e: Entry): HTMLElement {
    const save = button('Save', 'primary small', undefined, 'submit', `decision-save:${e.id}`)
    const cancel = button('Cancel', 'link small', () => {
      if (this.#busy) return
      this.#editing = ''
      this.#paint()
    }, 'button', `decision-cancel:${e.id}`)
    this.#hold(save)
    this.#hold(cancel)
    return form({
      children: [
        field('Name', this.#name),
        e.added ? field('Text', this.#text) : field('Your wording', this.#text, { optional: true }),
        field('Hide it from new sessions', this.#hidden, { kind: 'check' }),
        actions([cancel, save]),
      ],
      onSubmit: () => void this.#save(e),
    })
  }

  #entry(e: Entry): HTMLElement {
    const li = el('li', e.hidden ? 'muted' : '')
    li.append(el('span', 'muted', `${day(e.at)} `), el('strong', '', e.label))
    if (e.name) li.append(` “${shown(e.name)}”`)
    if (e.hidden) li.append(' ', el('span', 'tag', 'hidden'))
    if (e.line) li.append(el('p', 'small muted', e.line))
    if (e.reason) li.append(el('p', 'small', `${e.withheld ? '' : 'Reason: '}${e.reason}`))
    if (e.text) li.append(el('p', 'small', `${e.added ? '' : 'Your wording: '}${shown(e.text)}`))
    if (this.#editing === e.id) {
      li.append(this.#form(e))
      return li
    }
    const acts = el('div', 'row')
    const edit = button(e.added ? 'Edit' : 'Name or edit', 'link small', () => this.#edit(e), 'button', `decision-edit:${e.id}`)
    this.#hold(edit)
    acts.append(edit)
    if (e.added || e.edited) {
      const remove = button(e.added ? 'Remove' : 'Undo my changes', 'link small', () => {
        if (!this.#busy) void this.#call({ op: 'remove', repo: this.#project, id: e.id }, `decision-remove:${e.id}`)
      }, 'button', `decision-remove:${e.id}`)
      this.#hold(remove)
      acts.append(remove)
    }
    li.append(acts)
    return li
  }

  #near(key: string): string[] {
    const cut = key.indexOf(':')
    const kind = cut < 0 ? '' : key.slice(0, cut)
    const id = cut < 0 ? this.#formFor : key.slice(cut + 1)
    const others = around(this.#order, id)
    const same = kind === 'decision-remove' ? kind : 'decision-edit'
    return [`decision-edit:${id}`, ...others.flatMap(o => [`${same}:${o}`, `decision-edit:${o}`])]
  }

  #paint() {
    if (!this.#project) return this.replaceChildren()
    const focused = focusedKey(this)
    const near = focused ? this.#near(focused) : []
    const parts: HTMLElement[] = [...this.#head]
    const v = this.#view
    if (!v) parts.push(el('p', 'small muted', 'Reading the log…'))
    else {
      if (v.auditSince) parts.push(el('p', 'small muted', `Decisions since ${day(v.auditSince)}.`))
      for (const p of v.problems) parts.push(el('p', 'small warn', p))
      const add = button('Add', 'primary small', undefined, 'submit', 'decision-add')
      this.#hold(add)
      parts.push(form({ children: [field('Add an entry', this.#adding), actions([add])], onSubmit: () => void this.#add() }))
      if (!v.entries.length) parts.push(emptyState('No decisions yet.'))
      else {
        const ul = el('ul', 'small list')
        for (const e of v.entries) ul.append(this.#entry(e))
        parts.push(ul)
        if (v.more) parts.push(el('p', 'small muted', `${v.more} older entries aren't shown.`))
      }
      if (v.orphans) parts.push(el('p', 'small muted', `${v.orphans} of your edits are for entries the audit file no longer holds; they are kept until space runs out.`))
      if (v.note) {
        const preview = el('details')
        preview.open = this.#previewOpen
        preview.addEventListener('toggle', () => {
          this.#previewOpen = preview.open
        })
        preview.append(el('summary', 'small', `What a new session gets${v.left ? ` (${v.left} older entries left out)` : ''}`), el('pre', 'small', shown(v.note)))
        parts.push(preview)
      }
    }
    parts.push(this.#status)
    replaceKeeping(this, parts)
    for (const input of [this.#adding, this.#name, this.#text]) labelError(input, this.#bad?.input === input ? this.#bad.text : '')
    this.#order = v?.entries.map(e => e.id) ?? []
    this.#formFor = this.#editing
    refocus(this, focused, () => near)
  }
}

customElements.define('sf-decision-log', SfDecisionLog)

declare global {
  interface HTMLElementTagNameMap {
    'sf-decision-log': SfDecisionLog
  }
}
