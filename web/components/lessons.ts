import { request, type EngineAnswer } from '../api.ts'
import { busy, button, el, labelError, say, statusLine, unspoofed, visible, whileBusy } from '../lib/dom.ts'
import { actions, field, form } from '../lib/form.ts'
import { consentFor, money } from './consent.ts'
import type { Needs } from '../types.ts'
import { emptyState, pageHead } from '../lib/page.ts'
import { dropdown, option } from '../lib/controls.ts'
import { CHEAP_EVERY_MS, whileOpen, type WhileOpen } from '../lib/refresh.ts'
import { toast } from './toasts.ts'

interface LessonFinding {
  id: string
  agent: string
  path: string
  title: string
  commits: number
  offered: boolean
}

interface LessonLine {
  id: string
  text: string
  from: string[]
  state: 'offered' | 'approved' | 'declined'
  session?: string
  at: number
}

interface LessonRun {
  state: 'running' | 'done' | 'failed' | 'withheld'
  reason?: string
  costUsd?: number
  at: number
  given: number
  notes: string[]
}

interface LessonsView {
  project: string
  after: number
  findings: LessonFinding[]
  lines: LessonLine[]
  run?: LessonRun
  sessions: { sid: string; name: string; running: boolean }[]
  offered: number
}

function text(value: string): string {
  return visible(unspoofed(value))
}

function isView(v: unknown): v is LessonsView {
  const x = v as LessonsView | null
  return !!x && Array.isArray(x.findings) && Array.isArray(x.lines) && Array.isArray(x.sessions) && typeof x.offered === 'number'
}

class SfLessons extends HTMLElement {
  #project = ''
  #view: LessonsView | null = null
  #said = statusLine()
  #body = el('div', 'lessons-body')
  #busy = ''
  #doing = ''
  #chosen = new Map<string, string>()
  #pickRefused = new Map<string, string>()
  #built = false
  #poll: WhileOpen | null = null
  #failed = false

