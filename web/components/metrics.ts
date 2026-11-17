import { api } from '../api.ts'
import { busy, button, el, focusedKey, refocus, replaceKeeping, say, shown, statusLine } from '../lib/dom.ts'

interface RoleNumbers {
  role: string
  reviews: number
  passed: number
  blocked: number
  noVerdict: number
  failed: number
  overridden: number
}

interface DonePull {
  number: number
  state: string
  at: number
}

interface MetricsView {
  auditSince: number
  auditLines: number
  auditCut: boolean
  pushes: number
  timed: number
  median: number
  mean: number
  longest: number
  rounds: number
  roles: RoleNumbers[]
  allows: number
  denied: number
  overrides: number
  reviewsSince: number
  reviewRuns: number
  reviewCost: number
  importedRuns: number
  importedCost: number
  agentSessions: number
  agentCost: number
  pullsSince: number
  pullsOpened: number
  pullsMerged: number
  pullsClosed: number
  pulls: DonePull[]
  problems: string[]
}

function count(x: unknown): number {
  const n = Number(x)
  return Number.isFinite(n) && n >= 0 ? n : 0
}

function readRole(x: unknown): RoleNumbers[] {
  if (!x || typeof x !== 'object') return []
  const r = x as Record<string, unknown>
  if (typeof r.role !== 'string') return []
  return [{ role: shown(r.role), reviews: count(r.reviews), passed: count(r.passed), blocked: count(r.blocked), noVerdict: count(r.noVerdict), failed: count(r.failed), overridden: count(r.overridden) }]
}

function readPull(x: unknown): DonePull[] {
  if (!x || typeof x !== 'object') return []
  const p = x as Record<string, unknown>
  if (p.state !== 'merged' && p.state !== 'closed') return []
  return [{ number: count(p.number), state: p.state, at: count(p.at) }]
}

function readView(body: unknown): MetricsView | null {
  if (!body || typeof body !== 'object') return null
  const v = body as Record<string, unknown>
  if (!Array.isArray(v.roles)) return null
  return {
    auditSince: count(v.auditSince), auditLines: count(v.auditLines), auditCut: v.auditCut === true, pushes: count(v.pushes), timed: count(v.timed),
    median: count(v.startToPushMedian), mean: count(v.startToPushMean), longest: count(v.startToPushLongest), rounds: count(v.rounds), roles: v.roles.flatMap(readRole),
    allows: count(v.allows), denied: count(v.denied), overrides: count(v.overrides), reviewsSince: count(v.reviewsSince), reviewRuns: count(v.reviewRuns), reviewCost: count(v.reviewCostUsd), importedRuns: count(v.importedReviewRuns), importedCost: count(v.importedReviewCostUsd),
    agentSessions: count(v.agentSessions), agentCost: count(v.agentCostUsd), pullsSince: count(v.pullsSince), pullsOpened: count(v.pullsOpened), pullsMerged: count(v.pullsMerged),
    pullsClosed: count(v.pullsClosed), pulls: Array.isArray(v.pulls) ? v.pulls.flatMap(readPull) : [],
    problems: Array.isArray(v.problems) ? v.problems.filter((s): s is string => typeof s === 'string').map(shown) : [],
  }
}

