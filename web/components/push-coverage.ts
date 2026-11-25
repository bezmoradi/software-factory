import { el, unspoofed, visible } from '../lib/dom.ts'
import { isRecord, saidOf, tagClass } from '../lib/json.ts'
import type { CoverageRole, CoverageRow, SessionReview } from '../types.ts'

const FINISHED = new Set(['pass', 'block', 'unknown'])

function text(value: string): string {
  return visible(unspoofed(value))
}

function strings(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
}

function rowsOf(v: unknown): CoverageRow[] {
  if (!Array.isArray(v)) return []
  return v.filter((r): r is CoverageRow => !!r && typeof r.id === 'string' && typeof r.status === 'string' && isRecord(r.said) && typeof r.said.label === 'string')
}

export function reviewCoverage(review: SessionReview | undefined, commit: string): CoverageRole[] {
  if (!review || review.running || !commit || review.commit !== commit || !Array.isArray(review.roles)) return []
  const out: CoverageRole[] = []
  for (const r of review.roles) {
    const reqIds = strings(r.reqIds)
    if (!FINISHED.has(r.state) || !reqIds.length) continue
    out.push({ name: typeof r.name === 'string' ? r.name : '', reqIds, lines: rowsOf(r.coverage), dropped: Math.max(0, Number(r.coverageDropped) || 0), cut: r.coverageCut === true })
  }
  return out
}

function cell(role: CoverageRole, id: string): HTMLElement {
  const td = el('td')
  const line = role.lines.find(l => l.id === id)
  if (!line) {
    td.append(el('span', 'muted', 'not reported'))
    return td
  }
  const status = saidOf(line.said, text(line.status))
  td.append(el('span', tagClass(status), status.label))
  if (line.evidence) td.append(' ', el('code', '', text(line.evidence)))
  if (line.note) td.append(el('div', 'muted', text(line.note)))
  return td
}

function table(roles: CoverageRole[], ids: string[]): HTMLElement {
  const t = el('table', 'coverage-table small')
  t.append(el('caption', 'sr-only', 'Each requirement, and what each reviewer reported for it'))
  const head = el('tr')
  const corner = el('th', '', 'Requirement')
  corner.scope = 'col'
  head.append(corner)
  for (const r of roles) {
    const th = el('th', '', text(r.name))
    th.scope = 'col'
    head.append(th)
  }
  const thead = el('thead')
  thead.append(head)
  const tbody = el('tbody')
  for (const id of ids) {
    const tr = el('tr')
    const th = el('th')
    th.scope = 'row'
    th.append(el('code', '', text(id)))
    tr.append(th, ...roles.map(r => cell(r, id)))
    tbody.append(tr)
  }
  t.append(thead, tbody)
  return t
}

export function coverageFacts(roles: CoverageRole[]): HTMLElement | null {
  if (!roles.length) return null
  const given: string[] = []
  for (const r of roles) for (const id of r.reqIds) if (!given.includes(id)) given.push(id)
  const reporting = roles.filter(r => r.lines.length)
  const reported = given.filter(id => reporting.some(r => r.lines.some(l => l.id === id)))
  const box = el('div', 'push-facts')
  box.append(el('p', 'small', 'Requirements, as the reviewers reported them'), el('p', 'small muted', "The reviewers' own report on each requirement ID in the approved requirements they were given. Display only: it doesn't hold or allow the push."))
  if (reported.length) box.append(table(reporting, reported))
  const unreported = given.filter(id => !reported.includes(id))
  if (reporting.length && unreported.length) {
    const p = el('p', 'small muted', 'Named in the approved requirements and reported by no reviewer: ')
    unreported.forEach((id, i) => {
      if (i) p.append(', ')
      p.append(el('code', '', text(id)))
    })
    box.append(p)
  }
  for (const r of roles) {
    if (r.cut && !r.lines.length) box.append(el('p', 'small warn', `${text(r.name)}'s reply was too long to keep whole, and the part kept holds none of its requirement lines: they may be in the part left out.`))
    else if (!r.lines.length) box.append(el('p', 'small warn', `${text(r.name)} reported no requirement coverage.`))
    else if (r.cut) box.append(el('p', 'small muted', `${text(r.name)}'s reply was too long to keep whole, so its lines for the requirements it doesn't show here may be in the part left out.`))
    if (r.dropped) box.append(el('p', 'small muted', `${r.dropped} ${r.dropped === 1 ? 'coverage line' : 'coverage lines'} from ${text(r.name)} ${r.dropped === 1 ? 'was' : 'were'} malformed or named a requirement this review wasn't given, and ${r.dropped === 1 ? 'is' : 'are'} left out.`))
  }
  return box
}
