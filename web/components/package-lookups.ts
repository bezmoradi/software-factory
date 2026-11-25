import { request, savedTone, UNREAD, type EngineAnswer } from '../api.ts'
import { button, say, statusLine, whileBusy } from '../lib/dom.ts'
import { actions, field, form, showAnswer } from '../lib/form.ts'
import { checkbox } from '../lib/controls.ts'
import { pageHead } from '../lib/page.ts'
import { CHEAP_EVERY_MS, whileOpen, type WhileOpen } from '../lib/refresh.ts'
import { toast } from './toasts.ts'

let serial = 0

function lookupsOf(body: unknown): boolean | null {
  const v = body as { lookups?: unknown } | null
  return v && typeof v.lookups === 'boolean' ? v.lookups : null
}

class SfPackageLookups extends HTMLElement {
  #project = ''
  #built = false
  #on = checkbox({ name: 'lookups' })
  #save = button('Save', 'primary small', undefined, 'submit', 'lookups-save')
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
    this.#on.id = `lookups-${++serial}`
    this.#on.addEventListener('change', () => {
      this.#dirty = true
    })
    this.replaceChildren(
      ...pageHead({ title: 'New packages', level: 'h2', headingClass: 'label', intro: 'Whether the push card checks the packages a push adds on their public registry.', docs: { doc: 'new-packages', key: 'lookups-docs' } }),
      this.#form = form({
        children: [
          field('Look up new packages on npm, PyPI, proxy.golang.org and crates.io', this.#on, { kind: 'check' }),
          actions([this.#save]),
        ],
        onSubmit: () => void this.#store(),
      }),
      this.#said,
    )
    this.#watch()
  }

  set project(value: string) {
    if (value === this.#project) return
    this.#project = value
    this.#dirty = false
    this.#watch()
  }

  #watch() {
    this.#poll?.stop()
    this.#poll = null
    const repo = this.#project
    if (!repo || !this.isConnected || !this.#built) return
    this.#poll = whileOpen({ root: this, everyMs: CHEAP_EVERY_MS, hold: () => this.#dirty || this.#saving, read: () => request('/api/project-facts', { op: 'read-lookups', repo }), show: a => this.#took(a), near: () => ['lookups-save'] })
  }

  #took(a: EngineAnswer) {
    const on = a.ok ? lookupsOf(a.body) : null
    if (on === null) {
      this.#failed = true
      say(this.#said, a.ok ? UNREAD : a.message, 'warn')
      return
    }
    if (this.#failed) say(this.#said, '')
    this.#failed = false
    this.#on.checked = on
  }

  async #store() {
    const on = this.#on.checked
    say(this.#said, '')
    this.#saving = true
    const a = await whileBusy(this.#save, () => request('/api/project-facts', { op: 'lookups', repo: this.#project, on }))
    this.#saving = false
    if (this.#form && !showAnswer(this.#form, a, this.#said, { tone: 'warn' })) return
    this.#failed = false
    this.#dirty = false
    const saved = lookupsOf(a.body)
    if (saved !== null) this.#on.checked = saved
    toast({ id: `package-lookups:${this.#project}`, title: a.message, tone: savedTone(a) })
  }
}

customElements.define('sf-package-lookups', SfPackageLookups)

declare global {
  interface HTMLElementTagNameMap {
    'sf-package-lookups': SfPackageLookups
  }
}
