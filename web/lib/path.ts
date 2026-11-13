import { el, visible } from './dom.ts'
import { shortSha } from './format.ts'
import type { GateEvent } from '../types.ts'

const OK: string[] = ['pass', 'overridden']
const SKIPPED = 'skipped'
const OUTDATED = 'outdated'
type Mark = boolean | typeof SKIPPED | typeof OUTDATED
export type Started = boolean | null | 'nobase'
export type Step = '' | 'define' | 'plan' | 'tasks'
type Active = '' | 'work' | 'verify' | 'define' | 'plan' | 'tasks'
export interface Turn {
  ran: ReadonlySet<Step>
  running: Step
  busy: boolean
}
const IDLE: Turn = { ran: new Set(), running: '', busy: false }

function count(done: number, all: number, one: string, many: string): string {
  return `${done} of ${all} ${all === 1 ? `review ${one}` : `reviews ${many}`} on this commit`
}

export function uncommittedIn(gate: GateEvent | null): boolean {
  const pending = gate?.uncommitted
  return !!pending && (!pending.complete || pending.count > 0)
}

function press(off: boolean, ran: boolean): string {
  return off ? '' : `: press ${ran ? 'Continue' : 'Start'}`
}

function stages(gate: GateEvent | null, started: Started, active: Active, off: boolean, turn: Turn, pushed: boolean): Record<string, [Mark, string]> {
  if (!gate) {
    return {
      define: [false, 'Waiting for the session'],
      plan: [false, ''],
      implement: [false, ''],
      verify: [false, ''],
      deliver: [false, ''],
    }
  }
  const reviewers = gate.reviewers || []
  const begun = started === true || !!gate.plan || reviewers.some(r => r.state !== 'none' || !!r.round)
  const strict = (gate.size ?? 'feature') === 'feature'
  const define: [boolean | typeof SKIPPED, string] = strict
    ? gate.define ? [true, 'Requirements approved'] : active === 'define' || turn.running === 'define' ? [false, 'The agent is working on the requirements'] : gate.requirements?.hash ? [false, turn.busy ? 'The agent is still working on the requirements' : 'Agreed requirements wait for your approval'] : gate.holds?.define ? [false, gate.uncommitted?.complete ? 'Start needs a clean working tree: commit or discard the uncommitted files' : 'Start waits until the gate can check the working tree'] : [false, `No agreed requirements yet${press(off, turn.ran.has('define'))}`]
    : gate.define ? [true, 'Requirements agreed'] : active === 'define' || turn.running === 'define' ? [false, 'The agent is working on the requirements'] : begun ? [SKIPPED, 'Skipped: no agreed requirements'] : [false, `No agreed requirements yet${press(off, turn.ran.has('define'))}`]
  const feature = (gate.effective ?? 'feature') === 'feature'
  const planned = !feature || (!!gate.plan && gate.approved !== false)
  const verified = reviewers.length > 0 && reviewers.every(r => OK.includes(r.state))
  const name = (path: string) => path.split('/').pop()
  const sized = gate.effective === 'fix' ? 'a Fix' : gate.effective === 'explore' ? 'Exploring' : 'Docs only'
  if (gate.effective === 'explore') return {
    define,
    plan: [SKIPPED, 'Not needed while Exploring'],
    implement: [false, gate.head ? `${gate.branch ? `On ${gate.branch} at` : 'At'} ${shortSha(gate.head)}` : "Couldn't read the current commit"],
    verify: [false, 'No reviews while Exploring'],
    deliver: [false, "Exploring can't push. Pick another work type to push:"],
  }
  const pending = gate.uncommitted
  const dirty = uncommittedIn(gate)
  const local = !pending ? '' : !pending.complete ? " · couldn't check for uncommitted changes" : pending.count === 1 ? ' · 1 file not committed' : ` · ${pending.count} files not committed`
  const at = gate.head ? `${gate.branch ? `On ${gate.branch} at` : 'At'} ${shortSha(gate.head)}` : "Couldn't read the current commit"
  const wait = gate?.waiting ?? ''
  const passed = reviewers.some(r => r.state === 'overridden') ? 'Reviews done on this commit, with an override' : 'Every review passes on this commit'
  const running = reviewers.some(r => r.state === 'running')
  return {
    define,
    plan: [!feature ? SKIPPED : planned, !feature ? `Not needed for ${sized}` : !gate.plan ? (active === 'plan' || turn.running === 'plan' ? 'The agent is making the plan' : strict && gate.holds?.plan ? 'Starts once you approve the requirements' : `Needs a plan${press(off, turn.ran.has('plan'))}`) : gate.approved === false ? (turn.running === 'tasks' ? `${name(gate.plan)} · the agent is writing the tasks` : turn.busy ? 'The agent is still working on the plan' : `${name(gate.plan)} · waiting for your approval`) : `${name(gate.plan)} · approved · research ${gate.research ? 'done' : 'skipped'}`],
    implement: [!wait, `${at}${dirty ? local : wait && gate.head ? ` · ${wait}` : started === 'nobase' ? " · no base branch, so the page can't tell this branch's commits" : ''}`],
    verify: [verified ? (dirty ? OUTDATED : !wait) : false, !verified ? `${count(reviewers.filter(r => OK.includes(r.state)).length, reviewers.length, 'passes', 'pass')}${!wait ? '' : running ? `. Reviews are running before Implement is done: ${wait}${dirty ? ", so they won't count" : ''}` : `. Reviews start once Implement is done: ${wait}`}` : dirty ? `${passed}, but ${pending?.complete ? "uncommitted files aren't reviewed" : "the gate couldn't check for uncommitted files"}` : wait ? `${passed}, but Implement isn't done: ${wait}` : passed],
    deliver: pushed ? [true, `Pushed ${shortSha(gate.head)}${gate.branch ? ` to ${gate.branch}` : ''}`] : [!!gate.allowed, gate.allowed ? 'The gate has what it needs for this commit: Push sends it, once the sandbox check holds' : 'Push held until it has:'],
  }
}

