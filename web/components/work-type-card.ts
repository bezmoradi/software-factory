import { button, el, emit, focusedKey, refocus, visibleCommand, unspoofed, visible, whileBusy } from '../lib/dom.ts'
import { actions, field, form } from '../lib/form.ts'
import { dropdown, option } from '../lib/controls.ts'
import type { TypeAsk } from '../types.ts'

export interface WorkTypeCardView {
  sid: string
  ask: TypeAsk
  types: { id: string; name: string }[]
  disabled: boolean
}

let headings = 0

class SfWorkTypeCard extends HTMLElement {
  #view: WorkTypeCardView | null = null
  #key = ''
  #refused = ''
  #heading = `work-type-card-${++headings}`

  connectedCallback() {
    if (!this.#view) this.hidden = true
  }

  set view(value: WorkTypeCardView | null) {
    if (value?.sid !== this.#view?.sid) this.#refused = ''
    this.#view = value
    this.paint()
  }

  paint() {
    const v = this.#view
    const key = JSON.stringify(v ? [v.sid, v.ask, v.types, v.disabled, this.#refused] : null)
    if (key === this.#key) return
    this.#key = key
    this.hidden = !v
    const focused = focusedKey(this)
    this.replaceChildren(...(v ? [this.#card(v)] : []))
    refocus(this, focused)
  }

  #pick(b: HTMLButtonElement, type: string): Promise<void> {
    return whileBusy(b, () => new Promise<void>(settle => emit(this, 'sf-work-type', { type, done: refusal => {
      this.#refused = refusal
      this.#key = ''
      this.paint()
      settle()
    } })))
  }

  #card(v: WorkTypeCardView): HTMLElement {
    const box = el('section', 'notice work-type-card')
    box.setAttribute('aria-labelledby', this.#heading)
    const head = el('h2', 'label', 'Pick a work type')
    head.id = this.#heading
    box.append(head, el('p', 'small', visible(v.ask.text)))
    const suggested = v.ask.type ?? ''
    if (suggested && v.ask.suggests && v.ask.use) {
      box.append(el('p', 'small', visible(v.ask.suggests)))
      const words = el('blockquote', 'agent-words small', visibleCommand(unspoofed(v.ask.reason ?? '')))
      box.append(words)
      const use = button(visible(v.ask.use), 'small', undefined, 'button', 'use')
      use.disabled = v.disabled
      use.addEventListener('click', () => void this.#pick(use, suggested))
      const row = el('div', 'row')
      row.append(use)
      box.append(row)
    }
    const others = v.types.filter(t => t.id !== suggested)
    if (others.length) {
      const pick = dropdown({ options: others.map(t => option(visible(t.name), t.id)) })
      pick.disabled = v.disabled
      pick.dataset.key = 'other'
      const submit = button(suggested ? 'Use this one instead' : 'Use this work type', 'small', undefined, 'submit', 'pick')
      submit.disabled = v.disabled
      box.append(form({ cls: 'work-type-other', onSubmit: () => void this.#pick(submit, pick.value), children: [field(suggested ? 'Or pick another' : 'Work type', pick), actions([submit])] }))
    }
    const refused = el('p', 'small bad')
    refused.setAttribute('role', 'status')
    refused.textContent = this.#refused
    box.append(refused)
    return box
  }
}

customElements.define('sf-work-type-card', SfWorkTypeCard)

declare global {
  interface HTMLElementTagNameMap {
    'sf-work-type-card': SfWorkTypeCard
  }
}
