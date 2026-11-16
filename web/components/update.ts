import { button, el } from '../lib/dom.ts'
import { commandBlock } from './login.ts'

type Post = (path: string, body: unknown) => Promise<Response>

interface Release {
  version: string
  digest: string
  digestProblem: string
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
const DIGEST = /^sha256:[0-9a-f]{64}$/
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
  const digest = typeof r.digest === 'string' && DIGEST.test(r.digest) ? r.digest : ''
  const problem = r.digestProblem === 'missing' || r.digestProblem === 'malformed' ? r.digestProblem : ''
  return { version: r.version, digest, digestProblem: digest ? '' : problem || 'missing' }
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

export function installCommand(version: string, digest: string): string {
  const compose = `ghcr.io/bezmoradi/software-factory-compose@${digest}`
  return `docker compose version --short | awk -F. 'NR==1{v=$0;sub(/^v/,"");ok=(($1+0>5)||($1+0==5&&($2+0>7||($2+0==7&&$3+0>=1))))}END{if(!ok){print "Stopped: this install needs Docker Compose 5.7.1 or later, and docker compose version --short gave: " v;exit 1}}' && cosign verify ${compose} --certificate-identity https://github.com/bezmoradi/software-factory/.github/workflows/release.yml@refs/tags/v${version} --certificate-oidc-issuer https://token.actions.githubusercontent.com && g=\${SF_DOCKER_GID:-0} s=\${SF_DOCKER_SOCKET:-/var/run/docker.sock} && { case "$s" in /*) [ "$(printf '%s.' "$g" | tr -d 0-9)" = . ] && [ "$(printf '%s:' "$s" | tr -d A-Za-z0-9._/-)" = : ];; *) false;; esac || { echo "Stopped: SF_DOCKER_GID must be digits, and SF_DOCKER_SOCKET an absolute path of letters, digits and . _ / -"; false; }; } && printf 'services:\\n  helper:\\n    group_add: ["%s"]\\n    volumes:\\n      - "%s:/var/run/docker.sock"\\n' "$g" "$s" > software-factory-socket.yaml && docker compose -p software-factory -f oci://${compose} -f software-factory-socket.yaml up -d --wait --remove-orphans -y`
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

function releaseSteps(r: Release): HTMLElement {
  const box = el('section', 'update-steps')
  const tag = `v${r.version}`
  box.append(el('h3', 'label', 'If you installed a release'))
  if (r.digest) {
    box.append(
      para('small', 'In a terminal, go to the folder that holds ', code('software-factory-socket.yaml'), ', the one you installed from, and run this command there, on one line. On Linux, first run ', code('export SF_DOCKER_GID=$(stat -c %g /var/run/docker.sock)'), '. It needs Docker Compose 5.7.1 or later and cosign.'),
      commandBlock(installCommand(r.version, r.digest)),
      para('small muted', code('cosign verify'), ' checks that the Compose file was signed by the release workflow for the tag ', code(tag), ', and the rest runs only if it was. The version comes from the repository’s tags, which only the owner can create, and the digest from the release’s notes.'),
    )
    return box
  }
  const why = r.digestProblem === 'malformed' ? 'The release’s notes name a Compose digest the engine doesn’t accept as one (a sha256: digest of 64 lowercase hex digits), so the page doesn’t fill in the command.' : 'The release’s notes carry no Compose digest, so the page can’t fill in the command.'
  box.append(
    para('small warn', why),
    para('small', 'Take the install command from the ', link(`${REPO}/blob/${tag}/README.md#run-it-with-docker`, `README at ${tag}`), ', with ', code(r.version), ' as the version and the digest of ', code('ghcr.io/bezmoradi/software-factory-compose'), ' from the ', link(`${REPO}/releases/tag/${tag}`, 'release’s notes'), ', and run it in the folder that holds ', code('software-factory-socket.yaml'), '.'),
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
    el('h3', 'label', development ? 'This engine runs a development image, built from source' : 'If you built it from source'),
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
  box.append(el('h3', 'label', 'What an update keeps and stops'), list)
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
  const heading = el('h2', 'label', title)
  heading.tabIndex = -1
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
  const steps = development ? [sourceSteps(true), releaseSteps(r)] : [releaseSteps(r), sourceSteps(false)]
  d.append(
    heading,
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
    const b = button(text, 'primary small update-open', () => openUpdate())
    b.setAttribute('aria-haspopup', 'dialog')
    b.title = `Software Factory ${r.version} is out. Shows how to update; nothing updates by itself.`
    this.replaceChildren(b)
    this.hidden = false
  }
}

class SfUpdateSetting extends HTMLElement {
  #box = el('input')
  #note = el('p', 'small muted')
  #said = el('p', 'small warn')

  connectedCallback() {
    painted.add(this)
    if (!this.childElementCount) {
      this.#box.type = 'checkbox'
      this.#box.addEventListener('change', () => void this.#change())
      this.#said.setAttribute('role', 'status')
      const label = el('label', 'notify-pref small')
      label.append(this.#box, el('span', '', 'Check for updates'))
      const set = el('fieldset', 'themes')
      set.append(el('legend', 'label', 'Updates'), label, this.#note, this.#said)
      this.append(set)
    }
    this.paint()
  }

  disconnectedCallback() {
    painted.delete(this)
  }

  async #change() {
    const on = this.#box.checked
    this.#box.disabled = true
    const a = await call({ check: on })
    this.#said.textContent = a.ok ? '' : a.message
    this.paint()
  }

  paint() {
    const v = view
    this.#box.checked = !!v?.check
    this.#box.disabled = !v || v.forced || v.unreadable
    if (!v) this.#note.textContent = 'Shown once the page is connected to the engine.'
    else if (v.forced) this.#note.textContent = 'Off: the engine was started with SF_NO_UPDATE_CHECK set.'
    else if (v.unreadable) this.#note.textContent = 'Off: the engine can’t read its update setting (update.json in its state).'
    else this.#note.textContent = 'At start and once a day the engine asks api.github.com for the newest release, from your computer, with no token or cookie. A newer one shows Update in the top bar.'
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
