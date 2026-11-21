import { request } from '../api.ts'
import { busy, button, el, focusedKey, labelError, refocus, say, statusLine, unspoofed, visible, whileBusy } from '../lib/dom.ts'
import { actions, field, form } from '../lib/form.ts'
import { consentFor, money } from './consent.ts'
import type { Needs } from '../types.ts'
import { emptyState } from '../lib/page.ts'

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

const POLL_MS = 3000

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
  #again = button('Read again', 'link small', () => {
    if (this.#reads) return
    say(this.#said, '')
    void whileBusy(this.#again, () => this.#call({ op: 'read', name: this.#project }))
  }, 'button', 'lessons-again')
  #timer = 0
  #busy = ''
  #doing = ''
  #chosen = new Map<string, string>()
  #pickRefused = new Map<string, string>()
  #built = false
  #gen = 0
  #reads = 0

  connectedCallback() {
    if (!this.#built) {
      this.#built = true
      const heading = el('h3', 'label', 'Lessons from reviews')
      heading.tabIndex = -1
      heading.dataset.key = 'lessons-heading'
      this.replaceChildren(
        heading,
        el('p', 'small muted', 'The engine counts the blocking findings of this project\'s reviews by reviewer, file and title, with case, numbers and punctuation left out. That matching is approximate: two different findings can count as one, and a reworded finding counts as a new one. Nothing here changes a file or a review on its own.'),
        this.#body,
        this.#said,
      )
    }
    if (this.#project && !this.#view && !this.#reads) void this.#call({ op: 'read', name: this.#project })
    else this.#poll()
  }

  disconnectedCallback() {
    window.clearTimeout(this.#timer)
    this.#timer = 0
  }

  set project(name: string) {
    if (name === this.#project) return
    this.#project = name
    this.#view = null
    if (this.isConnected) void this.#call({ op: 'read', name })
  }

  #poll() {
    window.clearTimeout(this.#timer)
    this.#timer = 0
    if (this.isConnected && this.#view?.run?.state === 'running') this.#timer = window.setTimeout(() => void this.#call({ op: 'read', name: this.#project }), POLL_MS)
  }

  async #call(body: Record<string, unknown>): Promise<boolean> {
    const project = this.#project
    if (!project) return false
    const gen = ++this.#gen
    const read = body.op === 'read'
    if (read) this.#reads++
    this.#hold()
    const a = await request('/api/lessons', body)
    if (read) this.#reads--
    this.#hold()
    if (project !== this.#project || (read && gen !== this.#gen)) return false
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
      if (read) this.#retry(true)
      return false
    }
    if (body.op === 'suggest') {
      say(this.#said, 'The run started in a reviewer container. Its lines show here when it ends.', 'muted')
      return this.#call({ op: 'read', name: project })
    }
    const v: unknown = a.body
    if (project !== this.#project || (read && gen !== this.#gen)) return false
    if (isView(v)) this.#view = v
    this.#paint()
    if (read && isView(v)) this.#retry(false)
    this.#poll()
    return true
  }

  #hold() {
    if (this.#reads) this.#again.setAttribute('aria-disabled', 'true')
    else this.#again.removeAttribute('aria-disabled')
  }

  #retry(on: boolean) {
    if (on === (this.#again.parentNode === this)) return
    if (on) return this.#said.after(this.#again)
    const focused = focusedKey(this)
    this.#again.remove()
    refocus(this, focused, () => ['lessons-suggest', 'lessons-heading'])
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
      b.title = 'Starts a run in a reviewer container of its own, with read-only tools and no verdict, given these findings and your own comments on this project\'s recent pull requests, read from the git host now. It proposes lines for the instruction file; you approve each one.'
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
      parts.push(el('h4', 'label', 'Suggested lines'))
      parts.push(el('p', 'small muted', 'Each line comes from a reviewer run that read text agents and reviewers wrote: read it before you approve it. An approved line goes to the session you pick as a note asking the agent to add it to the instruction file and commit it, which the usual reviews then cover. A declined line isn\'t offered again for the same findings.'))
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
      parts.push(el('h4', 'label', 'Approved lines'), list)
    }
    this.#body.replaceChildren(...parts)
  }

  #line(x: LessonLine, v: LessonsView, titles: Map<string, LessonFinding>): HTMLLIElement {
    const li = el('li')
    li.append(el('div', 'small', text(x.text)))
    const from = x.from.map(id => titles.get(id)).filter((f): f is LessonFinding => !!f).map(f => text(f.title))
    li.append(el('div', 'small muted', from.length ? `From: ${from.join('; ')}` : 'From findings no longer listed'))
    const pick = el('select', 'textbox')
    pick.id = `lesson-session-${x.id}`
    pick.name = 'sid'
    const none = el('option', '', v.sessions.length ? 'Pick a session' : 'No session of this project')
    none.value = ''
    pick.append(none)
    for (const s of v.sessions) {
      const o = el('option', '', `${text(s.name || s.sid)}${s.running ? '' : ' (stopped: the note waits for its next message)'}`)
      o.value = s.sid
      pick.append(o)
    }
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
        say(this.#said, 'Sent to the session as a note with its next message.', 'muted')
      })
    }
    const decline = button('Decline', 'small ghost', () => {
      if (held) return
      this.#busy = x.id
      this.#doing = 'decline'
      void whileBusy(decline, () => this.#call({ op: 'decline', name: this.#project, id: x.id })).then(ok => {
        if (!ok) return
        say(this.#said, 'Declined. Nothing was sent or changed.', 'muted')
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
