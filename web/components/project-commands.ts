import { request, savedTone } from '../api.ts'
import { button, el, focusedKey, labelError, refocus, say, statusLine, whileBusy } from '../lib/dom.ts'
import { actions, field, form, showAnswer } from '../lib/form.ts'

let serial = 0


class SfProjectCommands extends HTMLElement {
  #project = ''
  #check = el('input', 'textbox')
  #setup = el('input', 'textbox')
  #image = el('input', 'textbox')
  #browser = el('input', 'textbox')
  #save = button('Save commands', 'small', undefined, 'submit', 'commands-save')
  #said = statusLine()
  #form: HTMLFormElement | null = null
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
    for (const [input, id, name, holder] of [[this.#check, `check-command-${n}`, 'check', 'make check'], [this.#setup, `setup-command-${n}`, 'setup', 'npm ci'], [this.#image, `verify-image-${n}`, 'image', 'node@sha256:…'], [this.#browser, `browser-command-${n}`, 'browser', 'PLAYWRIGHT_BROWSERS_PATH=/ms-playwright npx playwright test']] as const) {
      input.id = id
      input.name = name
      input.placeholder = holder
      input.spellcheck = false
      input.autocomplete = 'off'
      input.addEventListener('input', () => labelError(input, ''))
    }
    this.replaceChildren(
      el('h3', 'label', 'Check and setup commands'),
      el('p', 'small muted', 'Both run in the agent container, in the session\'s own worktree, as the agents\' user, with the model keys cleared. They are what you type here, never anything read from the repository. Empty is off.'),
      this.#form = form({
        children: [
          field('Check command', this.#check, { help: 'Runs when the agent ends a turn, for at most 5 minutes. When it fails, Claude Code gets the end of its output and one more try in the same turn; the development ACP agent gets it as a note with your next message. The result shows on the session and never counts for a review or a push; a verification run of the same command, set up below, is the one a push can count.' }),
          field('Setup command', this.#setup, { help: 'Runs once in each new session\'s worktree, for at most 15 minutes, after you approve the start-up card, never before it and never when it is refused. Messages you send while it runs wait in the session\'s queue. Its output shows on the session.' }),
          field('Image for verification runs', this.#image, { help: 'A verification run runs the check command on a clean copy of a commit, in a container of this image with no network, no token and no sign-in; only its exit code counts. Name the image pinned by digest (name@sha256: and 64 hexadecimal digits), with the project\'s tools and dependencies in it, and pull it on the Docker host first. Software Factory ships no such image. The run keeps only the image\'s tooling variables (PATH, LANG, *_HOME, *_VERSION and the like), each only in the shape of its kind; a check command that needs another sets it itself, such as FOO=bar make check. Put no secret in the image, in a variable or a file: the code a run executes was written by an agent, which reads everything in the image, and whether the run passed, and so roughly when it ended, reaches the agent through whether and when its push goes out. Empty means this project can\'t run a verification.' }),
          field('Browser command', this.#browser, { help: 'A browser check runs this command, never anything read from the repository, the same way as a verification run: on a clean copy of the commit, in its own container of the image above, with no network, so it starts the app under test itself and drives the browser in the image over loopback. Pass or fail is the command\'s own exit code. Files it saves to $SF_OUT (/out) are kept with the run: .png, .jpg, .jpeg and .gif images, re-encoded before the page shows them, and .zip traces, which the page only downloads. The image needs the browser (Chromium at /usr/bin/chromium, or Playwright\'s browsers, with PLAYWRIGHT_BROWSERS_PATH set in the command, since the run keeps no such variable) and a folder /out owned by user 10006. Playwright doesn\'t read CHROME_BIN: point its launchOptions.executablePath at the image\'s Chromium. Sign in as a test user from the repository\'s seed data, never with a real or production login. Empty is off.' }),
          actions([this.#save]),
        ],
        onSubmit: () => void this.#store(),
      }),
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
      const a = await request('/api/project-commands', { op: 'read', name: project })
      if (project !== this.#project) return
      if (!a.ok) {
        say(this.#said, a.message, 'warn')
        return this.#retry(true)
      }
      const body = a.body
      this.#check.value = typeof body.check === 'string' ? body.check : ''
      this.#setup.value = typeof body.setup === 'string' ? body.setup : ''
      this.#image.value = typeof body.image === 'string' ? body.image : ''
      this.#browser.value = typeof body.browser === 'string' ? body.browser : ''
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
    const check = this.#check.value
    const setup = this.#setup.value
    const image = this.#image.value
    const browser = this.#browser.value
    say(this.#said, '')
    const a = await whileBusy(this.#save, () => request('/api/project-commands', { op: 'set', name: this.#project, check, setup, image, browser }))
    if (this.#form && !showAnswer(this.#form, a, this.#said, { tone: 'warn' })) return
    say(this.#said, a.message, savedTone(a))
  }
}

customElements.define('sf-project-commands', SfProjectCommands)

declare global {
  interface HTMLElementTagNameMap {
    'sf-project-commands': SfProjectCommands
  }
}
