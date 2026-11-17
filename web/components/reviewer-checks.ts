import { api } from '../api.ts'
import { busy, button, el, focusedKey, refocus, remembered, say, shown, statusLine, visible } from '../lib/dom.ts'

interface CheckRun {
  state: string
  verdict: string
  reason: string
  reply: string
}

interface ReviewerCheck {
  result: string
  reason: string
  claudeCode: string
  plugin: string
  models: string[]
  model: string
  effort: string
  defect: CheckRun
  clean: CheckRun
  by: string
  startedAt: number
  endedAt: number
  costUsd: number
}

interface RoleCheck {
  agent: string
  role: string
  roleName: string
  running: boolean
  check: ReviewerCheck | null
}

interface ChecksView {
  claudeCode: string
  plugin: string
  minimum: { claudeCode: string; plugin: string }
  roles: RoleCheck[]
}

export interface ReviewerChecksTarget {
  disabled: boolean
  review: string
}

const POLL_MS = 10000

const RESULTS: Record<string, [string, string]> = {
  running: ['Checking now', 'muted'],
  passed: ['Passed its check', 'ok'],
  failed: ['Failed its check after an update', 'bad'],
  warned: ['Passed its check, with a warning', 'warn'],
  unsettled: ['Check not settled', 'warn'],
}

function text(x: unknown): string {
  return typeof x === 'string' ? x : ''
}

function readRun(x: unknown): CheckRun {
  const r = x && typeof x === 'object' ? x as Record<string, unknown> : {}
  return { state: text(r.state), verdict: text(r.verdict), reason: text(r.reason), reply: text(r.reply) }
}

function readCheck(x: unknown): ReviewerCheck | null {
  if (!x || typeof x !== 'object') return null
  const c = x as Record<string, unknown>
  return {
    result: text(c.result), reason: text(c.reason), claudeCode: text(c.claudeCode), plugin: text(c.plugin),
    models: Array.isArray(c.models) ? c.models.filter((m): m is string => typeof m === 'string') : [],
    model: text(c.model), effort: text(c.effort),
    defect: readRun(c.defect), clean: readRun(c.clean), by: text(c.by),
    startedAt: Number(c.startedAt) || 0, endedAt: Number(c.endedAt) || 0, costUsd: Number(c.costUsd) || 0,
  }
}

function readView(body: unknown): ChecksView | null {
  if (!body || typeof body !== 'object') return null
  const v = body as Record<string, unknown>
  if (!Array.isArray(v.roles)) return null
  const min = v.minimum && typeof v.minimum === 'object' ? v.minimum as Record<string, unknown> : {}
  const roles = v.roles.flatMap((x): RoleCheck[] => {
    if (!x || typeof x !== 'object') return []
    const r = x as Record<string, unknown>
    if (typeof r.agent !== 'string' || !r.agent) return []
    return [{ agent: r.agent, role: text(r.role), roleName: text(r.roleName) || text(r.role) || r.agent, running: r.running === true, check: readCheck(r.check) }]
  })
  return { claudeCode: text(v.claudeCode), plugin: text(v.plugin), minimum: { claudeCode: text(min.claudeCode), plugin: text(min.plugin) }, roles }
}

function short(sha: string): string {
  return /^[0-9a-f]{40}$/.test(sha) ? sha.slice(0, 7) : sha
}

function when(ms: number): string {
  return ms ? new Date(ms).toLocaleString() : ''
}

class SfReviewerChecks extends HTMLElement {
  #target: ReviewerChecksTarget | null = null
  #view: ChecksView | null = null
  #busy = ''
  #cost = ''
  #status = statusLine()
  #timer = 0
  #key = ''
  #open = new Map<string, boolean>()

