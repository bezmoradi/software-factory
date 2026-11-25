import { el, unspoofed, visible } from '../lib/dom.ts'
import { shortSha } from '../lib/format.ts'
import { isRecord } from '../lib/json.ts'
import type { AgentCheck, DependencyChange, LockedTests, PushExtra, SessionPush, TypeChange, TypeLine, WorkflowRun } from '../types.ts'

const DAY = 86400000

function text(value: string): string {
  return visible(unspoofed(value))
}

function code(value: string): HTMLElement {
  return el('code', '', text(value))
}

function section(title: string, ...body: Node[]): HTMLElement {
  const box = el('div', 'push-facts-part')
  box.append(el('p', 'small', title), ...body)
  return box
}

function list(items: HTMLElement[]): HTMLElement {
  const ul = el('ul', 'small list')
  ul.append(...items)
  return ul
}

function item(tone: string, ...parts: (Node | string)[]): HTMLElement {
  const li = el('li', tone)
  li.append(...parts)
  return li
}

function tag(label: string, tone: string): HTMLElement {
  return el('span', `tag ${tone}`, label)
}

function age(ms: number): string {
  const days = Math.floor((Date.now() - ms) / DAY)
  if (days < 1) return 'first published today'
  if (days < 60) return `first published ${days} ${days === 1 ? 'day' : 'days'} ago`
  return `first published ${new Date(ms).toLocaleDateString(undefined, { year: 'numeric', month: 'short' })}`
}

function dependency(d: DependencyChange, lookup: string): HTMLElement {
  const li = item(d.change === 'added' ? 'warn' : '', `${text(d.ecosystemLabel || d.ecosystem)} `, code(d.name))
  if (d.change === 'added') li.append(` added${d.to ? ` at ${text(d.to)}` : ''}`)
  else if (d.change === 'removed') li.append(` removed${d.from ? ` (was ${text(d.from)})` : ''}`)
  else li.append(` changed from ${text(d.from) || 'none'} to ${text(d.to) || 'none'}`)
  li.append(' in ', code(d.file))
  if (d.change === 'added' && !d.lockfile) {
    li.append(' ', tag('new', 'warn'))
    if (d.looked && !d.found) li.append(' ', tag('not on the public registry', 'bad'))
    if (d.looked && d.found && d.young) li.append(' ', tag(age(d.published), 'bad'))
    else if (d.looked && d.found && d.published) li.append(el('span', 'muted', ` · ${age(d.published)}`))
    if (d.note) li.append(el('span', 'muted', ` · not looked up: ${text(d.note)}`))
    if (lookup === 'off') li.append(el('span', 'muted', ' · registry lookup off'))
  }
  return li
}

function workflow(w: WorkflowRun): HTMLElement {
  const li = item(w.secrets.length || w.idToken || w.trusted.length || w.new || w.changed || w.problem ? 'warn' : '', code(w.path))
  if (w.name) li.append(` (${text(w.name)})`)
  if (w.problem) {
    li.append(' ', tag("couldn't read", 'warn'), el('span', 'muted', ` ${text(w.problem)}`))
    return li
  }
  li.append(el('span', 'muted', ` runs ${text(w.when)}`))
  if (w.new) li.append(' ', tag('new on this branch', 'warn'))
  if (w.changed) li.append(' ', tag('changed on this branch', 'warn'))
  if (w.secrets.length) li.append(' ', tag(`uses secrets: ${w.secrets.map(text).join(', ')}`, 'warn'))
  if (w.idToken) li.append(' ', tag('id-token: write', 'warn'))
  if (w.trusted.length) li.append(' ', tag(`also ${w.trusted.join(' and ')}, which run with secrets`, 'warn'))
  return li
}

function ranCheck(p: SessionPush, c: AgentCheck): HTMLElement {
  const stale = !!c.editedAt && (!p.at || c.editedAt <= p.at)
  const li = item(c.ok && !c.hidden && !stale ? '' : 'warn', `${text(c.label || c.kind)}: `, code(c.command), ` ${text(c.result ?? '')}`)
  if (stale) li.append(' ', tag('the agent edited files after it ran', 'warn'))
  return li
}

function checks(p: SessionPush): HTMLElement {
  const rows = (p.checks ?? []).map(c => ranCheck(p, c))
  if (p.notReported) rows.push(item('muted', text(p.notReported)))
  return section('Checks the agent ran before this, reported by the agent:', list(rows))
}