function date(ms: number): string {
  return ms ? new Date(ms).toLocaleString(undefined, { year: 'numeric', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' }) : ''
}

function span(ms: number): string {
  const min = Math.round(ms / 60000)
  if (min < 1) return 'under a minute'
  if (min < 120) return `${min} min`
  const h = Math.round(min / 6) / 10
  if (h < 48) return `${h} h`
  return `${Math.round(h / 2.4) / 10} days`
}

function share(n: number, of: number): string {
  return of ? `${n} (${Math.round((n / of) * 100)}%)` : String(n)
}

function plural(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`
}

class SfProjectMetrics extends HTMLElement {
  #project = ''
  #view: MetricsView | null = null
  #busy = false
  #status = statusLine()

  set project(value: string) {
    if (value === this.#project) return
    this.#project = value
    this.#view = null
    this.#status.textContent = ''
    this.#paint()
    if (value) void this.#read()
  }

  async #read() {
    const project = this.#project
    this.#busy = true
    this.#paint()
    const res = await api('/api/metrics', { op: 'read', repo: project }).catch(() => null)
    if (project !== this.#project) return
    this.#busy = false
    const body = await res?.json().catch(() => null) as Record<string, unknown> | null
    if (!res || !res.ok) {
      say(this.#status, typeof body?.message === 'string' && body.message ? shown(body.message) : 'The engine didn\'t answer. Try again.', 'warn')
    } else {
      const v = readView(body)
      if (v) this.#view = v
      this.#status.textContent = ''
    }
    this.#paint()
  }

  #numbers(v: MetricsView): HTMLElement[] {
    const out: HTMLElement[] = []
    const since = v.auditSince ? `since ${date(v.auditSince)}, the oldest line the audit file still holds${v.auditCut ? ` (the engine read its newest ${v.auditLines} lines)` : ''}` : 'with no audit lines yet'
    out.push(el('p', 'small muted', `Counted from the engine's own records, ${since}. Older lines rotate out of the audit file, so the window moves.`))
    const ul = el('ul', 'small list')
    ul.append(el('li', '', `Pushes: ${v.pushes}`))
    ul.append(el('li', '', v.timed ? `Time from a session's start to its first push: median ${span(v.median)}, mean ${span(v.mean)}, longest ${span(v.longest)}, over ${plural(v.timed, 'session')} whose start the engine still has` : 'Time from a session\'s start to its first push: no session with both in the records'))
    ul.append(el('li', '', v.pushes ? `Review rounds per push: ${(v.rounds / v.pushes).toFixed(1)} (${plural(v.rounds, 'reviewed commit')} before ${plural(v.pushes, 'push', 'pushes')})` : 'Review rounds per push: no pushes yet'))
    ul.append(el('li', v.allows ? 'warn' : '', `Pushes allowed with items missing (Allow with a reason): ${v.allows}`))
    ul.append(el('li', '', `Pushes denied: ${v.denied}`))
    ul.append(el('li', v.overrides ? 'warn' : '', `Verdicts overridden: ${v.overrides}`))
    out.push(ul)
    return out
  }

  #roles(v: MetricsView): HTMLElement {
    const box = el('div')
    box.append(el('p', 'small', 'Per review role'))
    if (!v.roles.length) {
      box.append(el('p', 'small muted', 'No finished reviews in the window.'))
      return box
    }
    const ul = el('ul', 'small list')
    for (const r of v.roles) {
      const li = el('li')
      li.append(el('code', '', r.role), `: ${plural(r.reviews, 'review')}, ${share(r.passed, r.reviews)} passed, ${share(r.blocked, r.reviews)} blocked, ${share(r.overridden, r.reviews)} overridden, ${share(r.noVerdict, r.reviews)} with no verdict${r.failed ? `, ${share(r.failed, r.reviews)} failed` : ''}`)
      ul.append(li)
    }
    box.append(ul)
    return box
  }

  #pulls(v: MetricsView): HTMLElement {
    const box = el('div')
    box.append(el('p', 'small', 'Pull requests'))
    const since = v.pullsSince ? `, from pushes since ${date(v.pullsSince)}` : ''
    box.append(el('p', 'small muted', `From the pull requests the engine follows (at most 500)${since}: ${v.pullsOpened} opened, ${v.pullsMerged} merged, ${v.pullsClosed} closed without merging.`))
    if (v.pulls.length) {
      const ul = el('ul', 'small list')
      for (const p of v.pulls) ul.append(el('li', p.state === 'merged' ? 'ok' : '', `#${p.number} ${p.state === 'merged' ? 'merged' : 'closed without merging'}${p.at ? ` ${date(p.at)}` : ''}`))
      box.append(ul)
    }
    return box
  }

  #cost(v: MetricsView): HTMLElement {
    const box = el('div')
    box.append(el('p', 'small', 'Cost, the agents\' own estimate'))
    box.append(el('p', 'small muted', 'Claude Code and Codex report these numbers themselves; the engine adds them up and checks nothing. On a subscription they are what the same use would cost per token, not a bill.'))
    const ul = el('ul', 'small list')
    ul.append(el('li', '', `Agents: $${v.agentCost.toFixed(2)} over ${plural(v.agentSessions, 'session')} the caps record still holds`))
    ul.append(el('li', '', `Reviewers: $${v.reviewCost.toFixed(2)} over ${plural(v.reviewRuns, 'review')}${v.reviewsSince ? ` since ${date(v.reviewsSince)}` : ''}, from the review records (at most 500)${v.importedRuns ? `, including $${v.importedCost.toFixed(2)} over ${plural(v.importedRuns, 'review')} imported from another engine, shown for the record` : ''}`))
    box.append(ul)
    return box
  }

  #paint() {
    if (!this.#project) return this.replaceChildren()
    const focused = focusedKey(this)
    const parts: HTMLElement[] = [el('h3', 'label', 'Delivery and reviewer numbers')]
    const v = this.#view
    if (!v) parts.push(el('p', 'small muted', this.#busy ? 'Counting…' : 'Nothing read yet.'))
    else {
      parts.push(...this.#numbers(v), this.#roles(v), this.#pulls(v), this.#cost(v))
      for (const p of v.problems) parts.push(el('p', 'small warn', p))
    }
    const again = button('Count again', 'link small', () => void this.#read(), 'button', 'metrics-again')
    if (this.#busy) busy(again, true)
    parts.push(again, this.#status)
    replaceKeeping(this, parts)
    refocus(this, focused)
  }
}

customElements.define('sf-project-metrics', SfProjectMetrics)

declare global {
  interface HTMLElementTagNameMap {
    'sf-project-metrics': SfProjectMetrics
  }
}
