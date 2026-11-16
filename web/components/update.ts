import { button, el } from '../lib/dom.ts'
import { icon } from './icon.ts'
import { commandBlock } from './login.ts'

type Post = (path: string, body: unknown) => Promise<Response>

interface Release {
  version: string
}

export interface UpdateView {
  running: string
  check: boolean
  forced: boolean
  unreadable: boolean
  development: boolean
  result: string
  update: Release | null
}

const ROUTE = '/api/update'
const REPO = 'https://github.com/bezmoradi/software-factory'
const VERSION = /^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$/
const INSTALL_COMMAND = 'curl -fsSL https://github.com/bezmoradi/software-factory/releases/latest/download/install.sh | sh'
const KIND = /^[a-z][a-z-]{0,40}$/
const RELOAD_MS = 60 * 60 * 1000
const SOON_MS = 15_000
const SOON_TRIES = 8

let post: Post = () => Promise.reject(new Error('not ready'))
let view: UpdateView | null = null
let timer = 0
let soon = 0
let tries = 0
const painted = new Set<{ paint(): void }>()

function releaseOf(v: unknown): Release | null {
  if (v === null || typeof v !== 'object') return null
  const r = v as Record<string, unknown>
  if (typeof r.version !== 'string' || !VERSION.test(r.version)) return null
  return { version: r.version }
}

export function updateOf(v: unknown): UpdateView | null {
  if (v === null || typeof v !== 'object') return null
  const r = v as Record<string, unknown>
  if (typeof r.running !== 'string' || typeof r.check !== 'boolean') return null
  return {
    running: VERSION.test(r.running) ? r.running : '',
    check: r.check,
    forced: r.forced === true,
    unreadable: r.unreadable === true,
    development: r.development === true,
    result: typeof r.result === 'string' && KIND.test(r.result) ? r.result : '',
    update: releaseOf(r.update),
  }
}

function settle() {
  clearTimeout(soon)
  if (view?.check && view.result === 'not-yet' && tries < SOON_TRIES) {
    tries++
    soon = window.setTimeout(() => void loadUpdate(), SOON_MS)
    return
  }
  tries = 0
}

function paintAll() {
  for (const node of painted) node.paint()
}

async function call(body: Record<string, unknown>): Promise<{ ok: boolean; message: string }> {
  const res = await post(ROUTE, body).catch(() => null)
  if (!res) return { ok: false, message: 'The engine didn’t answer.' }
  const parsed: unknown = await res.json().catch(() => null)
  const next = res.ok ? updateOf(parsed) : null
  if (next) {
    view = next
    paintAll()
    settle()
    return { ok: true, message: '' }
  }
  const message = parsed !== null && typeof parsed === 'object' && typeof (parsed as { message?: unknown }).message === 'string' ? (parsed as { message: string }).message : ''
  return { ok: false, message: message || 'The engine didn’t take that. Try again once the page is connected.' }
}

export function initUpdates(p: Post): void {
  post = p
  clearInterval(timer)
  timer = window.setInterval(() => void loadUpdate(), RELOAD_MS)
}

export async function loadUpdate(): Promise<void> {
  await call({})
}

function link(href: string, text: string): HTMLAnchorElement {
  const a = el('a', '', text)
  a.href = href
  a.target = '_blank'
  a.rel = 'noopener noreferrer'
  return a
}

function para(cls: string, ...parts: (string | Node)[]): HTMLParagraphElement {
  const p = el('p', cls)
  p.append(...parts)
  return p
}

function code(text: string): HTMLElement {
  return el('code', '', text)
}

function oneLine(command: string, name: string): HTMLElement {
  const block = commandBlock(command)
  block.classList.add('update-oneline')
  const text = block.querySelector('code')
  if (!text) return block
  const scroll = el('div', 'update-scroll')
  scroll.tabIndex = 0
  scroll.setAttribute('role', 'region')
  scroll.setAttribute('aria-label', name)
  text.replaceWith(scroll)
  scroll.append(text)
  return block
}

function releaseSteps(): HTMLElement {
  const box = el('section', 'update-steps')
  box.append(
    el('h3', 'update-subhead', 'If you installed a release'),
    para('small', 'Run the install command again in a terminal. It installs the newest release, which may be newer than the one named here:'),
    oneLine(INSTALL_COMMAND, 'The install command'),
    para('small muted', 'It replaces the containers of the project software-factory and keeps its volumes. If you installed with SF_HOME, SF_DOCKER_GID, SF_DOCKER_SOCKET or SF_PORT set, set them the same way again.'),
  )
  return box
}

function sourceSteps(development: boolean): HTMLElement {
  const box = el('section', 'update-steps')
  const steps = el('ol', 'small update-list')
  const pull = el('li')
  pull.append(para('', 'Get the new code:'), commandBlock('git pull'))
  const up = el('li')
  up.append(para('', 'Rebuild and restart the stack:'), commandBlock('make up'))
  steps.append(pull, up)
  box.append(
    el('h3', 'update-subhead', development ? 'This engine runs a development image, built from source' : 'If you built it from source'),
    para('small', 'In your clone of the repository. Stop ', code('make dev'), ' first if it runs, and read what the pull brings before you build it.'),
    steps,
  )
  return box
}

function keepsAndStops(): HTMLElement {
  const box = el('section', 'update-steps')
  const list = el('ul', 'small update-list')
  for (const text of [
    'Running sessions stop when the containers are recreated. Each one comes back Stopped and wakes in the new version when you press Wake, with its conversation.',
    'Projects, git host tokens, logins, reviews, settings and the audit stay in their volumes, since the project name stays software-factory.',
    'The engine installs the agent versions the new release pins when it starts. The open page reloads itself and signs in again once the new engine is up.',
  ]) list.append(el('li', '', text))
  box.append(el('h3', 'update-subhead', 'What an update keeps and stops'), list)
  return box
}

