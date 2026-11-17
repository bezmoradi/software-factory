import { api } from '../api.ts'
import { busy, button, el, field, focusedKey, refocus, replaceKeeping, say, statusLine, visible } from '../lib/dom.ts'

interface AgentFileView {
  name: string
  agent: string
  description: string
  model: string
  tools: string[]
  bytes: number
  sha: string
  at: number
}

interface AgentFilesView {
  files: AgentFileView[]
  roles: Record<string, string>
  defaults: Record<string, string>
  roleNames: Record<string, string>
  limit: number
  maxBytes: number
  tools: string[]
}

export interface ReviewerAgentsTarget {
  project: string
  disabled: boolean
}

function readView(body: unknown): AgentFilesView | null {
  if (!body || typeof body !== 'object') return null
  const v = body as Partial<AgentFilesView>
  const record = (x: unknown) => x && typeof x === 'object' && !Array.isArray(x) ? x as Record<string, string> : null
  if (!Array.isArray(v.files) || !record(v.roles) || !record(v.defaults) || !record(v.roleNames) || !Array.isArray(v.tools)) return null
  const files = v.files.filter((f): f is AgentFileView => !!f && typeof f.name === 'string' && typeof f.agent === 'string' && typeof f.description === 'string' && Array.isArray(f.tools))
  return { files, roles: v.roles!, defaults: v.defaults!, roleNames: v.roleNames!, limit: Number(v.limit) || 0, maxBytes: Number(v.maxBytes) || 0, tools: v.tools.filter((t): t is string => typeof t === 'string') }
}

async function problemOf(res: Response | null): Promise<string> {
  if (!res) return 'The engine didn\'t answer. Try again.'
  const body = await res.json().catch(() => null) as { message?: unknown } | null
  return typeof body?.message === 'string' && body.message ? body.message : 'The engine refused the change. Try again.'
}

class SfReviewerAgents extends HTMLElement {
  #target: ReviewerAgentsTarget | null = null
  #view: AgentFilesView | null = null
  #busy = false
  #removing = ''
  #file = el('input')
  #status = statusLine()
  #loaded = ''
  #key = ''

