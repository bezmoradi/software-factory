import { api } from '../api.ts'
import { el, visible } from '../lib/dom.ts'
import { label, shortSha } from '../lib/format.ts'
import type { SessionInfo, StartFrom, StartPointsView } from '../types.ts'

const SHA = /^[0-9a-f]{40}$/
let serial = 0

function option(value: string, text: string): HTMLOptionElement {
  const o = el('option', '', text)
  o.value = value
  return o
}

function pointsOf(raw: unknown): StartPointsView | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  const branches = Array.isArray(r.branches) ? r.branches.filter((b): b is { name: string; commit: string } => !!b && typeof b.name === 'string' && typeof b.commit === 'string' && SHA.test(b.commit)) : []
  const pulls = Array.isArray(r.pulls) ? r.pulls.filter((p): p is { number: number; title: string; branch: string; commit: string } => !!p && Number.isInteger(p.number) && p.number > 0 && typeof p.title === 'string' && typeof p.branch === 'string' && typeof p.commit === 'string' && SHA.test(p.commit)) : []
  return { main: typeof r.main === 'string' ? r.main : '', branches, pulls, pullsWhy: typeof r.pullsWhy === 'string' ? r.pullsWhy : '' }
}

export class StartFromField {
  box = el('div', 'field start-from')
  select = el('select', 'textbox')
  said = el('p', 'small muted')
  #loading = option('', 'Loading branches and pull requests…')

  constructor(path: string, main: string, sessions: SessionInfo[], fork: string) {
    const id = `start-from-${++serial}`
    this.select.id = id
    this.select.name = 'from'
    const name = el('label', 'small', 'Start from')
    name.htmlFor = id
    this.select.append(option('', `main${main ? ` (${main})` : ''}, as fetched now`))
    const own = sessions.filter(s => s.verified && s.kind === 'background')
    if (own.length) {
      const group = el('optgroup')
      group.label = 'A session’s latest commit'
      for (const s of own) group.append(option(`sid:${s.sid}`, `Session ${visible(label(s))}${s.lineage?.handedTo ? ' (handed over)' : ''}`))
      this.select.append(group)
    }
    this.#loading.disabled = true
    this.select.append(this.#loading)
    if (fork && own.some(s => s.sid === fork)) this.select.value = `sid:${fork}`
    this.box.append(name, this.select, this.said)
    this.#explain()
    this.select.addEventListener('change', () => this.#explain())
    void this.#load(path)
  }

  #explain() {
    const v = this.select.value
    this.said.textContent = v ? 'The session gets its own new branch, sf/<id>, at that commit. Its reviews compare it against main, as for any session.' : ''
    this.said.hidden = !v
  }

  async #load(path: string) {
    const res = await api('/api/start-points', { path }).catch(() => null)
    const body: unknown = res ? await res.json().catch(() => null) : null
    this.#loading.remove()
    if (!res?.ok) {
      const message = body && typeof body === 'object' && typeof (body as { message?: unknown }).message === 'string' ? visible((body as { message: string }).message) : 'The engine couldn’t list the branches.'
      this.select.append(Object.assign(option('', `Branches and pull requests aren’t listed: ${message}`), { disabled: true }))
      return
    }
    const points = pointsOf(body)
    if (!points) return
    if (points.branches.length) {
      const group = el('optgroup')
      group.label = 'A branch of the repository'
      for (const b of points.branches) group.append(option(`branch:${b.commit}:${b.name}`, `Branch ${visible(b.name)} (${shortSha(b.commit)})`))
      this.select.append(group)
    }
    if (points.pulls.length) {
      const group = el('optgroup')
      group.label = 'An open pull request’s branch'
      for (const p of points.pulls) group.append(option(`pull:${p.commit}:${p.number}`, `#${p.number} ${visible(p.title)}${p.branch ? ` (${visible(p.branch)})` : ''}, at ${shortSha(p.commit)}`))
      this.select.append(group)
    } else if (points.pullsWhy) this.select.append(Object.assign(option('', `No pull requests listed: ${visible(points.pullsWhy)}`), { disabled: true }))
  }

  value(): StartFrom | undefined {
    const v = this.select.value
    if (v.startsWith('sid:')) return { sid: v.slice(4) }
    const at = /^(branch|pull):([0-9a-f]{40}):(.+)$/.exec(v)
    if (!at) return undefined
    const [, kind, commit, rest] = at
    if (kind === 'branch') return { branch: rest, commit }
    const n = Number(rest)
    return Number.isInteger(n) && n > 0 ? { pull: n, commit } : undefined
    return undefined
  }
}
