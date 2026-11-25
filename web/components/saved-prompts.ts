import { request, savedTone, type EngineAnswer } from '../api.ts'
import { busy, button, el, emit, focusedKey, labelError, refocus, replaceKeeping, say, shown, statusLine, visible } from '../lib/dom.ts'
import { actions, field, fieldGroup, showAnswer } from '../lib/form.ts'
import { emptyState } from '../lib/page.ts'
import { modal, type Modal } from '../lib/dialog.ts'
import { placeMenu } from '../lib/menu.ts'
import { icon } from './icon.ts'
import { titled } from './session-list.ts'
import { textbox, textarea } from '../lib/controls.ts'
import { outcome } from './toasts.ts'

interface SavedPrompt {
  id: string
  name: string
  text: string
}

export interface SavedPromptsView {
  repo: string
  disabled: boolean
}

function readList(body: unknown): SavedPrompt[] | null {
  if (!body || typeof body !== 'object') return null
  const v = body as Record<string, unknown>
  if (!Array.isArray(v.prompts)) return null
  return v.prompts.flatMap((x): SavedPrompt[] => {
    if (!x || typeof x !== 'object') return []
    const p = x as Record<string, unknown>
    if (typeof p.id !== 'string' || typeof p.name !== 'string' || typeof p.text !== 'string') return []
    return [{ id: p.id, name: p.name, text: p.text }]
  })
}

class SfSavedPrompts extends HTMLElement {
  #view: SavedPromptsView | null = null
  #list: SavedPrompt[] | null = null
  #canAdd = true
  #full = ''
  #editing: SavedPrompt | null = null
  #deleting = ''
  #busy = false
  #busyOp = ''
  #refused = ''
  #modal: Modal | null = null
  #toggle = button('Saved prompts', 'ghost small', undefined, 'button', 'saved-prompts-toggle')
  #panel = el('div', 'saved-prompts')
  #status = statusLine()
  #intro = el('p', 'small')
  #name = textbox({ name: 'name' })
  #text = textarea({ rows: 5, name: 'text' })
  #nameField = field('Prompt name', this.#name)
  #textField = field('Prompt text', this.#text)
  #editor = fieldGroup([])
  draft: () => string = () => ''

