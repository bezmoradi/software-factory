import { request, savedTone, type EngineAnswer } from '../api.ts'
import { button, labelError, say, statusLine, whileBusy } from '../lib/dom.ts'
import { actions, field, fieldRow, form, showAnswer } from '../lib/form.ts'
import { textbox } from '../lib/controls.ts'
import { toast } from './toasts.ts'
import { pageHead } from '../lib/page.ts'
import { CHEAP_EVERY_MS, whileOpen, type WhileOpen } from '../lib/refresh.ts'

let serial = 0


class SfProjectCommands extends HTMLElement {
  #project = ''
  #check = textbox()
  #setup = textbox()
  #image = textbox()
  #browser = textbox()
  #save = button('Save', 'primary small', undefined, 'submit', 'commands-save')
  #said = statusLine()
  #form: HTMLFormElement | null = null
  #built = false
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
    const n = ++serial
    for (const [input, id, name, holder] of [[this.#check, `check-command-${n}`, 'check', 'make check'], [this.#setup, `setup-command-${n}`, 'setup', 'npm ci'], [this.#image, `verify-image-${n}`, 'image', 'node@sha256:…'], [this.#browser, `browser-command-${n}`, 'browser', 'PLAYWRIGHT_BROWSERS_PATH=/ms-playwright npx playwright test']] as const) {
      input.id = id
      input.name = name
      input.placeholder = holder
      input.spellcheck = false
      input.autocomplete = 'off'
      input.addEventListener('input', () => labelError(input, ''))
    }
    this.replaceChildren(
      ...pageHead({ title: 'Check and setup commands', level: 'h2', headingClass: 'label', intro: 'The commands sessions and verification runs of this project run, as you type them here.', docs: { doc: 'check-and-setup-commands', key: 'commands-docs' } }),
      this.#form = form({
        children: [
          fieldRow([
            field('Check command', this.#check, { optional: true }),
            field('Setup command', this.#setup, { optional: true }),
          ], 'halves'),
          fieldRow([
            field('Image for verification runs', this.#image, { optional: true }),
            field('Browser command', this.#browser, { optional: true }),
          ], 'halves'),
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

  set project(name: string) {
    if (name === this.#project) return
    this.#project = name
    this.#dirty = false
    this.#watch()
  }

  #watch() {
    this.#poll?.stop()
    this.#poll = null
    const project = this.#project
    if (!project || !this.isConnected || !this.#built) return
    this.#poll = whileOpen({ root: this, everyMs: CHEAP_EVERY_MS, hold: () => this.#typing(), read: () => request('/api/project-commands', { op: 'read', name: project }), show: a => this.#took(a), near: () => ['commands-save'] })
  }

  #typing(): boolean {
    const a = document.activeElement
    return this.#dirty || this.#saving || (a instanceof HTMLElement && !!this.#form?.contains(a) && a.matches('input, textarea, select'))
  }

  #took(a: EngineAnswer) {
    if (!a.ok) {
      this.#failed = true
      say(this.#said, a.message, 'warn')
      return
    }
    if (this.#failed) say(this.#said, '')
    this.#failed = false
    const body = a.body
    this.#check.value = typeof body.check === 'string' ? body.check : ''
    this.#setup.value = typeof body.setup === 'string' ? body.setup : ''
    this.#image.value = typeof body.image === 'string' ? body.image : ''
    this.#browser.value = typeof body.browser === 'string' ? body.browser : ''
  }

  async #store() {
    const check = this.#check.value
    const setup = this.#setup.value
    const image = this.#image.value
    const browser = this.#browser.value
    say(this.#said, '')
    this.#saving = true
    const a = await whileBusy(this.#save, () => request('/api/project-commands', { op: 'set', name: this.#project, check, setup, image, browser }))
    this.#saving = false
    if (this.#form && !showAnswer(this.#form, a, this.#said, { tone: 'warn' })) return
    this.#failed = false
    this.#dirty = false
    toast({ id: `commands:${this.#project}`, title: a.message, tone: savedTone(a) })
  }
}

customElements.define('sf-project-commands', SfProjectCommands)

declare global {
  interface HTMLElementTagNameMap {
    'sf-project-commands': SfProjectCommands
  }
}
