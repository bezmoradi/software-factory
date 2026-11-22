import { request, type EngineAnswer } from '../api.ts'
import { around, busy, button, el, focusedKey, refocus, replaceKeeping, say, shown, statusLine, visible } from '../lib/dom.ts'
import { emptyState } from '../lib/page.ts'
import { CHEAP_EVERY_MS, whileOpen, type WhileOpen } from '../lib/refresh.ts'

interface SavedRule {
  box: string
  rule: string
  place: string
  worktree: string
  path: string
  wide: string[]
}

interface SettingsFile {
  box: string
  container: string
  place: string
  worktree: string
  path: string
  count: number
  problem: string
}

interface RulesView {
  idle: boolean
  files: SettingsFile[]
  rules: SavedRule[]
}

const PLACES: Record<string, string> = {
  clone: 'The clone folder; Claude Code 2.1.322 didn\'t apply a rule from it in a session\'s worktree, but an agent can write it',
  worktree: "A session's worktree, where Claude Code saves a card's \"don't ask again\"",
  user: "The agent's user settings in the session's container, which it loads in every folder",
}

function text(x: unknown): string {
  return typeof x === 'string' ? x : ''
}

function strings(x: unknown): string[] {
  return Array.isArray(x) ? x.filter((s): s is string => typeof s === 'string') : []
}

function readView(body: unknown): RulesView | null {
  if (!body || typeof body !== 'object') return null
  const v = body as Record<string, unknown>
  if (!Array.isArray(v.rules) || !Array.isArray(v.files)) return null
  const rules = v.rules.flatMap((x): SavedRule[] => {
    if (!x || typeof x !== 'object') return []
    const r = x as Record<string, unknown>
    if (typeof r.rule !== 'string' || !r.rule) return []
    return [{ box: text(r.box), rule: r.rule, place: text(r.place), worktree: text(r.worktree), path: text(r.path), wide: strings(r.wide) }]
  })
  const files = v.files.flatMap((x): SettingsFile[] => {
    if (!x || typeof x !== 'object') return []
    const f = x as Record<string, unknown>
    return [{ box: text(f.box), container: text(f.container), place: text(f.place), worktree: text(f.worktree), path: text(f.path), count: Number(f.count) || 0, problem: text(f.problem) }]
  })
  return { idle: v.idle === true, files, rules }
}

function fileKey(box: string, place: string, worktree: string): string {
  return `${box}:${place}:${worktree}`
}

class SfAllowRules extends HTMLElement {
  #repo = ''
  #view: RulesView | null = null
  #busy = ''
  #gen = 0
  #status = statusLine()
  #poll: WhileOpen | null = null
  #failed = false

