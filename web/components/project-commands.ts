import { api } from '../api.ts'
import { button, el, field, focusedKey, refocus, say, statusLine, whileBusy } from '../lib/dom.ts'

const MAX = 300

let serial = 0

async function problemOf(res: Response | null): Promise<string> {
  if (!res) return 'The engine didn\'t answer. Try again.'
  const body = await res.json().catch(() => null) as { message?: unknown } | null
  return typeof body?.message === 'string' && body.message ? body.message : 'The engine refused the change. Try again.'
}

function bytes(text: string): number {
  return new TextEncoder().encode(text).length
}

class SfProjectCommands extends HTMLElement {
  #project = ''
  #check = el('input', 'textbox')
  #setup = el('input', 'textbox')
  #save = button('Save commands', 'small', () => void this.#store(), 'button', 'commands-save')
  #said = statusLine()
  #again = button('Read again', 'link small', () => {
    if (this.#reading) return
    say(this.#said, '')
    void whileBusy(this.#again, () => this.#load())
  }, 'button', 'commands-again')
  #reading = 0
  #loaded = ''
  #built = false

  connectedCallback() {
    if (this.#built) {
      if (this.#project && this.#loaded !== this.#project) void this.#load()
      return
    }
    this.#built = true
    const n = ++serial
    for (const [input, id, holder] of [[this.#check, `check-command-${n}`, 'make check'], [this.#setup, `setup-command-${n}`, 'npm ci']] as const) {
      input.id = id
      input.placeholder = holder
      input.spellcheck = false
      input.autocomplete = 'off'
      input.maxLength = MAX
    }
    this.replaceChildren(
      el('h3', 'label', 'Check and setup commands'),
      el('p', 'small muted', 'Both run in the agent container, in the session\'s own worktree, as the agents\' user, with the model keys cleared. They are what you type here, never anything read from the repository. Empty is off.'),
      field('Check command', this.#check, 'Runs when the agent ends a turn, for at most 5 minutes. When it fails, Claude Code gets the end of its output and one more try in the same turn; Codex gets it as a note with your next message. The result shows on the session and never counts for a review or a push.'),
      field('Setup command', this.#setup, 'Runs once in each new session\'s worktree, for at most 15 minutes, after you approve the start-up card, never before it and never when it is refused. Messages you send while it runs wait in the session\'s queue. Its output shows on the session.'),
      this.#save,
      this.#said,
    )
    if (this.#project && this.#loaded !== this.#project) void this.#load()
  }

  set project(name: string) {
    if (name === this.#project) return
    this.#project = name
    this.#loaded = ''
    if (this.isConnected) void this.#load()
  }

  async #load() {
    const project = this.#project
    if (!project) return
    this.#reading++
    this.#again.setAttribute('aria-disabled', 'true')
    try {
      const res = await api('/api/project-commands', { op: 'read', name: project }).catch(() => null)
      if (project !== this.#project) return
      if (!res || !res.ok) {
        say(this.#said, await problemOf(res), 'warn')
        return this.#retry(true)
      }
      const body = await res.json().catch(() => null) as { check?: unknown; setup?: unknown } | null
      this.#check.value = typeof body?.check === 'string' ? body.check : ''
      this.#setup.value = typeof body?.setup === 'string' ? body.setup : ''
      this.#loaded = project
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
    refocus(this, focused, () => ['commands-save'])
  }

  async #store() {
    const check = this.#check.value.trim()
    const setup = this.#setup.value.trim()
    if (bytes(check) > MAX || bytes(setup) > MAX || /[\r\n]/.test(check + setup)) {
      say(this.#said, `Each command is one line of at most ${MAX} bytes.`, 'warn')
      return
    }
    const res = await whileBusy(this.#save, () => api('/api/project-commands', { op: 'set', name: this.#project, check, setup }).catch(() => null))
    if (!res || !res.ok) {
      say(this.#said, await problemOf(res), 'warn')
      return
    }
    say(this.#said, 'Saved. New turns and new sessions use them.', 'muted')
  }
}

customElements.define('sf-project-commands', SfProjectCommands)

declare global {
  interface HTMLElementTagNameMap {
    'sf-project-commands': SfProjectCommands
  }
}