  connectedCallback() {
    if (!this.#built) {
      this.#built = true
      const head = pageHead({ title: 'Lessons from reviews', level: 'h2', headingClass: 'label', intro: 'Blocking findings that keep coming back, and the instruction lines they suggest.', docs: { doc: 'lessons-from-reviews', key: 'lessons-docs' } })
      const heading = head[0]?.querySelector<HTMLElement>('h2')
      if (heading) {
        heading.tabIndex = -1
        heading.dataset.key = 'lessons-heading'
      }
      this.replaceChildren(...head, this.#body, this.#said)
    }
    if (!this.#poll?.live) this.#watch()
  }

  set project(name: string) {
    if (name === this.#project) return
    this.#project = name
    this.#view = null
    this.#paint()
    this.#watch()
  }

  #watch() {
    this.#poll?.stop()
    this.#poll = null
    const name = this.#project
    if (!name || !this.isConnected) return
    this.#poll = whileOpen({ root: this, everyMs: CHEAP_EVERY_MS, hold: () => this.#typing(), read: () => request('/api/lessons', { op: 'read', name }), show: a => this.#took(a), near: () => ['lessons-suggest', 'lessons-heading'] })
  }

  #typing(): boolean {
    const a = document.activeElement
    return !!this.#busy || (a instanceof HTMLElement && this.contains(a) && a.matches('input, textarea, select'))
  }

  #took(a: EngineAnswer) {
    if (!a.ok) {
      this.#failed = true
      say(this.#said, a.message, 'warn')
      return
    }
    if (this.#failed) say(this.#said, '')
    this.#failed = false
    const v: unknown = a.body
    if (isView(v)) this.#view = v
    this.#paint()
  }

  async #call(body: Record<string, unknown>): Promise<boolean> {
    const project = this.#project
    if (!project) return false
    const a = await request('/api/lessons', body)
    if (project !== this.#project) return false
    this.#busy = ''
    if (!a.ok) {
      const needs = a.body.needs as Needs | undefined
      if (needs?.next === 'cost' && body.op === 'suggest' && !body.costOk) {
        const ok = await consentFor(needs, project, 'suggesting instruction lines')
        if (ok?.costOk) return this.#call({ ...body, costOk: true })
      }
      const pick = body.op === 'approve' && a.field === 'sid' && a.message && typeof body.id === 'string' ? body.id : ''
      if (pick) this.#pickRefused.set(pick, a.message)
      say(this.#said, pick ? '' : a.message, 'warn')
      this.#paint()
      if (pick) document.getElementById(`lesson-session-${pick}`)?.focus()
      return false
    }
    if (body.op === 'suggest') {
      say(this.#said, 'The run started in a reviewer container. Its lines show here when it ends.', 'muted')
      await this.#poll?.now()
      return true
    }
    const v: unknown = a.body
    if (isView(v)) this.#view = v
    this.#paint()
    void this.#poll?.now()
    return true
  }

  #paint() {
    const v = this.#view
    if (!v) {
      this.#body.replaceChildren()
      return
    }
    const parts: Node[] = []
    const titles = new Map(v.findings.map(f => [f.id, f]))
    if (!v.findings.length) {
      parts.push(emptyState(`No blocking finding has come back on this project yet. When one blocks ${v.after} commits, Suggest lines appears here.`))
    } else {
      const list = el('ul', 'small lessons-findings')
      for (const f of v.findings) {
        const where = f.path ? ` in ${text(f.path)}` : ''
        list.append(el('li', '', `${text(f.title)}${where}: ${text(f.agent)} blocked ${f.commits} ${f.commits === 1 ? 'commit' : 'commits'} with it${f.offered ? '' : f.commits >= v.after ? ', and a line you approved covers it' : ''}`))
      }
      parts.push(list)
    }
    const run = v.run
    const running = run?.state === 'running' || this.#busy === 'suggest'
    if (v.offered > 0 || running) {
      const b = button('Suggest lines', 'small', () => {
        if (b.getAttribute('aria-disabled') === 'true') return
        this.#busy = 'suggest'
        this.#paint()
        void this.#call({ op: 'suggest', name: this.#project })
      }, 'button', 'lessons-suggest')
      if (running) busy(b, true)
      parts.push(el('p', 'small muted', `${v.offered} ${v.offered === 1 ? 'finding has' : 'findings have'} blocked ${v.after} or more commits. Suggest lines costs one reviewer run.`), b)
    }
    if (run && run.state !== 'running') {
      const cost = money(run.costUsd)
      const about = `The last run read ${run.given} ${run.given === 1 ? 'finding' : 'findings'}${cost ? `; ≈ ${cost}, Claude Code's own estimate` : ''}.`
      parts.push(el('p', 'small muted', about))
      if (run.reason) parts.push(el('p', `small ${run.state === 'done' ? 'muted' : 'warn'}`, text(run.reason)))
      for (const n of run.notes) parts.push(el('p', 'small muted', text(n)))
    }
    const offered = v.lines.filter(x => x.state === 'offered')
    if (offered.length) {
      parts.push(...pageHead({ title: 'Suggested lines', level: 'h3', headingClass: 'label', intro: 'Lines for the instruction file that a reviewer run proposed from these findings. Read each one before you approve or decline it.', docs: { doc: 'suggested-lines', key: 'suggested-docs' } }))
      const list = el('ul', 'list lessons-lines')
      for (const x of offered) list.append(this.#line(x, v, titles))
      parts.push(list)
    }
    const approved = v.lines.filter(x => x.state === 'approved')
    if (approved.length) {
      const list = el('ul', 'small lessons-sent')
      for (const x of approved) {
        const s = v.sessions.find(y => y.sid === x.session)
        list.append(el('li', '', `${text(x.text)}: sent to ${s ? text(s.name || s.sid) : 'a session'} as a note`))
      }
      parts.push(...pageHead({ title: 'Approved lines', level: 'h3', headingClass: 'label', intro: 'Lines you approved, each sent to a session as a note asking its agent to add it to the instruction file.', docs: { doc: 'approved-lines', key: 'approved-docs' } }), list)
    }
    this.#body.replaceChildren(...parts)
  }

  #line(x: LessonLine, v: LessonsView, titles: Map<string, LessonFinding>): HTMLLIElement {
    const li = el('li')
    li.append(el('div', 'small', text(x.text)))
    const from = x.from.map(id => titles.get(id)).filter((f): f is LessonFinding => !!f).map(f => text(f.title))
    li.append(el('div', 'small muted', from.length ? `From: ${from.join('; ')}` : 'From findings no longer listed'))
    const pick = dropdown({ name: 'sid' })
    pick.id = `lesson-session-${x.id}`
    pick.append(option(v.sessions.length ? 'Pick a session' : 'No session of this project', ''))
    for (const s of v.sessions) pick.append(option(`${text(s.name || s.sid)}${s.running ? '' : ' (stopped: the note waits for its next message)'}`, s.sid))
    pick.value = this.#chosen.get(x.id) ?? ''
    pick.addEventListener('change', () => {
      this.#chosen.set(x.id, pick.value)
      if (this.#pickRefused.delete(x.id)) labelError(pick, '')
    })
    const held = this.#busy === x.id
    const approve = button('Approve', 'small', undefined, 'submit', `lessons-approve:${this.#project}:${x.id}`)
    const send = () => {
      if (held) return
      this.#pickRefused.delete(x.id)
      labelError(pick, '')
      this.#busy = x.id
      this.#doing = 'approve'
      void whileBusy(approve, () => this.#call({ op: 'approve', name: this.#project, id: x.id, sid: pick.value })).then(ok => {
        if (!ok) return
        say(this.#said, '')
        toast({ id: `lessons:${this.#project}`, title: 'Sent to the session as a note with its next message.', tone: 'ok' })
      })
    }
    const decline = button('Decline', 'small ghost', () => {
      if (held) return
      this.#busy = x.id
      this.#doing = 'decline'
      void whileBusy(decline, () => this.#call({ op: 'decline', name: this.#project, id: x.id })).then(ok => {
        if (!ok) return
        say(this.#said, '')
        toast({ id: `lessons:${this.#project}`, title: 'Declined. Nothing was sent or changed.', tone: 'ok' })
      })
    }, 'button', `lessons-decline:${this.#project}:${x.id}`)
    if (held && this.#doing === 'approve') busy(approve, true)
    else approve.setAttribute('aria-disabled', String(held))
    if (held && this.#doing === 'decline') busy(decline, true)
    else decline.setAttribute('aria-disabled', String(held))
    const row = form({ inline: true, cls: 'field-row', children: [field('Send to', pick, { kind: 'inline', cls: 'field-row' }), actions([approve, decline])], onSubmit: send })
    labelError(pick, this.#pickRefused.get(x.id) ?? '')
    li.append(row)
    return li
  }
}

customElements.define('sf-lessons', SfLessons)

declare global {
  interface HTMLElementTagNameMap {
    'sf-lessons': SfLessons
  }
}