  constructor() {
    super()
    this.#file.type = 'file'
    this.#file.id = 'reviewer-agent-file'
    this.#file.accept = '.md,text/markdown,text/plain'
    this.#file.dataset.key = 'agent-file'
    this.#file.setAttribute('aria-describedby', 'reviewer-agent-help')
    this.#file.addEventListener('click', e => {
      if (this.#busy) e.preventDefault()
    })
    this.#file.addEventListener('change', () => void this.#upload())
  }

  set view(value: ReviewerAgentsTarget | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    const changed = value?.project !== this.#target?.project
    this.#target = value
    this.hidden = !value?.project
    if (!value?.project) {
      this.#view = null
      this.#loaded = ''
      this.replaceChildren()
      return
    }
    if (changed) {
      this.#view = null
      this.#status.textContent = ''
    }
    if (changed || this.#loaded !== value.project) void this.#load()
    else this.#paint()
  }

  async #call(body: Record<string, unknown>): Promise<boolean> {
    const project = this.#target?.project
    if (!project) return false
    this.#busy = true
    this.#paint()
    const res = await api('/api/reviewer-agents', { ...body, path: project }).catch(() => null)
    this.#busy = false
    if (project !== this.#target?.project) return false
    if (!res || !res.ok) {
      say(this.#status, await problemOf(res), 'warn')
      this.#paint()
      return false
    }
    const v = readView(await res.json().catch(() => null))
    if (v) {
      this.#view = v
      this.#loaded = project
    }
    this.#paint()
    return true
  }

  async #load() {
    await this.#call({ op: 'list' })
  }

  async #upload() {
    const file = this.#file.files?.[0]
    this.#file.value = ''
    if (!file || this.#busy) return
    const max = this.#view?.maxBytes || 0
    if (max && file.size > max) {
      say(this.#status, `The engine refused ${visible(file.name)}: it is over ${Math.floor(max / 1024)} KiB.`, 'warn')
      return this.#paint()
    }
    const text = await file.text().catch(() => null)
    if (text === null) {
      say(this.#status, `The page couldn't read ${visible(file.name)}.`, 'warn')
      return this.#paint()
    }
    if (await this.#call({ op: 'add', text })) {
      say(this.#status, `Added ${visible(file.name)}. Pick it for a role below; reviews run with it from the next review on.`, 'ok')
      this.#paint()
    }
  }

  async #remove(name: string) {
    this.#removing = name
    const done = await this.#call({ op: 'remove', name })
    this.#removing = ''
    if (done) {
      say(this.#status, `Removed ${visible(name)}. Its roles fall back to knackpack's agents.`, 'ok')
      this.#paint()
    }
  }

  async #pick(role: string, name: string) {
    if (await this.#call({ op: 'role', role, name })) {
      say(this.#status, name ? `The ${this.#view?.roleNames[role] ?? role} role uses ${visible(name)} from the next review on.` : `The ${this.#view?.roleNames[role] ?? role} role uses knackpack's agent again.`, 'ok')
      this.#paint()
    }
  }

  #paint() {
    const target = this.#target
    if (!target?.project) return this.replaceChildren()
    const focused = focusedKey(this)
    const v = this.#view
    const off = target.disabled || this.#busy
    const tools = (v?.tools ?? ['Read', 'Grep', 'Glob', 'Agent', 'ToolSearch']).join(', ')
    const help = el('div', 'small muted')
    help.id = 'reviewer-agent-help'
    help.append(
      el('p', '', `A Claude Code agent file (.md) of your own can fill the bug hunter or simplicity checker role in place of knackpack's agent. Its front matter may hold only name, description, model and tools, with tools any of ${tools}. The engine refuses mcpServers, hooks, permissionMode, skills and every other key, and says why.`),
      el('p', '', 'The body is your own instructions. They run in a trusted reviewer container, with read-only tools, on the code under review, so write them as you would a reviewer you trust. The engine writes the file into that container for each review, never from the repository.'),
      el('p', '', 'Its reply must end with a line of its own, Verdict: pass or Verdict: block. A last line knackpack-verdict/1: pass or knackpack-verdict/1: block counts too. Anything else counts as no verdict, and a push still needs every required role passing.'),
    )
    this.#file.disabled = target.disabled || (!!v && v.files.length >= v.limit)
    if (this.#busy) this.#file.setAttribute('aria-disabled', 'true')
    else this.#file.removeAttribute('aria-disabled')
    const add = field('Add an agent file', this.#file, undefined, 'row field-row')
    const parts: HTMLElement[] = [el('p', 'small', 'Reviewer agents for this project'), help, add]
    if (v && v.files.length >= v.limit) parts.push(el('p', 'small muted', `This project keeps ${v.limit} agent files, its limit. Remove one to add another.`))
    if (v?.files.length) {
      const list = el('ul', 'list')
      for (const f of v.files) {
        const li = el('li')
        const row = el('div', 'row')
        const what = el('div')
        what.append(el('strong', '', visible(f.name)), el('span', 'small muted', ` ${visible(f.agent)}`))
        const detail = [f.model ? `model ${visible(f.model)}` : 'model: the default', f.tools.length ? `tools ${f.tools.map(visible).join(', ')}` : 'tools: every review tool', `${Math.max(1, Math.round(f.bytes / 1024))} KiB`].join(' · ')
        what.append(el('p', 'small muted', detail), el('p', 'small', visible(f.description.length > 300 ? `${f.description.slice(0, 300)}…` : f.description)))
        const remove = button('Remove', 'ghost small', () => void this.#remove(f.name), 'button', `remove:${f.name}`)
        if (this.#busy && this.#removing === f.name) busy(remove, true)
        else remove.disabled = off
        remove.setAttribute('aria-label', `Remove the agent file ${visible(f.name)}`)
        row.append(what, remove)
        li.append(row)
        list.append(li)
      }
      parts.push(list)
    }
    if (v) {
      for (const role of Object.keys(v.roleNames)) {
        const select = el('select', 'textbox')
        select.id = `reviewer-role-${role}`
        select.dataset.key = `role:${role}`
        const knack = el('option', '', `knackpack's agent (${visible(v.defaults[role] ?? '')})`)
        knack.value = ''
        select.append(knack)
        for (const f of v.files) {
          const o = el('option', '', visible(f.name))
          o.value = f.name
          select.append(o)
        }
        select.value = v.files.some(f => f.name === v.roles[role]) ? v.roles[role] ?? '' : ''
        const chosen = select.value
        select.disabled = target.disabled
        if (this.#busy) select.setAttribute('aria-disabled', 'true')
        select.addEventListener('change', () => {
          if (this.#busy) {
            select.value = chosen
            return
          }
          void this.#pick(role, select.value)
        })
        const name = v.roleNames[role] ?? role
        parts.push(field(`The ${name} role uses`, select, undefined, 'row field-row'))
      }
    }
    parts.push(this.#status)
    replaceKeeping(this, parts)
    refocus(this, focused, key => key === 'agent-file' ? Object.keys(v?.roleNames ?? {}).map(role => `role:${role}`) : ['agent-file'])
  }
}

customElements.define('sf-reviewer-agents', SfReviewerAgents)

declare global {
  interface HTMLElementTagNameMap {
    'sf-reviewer-agents': SfReviewerAgents
  }
}