function lockedTests(l: LockedTests): HTMLElement[] {
  if (l.problem) return [el('p', 'small warn', `${text(l.problem.charAt(0).toUpperCase() + l.problem.slice(1))}.`)]
  if (!l.count) return []
  const rows = l.paths.map(p => item('warn', code(p)))
  if (l.count > l.paths.length) rows.push(item('muted', `and ${l.count - l.paths.length} more`))
  return [section(`Locked tests changed since ${shortSha(l.commit)}: ${l.count}`, el('p', 'small muted', 'Changed, deleted or moved away from their path since you locked them, however the change was made. A new test file doesn\'t count.'), list(rows))]
}

function stringOf(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

function typeLineOf(v: unknown): TypeLine | null {
  if (!isRecord(v) || typeof v.what !== 'string' || !Array.isArray(v.files)) return null
  const files = v.files.filter(isRecord).filter(f => typeof f.path === 'string').map(f => ({ path: stringOf(f.path), what: stringOf(f.what) }))
  return { what: v.what, files, more: Math.max(0, Number(v.more) || 0) }
}

export function typeChangeOf(v: unknown): TypeChange | null {
  if (!isRecord(v) || typeof v.type !== 'string' || !Array.isArray(v.lines)) return null
  const lines = v.lines.map(typeLineOf).filter((l): l is TypeLine => l !== null)
  return lines.length ? { type: v.type, lines } : null
}

function capital(value: string): string {
  return value.charAt(0).toUpperCase() + value.slice(1)
}

export function typeChangeParts(t: TypeChange): HTMLElement[] {
  const parts: HTMLElement[] = [el('p', 'small muted', `This session's work type, as set on the page: ${text(t.type)}. The files come from the engine's own reading of the commits. These lines don't hold or allow the push themselves.`)]
  for (const l of t.lines) {
    parts.push(el('p', 'small warn', `${text(capital(l.what))}.`))
    const rows = l.files.map(f => item('warn', code(f.path), ...(f.what ? [`: ${text(f.what)}`] : [])))
    if (l.more) rows.push(item('muted', `and ${l.more} more`))
    if (rows.length) parts.push(list(rows))
  }
  return parts
}

function typeChangePart(t: TypeChange): HTMLElement {
  const box = el('div', 'push-facts-part')
  box.append(el('p', 'small', 'The change and its work type'), ...typeChangeParts(t))
  return box
}

function extraParts(x: PushExtra): HTMLElement[] {
  const parts: HTMLElement[] = []
  if (x.locked) parts.push(...lockedTests(x.locked))
  if (x.tests.length) parts.push(section('Tests, suppressions and files run later:', list(x.tests.map(n => item('warn', code(n.path), `: ${text(n.what)}`)))))
  if (x.dependencies.length) {
    const rows = x.dependencies.map(d => dependency(d, x.lookup))
    if (x.moreDependencies) rows.push(item('muted', `and ${x.moreDependencies} more dependency changes`))
    const note = x.lookup === 'off' ? ' New packages aren\'t looked up on their public registry: the project setting is off.' : x.lookup !== 'on' ? ` ${text(x.lookup)}.` : ''
    parts.push(section(`Dependency changes.${note}`, list(rows)))
  }
  if (x.kinds.length) {
    parts.push(section("Files change.diff doesn't show as text:", list(x.kinds.map(k => {
      const li = item('warn', code(k.path), `: ${k.kind === 'symlink' ? 'symbolic link' : k.kind === 'executable' ? 'newly executable' : text(k.kind)}`)
      if (k.target) li.append(' to ', code(k.target))
      return li
    }))))
  }
  if (x.workflows.length) parts.push(section('CI workflows this push starts, with the repository\'s secrets:', list(x.workflows.map(workflow))))
  else if (!x.workflowsProblem) parts.push(section('CI workflows: none in this commit starts on a push to this branch.'))
  if (x.workflowsProblem) parts.push(el('p', 'small warn', text(x.workflowsProblem)))
  for (const problem of x.problems) parts.push(el('p', 'small muted', `${text(problem.charAt(0).toUpperCase() + problem.slice(1))}.`))
  return parts
}

export function changeFacts(p: SessionPush, typed: TypeChange | null = null): HTMLElement | null {
  if (!p.commit || !p.result) return null
  const x = p.extra && p.extra.commit === p.commit ? p.extra : null
  const parts: HTMLElement[] = []
  if (typed) parts.push(typeChangePart(typed))
  if (x) parts.push(...extraParts(x))
  else parts.push(el('p', 'small muted', 'Reading the change for tests, dependencies, file kinds and workflows…'))
  parts.push(checks(p))
  const box = el('div', 'push-facts')
  box.append(el('p', 'small', 'About this change'), el('p', 'small muted', 'Facts the engine read from the commit, for you to check. None of them holds or allows the push.'), ...parts)
  return box
}