  connectedCallback() {
    if (!this.#poll?.live) this.#watch()
  }

  set repo(value: string) {
    if (value === this.#repo) return
    this.#repo = value
    this.#gen++
    this.#busy = ''
    this.#view = null
    this.#status.textContent = ''
    this.#paint()
    this.#watch()
  }

  #watch() {
    this.#poll?.stop()
    this.#poll = null
    const repo = this.#repo
    if (!repo || !this.isConnected) return
    this.#poll = whileOpen({ root: this, everyMs: CHEAP_EVERY_MS, hold: () => !!this.#busy, read: () => request('/api/allow-rules', { op: 'list', repo }), show: a => this.#took(a) })
  }

  #took(a: EngineAnswer) {
    if (!a.ok) {
      this.#failed = true
      say(this.#status, a.message, 'warn')
    } else {
      if (this.#failed) this.#status.textContent = ''
      this.#failed = false
      const v = readView(a.body)
      if (v) this.#view = v
    }
    this.#paint()
  }

  async #call(body: Record<string, unknown>): Promise<boolean> {
    const gen = ++this.#gen
    const current = () => gen === this.#gen
    const a = await request('/api/allow-rules', body)
    if (!current()) return false
    this.#busy = ''
    if (!a.ok) {
      say(this.#status, a.message, 'warn')
      this.#paint()
      return false
    }
    const v = readView(a.body)
    if (v) this.#view = v
    this.#paint()
    return true
  }

  async #remove(r: SavedRule) {
    if (this.#busy) return
    this.#busy = `${fileKey(r.box, r.place, r.worktree)}:${r.rule}`
    this.#status.textContent = ''
    this.#paint()
    const body: Record<string, unknown> = { op: 'remove', repo: this.#repo, box: r.box, place: r.place, rule: r.rule }
    if (r.worktree) body.worktree = r.worktree
    if (await this.#call(body)) {
      void this.#poll?.now()
      say(this.#status, `Removed ${visible(r.rule)} from ${visible(r.path)}. The next matching call shows a card again.`, 'ok')
      this.#paint()
    }
  }

  #rule(r: SavedRule): HTMLElement {
    const li = el('li')
    const row = el('div', 'row')
    const what = el('div')
    what.append(el('code', '', shown(r.rule)))
    for (const why of r.wide) what.append(' ', el('span', 'tag warn', `Wide: ${visible(why)}`))
    const key = `${fileKey(r.box, r.place, r.worktree)}:${r.rule}`
    const remove = button('Remove', 'ghost small', () => void this.#remove(r), 'button', `remove:${key}`)
    if (this.#busy === key) busy(remove, true)
    else if (this.#busy) remove.setAttribute('aria-disabled', 'true')
    remove.setAttribute('aria-label', `Remove the rule ${visible(r.rule)} from ${visible(r.path)}`)
    row.append(what, remove)
    li.append(row)
    return li
  }

  #paint() {
    if (!this.#repo) return this.replaceChildren()
    const focused = focusedKey(this)
    const before = [...this.querySelectorAll<HTMLElement>('[data-key^="remove:"]')].map(b => b.dataset.key ?? '')
    const parts: HTMLElement[] = [
      el('h3', 'label', 'Saved allow rules'),
      el('p', 'small muted', 'The allow rules Claude Code saved when a card\'s "don\'t ask again" was chosen, in the session\'s worktree (.claude/settings.local.json), and those in the clone folder\'s .claude/settings.local.json, which a live run of Claude Code 2.1.322 didn\'t apply in a worktree, and the agents\' user settings, including any written there another way. Agents can write these files too, so this is a report of what is in them, not a control: an agent can add a rule again, and Remove changes only the file it names. Rules marked wide allow much more than one command.'),
    ]
    const v = this.#view
    if (!v) parts.push(el('p', 'small muted', 'Reading the saved rules…'))
    else if (v.idle) parts.push(emptyState('No session of this project is running, so no session’s container is there to read. Each session keeps its rules in its own container.'))
    else if (!v.files.length) parts.push(emptyState('No settings file holds saved rules for this project.'))
    else {
      for (const f of v.files) {
        const section = el('div')
        section.append(el('p', 'small', `${PLACES[f.place] ?? visible(f.place)}, in ${visible(f.container || 'a session’s container')}: `), el('code', 'small', visible(f.path)))
        if (f.problem) section.append(el('p', 'small warn', visible(f.problem)))
        const rules = v.rules.filter(r => r.box === f.box && r.place === f.place && r.worktree === f.worktree)
        if (!rules.length && !f.problem) section.append(emptyState('No allow rules in this file.'))
        if (rules.length) {
          const list = el('ul', 'list')
          for (const r of rules) list.append(this.#rule(r))
          section.append(list)
        }
        parts.push(section)
      }
    }
    parts.push(this.#status)
    replaceKeeping(this, parts)
    refocus(this, focused, key => around(before, key))
  }
}

customElements.define('sf-allow-rules', SfAllowRules)

declare global {
  interface HTMLElementTagNameMap {
    'sf-allow-rules': SfAllowRules
  }
}
