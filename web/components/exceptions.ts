import type { EngineAnswer } from '../api.ts'
import { button, el, statusLine, visible } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { dialogDocs } from '../lib/dialog-docs.ts'
import { shortSha } from '../lib/format.ts'
import { emptyState } from '../lib/page.ts'
import { CHEAP_EVERY_MS, whileOpen } from '../lib/refresh.ts'

type Post = (path: string, body: unknown) => Promise<EngineAnswer>

const ROUTE = '/api/exceptions'

interface Item {
  kind: string
  time: number
  sid: string
  repo: string
  branch: string
  commit: string
  reviewer: string
  path: string
  missing: number
  result: string
  verdict: string
  reason: string
  label: string
  note: string
}

interface Rule {
  rule: string
  place: string
  placeLabel: string
  worktree: string
  path: string
  wide: string[]
}

const text = (v: unknown) => (typeof v === 'string' ? v : '')
const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) && v >= 0 ? v : 0)
const records = (v: unknown) => (Array.isArray(v) ? (v as unknown[]).filter((x): x is Record<string, unknown> => typeof x === 'object' && x !== null) : [])

function at(ms: number): HTMLTimeElement {
  const t = el('time', 'small muted', ms ? new Date(ms).toLocaleString() : '')
  if (ms) t.dateTime = new Date(ms).toISOString()
  return t
}

function itemOf(r: Record<string, unknown>): Item {
  return { kind: text(r.kind), time: num(r.time), sid: text(r.sid), repo: text(r.repo), branch: text(r.branch), commit: text(r.commit), reviewer: text(r.reviewer), path: text(r.path), missing: num(r.missing), result: text(r.result), verdict: text(r.verdict), reason: text(r.reason), label: text(r.label) || text(r.kind), note: text(r.note) }
}

function ruleOf(r: Record<string, unknown>): Rule {
  return { rule: text(r.rule), place: text(r.place), placeLabel: text(r.placeLabel), worktree: text(r.worktree), path: text(r.path), wide: Array.isArray(r.wide) ? (r.wide as unknown[]).filter((w): w is string => typeof w === 'string') : [] }
}

function until(expires: number, now: number): string {
  if (!expires) return 'no expiry'
  if (expires <= now) return 'expired'
  return `expires ${new Date(expires).toLocaleString()}`
}

export function openExceptions(post: Post, labelOf: (sid: string) => string): void {
  const say = statusLine()
  const body = el('div', 'exceptions-body')

  const paint = (v: Record<string, unknown>) => {
    const now = num(v.now) || Date.now()
    const items = records(v.items).map(itemOf)
    const parts: Node[] = [el('h3', 'label', 'Decisions you made')]
    if (text(v.auditProblem)) parts.push(el('p', 'small bad', visible(text(v.auditProblem))))
    const list = el('ul', 'list')
    if (!items.length) list.append(emptyState('No Allows, overrides or allowed flags in the audit file.', { tag: 'li' }))
    for (const e of items) {
      const li = el('li', 'exception-row')
      const head = el('div', 'row')
      head.append(el('strong', 'small', visible(e.label)), el('span', 'spacer'), at(e.time))
      const session = e.sid ? labelOf(e.sid) || `session ${e.sid.slice(0, 8)}` : ''
      const where = [session && visible(session), e.repo && visible(e.repo), e.branch && visible(e.branch), e.commit && `commit ${shortSha(e.commit)}`].filter(Boolean).join(' · ')
      li.append(head)
      if (where) li.append(el('p', 'small muted', where))
      li.append(el('p', 'small', visible(e.note)))
      list.append(li)
    }
    parts.push(list)
    if (v.more === true) parts.push(el('p', 'small muted', 'Only the newest 500 are shown.'))
    parts.push(el('h3', 'label', 'Hosts you added'))
    const hosts = el('ul', 'list')
    if (v.hostsKnown !== true) hosts.append(el('li', 'muted', 'This engine has no network access settings to show.'))
    else {
      const added = records(v.hosts)
      if (!added.length) hosts.append(emptyState('No hosts added.', { tag: 'li' }))
      for (const h of added) {
        const li = el('li', 'row')
        li.append(el('code', '', visible(text(h.host))), el('span', 'small muted', `added ${num(h.addedAt) ? new Date(num(h.addedAt)).toLocaleString() : 'at an unknown time'}, ${until(num(h.expires), now)}`), el('span', 'small', text(h.reason) ? `Your reason: “${visible(text(h.reason))}”` : 'no reason given'))
        hosts.append(li)
      }
      const gw = v.gateway
      if (gw && typeof gw === 'object') {
        const g = gw as Record<string, unknown>
        const li = el('li', 'row')
        li.append(el('span', 'small', 'Company LLM gateway '), el('code', '', visible(text(g.host))), el('span', 'small muted', 'for the sessions and reviewers'))
        hosts.append(li)
      }
    }
    parts.push(el('p', 'small muted', 'Change these under Network access.'), hosts)
    parts.push(el('h3', 'label', 'Saved allow rules'))
    if (v.rulesKnown !== true) parts.push(el('p', 'small muted', 'This engine has no agent-host to list saved rules from.'))
    else {
      const repos = records(v.rules)
      parts.push(el('p', 'small muted', 'Claude Code saved these from a card’s “don’t ask again”; they carry no reason. Remove one in its session’s “What this session may and may not do”.'))
      if (!repos.length) parts.push(emptyState('No projects.'))
      for (const r of repos) {
        const rules = records(r.rules).map(ruleOf)
        const ul = el('ul', 'list')
        if (text(r.problem)) ul.append(el('li', 'bad small', visible(text(r.problem))))
        else if (!rules.length) ul.append(emptyState('No saved rules.', { tag: 'li' }))
        for (const rule of rules) {
          const li = el('li', '')
          const row = el('div', 'row')
          row.append(el('code', '', visible(rule.rule)))
          if (rule.wide.length) row.append(el('span', 'tag warn', 'wide'))
          li.append(row, el('p', 'small muted', `In ${visible(rule.placeLabel || rule.place)}${rule.worktree ? ` (${visible(rule.worktree)})` : ''}: ${visible(rule.path)}`))
          if (rule.wide.length) li.append(el('p', 'small warn', rule.wide.map(visible).join('; ')))
          ul.append(li)
        }
        parts.push(el('h4', 'small', visible(text(r.repo))), ul)
      }
    }
    body.replaceChildren(...parts)
  }

  const show = (a: EngineAnswer) => {
    if (!a.ok) {
      say.textContent = a.message
      return
    }
    say.textContent = ''
    paint(a.body)
  }

  say.textContent = 'Loading…'
  const m = modal({
    kind: 'exceptions',
    ...dialogDocs('exceptions'),
    body: [
      el('p', 'small', 'Every exception to the usual rules in one place, read only: pushes you allowed with a reason, verdicts you overrode, flagged files and commits you allowed, hosts you added to the network allowlist and the allow rules Claude Code saved. Decisions come from the engine’s audit file, newest first.'),
      say, body,
    ],
    actions: [button('Close', 'ghost small', () => m.close())],
  })
  const refresh = whileOpen({ root: body, read: () => post(ROUTE, {}), show, everyMs: CHEAP_EVERY_MS })
  m.onClose(() => refresh.stop())
}
