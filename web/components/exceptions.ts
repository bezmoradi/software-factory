import { actionButton, button, el, statusLine, visible } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { shortSha } from '../lib/format.ts'

type Post = (path: string, body: unknown) => Promise<Response>

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
}

interface Rule {
  rule: string
  place: string
  worktree: string
  path: string
  wide: string[]
}

const KIND: Record<string, string> = {
  'allow-push': 'Push allowed with items missing',
  override: 'Verdict overridden',
  'allow-file': 'Flagged file allowed',
  'allow-commit': 'Flagged commit allowed',
  'cap-reached': 'Cap reached',
  'cap-answer': 'Cap card answered',
  'usage-answer': 'Automatic work kept running near a usage limit',
}

const CAP: Record<string, string> = { money: 'spending cap', time: 'time cap', turns: 'turn cap', disk: 'disk cap' }
const CAP_ANSWER: Record<string, string> = { raised: 'raised', stopped: 'stopped' }

const PLACE: Record<string, string> = { worktree: 'a session worktree', clone: 'the clone folder', user: 'the agents’ user settings' }

const text = (v: unknown) => (typeof v === 'string' ? v : '')
const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) && v >= 0 ? v : 0)
const records = (v: unknown) => (Array.isArray(v) ? (v as unknown[]).filter((x): x is Record<string, unknown> => typeof x === 'object' && x !== null) : [])

function at(ms: number): HTMLTimeElement {
  const t = el('time', 'small muted', ms ? new Date(ms).toLocaleString() : '')
  if (ms) t.dateTime = new Date(ms).toISOString()
  return t
}

function itemOf(r: Record<string, unknown>): Item {
  return { kind: text(r.kind), time: num(r.time), sid: text(r.sid), repo: text(r.repo), branch: text(r.branch), commit: text(r.commit), reviewer: text(r.reviewer), path: text(r.path), missing: num(r.missing), result: text(r.result), verdict: text(r.verdict), reason: text(r.reason) }
}

function ruleOf(r: Record<string, unknown>): Rule {
  return { rule: text(r.rule), place: text(r.place), worktree: text(r.worktree), path: text(r.path), wide: Array.isArray(r.wide) ? (r.wide as unknown[]).filter((w): w is string => typeof w === 'string') : [] }
}

function until(expires: number, now: number): string {
  if (!expires) return 'no expiry'
  if (expires <= now) return 'expired'
  return `expires ${new Date(expires).toLocaleString()}`
}

