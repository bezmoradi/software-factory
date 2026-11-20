import { el, unspoofed, visible } from '../lib/dom.ts'
import { shortSha } from '../lib/format.ts'
import { icon } from './icon.ts'
import { cardFlow, flowDiagram } from './push-flow.ts'
import type { PushCheckedBy, PushCheckedReviewer, PushCheckedRun, SessionPush } from '../types.ts'

const FINISHED = new Set(['pass', 'block', 'unknown'])

function text(value: string): string {
  return visible(unspoofed(value))
}

function count(n: unknown): number {
  return Math.max(0, Number(n) || 0)
}

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`
}

function resultOf(r: PushCheckedReviewer): [string, string, string] {
  switch (r.state) {
    case 'pass':
      return ['check', 'ok', 'passed']
    case 'block':
      return r.overridden ? ['warning', 'warn', 'blocked, overridden by you'] : ['error', r.advisory ? 'warn' : 'bad', 'blocked']
    case 'unknown':
      return r.setAside ? ['warning', 'warn', 'passed, but set aside'] : ['warning', 'warn', 'no verdict the gate counts']
    case 'failed':
      return ['error', 'bad', "didn't finish"]
    case 'running':
      return ['info', '', 'still running']
  }
  return ['info', '', 'no review of this commit']
}

function details(r: PushCheckedReviewer): string {
  const parts: string[] = []
  const blocking = count(r.blocking)
  if (FINISHED.has(r.state)) parts.push(blocking ? plural(blocking, 'blocking finding', 'blocking findings') : 'no blocking findings')
  const asked = count(r.requirements)
  if (asked) {
    const verified = count(r.verified)
    const violated = count(r.violated)
    const notVerified = count(r.notVerified)
    const unreported = Math.max(0, asked - verified - violated - notVerified)
    parts.push(`requirements: ${verified} verified, ${violated} violated, ${notVerified} not verified${unreported ? `, ${unreported} not reported` : ''}`)
  }
  const runs = count(r.runs)
  if (runs > 1) parts.push(plural(runs, 'run', 'runs'))
  return parts.join(' · ')
}

function tags(r: PushCheckedReviewer): HTMLElement[] {
  const out: HTMLElement[] = []
  if (r.required) out.push(el('span', `tag${r.counted ? ' ok' : ' warn'}`, r.counted ? 'counts for the push' : "doesn't count for the push"))
  else out.push(el('span', 'tag', r.advisory ? 'advisory, never counts' : 'not required for this push'))
  if (r.unchecked) out.push(el('span', 'tag warn', 'not rechecked since the engine restarted'))
  return out
}

function reviewer(r: PushCheckedReviewer): HTMLElement {
  const [name, tone, said] = resultOf(r)
  const li = el('li', 'push-checked-item')
  const head = el('div', 'push-checked-head')
  head.append(icon(name, `icon push-icon ${tone}`.trim()), el('strong', '', text(r.name || r.agent)), el('span', '', `: ${said}`), ...tags(r))
  li.append(head)
  const more = details(r)
  if (more) li.append(el('div', 'small muted', more))
  if (r.setAside) li.append(el('div', 'small muted', `Set aside: ${text(r.setAside)}`))
  return li
}

function run(r: PushCheckedRun): HTMLElement {
  const kind = text(r.kind)
  const [name, tone, said] = r.state === 'passed' ? ['check', 'ok', 'passed'] : r.state === 'running' ? ['info', '', 'still running'] : r.state === 'failed' ? ['error', 'bad', `failed, exit code ${count(r.code)}`] : ['warning', 'warn', text(r.state)]
  const li = el('li', 'push-checked-item')
  const head = el('div', 'push-checked-head')
  head.append(icon(name, `icon push-icon ${tone}`.trim()), el('strong', '', `Verification run (${kind})`), el('span', '', `: ${said}`))
  li.append(head)
  return li
}

function checkedOf(v: unknown): PushCheckedBy | null {
  if (!v || typeof v !== 'object') return null
  const c = v as Partial<PushCheckedBy>
  const reviewers = Array.isArray(c.reviewers) ? c.reviewers.filter((r): r is PushCheckedReviewer => !!r && typeof r.agent === 'string' && typeof r.state === 'string') : []
  const runs = Array.isArray(c.runs) ? c.runs.filter((r): r is PushCheckedRun => !!r && typeof r.kind === 'string' && typeof r.state === 'string') : []
  return { reviewers, runs, unreadable: c.unreadable === true }
}

export function checkedFacts(p: SessionPush, decision: string): HTMLElement | null {
  if (p.running || !p.result) return null
  const box = el('section', 'push-facts push-checked')
  const title = el('h3', 'small push-checked-title', 'How this change was checked')
  title.id = 'push-checked-title'
  box.setAttribute('aria-labelledby', title.id)
  box.append(title)
  const commit = p.commit ? ` of commit ${shortSha(p.commit)}` : ''
  box.append(el('p', 'small muted', `From the engine's own records${commit}: the reviews, the verification runs and the gate's decision. Nothing the agent reported about itself is here.`))
  const checked = checkedOf(p.checkedBy)
  box.append(flowDiagram(cardFlow(p, checked)))
  if (!checked) box.append(el('p', 'small muted', 'The engine decided this card before it looked at the reviews, so no reviewer is listed.'))
  else {
    if (checked.unreadable) box.append(el('p', 'small warn', "The engine couldn't read its review records, so this list may be incomplete."))
    if (checked.reviewers.length) {
      const list = el('ul', 'small push-checked-list')
      list.setAttribute('role', 'list')
      list.setAttribute('aria-label', 'Reviewers')
      for (const r of checked.reviewers) list.append(reviewer(r))
      box.append(list)
    } else box.append(el('p', 'small muted', 'No reviewer was required, and none reviewed this commit.'))
    if (checked.runs.length) {
      const list = el('ul', 'small push-checked-list')
      list.setAttribute('role', 'list')
      list.setAttribute('aria-label', 'Verification runs')
      for (const r of checked.runs) list.append(run(r))
      box.append(list)
    }
  }
  const gate = el('p', 'small push-checked-gate')
  gate.append(el('strong', '', 'Gate: '), text(decision))
  box.append(gate)
  return box
}
