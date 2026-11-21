import { api } from '../api.ts'
import { around, busy, button, el, field, focusedKey, labelError, refocus, replaceKeeping, say, shown, statusLine } from '../lib/dom.ts'


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
  #adding = el('textarea', 'textbox')
  #name = el('input', 'textbox')
  #text = el('textarea', 'textbox')
  #hidden = el('input')
  #n = ++serial
  #order: string[] = []
  #formFor = ''
  #refused = ''
  #bad: { input: HTMLElement; text: string } | null = null

  constructor() {
    super()
    this.#adding.rows = 2
    this.#adding.id = `decision-add-${this.#n}`
    this.#adding.placeholder = 'A decision for the next sessions, such as "Keep the public API stable until 2.0"'
    this.#name.id = `decision-name-${this.#n}`
    this.#name.placeholder = 'What it was, such as "Sign-in by email"'
    this.#text.rows = 3
    this.#text.id = `decision-text-${this.#n}`
    this.#text.placeholder = 'Your own wording; empty keeps the engine\'s line'
    this.#hidden.type = 'checkbox'
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

  set project(value: string) {
    if (value === this.#project) return
    this.#project = value
    this.#view = null
    this.#editing = ''
    this.#status.textContent = ''
    this.#paint()
    if (value) void this.#call({ op: 'read', repo: value }, 'decision-again')
  }

  #hold(b: HTMLButtonElement): void {
    if (this.#busy && this.#doing === b.dataset.key) busy(b, true)
    else if (this.#busy) b.setAttribute('aria-disabled', 'true')
  }

  async #call(body: Record<string, unknown>, key: string): Promise<boolean> {
    const project = this.#project
    this.#busy = true
    this.#doing = key
    this.#bad = null
    this.#paint()
    const res = await api('/api/decision-log', body).catch(() => null)
    if (project !== this.#project) return false
    this.#busy = false
    const answer = await res?.json().catch(() => null) as Record<string, unknown> | null
    this.#refused = typeof answer?.error === 'string' ? answer.error : ''
    if (!res || !res.ok) {
      const said = typeof answer?.message === 'string' && answer.message ? shown(answer.message) : ''
      const input = !said ? null : body.op === 'add' ? (this.#refused === 'text' ? this.#adding : null) : body.op === 'set' ? (this.#refused === 'name' ? this.#name : this.#refused === 'text' ? this.#text : null) : null
      this.#bad = input && { input, text: said }
      say(this.#status, input ? '' : said || 'The engine didn\'t answer. Try again.', 'warn')
      this.#paint()
      return false
    }
    const v = readView(answer)
    if (v) this.#view = v
    this.#paint()
    return true
  }

  async #add() {
    const text = this.#adding.value
    if (this.#busy) return
    if (await this.#call({ op: 'add', repo: this.#project, text }, 'decision-add')) {
      this.#adding.value = ''
      say(this.#status, 'Added. New sessions get it with their first message.', 'muted')
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
    if (await this.#call({ op: 'set', repo: this.#project, id: e.id, name, text, hidden: this.#hidden.checked }, `decision-save:${e.id}`)) {
      this.#editing = ''
      say(this.#status, 'Saved. It stays through every rebuild of the log.', 'muted')
      this.#paint()
    } else (this.#refused === 'name' ? this.#name : this.#text).focus()
  }

  #form(e: Entry): HTMLElement {
    const box = el('div', 'field')
    const labelled = (label: string, input: HTMLElement & { id: string }) => {
      const l = el('label', 'small', label)
      l.htmlFor = input.id
      box.append(l, input)
    }
    labelled('Name', this.#name)
    labelled(e.added ? 'Text' : 'Your wording (optional)', this.#text)
    const row = el('div', 'row field-row')
    const hide = el('label', 'small', 'Hide it from new sessions')
    hide.htmlFor = this.#hidden.id
    row.append(this.#hidden, hide)
    const save = button('Save', 'small', () => void this.#save(e), 'button', `decision-save:${e.id}`)
    const cancel = button('Cancel', 'link small', () => {
      if (this.#busy) return
      this.#editing = ''
      this.#paint()
    }, 'button', `decision-cancel:${e.id}`)
    this.#hold(save)
    this.#hold(cancel)
    box.append(row, cancel, save)
    return box
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
    return [`decision-edit:${id}`, ...others.flatMap(o => [`${same}:${o}`, `decision-edit:${o}`]), 'decision-again']
  }

  #paint() {
    if (!this.#project) return this.replaceChildren()
    const focused = focusedKey(this)
    const near = focused ? this.#near(focused) : []
    const parts: HTMLElement[] = [el('h3', 'label', 'Decision log')]
    parts.push(el('p', 'small muted', 'Built by the engine from its own records: requirements and plans you approved, by date and approval hash; verdicts you overrode, pushes you allowed or denied, with your reasons; and pull requests merged. It holds no agent text, so an approval shows only its kind and date until you name it. Each new session gets the log with its first message, as one engine note of at most 4 KiB, newest kept. Your names, wording, hidden entries and own entries are kept apart and survive each rebuild; a token-shaped reason is withheld.'))
    const v = this.#view
    if (!v) parts.push(el('p', 'small muted', this.#busy ? 'Reading the log…' : 'Nothing read yet.'))
    else {
      if (v.auditSince) parts.push(el('p', 'small muted', `The audit file goes back to ${day(v.auditSince)}; older decisions have rotated out.`))
      for (const p of v.problems) parts.push(el('p', 'small warn', p))
      const add = button('Add', 'small', () => void this.#add(), 'button', 'decision-add')
      this.#hold(add)
      const addBox = field('Add an entry', this.#adding)
      addBox.append(add)
      parts.push(addBox)
      if (!v.entries.length) parts.push(el('p', 'small muted', 'No decisions in the window yet.'))
      else {
        const ul = el('ul', 'small list')
        for (const e of v.entries) ul.append(this.#entry(e))
        parts.push(ul)
        if (v.more) parts.push(el('p', 'small muted', `${v.more} older entries aren't shown.`))
      }
      if (v.orphans) parts.push(el('p', 'small muted', `${v.orphans} of your edits are for entries the audit file no longer holds; they are kept until space runs out.`))
      const preview = el('details')
      preview.append(el('summary', 'small', v.note ? `What a new session gets (${new TextEncoder().encode(v.note).length} bytes${v.left ? `, ${v.left} older entries left out` : ''})` : 'What a new session gets: nothing, the log is empty'))
      if (v.note) preview.append(el('pre', 'small', shown(v.note)))
      parts.push(preview)
    }
    const again = button('Read again', 'link small', () => {
      if (!this.#busy) void this.#call({ op: 'read', repo: this.#project }, 'decision-again')
    }, 'button', 'decision-again')
    this.#hold(again)
    parts.push(again, this.#status)
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
