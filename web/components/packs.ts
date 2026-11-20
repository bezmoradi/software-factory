import { api } from '../api.ts'
import { busy, button, el, emit, field, focusedKey, refocus, replaceKeeping, say, statusLine, visible } from '../lib/dom.ts'
import { shortSha } from '../lib/format.ts'

interface PackSource {
  kind: string
  url: string
  commit: string
}

interface PackView {
  digest: string
  sessionDigest: string
  name: string
  version: string
  source: PackSource
  roles: Record<string, string>
  steps: Record<string, string>
  documents: boolean
  agents: string[]
  codeFiles: string[]
  usedBy: string[]
}

interface PacksView {
  using: string
  knackpack: { version?: string; commit?: string }
  packs: PackView[]
  max: number
  roleNames: Record<string, string>
}

export interface PacksTarget {
  project: string
  disabled: boolean
}

const NONE = 'none'
const KNACKPACK = 'knackpack'
const MANIFEST = 'software-factory-pack.json'
const MAX_FILES = 200
const MAX_FILE = 256 * 1024
const MAX_UPLOAD = 2 * 1024 * 1024

function strings(x: unknown): string[] {
  return Array.isArray(x) ? x.filter((s): s is string => typeof s === 'string') : []
}

function record(x: unknown): Record<string, string> {
  if (!x || typeof x !== 'object' || Array.isArray(x)) return {}
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(x)) if (typeof v === 'string') out[k] = v
  return out
}

function readPack(x: unknown): PackView | null {
  if (!x || typeof x !== 'object') return null
  const p = x as Partial<PackView>
  const src = p.source && typeof p.source === 'object' ? p.source as Partial<PackSource> : null
  if (typeof p.digest !== 'string' || typeof p.name !== 'string' || typeof p.version !== 'string' || !src) return null
  return {
    digest: p.digest, sessionDigest: typeof p.sessionDigest === 'string' ? p.sessionDigest : '', name: p.name, version: p.version,
    source: { kind: String(src.kind ?? ''), url: String(src.url ?? ''), commit: String(src.commit ?? '') },
    roles: record(p.roles), steps: record(p.steps), documents: p.documents === true, agents: strings(p.agents), codeFiles: strings(p.codeFiles), usedBy: strings(p.usedBy),
  }
}

function readView(body: unknown): PacksView | null {
  if (!body || typeof body !== 'object') return null
  const v = body as Partial<PacksView>
  if (typeof v.using !== 'string' || !Array.isArray(v.packs)) return null
  const k = v.knackpack && typeof v.knackpack === 'object' ? v.knackpack : {}
  return { using: v.using, knackpack: { version: typeof k.version === 'string' ? k.version : '', commit: typeof k.commit === 'string' ? k.commit : '' }, packs: v.packs.map(readPack).filter((p): p is PackView => !!p), max: Number(v.max) || 0, roleNames: record(v.roleNames) }
}

async function problemOf(res: Response | null): Promise<string> {
  if (!res) return 'The engine didn\'t answer. Try again.'
  const body = await res.json().catch(() => null) as { message?: unknown } | null
  return typeof body?.message === 'string' && body.message ? body.message : 'The engine refused the change. Try again.'
}

function digestText(d: string): string {
  return visible(d.slice(0, 12))
}

class SfPacks extends HTMLElement {
  #target: PacksTarget | null = null
  #view: PacksView | null = null
  #preview: PackView | null = null
  #busy = ''
  #confirmNone = false
  #url = el('input', 'textbox')
  #commit = el('input', 'textbox')
  #folder = el('input')
  #status = statusLine()
  #key = ''

  constructor() {
    super()
    this.#url.type = 'url'
    this.#url.id = 'pack-url'
    this.#url.dataset.key = 'pack-url'
    this.#url.autocomplete = 'off'
    this.#url.spellcheck = false
    this.#url.placeholder = 'https://github.com/owner/pack.git'
    this.#commit.id = 'pack-commit'
    this.#commit.dataset.key = 'pack-commit'
    this.#commit.autocomplete = 'off'
    this.#commit.spellcheck = false
    this.#commit.maxLength = 40
    this.#commit.placeholder = '40-character commit hash'
    this.#folder.type = 'file'
    this.#folder.id = 'pack-folder'
    this.#folder.dataset.key = 'pack-folder'
    this.#folder.webkitdirectory = true
    this.#folder.multiple = true
    this.#folder.setAttribute('aria-describedby', 'pack-folder-help')
    this.#folder.addEventListener('click', e => {
      if (this.#busy) e.preventDefault()
    })
    this.#folder.addEventListener('change', () => void this.#upload())
    for (const input of [this.#url, this.#commit]) input.addEventListener('input', () => {
      if (!this.#preview) return
      this.#preview = null
      this.#paint()
    })
  }