  constructor() {
    super()
    this.#toggle.addEventListener('click', () => this.#open())
    this.#toggle.setAttribute('aria-haspopup', 'dialog')
    this.#toggle.title = "Prompts saved for this project: insert one into the message box, or edit and delete them"
    this.#name.setAttribute('aria-required', 'true')
    this.#name.dataset.key = 'name'
    this.#text.setAttribute('aria-required', 'true')
    this.#text.dataset.key = 'text'
    this.#text.placeholder = 'The text to insert into the message box'
    for (const input of [this.#name, this.#text]) input.addEventListener('input', () => labelError(input, ''))
    this.#name.addEventListener('keydown', e => {
      if (e.key !== 'Enter' || e.isComposing) return
      e.preventDefault()
      void this.#save()
    })
    this.#text.addEventListener('keydown', e => {
      if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        void this.#save()
      }
    })
  }

  connectedCallback() {
    if (!this.#toggle.isConnected) this.append(this.#toggle)
  }

  #open() {
    const v = this.#view
    if (!v || v.disabled || this.#modal) return
    this.#status.textContent = ''
    this.#paint()
    const m = modal({ kind: '', title: 'Saved prompts', docs: 'saved-prompts', single: 'saved-prompts', focus: 'heading', body: [this.#intro, this.#panel] })
    this.#modal = m
    m.onClose(() => {
      this.#modal = null
      this.#editing = null
      this.#deleting = ''
    })
    void this.#call({ op: 'list', repo: v.repo }).then(() => {
      if (this.#modal === m && m.dialog.contains(document.activeElement) && document.activeElement?.matches('h2')) this.#focusFirst()
    })
  }

  #close() {
    this.#modal?.close()
  }

  set view(value: SavedPromptsView | null) {
    const changed = value?.repo !== this.#view?.repo
    this.#view = value
    this.hidden = !value
    if (changed) {
      this.#list = null
      this.#canAdd = true
      this.#full = ''
      this.#editing = null
      this.#deleting = ''
      this.#close()
    }
    this.#toggle.disabled = !value || value.disabled
  }

  async #call(body: Record<string, unknown>): Promise<EngineAnswer> {
    this.#busy = true
    this.#busyOp = typeof body.op === 'string' ? body.op : ''
    if (body.op === 'save') this.#unrefuse()
    this.#paint()
    const a = await request('/api/saved-prompts', body)
    this.#busy = false
    this.#refused = a.field
    if (!a.ok) {
      if (body.op === 'save') showAnswer(this.#editor, a, this.#status)
      else say(this.#status, a.message, 'bad')
      this.#paint()
      return a
    }
    const answer = a.body
    if (answer.repo === this.#view?.repo) {
      this.#list = readList(answer)
      this.#canAdd = answer.canAdd !== false
      this.#full = typeof answer.full === 'string' ? answer.full : ''
    }
    this.#paint()
    return a
  }

  async #save() {
    const v = this.#view
    const e = this.#editing
    if (!v || !e || this.#busy) return
    const name = this.#name.value
    const text = this.#text.value
    const a = await this.#call({ op: 'save', repo: v.repo, ...(e.id ? { id: e.id } : {}), name, text })
    if (!a.ok) return void (this.#refused === 'text' ? this.#text : this.#name).focus()
    say(this.#status, '')
    if (savedTone(a) === 'warn') outcome(this.#panel, this.#status, { id: `saved-prompts:${v.repo}`, title: a.message, tone: 'warn' })
    this.#editing = null
    this.#paint()
    this.#toggleFocus()
  }

  #unrefuse() {
    for (const input of [this.#name, this.#text]) labelError(input, '')
  }

  #focusFirst() {
    if (!this.#modal) return
    this.#panel.querySelector<HTMLElement>('button:not(:disabled), input:not(:disabled), textarea:not(:disabled)')?.focus()
  }

  #toggleFocus() {
    this.#panel.querySelector<HTMLElement>('[data-key="new"]')?.focus()
  }

  #edit(p: SavedPrompt | null) {
    this.#editing = p ?? { id: '', name: '', text: this.draft() }
    this.#deleting = ''
    this.#unrefuse()
    this.#name.value = this.#editing.name
    this.#text.value = this.#editing.text
    this.#status.textContent = ''
    this.#paint()
    this.#name.focus()
  }

  #paint() {
    const v = this.#view
    if (!v) return this.#panel.replaceChildren()
    const focused = focusedKey(this.#panel)
    const parts: HTMLElement[] = []
    if (this.#editing) {
      const e = this.#editing
      const save = button(e.id ? 'Save changes' : 'Save prompt', 'primary small', () => void this.#save(), 'button', 'save')
      if (this.#busy && this.#busyOp === 'save') busy(save, true)
      else if (this.#busy) save.setAttribute('aria-disabled', 'true')
      const cancel = button('Cancel', 'ghost small', () => {
        this.#editing = null
        this.#paint()
        this.#toggleFocus()
      }, 'button', 'cancel')
      this.#intro.textContent = e.id ? 'Edit the prompt. It is your own text: the engine keeps it for this project and sends it only when you send the message.' : 'Name it and write the text. It is your own text: the engine keeps it for this project and sends it only when you send the message.'
      replaceKeeping(this.#editor, [this.#nameField, this.#textField, actions([cancel, save])])
      parts.push(this.#editor, this.#status)
    } else {
      const list = this.#list
      this.#intro.textContent = 'Prompts saved for this project. Insert puts one into the message box.'
      const empty = !!list && !list.length
      const add = button('New prompt', 'primary small', () => {
        if (!this.#busy) this.#edit(null)
      }, 'button', 'new')
      add.disabled = v.disabled || !this.#canAdd
      if (this.#busy) add.setAttribute('aria-disabled', 'true')
      if (this.#full) add.title = visible(this.#full)
      if (!list) parts.push(el('p', 'small muted', this.#busy ? 'Loading…' : 'The list isn’t loaded.'))
      else if (empty) parts.push(emptyState('Save a prompt you send often, then insert it into the message box.', { title: 'No saved prompts yet', icon: 'file', action: add }))
      else parts.push(this.#table(list, v))
      const close = button('Close', 'ghost small', () => this.#close(), 'button', 'close')
      parts.push(this.#status, actions([close], empty ? [] : [add]))
    }
    replaceKeeping(this.#panel, parts)
    refocus(this.#panel, focused)
  }

  #table(list: SavedPrompt[], v: SavedPromptsView): HTMLElement {
    const head = el('div', 'sessions-head secondary')
    head.setAttribute('aria-hidden', 'true')
    head.append(el('span', '', 'Prompt'), el('span', '', 'Text'), el('span'))
    const ul = el('ul', 'sessions')
    ul.setAttribute('aria-label', 'Saved prompts')
    for (const p of list) ul.append(this.#row(p, v))
    const table = el('div', 'session-table prompt-table with-menu')
    table.append(head, ul)
    return table
  }

  #row(p: SavedPrompt, v: SavedPromptsView): HTMLLIElement {
    const li = el('li', 'with-menu')
    const cells = el('div', 'boxlist-row session-cells')
    const name = titled(shown(p.name))
    if (this.#deleting === p.id) {
      const yes = button('Delete', 'ghost small danger', () => {
        if (this.#busy) return
        void this.#call({ op: 'remove', repo: v.repo, id: p.id }).then(() => {
          this.#deleting = ''
          this.#paint()
          this.#toggleFocus()
        })
      }, 'button', `yes:${p.id}`)
      if (this.#busy && this.#busyOp === 'remove') busy(yes, true)
      else if (this.#busy) yes.setAttribute('aria-disabled', 'true')
      const no = button('Cancel', 'ghost small', () => {
        this.#deleting = ''
        this.#paint()
        this.#panel.querySelector<HTMLElement>(`[data-key="more:${CSS.escape(p.id)}"]`)?.focus()
      }, 'button', `no:${p.id}`)
      const ask = el('span', 'acts session-ask')
      ask.append(el('span', 'small muted', `Delete “${visible(p.name)}”?`), no, yes)
      cells.append(name, ask)
      li.classList.add('asking')
      li.append(cells)
      return li
    }
    const insert = button('Insert', 'primary small', () => {
      this.#close()
      emit(this, 'sf-insert-prompt', { text: p.text })
    }, 'button', `insert:${p.id}`)
    insert.disabled = v.disabled
    insert.setAttribute('aria-label', `Insert ${visible(p.name)} into the message box`)
    cells.append(name, el('span', 'secondary prompt-text', shown(p.text)), insert)
    li.append(cells, ...this.#menu(p, v))
    return li
  }

  #menu(p: SavedPrompt, v: SavedPromptsView): HTMLElement[] {
    const more = button('', 'ghost icon-button', undefined, 'button', `more:${p.id}`)
    more.setAttribute('aria-label', `More actions for ${visible(p.name)}`)
    more.setAttribute('aria-haspopup', 'menu')
    more.append(icon('more'))
    more.disabled = v.disabled
    const menu = el('div', 'menu row-menu surface')
    menu.popover = 'auto'
    menu.setAttribute('role', 'menu')
    menu.setAttribute('aria-label', `Actions for ${visible(p.name)}`)
    more.popoverTargetElement = menu
    const item = (text: string, mark: string, run: () => void) => {
      const b = button('', '', () => {
        menu.hidePopover()
        if (!this.#busy) run()
      })
      b.setAttribute('role', 'menuitem')
      const m = el('span', 'mark')
      m.append(icon(mark))
      b.append(m, el('span', '', text))
      return b
    }
    const edit = item('Edit', 'edit', () => this.#edit(p))
    menu.append(edit, item('Delete', 'trash', () => {
      this.#deleting = p.id
      this.#paint()
      this.#panel.querySelector<HTMLElement>(`[data-key="yes:${CSS.escape(p.id)}"]`)?.focus()
    }))
    menu.addEventListener('toggle', e => {
      if ((e as ToggleEvent).newState !== 'open') return
      placeMenu(more, menu)
      edit.focus()
    })
    return [more, menu]
  }
}

customElements.define('sf-saved-prompts', SfSavedPrompts)

declare global {
  interface HTMLElementTagNameMap {
    'sf-saved-prompts': SfSavedPrompts
  }
}
