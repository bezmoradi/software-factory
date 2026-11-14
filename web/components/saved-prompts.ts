import { api } from '../api.ts'
import { button, el, emit, shown, visible } from '../lib/dom.ts'

const MAX_NAME = 60
const MAX_TEXT = 20000

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
  #editing: SavedPrompt | null = null
  #deleting = ''
  #busy = false
  #toggle = button('Saved prompts', 'ghost small', undefined, 'button', 'saved-prompts-toggle')
  #panel = el('div', 'saved-prompts surface')
  #status = el('p', 'small')
  #name = el('input', 'textbox')
  #text = el('textarea', 'textbox')
  draft: () => string = () => ''

  constructor() {
    super()
    this.#panel.id = 'saved-prompts-panel'
    this.#panel.popover = 'auto'
    this.#panel.setAttribute('role', 'dialog')
    this.#panel.setAttribute('aria-label', 'Saved prompts')
    this.#toggle.popoverTargetElement = this.#panel
    this.#toggle.setAttribute('aria-haspopup', 'dialog')
    this.#toggle.title = "Prompts saved for this project: insert one into the message box, or edit and delete them"
    this.#status.setAttribute('role', 'status')
    this.#name.maxLength = MAX_NAME
    this.#name.required = true
    this.#name.setAttribute('aria-label', 'Prompt name')
    this.#name.placeholder = 'Name'
    this.#text.maxLength = MAX_TEXT
    this.#text.rows = 5
    this.#text.required = true
    this.#text.setAttribute('aria-label', 'Prompt text')
    this.#text.placeholder = 'The text to insert into the message box'
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
    this.#panel.addEventListener('toggle', e => {
      if ((e as ToggleEvent).newState !== 'open') {
        this.#editing = null
        this.#deleting = ''
        return
      }
      this.#status.textContent = ''
      this.#paint()
      if (this.#view) void this.#call({ op: 'list', repo: this.#view.repo })
    })
  }

  connectedCallback() {
    if (!this.#toggle.isConnected) this.append(this.#toggle, this.#panel)
  }

  set view(value: SavedPromptsView | null) {
    const changed = value?.repo !== this.#view?.repo
    this.#view = value
    this.hidden = !value
    if (changed) {
      this.#list = null
      this.#editing = null
      this.#deleting = ''
      if (this.#panel.matches(':popover-open')) this.#panel.hidePopover()
    }
    this.#toggle.disabled = !value || value.disabled
  }

  async #call(body: Record<string, unknown>): Promise<boolean> {
    this.#busy = true
    this.#paint()
    const res = await api('/api/saved-prompts', body).catch(() => null)
    this.#busy = false
    const answer = await res?.json().catch(() => null) as Record<string, unknown> | null
    if (!res || !res.ok) {
      this.#status.className = 'small bad'
      this.#status.textContent = typeof answer?.message === 'string' ? visible(answer.message) : 'The engine didn’t answer. Check that it is running.'
      this.#paint()
      return false
    }
    if (answer?.repo === this.#view?.repo) this.#list = readList(answer)
    this.#paint()
    return true
  }

  async #save() {
    const v = this.#view
    const e = this.#editing
    if (!v || !e || this.#busy) return
    const name = this.#name.value.trim()
    const text = this.#text.value
    if (!name) return this.#name.reportValidity()
    if (!text.trim()) return this.#text.reportValidity()
    const ok = await this.#call({ op: 'save', repo: v.repo, ...(e.id ? { id: e.id } : {}), name, text })
    if (!ok) return
    this.#status.className = 'small ok'
    this.#status.textContent = `Saved “${visible(name)}”.`
    this.#editing = null
    this.#paint()
    this.#toggleFocus()
  }

  #toggleFocus() {
    this.#panel.querySelector<HTMLElement>('[data-key="new"]')?.focus()
  }

  #edit(p: SavedPrompt | null) {
    this.#editing = p ?? { id: '', name: '', text: this.draft() }
    this.#deleting = ''
    this.#name.value = this.#editing.name
    this.#text.value = this.#editing.text.slice(0, MAX_TEXT)
    this.#status.textContent = ''
    this.#paint()
    this.#name.focus()
  }

  #paint() {
    const v = this.#view
    if (!v) return this.#panel.replaceChildren()
    const parts: HTMLElement[] = [el('h2', 'label', 'Saved prompts for this project')]
    if (this.#editing) {
      const e = this.#editing
      const save = button(e.id ? 'Save changes' : 'Save prompt', 'primary small', () => void this.#save(), 'button', 'save')
      save.disabled = this.#busy
      const cancel = button('Cancel', 'ghost small', () => {
        this.#editing = null
        this.#paint()
        this.#toggleFocus()
      }, 'button', 'cancel')
      const row = el('div', 'row')
      row.append(save, cancel)
      parts.push(el('p', 'small muted', e.id ? 'Edit the prompt. It is your own text: the engine keeps it for this project and sends it only when you send the message.' : 'Name it and write the text. It is your own text: the engine keeps it for this project and sends it only when you send the message.'), this.#name, this.#text, row)
    } else {
      const list = this.#list
      if (!list) parts.push(el('p', 'small muted', this.#busy ? 'Loading…' : 'The list isn’t loaded.'))
      else if (!list.length) parts.push(el('p', 'small muted', 'None yet. Save one with New prompt; Insert puts it into the message box.'))
      else {
        const ul = el('ul', 'list saved-prompts-list')
        for (const p of list) ul.append(this.#row(p, v))
        parts.push(ul)
      }
      const add = button('New prompt', 'ghost small', () => this.#edit(null), 'button', 'new')
      add.disabled = this.#busy || v.disabled || (list?.length ?? 0) >= 50
      if ((list?.length ?? 0) >= 50) add.title = 'A project keeps at most 50 saved prompts'
      parts.push(add)
    }
    parts.push(this.#status)
    this.#panel.replaceChildren(...parts)
  }

  #row(p: SavedPrompt, v: SavedPromptsView): HTMLLIElement {
    const li = el('li', 'saved-prompt')
    const head = el('div', 'row')
    head.append(el('span', 'saved-prompt-name', shown(p.name)), el('span', 'spacer'))
    if (this.#deleting === p.id) {
      const yes = button('Delete', 'danger small', () => void this.#call({ op: 'remove', repo: v.repo, id: p.id }).then(ok => {
        if (ok) {
          this.#status.className = 'small ok'
          this.#status.textContent = `Deleted “${visible(p.name)}”.`
        }
        this.#deleting = ''
        this.#paint()
        this.#toggleFocus()
      }), 'button', `yes:${p.id}`)
      const no = button('Keep it', 'ghost small', () => {
        this.#deleting = ''
        this.#paint()
        this.#panel.querySelector<HTMLElement>(`[data-key="delete:${p.id}"]`)?.focus()
      }, 'button', `no:${p.id}`)
      yes.disabled = this.#busy
      head.append(el('span', 'small warn', 'Delete this prompt?'), yes, no)
      li.append(head)
      queueMicrotask(() => no.focus())
      return li
    }
    const insert = button('Insert', 'primary small', () => {
      emit(this, 'sf-insert-prompt', { text: p.text })
      this.#panel.hidePopover()
    }, 'button', `insert:${p.id}`)
    insert.disabled = v.disabled
    insert.setAttribute('aria-label', `Insert ${visible(p.name)} into the message box`)
    const edit = button('Edit', 'ghost small', () => this.#edit(p), 'button', `edit:${p.id}`)
    edit.setAttribute('aria-label', `Edit ${visible(p.name)}`)
    const del = button('Delete', 'ghost small', () => {
      this.#deleting = p.id
      this.#paint()
    }, 'button', `delete:${p.id}`)
    del.setAttribute('aria-label', `Delete ${visible(p.name)}`)
    for (const b of [edit, del]) b.disabled = this.#busy || v.disabled
    head.append(insert, edit, del)
    li.append(head, el('p', 'small muted saved-prompt-text', shown(p.text.length > 200 ? `${p.text.slice(0, 200)}…` : p.text)))
    return li
  }
}

customElements.define('sf-saved-prompts', SfSavedPrompts)

declare global {
  interface HTMLElementTagNameMap {
    'sf-saved-prompts': SfSavedPrompts
  }
}