function paintCard(card: HTMLElement, step: string, count: string, detail: string, done: number) {
  card.querySelector('.path-card-step')!.textContent = step
  card.querySelector('.path-card-count')!.textContent = count
  card.querySelector('.path-card-detail')!.textContent = detail
  card.style.setProperty('--done', String(done))
}

function produced(stage: string, gate: GateEvent | null): boolean {
  return stage === 'define' ? !!gate?.requirements?.hash : stage === 'plan' ? !!gate?.plan : false
}

export function paintPath(root: HTMLElement, gate: GateEvent | null, started: Started = false, card?: HTMLElement, active: Active = '', off = false, turn: Turn = IDLE, pushed = false): void {
  const all = stages(gate, started, active, off, turn, pushed)
  const typed = gate?.types?.find(t => t.id === gate.workType)
  if (typed && !typed.builtin) {
    const kinds: Record<string, string[]> = { define: ['define'], plan: ['plan', 'tasks'], implement: ['implement'], verify: ['review'], deliver: ['deliver'] }
    for (const [stage, wants] of Object.entries(kinds)) {
      const got = all[stage]
      if (!wants.some(k => typed.steps.includes(k)) || got?.[0] === SKIPPED) all[stage] = [SKIPPED, `Not part of ${visible(typed.name)}`]
    }
  }
  const steps = [...root.children] as HTMLElement[]
  let current = false
  let finished = 0
  let at: { index: number; name: string; detail: string } | null = null
  for (const [index, li] of steps.entries()) {
    const stage = li.dataset.stage!
    const [done, detail] = all[stage] ?? [false, '']
    const now = !done && !current && !!gate
    if (now) {
      current = true
      at = { index, name: li.querySelector('strong')?.textContent ?? '', detail }
    }
    if (done && done !== OUTDATED) finished++
    li.className = done === SKIPPED ? 'skipped' : done === OUTDATED ? 'outdated' : done ? 'done' : now ? 'current' : ''
    const unchecked = stage === 'implement' && !!gate?.uncommitted && !gate.uncommitted.complete
    li.classList.toggle('unchecked', unchecked)
    const ring = active === 'verify' ? stage === 'verify' : active === 'define' || active === 'plan' || active === 'tasks' ? stage === (active === 'tasks' ? 'plan' : active) && !done : active === 'work' && now && stage !== 'define' && stage !== 'plan'
    li.classList.toggle('active', !unchecked && ring)
    li.querySelector('.stage-detail')!.textContent = detail
    const complete = !!done && done !== OUTDATED
    const running = turn.running === stage || (stage === 'plan' && turn.running === 'tasks')
    if (stage === 'define' || stage === 'plan') {
      const word = turn.ran.has(stage) ? 'Continue' : 'Start'
      for (const b of li.querySelectorAll<HTMLButtonElement>('button.stage:not(#tasks-start)')) {
        b.hidden = complete || running || produced(stage, gate)
        if (b.textContent !== word) b.textContent = word
      }
    } else if (stage !== 'deliver') for (const b of li.querySelectorAll<HTMLButtonElement>('button.stage')) b.hidden = complete || (b.dataset.command === 'commit' && !gate?.uncommitted?.count)
  }
  const pending = gate?.uncommitted?.complete ? gate.uncommitted : null
  const files = (pending?.files ?? []).slice(0, 5).map(p => el('li', '', `${p.path} (${p.status})`))
  if (pending && pending.count > files.length && files.length) files.push(el('li', '', `and ${pending.count - files.length} more`))
  root.querySelector('.pending')?.replaceChildren(...files)
  const missing = root.querySelector('.missing')!
  missing.replaceChildren(...(gate && !gate.allowed && !pushed ? (gate.missing || []).map(m => el('li', '', visible(m))) : []))
  if (!card) return
  if (!gate) paintCard(card, 'Waiting for the session', '', '', 0)
  else if (at) paintCard(card, at.name, `Step ${at.index + 1} of ${steps.length}`, at.detail, finished / steps.length)
  else paintCard(card, 'Every step done', '', all.deliver?.[1] ?? '', 1)
}
