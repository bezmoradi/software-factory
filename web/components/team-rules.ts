import { api } from '../api.ts'
import { el, shown, visible } from '../lib/dom.ts'
import { shortSha } from '../lib/format.ts'

interface TeamRules {
  head: string
  present: boolean
  approved: boolean
  same: boolean
  problems: string[]
  rules: { pattern: string; agent: string; instruction: string }[]
  protect: string[]
}

function strings(x: unknown): string[] {
  return Array.isArray(x) ? x.filter((s): s is string => typeof s === 'string') : []
}

function readRules(body: unknown): TeamRules | null {
  if (!body || typeof body !== 'object') return null
  const v = body as Record<string, unknown>
  const setup = (v.setup && typeof v.setup === 'object' ? v.setup : null) as Record<string, unknown> | null
  const approved = (v.approved && typeof v.approved === 'object' ? v.approved : null) as Record<string, unknown> | null
  const rules = Array.isArray(setup?.fileRules) ? setup.fileRules.flatMap(x => {
    if (!x || typeof x !== 'object') return []
    const r = x as Record<string, unknown>
    return typeof r.pattern === 'string' ? [{ pattern: r.pattern, agent: typeof r.agent === 'string' ? r.agent : '', instruction: typeof r.instruction === 'string' ? r.instruction : '' }] : []
  }) : []
  return {
    head: typeof v.head === 'string' ? v.head : '', present: typeof v.text === 'string', approved: !!approved, same: approved?.same === true,
    problems: [...strings(v.problems), ...strings(v.setupProblems)], rules, protect: strings(setup?.protect),
  }
}

class SfTeamRules extends HTMLElement {
  #path = ''
  #status = el('p', 'small')

  constructor() {
    super()
    this.#status.setAttribute('role', 'status')
  }

  set path(value: string) {
    if (value === this.#path) return
    this.#path = value
    this.replaceChildren(el('h3', 'label', 'Team rules from main'), el('p', 'small muted', 'Reading the team file on the main branch…'))
    if (value) void this.#load(value)
  }

  async #load(path: string) {
    const res = await api('/api/team', { path, op: 'read' }).catch(() => null)
    const body = await res?.json().catch(() => null) as Record<string, unknown> | null
    if (path !== this.#path) return
    const parts: HTMLElement[] = [el('h3', 'label', 'Team rules from main')]
    if (!res || !res.ok) {
      this.#status.className = 'small bad'
      this.#status.textContent = typeof body?.message === 'string' ? visible(body.message) : 'The engine didn’t answer. Check that it is running.'
      return this.replaceChildren(...parts, this.#status)
    }
    const v = readRules(body)
    if (!v || !v.present) return this.replaceChildren(...parts, el('p', 'small muted', 'The main branch has no software-factory.json, so no team rules apply.'))
    const where = `From software-factory.json on the main branch${v.head ? ` at commit ${shortSha(v.head)}` : ''}. Only main's version counts: the same file on a session's branch changes nothing.`
    const state = !v.approved ? 'You haven’t approved this team file, so these rules don’t apply yet. Approve it under Review the team file in a session of this project.'
      : !v.same ? 'This file changed since you approved it, so these rules don’t apply until you approve this version.' : 'You approved this version, so these rules apply.'
    parts.push(el('p', 'small muted', where), el('p', `small ${v.approved && v.same ? 'ok' : 'warn'}`, state))
    if (v.problems.length) {
      const ul = el('ul', 'small warn')
      ul.append(...v.problems.map(p => el('li', '', `Problem: ${visible(p)}`)))
      parts.push(ul)
    }
    const told = v.rules.filter(r => r.instruction)
    parts.push(el('p', 'small', told.length ? 'Reviewers of a change that touches these files get the team’s rule:' : 'No file rule gives reviewers an instruction.'))
    if (told.length) {
      const ul = el('ul', 'small team-rule-list')
      ul.append(...told.map(r => el('li', '', `${shown(r.pattern)}: ${shown(r.instruction)}`)))
      parts.push(ul)
    }
    parts.push(el('p', 'small', v.protect.length ? 'Claude Code asks you on a card before every write to these paths; a push isn’t held by them:' : 'No path is protected.'))
    if (v.protect.length) {
      const ul = el('ul', 'small team-rule-list')
      ul.append(...v.protect.map(p => el('li', 'mono', shown(p))))
      parts.push(ul)
    }
    this.replaceChildren(...parts)
  }
}

customElements.define('sf-team-rules', SfTeamRules)

declare global {
  interface HTMLElementTagNameMap {
    'sf-team-rules': SfTeamRules
  }
}
