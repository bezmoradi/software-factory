import { request } from '../api.ts'
import { labelError, visible } from '../lib/dom.ts'
import { field } from '../lib/form.ts'
import { label, shortSha } from '../lib/format.ts'
import type { SessionInfo, StartFrom, StartPointsView } from '../types.ts'
import { dropdown, option } from '../lib/controls.ts'

const SHA = /^[0-9a-f]{40}$/
let serial = 0

function pointsOf(raw: unknown): StartPointsView | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  const branches = Array.isArray(r.branches) ? r.branches.filter((b): b is { name: string; commit: string } => !!b && typeof b.name === 'string' && typeof b.commit === 'string' && SHA.test(b.commit)) : []
  const pulls = Array.isArray(r.pulls) ? r.pulls.filter((p): p is { number: number; title: string; branch: string; commit: string } => !!p && Number.isInteger(p.number) && p.number > 0 && typeof p.title === 'string' && typeof p.branch === 'string' && typeof p.commit === 'string' && SHA.test(p.commit)) : []
  return { main: typeof r.main === 'string' ? r.main : '', branches, pulls, pullsWhy: typeof r.pullsWhy === 'string' ? r.pullsWhy : '' }
}

export class StartFromField {
  readonly box: HTMLDivElement
  select = dropdown({ name: 'from' })
  #loading = option('Loading branches and pull requests…', '')

  constructor(path: string, main: string, sessions: SessionInfo[], fork: string, forkAt = '') {
    const id = `start-from-${++serial}`
    this.select.id = id
    this.select.append(option(`${main ? visible(main) : 'main'}, as fetched now`, ''))
    const own = sessions.filter(s => s.startPoint)
    this.select.append(...own.map(s => option(`Session ${visible(label(s))}`, `sid:${s.sid}`)))
    const at = own.find(s => s.sid === fork)
    if (at && SHA.test(forkAt)) this.select.append(option(`Session ${visible(label(at))} at commit ${shortSha(forkAt)}`, `sid:${fork}:${forkAt}`))
    this.#loading.disabled = true
    this.select.append(this.#loading)
    if (at) this.select.value = SHA.test(forkAt) ? `sid:${fork}:${forkAt}` : `sid:${fork}`
    this.box = field('Start from', this.select, { cls: 'start-from' })
    this.select.addEventListener('change', () => labelError(this.select, ''))
    void this.#load(path)
  }

  async #load(path: string) {
    const a = await request('/api/start-points', { path })
    this.#loading.remove()
    if (!a.ok) {
      const message = a.message
      this.select.append(option(`Branches and pull requests aren’t listed: ${message}`, '', { disabled: true }))
      return
    }
    const points = pointsOf(a.body)
    if (!points) return
    this.select.append(...points.branches.map(b => option(`${visible(b.name)} (${shortSha(b.commit)})`, `branch:${b.commit}:${b.name}`)))
    this.select.append(...points.pulls.map(p => option(`#${p.number} ${visible(p.title)}${p.branch ? ` (${visible(p.branch)})` : ''}, at ${shortSha(p.commit)}`, `pull:${p.commit}:${p.number}`)))
    if (!points.pulls.length && points.pullsWhy) this.select.append(option(`No pull requests listed: ${visible(points.pullsWhy)}`, '', { disabled: true }))
  }

  value(): StartFrom | undefined {
    const v = this.select.value
    const sid = /^sid:([^:]+)(?::([0-9a-f]{40}))?$/.exec(v)
    if (sid) return sid[2] ? { sid: sid[1], commit: sid[2] } : { sid: sid[1] }
    const at = /^(branch|pull):([0-9a-f]{40}):(.+)$/.exec(v)
    if (!at) return undefined
    const [, kind, commit, rest] = at
    if (kind === 'branch') return { branch: rest, commit }
    const n = Number(rest)
    return Number.isInteger(n) && n > 0 ? { pull: n, commit } : undefined
  }
}