  connectedCallback() {
    if (this.#target && !this.#timer) this.#poll()
  }

  disconnectedCallback() {
    window.clearTimeout(this.#timer)
    this.#timer = 0
  }

  set view(value: ReviewerChecksTarget | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    this.#target = value
    this.hidden = !value
    if (!value) {
      window.clearTimeout(this.#timer)
      this.#timer = 0
      this.replaceChildren()
      return
    }
    this.#paint()
    void this.#call({ op: 'list' })
  }

  #poll() {
    window.clearTimeout(this.#timer)
    this.#timer = 0
    if (!this.#target || !this.isConnected || !this.#view?.roles.some(r => r.running || r.check?.result === 'running')) return
    this.#timer = window.setTimeout(() => void this.#call({ op: 'list' }), POLL_MS)
  }

  async #call(body: Record<string, unknown>): Promise<boolean> {
    if (!this.#target) return false
    const res = await api('/api/reviewer-checks', body).catch(() => null)
    this.#busy = ''
    if (!res || !res.ok) {
      const problem = await res?.json().catch(() => null) as { message?: unknown; needs?: { cost?: unknown } } | null
      say(this.#status, typeof problem?.message === 'string' && problem.message ? visible(problem.message) : 'The engine didn\'t answer. Try again.', 'warn')
      if (problem?.needs?.cost === true && typeof body.agent === 'string') this.#cost = body.agent
      this.#paint()
      return false
    }
    const v = readView(await res.json().catch(() => null))
    if (v) this.#view = v
    this.#paint()
    this.#poll()
    return true
  }

  async #check(agent: string, costOk: boolean) {
    this.#busy = agent
    this.#cost = ''
    this.#status.textContent = ''
    this.#paint()
    if (await this.#call({ op: 'check', agent, ...(costOk ? { costOk: true } : {}) })) {
      say(this.#status, 'The check started. It reviews a change with a planted defect and a clean one, like a real review, so it takes about as long.', 'ok')
      this.#paint()
    }
  }

  #replies(r: RoleCheck, c: ReviewerCheck): HTMLElement[] {
    const runs: [string, string, CheckRun][] = [['defect', 'the change with a planted defect', c.defect], ['clean', 'the clean change', c.clean]]
    return runs.filter(([, , run]) => run.reply).map(([kind, what, run]) => {
      const key = `reply:${r.agent}:${c.startedAt}:${kind}`
      const summary = el('summary', 'small', `Its reply to ${what}`)
      summary.dataset.key = key
      const pre = el('pre', 'review-message small', shown(run.reply))
      pre.tabIndex = 0
      pre.setAttribute('aria-label', `The ${visible(r.roleName)}'s reply to ${what}`)
      return remembered(this.#open, key, false, summary, pre)
    })
  }

  #row(r: RoleCheck, off: boolean): HTMLElement {
    const li = el('li')
    const row = el('div', 'row')
    const what = el('div')
    const c = r.check
    const [label, tone] = r.running && c?.result !== 'running' ? RESULTS.running! : RESULTS[c?.result ?? ''] ?? ['Not checked yet', 'muted']
    what.append(el('strong', '', `The ${visible(r.roleName)}`))
    if (r.roleName !== r.agent) what.append(el('span', 'small muted', ` ${visible(r.agent)}`))
    what.append(el('p', `small ${tone}`, label))
    if (c) {
      const versions = [`Claude Code ${visible(c.claudeCode)}`, c.plugin ? `plugin or agent file ${visible(short(c.plugin))}` : '', c.models.length ? `model ${c.models.map(visible).join(', ')}` : '', `run with ${c.model ? `--model=${visible(c.model)}` : 'the default model'} and ${c.effort ? `--effort=${visible(c.effort)}` : 'the default effort'}`].filter(Boolean).join(' · ')
      const at = c.endedAt ? `finished ${when(c.endedAt)}` : `started ${when(c.startedAt)}`
      what.append(el('p', 'small muted', `${versions} · ${c.by === 'page' ? 'started from this page' : 'after an update'}, ${at}`))
      if (c.reason) what.append(el('p', 'small', visible(c.reason)))
      what.append(...this.#replies(r, c))
    }
    const actions = el('div', 'row')
    const now = button('Check now', 'ghost small', () => void this.#check(r.agent, false), 'button', `check:${r.agent}`)
    now.setAttribute('aria-label', `Check now: the ${visible(r.roleName)}`)
    if (this.#busy === r.agent) busy(now, true)
    else now.disabled = off || r.running || c?.result === 'running'
    actions.append(now)
    if (this.#cost === r.agent) {
      const pay = button('Check now, it costs money', 'small', () => void this.#check(r.agent, true), 'button', `pay:${r.agent}`)
      pay.disabled = off
      actions.append(pay)
    }
    row.append(what, actions)
    li.append(row)
    return li
  }

  #paint() {
    const target = this.#target
    if (!target) return this.replaceChildren()
    const focused = focusedKey(this)
    const v = this.#view
    const off = target.disabled || this.#busy !== ''
    const help = el('div', 'small muted')
    help.append(
      el('p', '', 'After the reviewers\' Claude Code, a role\'s plugin or agent file, or its model or effort changes, the engine checks that role, run with the role\'s own model and effort, before its next pass counts: it reviews a change with a planted defect, which it must block (the simplicity checker may instead report it as a finding, as knackpack\'s slop reviewer does with code that costs upkeep), and a clean change, which it should pass. A role that passes the defect without reporting it stops counting until a later check passes; a role that blocks the clean change only gets a warning; a check with no verdict leaves passes counting until you press Check now, which checks again with the model and effort of the latest check shown. Each run\'s reply is kept, under the result.'),
    )
    const parts: HTMLElement[] = [el('p', 'small', 'Reviewer checks'), help]
    if (v) {
      const facts = [`The reviewer containers run Claude Code ${visible(v.claudeCode) || '(not read yet)'}`]
      if (v.plugin) facts.push(`knackpack ${visible(short(v.plugin))}`)
      const min = [v.minimum.claudeCode ? `Claude Code ${visible(v.minimum.claudeCode)}` : '', v.minimum.plugin ? `knackpack ${visible(short(v.minimum.plugin))}` : ''].filter(Boolean)
      parts.push(el('p', 'small muted', `${facts.join(', ')}. ${min.length ? `Passes by an older reviewer than ${min.join(' and ')} don't count.` : 'This engine sets no minimum reviewer version.'}`))
      const list = el('ul', 'list')
      for (const r of v.roles) list.append(this.#row(r, off))
      parts.push(list)
    }
    parts.push(this.#status)
    this.replaceChildren(...parts)
    refocus(this, focused)
  }
}

customElements.define('sf-reviewer-checks', SfReviewerChecks)

declare global {
  interface HTMLElementTagNameMap {
    'sf-reviewer-checks': SfReviewerChecks
  }
}
