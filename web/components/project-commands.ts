import { api } from '../api.ts'
import { button, el, field, focusedKey, refocus, say, statusLine, whileBusy } from '../lib/dom.ts'

const MAX = 300
const IMAGE = /^(?:[a-z0-9](?:[a-z0-9.-]{0,252}[a-z0-9])?(?::[0-9]{1,5})?\/)?[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:\/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?@sha256:[0-9a-f]{64}$/

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
  #image = el('input', 'textbox')
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
    for (const [input, id, holder] of [[this.#check, `check-command-${n}`, 'make check'], [this.#setup, `setup-command-${n}`, 'npm ci'], [this.#image, `verify-image-${n}`, 'node@sha256:…']] as const) {
      input.id = id
      input.placeholder = holder
      input.spellcheck = false
      input.autocomplete = 'off'
      input.maxLength = MAX
    }
    this.replaceChildren(
      el('h3', 'label', 'Check and setup commands'),
      el('p', 'small muted', 'Both run in the agent container, in the session\'s own worktree, as the agents\' user, with the model keys cleared. They are what you type here, never anything read from the repository. Empty is off.'),
      field('Check command', this.#check, 'Runs when the agent ends a turn, for at most 5 minutes. When it fails, Claude Code gets the end of its output and one more try in the same turn; the development ACP agent gets it as a note with your next message. The result shows on the session and never counts for a review or a push; a verification run of the same command, set up below, is the one a push can count.'),
      field('Setup command', this.#setup, 'Runs once in each new session\'s worktree, for at most 15 minutes, after you approve the start-up card, never before it and never when it is refused. Messages you send while it runs wait in the session\'s queue. Its output shows on the session.'),
      field('Image for verification runs', this.#image, 'A verification run runs the check command on a clean copy of a commit, in a container of this image with no network, no token and no sign-in; only its exit code counts. Name the image pinned by digest (name@sha256: and 64 hexadecimal digits), with the project\'s tools and dependencies in it, and pull it on the Docker host first. Software Factory ships no such image. The run keeps only the image\'s tooling variables (PATH, LANG, *_HOME, *_VERSION and the like), each only in the shape of its kind; a check command that needs another sets it itself, such as FOO=bar make check. Put no secret in the image, in a variable or a file: the code a run executes was written by an agent, which reads everything in the image, and whether the run passed, and so roughly when it ended, reaches the agent through whether and when its push goes out. Empty means this project can\'t run a verification.'),
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
      const body = await res.json().catch(() => null) as { check?: unknown; setup?: unknown; image?: unknown } | null
      this.#check.value = typeof body?.check === 'string' ? body.check : ''
      this.#setup.value = typeof body?.setup === 'string' ? body.setup : ''
      this.#image.value = typeof body?.image === 'string' ? body.image : ''
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
    const image = this.#image.value.trim()
    if (bytes(check) > MAX || bytes(setup) > MAX || /[\r\n]/.test(check + setup)) {
      say(this.#said, `Each command is one line of at most ${MAX} bytes.`, 'warn')
      return
    }
    if (image && (bytes(image) > MAX || !IMAGE.test(image))) {
      say(this.#said, 'The image is a name pinned by digest, such as node@sha256: and 64 hexadecimal digits.', 'warn')
      return
    }
    const res = await whileBusy(this.#save, () => api('/api/project-commands', { op: 'set', name: this.#project, check, setup, image }).catch(() => null))
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
