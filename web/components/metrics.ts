import { request, type EngineAnswer } from '../api.ts'
import { el, focusedKey, refocus, replaceKeeping, say, shown, statusLine } from '../lib/dom.ts'
import { emptyState, factGrid, pageHead, type Fact } from '../lib/page.ts'
import { tipText } from '../lib/tooltip.ts'
import { CHEAP_EVERY_MS, whileOpen, type WhileOpen } from '../lib/refresh.ts'

interface RoleNumbers {
  role: string
  reviews: number
  passed: number
  blocked: number
  noVerdict: number
  failed: number
  overridden: number
  afterCi: number
  afterComments: number
}

interface MetricsView {
  auditSince: number
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
  afterCi: number
  afterComments: number
  reviewRuns: number
  reviewCost: number
  importedRuns: number
  importedCost: number
  agentSessions: number
  agentCost: number
  pullsOpened: number
  pullsMerged: number
  pullsClosed: number
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
  return [{ role: shown(r.role), reviews: count(r.reviews), passed: count(r.passed), blocked: count(r.blocked), noVerdict: count(r.noVerdict), failed: count(r.failed), overridden: count(r.overridden), afterCi: count(r.afterCi), afterComments: count(r.afterComments) }]
}

function readView(body: unknown): MetricsView | null {
  if (!body || typeof body !== 'object') return null
  const v = body as Record<string, unknown>
  if (!Array.isArray(v.roles)) return null
  return {
    auditSince: count(v.auditSince), pushes: count(v.pushes), timed: count(v.timed),
    median: count(v.startToPushMedian), mean: count(v.startToPushMean), longest: count(v.startToPushLongest), rounds: count(v.rounds), roles: v.roles.flatMap(readRole),
    allows: count(v.allows), denied: count(v.denied), overrides: count(v.overrides), afterCi: count(v.afterCi), afterComments: count(v.afterComments), reviewRuns: count(v.reviewRuns), reviewCost: count(v.reviewCostUsd), importedRuns: count(v.importedReviewRuns), importedCost: count(v.importedReviewCostUsd),
    agentSessions: count(v.agentSessions), agentCost: count(v.agentCostUsd), pullsOpened: count(v.pullsOpened), pullsMerged: count(v.pullsMerged),
    pullsClosed: count(v.pullsClosed),
    problems: Array.isArray(v.problems) ? v.problems.filter((s): s is string => typeof s === 'string').map(shown) : [],
  }
}

