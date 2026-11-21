import { actionButton, button, el, statusLine } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { icon } from './icon.ts'
import { commandBlock } from './copy.ts'

type Post = (path: string, body: unknown) => Promise<Response>

interface Release {
  version: string
}

export interface UpdateView {
  running: string
  forced: boolean
  unreadable: boolean
  development: boolean
  result: string
  update: Release | null
}

const ROUTE = '/api/update'
const REPO = 'https://github.com/bezmoradi/software-factory'
const VERSION = /^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$/
const SIGNER = '-R bezmoradi/software-factory --signer-workflow bezmoradi/software-factory/.github/workflows/release.yml'
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
  if (typeof r.running !== 'string') return null
  return {
    running: VERSION.test(r.running) ? r.running : '',
    forced: r.forced === true,
    unreadable: r.unreadable === true,
    development: r.development === true,
    result: typeof r.result === 'string' && KIND.test(r.result) ? r.result : '',
    update: releaseOf(r.update),
  }
}

function settle() {
  clearTimeout(soon)
  if (view && !view.forced && !view.unreadable && view.result === 'not-yet' && tries < SOON_TRIES) {
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

function oneLine(command: string, what: string): HTMLElement {
  const block = commandBlock(command, `Copy the ${what}`)
  block.classList.add('update-oneline')
  const text = block.querySelector('code')
  if (!text) return block
  const scroll = el('div', 'update-scroll')
  scroll.tabIndex = 0
  scroll.setAttribute('role', 'region')
  scroll.setAttribute('aria-label', `The ${what}`)
  text.replaceWith(scroll)
  scroll.append(text)
  return block
}

function releaseSteps(version: string): HTMLElement {
  const tag = `v${version}`
  const script = `${REPO}/releases/download/${tag}/install.sh`
  const box = el('section', 'update-steps')
  const steps = el('ol', 'small update-list')
  const download = el('li')
  download.append(para('', 'Download it:'), oneLine(`curl -fsSLO ${script}`, 'download command'))
  const verify = el('li')
  verify.append(para('', `Check that this repository’s release workflow built it for ${tag}:`), oneLine(`gh attestation verify install.sh ${SIGNER} --source-ref refs/tags/${tag}`, 'check command'))
  const run = el('li')
  run.append(para('', 'Run it:'), commandBlock('sh install.sh', 'Copy the run command'))
  steps.append(download, verify, run)
  box.append(
    el('h3', 'update-subhead', 'If you installed a release'),
    para('small', `Run release ${version}’s install command in a terminal:`),
    oneLine(`curl -fsSL ${script} | sh`, 'install command'),
    para('small', 'Or check the script before it runs, with a signed-in gh:'),
    steps,
    para('small muted', 'Either way, the script replaces the containers of the project software-factory and keeps its volumes. If you installed with SF_HOME, SF_DOCKER_GID, SF_DOCKER_SOCKET or SF_PORT set, set them the same way again.'),
  )
  return box
}

function sourceSteps(development: boolean): HTMLElement {
  const box = el('section', 'update-steps')
  const steps = el('ol', 'small update-list')
  const pull = el('li')
  pull.append(para('', 'Get the new code:'), commandBlock('git pull', 'Copy the pull command'))
  const up = el('li')
  up.append(para('', 'Rebuild and restart the stack:'), commandBlock('make up', 'Copy the rebuild command'))
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
  const title = `Update to Software Factory ${r.version}`
  const said = statusLine('small warn')
  const later = actionButton('Remind me later', 'ghost small', async () => {
    const a = await call({ later: r.version })
    if (a.ok) return m.close()
    said.textContent = a.message
  })
  later.title = `Hides this until a release newer than ${r.version} is out`
  const tag = `v${r.version}`
  const steps = development ? [sourceSteps(true), releaseSteps(r.version)] : [releaseSteps(r.version), sourceSteps(false)]
  const m = modal({
    kind: 'update',
    title,
    closeButton: true,
    body: [
      para('small', running ? `You run ${running}. ` : '', `Release ${r.version} is out: `, link(`${REPO}/releases/tag/${tag}`, `${tag} on GitHub`), '.'),
      para('small muted', 'Software Factory never updates itself. Nothing changes until you run the steps below yourself.'),
      ...steps,
      keepsAndStops(),
      said,
    ],
    lead: [later],
    actions: [button('Close', 'primary small', () => m.close())],
    focus: 'heading',
  })
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

customElements.define('sf-update', SfUpdate)

declare global {
  interface HTMLElementTagNameMap {
    'sf-update': SfUpdate
  }
}