  set view(value: PacksTarget | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    const changed = value?.project !== this.#target?.project
    this.#target = value
    this.hidden = !value?.project
    if (!value?.project) {
      this.#view = null
      this.replaceChildren()
      return
    }
    if (changed) {
      this.#view = null
      this.#preview = null
      this.#confirmNone = false
      this.#status.textContent = ''
      void this.#call('list', { op: 'list' })
      return
    }
    this.#paint()
  }

  async #post(what: string, body: Record<string, unknown>): Promise<unknown | null> {
    const project = this.#target?.project
    if (!project || this.#busy) return null
    this.#busy = what
    this.#paint()
    const res = await api('/api/packs', { ...body, path: project }).catch(() => null)
    this.#busy = ''
    if (project !== this.#target?.project) return null
    if (!res || !res.ok) {
      say(this.#status, await problemOf(res), 'warn')
      this.#paint()
      return null
    }
    return await res.json().catch(() => null)
  }

  async #call(what: string, body: Record<string, unknown>): Promise<boolean> {
    const got = await this.#post(what, body)
    const v = readView(got)
    if (v) this.#view = v
    this.#paint()
    return !!v
  }

  async #fetch() {
    const url = this.#url.value.trim()
    const commit = this.#commit.value.trim().toLowerCase()
    if (!url || !/^[0-9a-f]{40}$/.test(commit)) {
      say(this.#status, 'Give the pack\'s https address and the full 40-character commit to pin it at.', 'warn')
      return this.#paint()
    }
    const got = await this.#post('fetch', { op: 'fetch', url, commit }) as { pack?: unknown } | null
    const p = readPack(got?.pack)
    this.#preview = p
    if (p) say(this.#status, `Fetched ${visible(p.name)} ${visible(p.version)}. Read what it holds below before you use it.`, 'ok')
    this.#paint()
  }

  async #upload() {
    const picked = [...(this.#folder.files ?? [])]
    this.#folder.value = ''
    if (!picked.length || this.#busy) return
    const files: { path: string; text: string }[] = []
    let left = 0
    let total = 0
    for (const f of picked) {
      const path = f.webkitRelativePath.split('/').slice(1).join('/')
      if (path !== MANIFEST && !/^(agents|skills|commands)\//.test(path)) {
        left++
        continue
      }
      if (f.size > MAX_FILE || files.length >= MAX_FILES || total + f.size > MAX_UPLOAD) {
        say(this.#status, `The page didn't send the folder: ${visible(path)} would take it past ${MAX_FILES} files, ${MAX_FILE / 1024} KiB a file or ${MAX_UPLOAD / 1048576} MiB in all.`, 'warn')
        return this.#paint()
      }
      const text = await f.text().catch(() => null)
      if (text === null) {
        say(this.#status, `The page couldn't read ${visible(path)}.`, 'warn')
        return this.#paint()
      }
      total += f.size
      files.push({ path, text })
    }
    const manifest = files.find(f => f.path === MANIFEST)
    let name = ''
    try {
      const m = manifest ? JSON.parse(manifest.text) as { name?: unknown } : null
      name = typeof m?.name === 'string' ? m.name : ''
    } catch {
      name = ''
    }
    if (!name) {
      say(this.#status, `The folder has no ${MANIFEST} naming the pack at its top.`, 'warn')
      return this.#paint()
    }
    if (await this.#call('upload', { op: 'upload', name, files })) {
      const p = this.#view?.packs.find(k => k.name === name && k.source.kind === 'upload')
      say(this.#status, `Uploaded ${visible(name)}${p ? ` ${visible(p.version)}` : ''}${left ? `, leaving out ${left} file${left === 1 ? '' : 's'} outside agents/, skills/ and commands/` : ''}. Read what it holds below, then press Use.`, 'ok')
      this.#paint()
    }
  }

  async #useFetched() {
    const p = this.#preview
    if (!p) return
    if (!this.#view?.packs.some(k => k.digest === p.digest) && !await this.#call('add', { op: 'add', url: p.source.url, commit: p.source.commit, digest: p.digest })) return
    await this.#use(p.digest, `${p.name} ${p.version}`)
    this.#preview = null
    this.#paint()
  }

  async #use(pack: string, name: string) {
    if (!await this.#call(`use:${pack}`, { op: 'use', pack })) return
    this.#confirmNone = false
    say(this.#status, pack === NONE ? 'No pack reviews this project now. Each push needs your Allow with a reason.' : `${visible(name)} reviews this project's pushes from the next review on.`, pack === NONE ? 'warn' : 'ok')
    emit(this, 'sf-packs-changed')
    this.#paint()
  }

  async #remove(p: PackView) {
    if (await this.#call(`remove:${p.digest}`, { op: 'remove', digest: p.digest })) {
      say(this.#status, `Removed ${visible(p.name)} ${visible(p.version)} from the engine.`, 'ok')
      this.#paint()
    }
  }

  #button(text: string, what: string, run: () => void, cls = 'ghost small'): HTMLButtonElement {
    const b = button(text, cls, run, 'button', what)
    if (this.#busy === what) busy(b, true)
    else b.disabled = !!this.#target?.disabled || !!this.#busy
    return b
  }

  #details(p: PackView, roleNames: Record<string, string>): HTMLElement {
    const box = el('div', 'small')
    const roles = Object.keys(roleNames).map(r => `${roleNames[r] ?? r}: ${p.roles[r] ? visible(`${p.name}:${p.roles[r]}`) : 'not filled, so a push that needs it waits for your Allow with a reason'}`)
    box.append(
      el('p', 'muted', `${p.source.kind === 'upload' ? 'Uploaded on this page' : `From ${visible(p.source.url)} at commit ${shortSha(p.source.commit)}`}. Reviewers' digest ${digestText(p.digest)}, session files' digest ${digestText(p.sessionDigest)}.`),
      el('p', '', roles.join(' · ')),
      el('p', 'muted', p.agents.length ? `Reviewer agents: ${p.agents.map(visible).join(', ')}` : 'It has no reviewer agents.'),
      el('p', 'muted', p.codeFiles.length ? `Session files that run code, never copied for reviewers: ${p.codeFiles.map(visible).join(', ')}` : 'No session file runs code.'),
    )
    const steps = Object.entries(p.steps).map(([step, cmd]) => `${visible(step)}: ${visible(cmd)}`)
    if (steps.length) box.append(el('p', 'muted', `Steps: ${steps.join(' · ')}`))
    if (!p.documents) box.append(el('p', 'muted', 'It names no documents folder, so its sessions offer no Feature: the engine can\'t approve requirements or a plan it can\'t read from a commit.'))
    return box
  }

  #current(v: PacksView): string {
    if (v.using === NONE) return 'No pack: nothing reviews this project\'s pushes, so each needs your Allow with a reason.'
    if (v.using === KNACKPACK) return `knackpack${v.knackpack.version ? ` ${visible(v.knackpack.version)}` : ''}${v.knackpack.commit ? ` at ${shortSha(v.knackpack.commit)}` : ''}, which follows its main branch.`
    const p = v.packs.find(k => k.digest === v.using)
    return p ? `${visible(p.name)} ${visible(p.version)}, reviewers' digest ${digestText(p.digest)}.` : 'A pack the engine no longer keeps, so reviews don\'t start. Pick another below.'
  }

  #paint() {
    const target = this.#target
    if (!target?.project) return this.replaceChildren()
    const focused = focusedKey(this)
    const v = this.#view
    const parts: HTMLElement[] = [el('p', 'small', 'Reviewer pack for this project')]
    const help = el('div', 'small muted')
    help.append(
      el('p', '', 'A pack is a Claude Code plugin whose agents fill the bug hunter and simplicity checker roles. The engine copies only its agent files for the reviewers, and keeps their tools read-only, the network closed and the verdict lines it reads. Its reviewers must end each reply with Verdict: pass or Verdict: block.'),
      el('p', '', 'A new session runs the pack\'s plugin, skills and step commands, and keeps them across Stop and Wake even when the project picks another pack. A pack\'s files that run code in a session, such as hooks and scripts, are shown for your approval before the session starts, and again for each new version. knackpack\'s are not: it follows its main branch, a risk the owner accepted. No pack starts sessions with neither.'),
    )
    parts.push(help)
    if (!v) {
      parts.push(this.#status)
      return replaceKeeping(this, parts)
    }
    parts.push(el('p', 'small', `In use: ${this.#current(v)}`))
    const choices = el('div', 'row')
    if (v.using !== KNACKPACK) choices.append(this.#button('Use knackpack', `use:${KNACKPACK}`, () => void this.#use(KNACKPACK, 'knackpack')))
    if (v.using !== NONE) {
      if (this.#confirmNone) {
        const sure = this.#button('No pack: hold every push for my Allow', `use:${NONE}`, () => void this.#use(NONE, 'No pack'), 'danger small')
        sure.setAttribute('aria-describedby', 'pack-none-help')
        const back = this.#button('Keep the pack', 'none-cancel', () => {
          this.#confirmNone = false
          this.#paint()
        })
        const note = el('p', 'small warn', 'Nothing reviews this project\'s pushes, so each needs your Allow with a reason.')
        note.id = 'pack-none-help'
        choices.append(sure, back, note)
      } else {
        choices.append(this.#button('No pack…', 'none-ask', () => {
          this.#confirmNone = true
          this.#paint()
          refocus(this, `use:${NONE}`)
        }))
      }
    }
    parts.push(choices)
    if (v.packs.length) {
      const list = el('ul', 'list')
      for (const p of v.packs) {
        const li = el('li')
        const row = el('div', 'row')
        const what = el('div')
        what.append(el('strong', '', `${visible(p.name)} ${visible(p.version)}`), el('span', 'small muted', p.usedBy.length ? ` reviews ${p.usedBy.map(visible).join(', ')}` : ' reviews no project'))
        what.append(this.#details(p, v.roleNames))
        const actions = el('div', 'row')
        if (v.using !== p.digest) {
          const use = this.#button('Use', `use:${p.digest}`, () => void this.#use(p.digest, `${p.name} ${p.version}`))
          use.setAttribute('aria-label', `Use the pack ${visible(p.name)} ${visible(p.version)} for this project`)
          actions.append(use)
        }
        const remove = this.#button('Remove', `remove:${p.digest}`, () => void this.#remove(p))
        remove.setAttribute('aria-label', `Remove the pack ${visible(p.name)} ${visible(p.version)} from the engine`)
        actions.append(remove)
        row.append(what, actions)
        li.append(row)
        list.append(li)
      }
      parts.push(list)
    }
    const full = v.max > 0 && v.packs.length >= v.max
    this.#url.disabled = this.#commit.disabled = target.disabled
    parts.push(el('p', 'small', 'Use a plugin from git'))
    parts.push(field('Repository address (https)', this.#url, undefined, 'row field-row'), field('Commit', this.#commit, 'A pack is pinned by its commit, never a branch, so it changes only when you pick a new commit.', 'row field-row'))
    const fetchButton = this.#button('Fetch and check', 'fetch', () => void this.#fetch(), 'small')
    if (full) {
      fetchButton.disabled = true
      parts.push(el('p', 'small muted', `The engine keeps ${v.max} packs, its limit. Remove one to add another.`))
    }
    parts.push(fetchButton)
    const folderHelp = el('p', 'small muted', `Or upload a pack: pick a folder holding ${MANIFEST} and the pack's agents/, skills/ and commands/ folders. The page sends those files only, at most ${MAX_FILES}, ${MAX_FILE / 1024} KiB each and ${MAX_UPLOAD / 1048576} MiB in all, as text; the engine writes the plugin's manifest itself, keeps the pack by its digest and refuses hooks, servers and programs.`)
    folderHelp.id = 'pack-folder-help'
    this.#folder.disabled = target.disabled || full
    if (this.#busy) this.#folder.setAttribute('aria-disabled', 'true')
    else this.#folder.removeAttribute('aria-disabled')
    parts.push(folderHelp, field('Upload a pack folder', this.#folder, undefined, 'row field-row'))
    const p = this.#preview
    if (p) {
      const box = el('div', 'small')
      box.append(el('strong', '', `${visible(p.name)} ${visible(p.version)}`), this.#details(p, v.roleNames))
      const use = this.#button('Use this pack', 'add', () => void this.#useFetched(), 'small')
      use.setAttribute('aria-label', `Use the pack ${visible(p.name)} ${visible(p.version)} for this project`)
      box.append(use)
      parts.push(box)
    }
    parts.push(this.#status)
    replaceKeeping(this, parts)
    refocus(this, focused, () => ['pack-url'])
  }
}

customElements.define('sf-packs', SfPacks)

declare global {
  interface HTMLElementTagNameMap {
    'sf-packs': SfPacks
  }
}