function date(ms: number): string {
  return ms ? new Date(ms).toLocaleDateString(undefined, { dateStyle: 'medium' }) : ''
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

function percent(n: number, of: number): string {
  return of ? `${Math.round((n / of) * 100)}%` : '0%'
}

function plural(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`
}

class SfProjectMetrics extends HTMLElement {
  #project = ''
  #view: MetricsView | null = null
  #status = statusLine()
  #poll: WhileOpen | null = null
  #head = pageHead({ title: 'Delivery and reviewer numbers', level: 'h2', headingClass: 'label', intro: 'How this project’s pushes, reviews and pull requests went, from the engine’s own records.', docs: { doc: 'delivery-and-reviewer-numbers', key: 'metrics-docs' } })

  connectedCallback() {
    if (!this.#poll?.live) this.#watch()
  }

  set project(value: string) {
    if (value === this.#project) return
    this.#project = value
    this.#view = null
    this.#status.textContent = ''
    this.#paint()
    this.#watch()
  }

  #watch() {
    this.#poll?.stop()
    this.#poll = null
    const repo = this.#project
    if (!repo || !this.isConnected) return
    this.#poll = whileOpen({ root: this, everyMs: CHEAP_EVERY_MS, read: () => request('/api/metrics', { op: 'read', repo }), show: a => this.#took(a) })
  }

  #took(a: EngineAnswer) {
    if (!a.ok) say(this.#status, a.message, 'warn')
    else {
      const v = readView(a.body)
      if (v) this.#view = v
      this.#status.textContent = ''
    }
    this.#paint()
  }

  #empty(v: MetricsView): boolean {
    return !v.pushes && !v.reviewRuns && !v.roles.length && !v.allows && !v.denied && !v.overrides && !v.pullsOpened && !v.pullsMerged && !v.pullsClosed
  }

  #numbers(v: MetricsView): HTMLElement {
    const facts: Fact[] = [
      { label: 'Pushes', value: String(v.pushes) },
      v.timed
        ? { label: 'Time to first push', value: `${span(v.median)} median`, tip: `Mean ${span(v.mean)}, longest ${span(v.longest)}, over ${plural(v.timed, 'session')} whose start the engine still has.` }
        : { label: 'Time to first push', value: 'none yet' },
      { label: 'Review rounds per push', value: v.pushes ? (v.rounds / v.pushes).toFixed(1) : 'none yet', tip: v.pushes ? `${plural(v.rounds, 'reviewed commit')} before ${plural(v.pushes, 'push', 'pushes')}.` : '' },
      { label: 'Allowed with items missing', value: String(v.allows), tone: v.allows ? 'warn' : '' },
      { label: 'Denied', value: String(v.denied) },
      { label: 'Verdicts overridden', value: String(v.overrides), tone: v.overrides ? 'warn' : '' },
      { label: 'CI failures after review', value: String(v.afterCi), tone: v.afterCi ? 'warn' : '', tip: 'On commits whose required reviews had all passed.' },
      { label: 'Review comments after review', value: String(v.afterComments), tone: v.afterComments ? 'warn' : '', tip: 'Pull request reviews and comments on those commits after the push.' },
      { label: 'Pull requests', value: `${v.pullsOpened} opened, ${v.pullsMerged} merged, ${v.pullsClosed} closed` },
      { label: 'Agent cost', value: `$${v.agentCost.toFixed(2)}`, tip: `Over ${plural(v.agentSessions, 'session')}.` },
      { label: 'Reviewer cost', value: `$${v.reviewCost.toFixed(2)}`, tip: `Over ${plural(v.reviewRuns, 'review')}${v.importedRuns ? `, including $${v.importedCost.toFixed(2)} over ${plural(v.importedRuns, 'review')} imported from another engine` : ''}.` },
    ]
    return factGrid(facts)
  }

  #roles(v: MetricsView): HTMLElement {
    const box = el('div')
    box.append(...pageHead({ title: 'Per reviewer', level: 'h3', headingClass: 'label', intro: 'How often each review role passed and blocked.', docs: { doc: 'per-reviewer', key: 'roles-docs' } }))
    if (!v.roles.length) {
      box.append(emptyState('No finished reviews yet.'))
      return box
    }
    const ul = el('ul', 'small list')
    for (const r of v.roles) {
      const li = el('li', 'row text-row')
      const line = el('p')
      const rest = `${share(r.overridden, r.reviews)} overridden, ${share(r.noVerdict, r.reviews)} with no verdict${r.failed ? `, ${share(r.failed, r.reviews)} failed` : ''}; ${plural(r.afterCi, 'CI failure')} and ${plural(r.afterComments, 'review comment')} after it passed.`
      line.append(el('code', '', r.role), ' ', ...tipText(`${plural(r.reviews, 'review')} · ${percent(r.passed, r.reviews)} passed · ${percent(r.blocked, r.reviews)} blocked`, rest, 'muted'))
      li.append(line)
      ul.append(li)
    }
    box.append(ul)
    return box
  }

  #paint() {
    if (!this.#project) return this.replaceChildren()
    const focused = focusedKey(this)
    const parts: HTMLElement[] = [...this.#head]
    const v = this.#view
    if (!v) parts.push(el('p', 'small muted', 'Counting…'))
    else {
      if (this.#empty(v)) parts.push(emptyState('No pushes or reviews yet. The numbers start with this project’s first push.'))
      else {
        parts.push(this.#numbers(v), this.#roles(v))
        const since = date(v.auditSince)
        parts.push(el('p', 'small muted', `${since ? `Counted since ${since}. ` : ''}Costs are the agents’ own estimates.`))
      }
      for (const p of v.problems) parts.push(el('p', 'small warn', p))
    }
    parts.push(this.#status)
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