export function openExceptions(post: Post, labelOf: (sid: string) => string): void {
  let generation = 0
  const say = statusLine()
  const body = el('div', 'exceptions-body')

  const paint = (v: Record<string, unknown>) => {
    const now = num(v.now) || Date.now()
    const items = records(v.items).map(itemOf)
    const parts: Node[] = [el('h3', 'label', 'Decisions you made')]
    if (text(v.auditProblem)) parts.push(el('p', 'small bad', visible(text(v.auditProblem))))
    const list = el('ul', 'list')
    if (!items.length) list.append(el('li', 'muted', 'No Allows, overrides or allowed flags in the audit file.'))
    for (const e of items) {
      const li = el('li', 'exception-row')
      const head = el('div', 'row')
      const freed = e.kind === 'cap-answer' && e.verdict === 'freed'
      const what = freed ? 'Disk cap cleared by the engine’s measurement' : KIND[e.kind] ?? visible(e.kind)
      const detail = freed ? ''
        : e.kind === 'override' ? (e.reviewer ? ` (${visible(e.reviewer)})` : '')
        : e.kind === 'allow-file' && e.path ? ` (${visible(e.path)})`
        : e.kind === 'allow-push' ? ` (${e.missing} missing)`
        : e.kind.startsWith('cap-') && (CAP[e.result] || CAP_ANSWER[e.verdict]) ? ` (${[CAP[e.result] ?? '', CAP_ANSWER[e.verdict] ?? ''].filter(Boolean).join(', ')})` : ''
      head.append(el('strong', 'small', `${what}${detail}`), el('span', 'spacer'), at(e.time))
      const session = e.sid ? labelOf(e.sid) || `session ${e.sid.slice(0, 8)}` : ''
      const where = [session && visible(session), e.repo && visible(e.repo), e.branch && visible(e.branch), e.commit && `commit ${shortSha(e.commit)}`].filter(Boolean).join(' · ')
      li.append(head)
      if (where) li.append(el('p', 'small muted', where))
      if (freed) li.append(el('p', 'small', 'The engine counted the whole session folder under its disk cap, so the session went on. Nobody answered the card.'))
      else li.append(el('p', 'small', e.reason ? `Your reason: “${visible(e.reason)}”` : 'No reason recorded.'))
      list.append(li)
    }
    parts.push(list)
    if (v.more === true) parts.push(el('p', 'small muted', 'Only the newest 500 are shown.'))
    parts.push(el('h3', 'label', 'Hosts you added'))
    const hosts = el('ul', 'list')
    if (v.hostsKnown !== true) hosts.append(el('li', 'muted', 'This engine has no network access settings to show.'))
    else {
      const added = records(v.hosts)
      if (!added.length) hosts.append(el('li', 'muted', 'No hosts added.'))
      for (const h of added) {
        const li = el('li', 'row')
        li.append(el('code', '', visible(text(h.host))), el('span', 'small muted', `added ${num(h.addedAt) ? new Date(num(h.addedAt)).toLocaleString() : 'at an unknown time'}, ${until(num(h.expires), now)}`), el('span', 'small', text(h.reason) ? `Your reason: “${visible(text(h.reason))}”` : 'no reason given'))
        hosts.append(li)
      }
      const gw = v.gateway
      if (gw && typeof gw === 'object') {
        const g = gw as Record<string, unknown>
        const sides = [g.agents === true && 'sessions', g.reviewers === true && 'reviewers'].filter(Boolean).join(' and ')
        const li = el('li', 'row')
        li.append(el('span', 'small', 'Company LLM gateway '), el('code', '', visible(text(g.host))), el('span', 'small muted', sides ? `for the ${sides}` : 'for neither side'))
        hosts.append(li)
      }
    }
    parts.push(el('p', 'small muted', 'Change these under Network access.'), hosts)
    parts.push(el('h3', 'label', 'Saved allow rules'))
    if (v.rulesKnown !== true) parts.push(el('p', 'small muted', 'This engine has no agent-host to list saved rules from.'))
    else {
      const repos = records(v.rules)
      if (!repos.length) parts.push(el('p', 'small muted', 'No projects.'))
      for (const r of repos) {
        const rules = records(r.rules).map(ruleOf)
        const ul = el('ul', 'list')
        if (text(r.problem)) ul.append(el('li', 'bad small', visible(text(r.problem))))
        else if (!rules.length) ul.append(el('li', 'muted small', 'No saved rules.'))
        for (const rule of rules) {
          const li = el('li', '')
          const row = el('div', 'row')
          row.append(el('code', '', visible(rule.rule)))
          if (rule.wide.length) row.append(el('span', 'tag warn', 'wide'))
          li.append(row, el('p', 'small muted', `In ${PLACE[rule.place] ?? visible(rule.place)}${rule.worktree ? ` (${visible(rule.worktree)})` : ''}: ${visible(rule.path)}`))
          if (rule.wide.length) li.append(el('p', 'small warn', rule.wide.map(visible).join('; ')))
          ul.append(li)
        }
        parts.push(el('h4', 'small', visible(text(r.repo))), ul)
      }
      parts.push(el('p', 'small muted', 'Claude Code saved these from a card’s “don’t ask again”; they carry no reason. Remove one from its project on Projects.'))
    }
    body.replaceChildren(...parts)
  }

  const reload = async () => {
    const mine = ++generation
    say.textContent = 'Loading…'
    const res = await post(ROUTE, {}).catch(() => null)
    const parsed: unknown = res ? await res.json().catch(() => ({})) : {}
    if (mine !== generation) return
    const v = parsed !== null && typeof parsed === 'object' ? (parsed as Record<string, unknown>) : {}
    if (!res || !res.ok) {
      say.textContent = text(v.message) ? visible(text(v.message)) : 'The register didn’t load.'
      return
    }
    say.textContent = ''
    paint(v)
  }

  const m = modal({
    kind: 'exceptions',
    title: 'Register of exceptions',
    body: [
      el('p', 'small', 'Every exception to the usual rules in one place, read only: pushes you allowed with a reason, verdicts you overrode, flagged files and commits you allowed, hosts you added to the network allowlist and the allow rules Claude Code saved. Decisions come from the engine’s audit file, newest first.'),
      say, body,
    ],
    lead: [actionButton('Refresh', 'ghost small', reload)],
    actions: [button('Close', 'ghost small', () => m.close())],
  })
  m.onClose(() => {
    generation++
  })
  void reload()
}