export function openUpdate(): void {
  const r = view?.update
  if (!view || !r) return
  const running = view.running
  const development = view.development
  const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
  const d = el('dialog', 'consent update surface')
  const title = `Update to Software Factory ${r.version}`
  d.setAttribute('aria-label', title)
  const close = () => {
    d.close()
    d.remove()
    const back = opener?.isConnected ? opener : document.getElementById('palette-open')
    back?.focus()
  }
  const heading = el('h2', 'update-title', title)
  heading.tabIndex = -1
  const cross = button('', 'ghost icon-button update-x', close)
  cross.setAttribute('aria-label', 'Close')
  cross.title = 'Close'
  cross.append(icon('close'))
  const head = el('div', 'update-head')
  head.append(heading, cross)
  const said = el('p', 'small warn')
  said.setAttribute('role', 'status')
  const later = button('Remind me later', 'ghost small', () => {
    later.disabled = true
    void call({ later: r.version }).then(a => {
      later.disabled = false
      if (a.ok) return close()
      said.textContent = a.message
    })
  })
  later.title = `Hides this until a release newer than ${r.version} is out`
  const actions = el('div', 'row consent-actions')
  actions.append(later, el('span', 'spacer'), button('Close', 'primary small', close))
  const tag = `v${r.version}`
  const steps = development ? [sourceSteps(true), releaseSteps()] : [releaseSteps(), sourceSteps(false)]
  d.append(
    head,
    para('small', running ? `You run ${running}. ` : '', `Release ${r.version} is out: `, link(`${REPO}/releases/tag/${tag}`, `${tag} on GitHub`), '.'),
    para('small muted', 'Software Factory never updates itself. Nothing changes until you run the steps below yourself.'),
    ...steps,
    keepsAndStops(),
    said,
    actions,
  )
  d.addEventListener('cancel', e => {
    e.preventDefault()
    close()
  })
  document.body.append(d)
  d.showModal()
  heading.focus()
}

class SfUpdate extends HTMLElement {
  connectedCallback() {
    painted.add(this)
    this.paint()
  }

  disconnectedCallback() {
    painted.delete(this)
  }

  paint() {
    const r = view?.update
    if (!r) {
      this.hidden = true
      this.replaceChildren()
      return
    }
    const text = `Update to ${r.version}`
    const existing = this.querySelector('button')
    if (existing?.textContent === text) {
      this.hidden = false
      return
    }
    const b = button('', 'primary small update-open', () => openUpdate())
    const badge = el('span', 'update-badge')
    badge.setAttribute('aria-hidden', 'true')
    b.append(icon('download', 'icon update-icon'), el('span', 'update-open-text', text), badge)
    b.setAttribute('aria-haspopup', 'dialog')
    b.title = `Software Factory ${r.version} is out. Shows how to update; nothing updates by itself.`
    this.replaceChildren(b)
    this.hidden = false
  }
}

class SfUpdateSetting extends HTMLElement {
  #box = el('input')
  #label = el('label', 'notify-pref small')
  #note = el('p', 'small muted')
  #off = el('span', 'update-off', '(off)')
  #said = el('p', 'small warn')
  #busy = false

  connectedCallback() {
    painted.add(this)
    if (!this.childElementCount) {
      this.#box.type = 'checkbox'
      this.#note.id = 'update-setting-note'
      this.#box.setAttribute('aria-describedby', this.#note.id)
      this.#box.addEventListener('click', e => {
        if (this.#busy || this.#box.getAttribute('aria-disabled') === 'true') e.preventDefault()
      })
      this.#box.addEventListener('change', () => void this.#change())
      this.#said.setAttribute('role', 'status')
      this.#off.setAttribute('aria-hidden', 'true')
      this.#label.append(this.#box, el('span', '', 'Check for updates'), this.#off)
      const set = el('fieldset', 'themes')
      set.append(el('legend', 'label', 'Updates'), this.#label, this.#note, this.#said)
      this.append(set)
    }
    this.paint()
  }

  disconnectedCallback() {
    painted.delete(this)
  }

  async #change() {
    const on = this.#box.checked
    this.#busy = true
    this.#box.setAttribute('aria-busy', 'true')
    const a = await call({ check: on })
    this.#busy = false
    this.#box.removeAttribute('aria-busy')
    this.#said.textContent = a.ok ? '' : a.message
    this.paint()
  }

  paint() {
    const v = view
    const locked = !v || v.forced || v.unreadable
    if (!this.#busy) this.#box.checked = !!v?.check
    if (locked) this.#box.setAttribute('aria-disabled', 'true')
    else this.#box.removeAttribute('aria-disabled')
    this.#label.classList.toggle('is-off', locked)
    this.#off.hidden = !v || !locked
    this.#note.classList.toggle('muted', !locked)
    if (!v) this.#note.textContent = 'Shown once the page is connected to the engine.'
    else if (v.forced) this.#note.textContent = 'Off: the engine was started with SF_NO_UPDATE_CHECK set, so this can’t be turned on here.'
    else if (v.unreadable) this.#note.textContent = 'Off: the engine can’t read its update setting (update.json in its state).'
    else this.#note.textContent = 'Once a day the engine asks api.github.com for the newest release, from your computer, with no token or cookie. A newer one shows Update in the top bar.'
  }
}

customElements.define('sf-update', SfUpdate)
customElements.define('sf-update-setting', SfUpdateSetting)

declare global {
  interface HTMLElementTagNameMap {
    'sf-update': SfUpdate
    'sf-update-setting': SfUpdateSetting
  }
}
