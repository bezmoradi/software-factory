import { request, savedTone, type EngineAnswer } from '../api.ts'
import { busy, button, el, emit, focusedKey, labelError, refocus, shown, unspoofed, visibleCommand } from '../lib/dom.ts'
import type { Tone } from '../lib/dom.ts'
import { actions, field, form } from '../lib/form.ts'
import { label } from '../lib/format.ts'
import { saidOf } from '../lib/json.ts'
import type { SessionInfo, SplitCard } from '../types.ts'
import { textbox, textarea } from '../lib/controls.ts'

export interface SplitPanelView {
  sid: string
  info: SessionInfo
  sessions: SessionInfo[]
  disabled: string
}

export type SplitStarter = (sid: string, card: string) => Promise<EngineAnswer>


function tasksOf(goal: string, sessions: SessionInfo[]): SessionInfo[] {
  return sessions.filter(s => s.lineage?.groupKind === 'goal' && s.lineage.group === goal)
}

class SfSplit extends HTMLElement {
  start: SplitStarter | null = null
  #view: SplitPanelView | null = null
  #key = ''
  #editing = new Map<string, { title: string; message: string }>()
  #busy = new Map<string, string>()
  #notes = new Map<string, { text: string; tone: Tone }>()
  #refused = new Map<string, { field: string; text: string }>()

  connectedCallback() {
    if (!this.#view) this.hidden = true
  }

  set view(value: SplitPanelView | null) {
    this.#view = value
    const goal = value ? tasksOf(value.sid, value.sessions).map(s => [s.sid, s.running === true, label(s)]) : []
    const key = JSON.stringify([value?.sid, value?.info.splits, value?.info.splitGoal, value?.info.lineage, value?.disabled, goal, [...this.#editing], [...this.#busy], [...this.#notes], [...this.#refused]])
    if (key !== this.#key) {
      this.#key = key
      this.paint()
    }
  }

  paint() {
    const v = this.#view
    const parts: Node[] = []
    if (v) {
      const goal = v.info.lineage?.groupKind === 'goal' ? v.info.lineage.group : ''
      if (goal) parts.push(this.#taskOf(goal, v.sessions))
      const cards = (v.info.splits ?? []).filter(c => c.state !== 'dismissed')
      if (cards.length) parts.push(...this.#cards(v, cards))
    }
    this.hidden = !parts.length
    const focused = focusedKey(this)
    this.replaceChildren(...parts)
    refocus(this, focused)
  }

  #taskOf(goal: string, sessions: SessionInfo[]): HTMLElement {
    const box = el('div', 'notice small')
    const parent = sessions.find(s => s.sid === goal)
    box.append(el('p', '', `This session is one task of the goal in session ${parent ? shown(label(parent)) : 'that was split'}. The agent there proposed it, and you started it.`))
    if (parent) box.append(button('Open the goal session', 'ghost small', () => emit(this, 'sf-open-session', { sid: goal })))
    return box
  }

  #cards(v: SplitPanelView, cards: SplitCard[]): Node[] {
    const goal = v.info.splitGoal
    const head = el('div', 'row split-head')
    head.append(el('h2', 'label', 'Tasks the agent proposed'))
    if (goal) head.append(el('span', 'small muted', `${goal.running} of ${goal.limit} of this goal's sessions running`))
    const intro = el('p', 'small muted', `The agent wrote these with factory_split. Each card shows its text exactly as written; read it before you start it, since it becomes the new session's first message. Start runs a normal session from main with this session's agent and work type, through the usual start-up, sign-in and cost checks.${goal ? ` At most ${goal.limit} run at once; a Start beyond that queues the task, and the engine starts it when one of them stops.` : ''}`)
    return [head, intro, ...cards.map(c => this.#card(v, c))]
  }

  #card(v: SplitPanelView, c: SplitCard): HTMLElement {
    const box = el('div', 'forge-card split-card')
    const { label: word, tone } = saidOf(c.said, c.state)
    const head = el('div', 'row forge-head')
    head.append(el('span', `tag ${tone}`, word), el('span', 'tag', 'written by the agent'))
    if (c.edited) head.append(el('span', 'tag', 'edited by you'))
    box.append(head)
    const editing = this.#editing.get(c.card)
    const open = c.state === 'waiting' || c.state === 'queued'
    if (editing && open) box.append(this.#editor(v, c, editing))
    else {
      const title = el('pre', 'forge-body split-title', visibleCommand(unspoofed(c.title)))
      title.tabIndex = 0
      title.setAttribute('aria-label', 'Title of the proposed task')
      box.append(title)
      if (c.message) {
        const body = el('pre', 'forge-body', visibleCommand(unspoofed(c.message)))
        body.tabIndex = 0
        body.setAttribute('aria-label', 'First message of the proposed task')
        box.append(body)
      }
    }
    if (c.secretAt && open) box.append(el('p', 'small bad', `This looks like a token (${shown(c.secretAt)}). Edit it out before starting.`))
    const queue = v.info.splitGoal?.queue.length ?? 0
    if (c.note) box.append(el('p', 'small warn', `${c.place && queue ? `In the queue: ${c.place} of ${queue}. ` : ''}${shown(c.note)}`))
    if (c.state === 'started' && c.sid) {
      const task = v.sessions.find(s => s.sid === c.sid)
      const line = el('div', 'row split-started')
      line.append(el('span', 'small', task ? `Session ${shown(label(task))}, ${task.running ? 'running' : 'stopped'}` : 'Its session was deleted.'))
      if (task) line.append(button('Open', 'ghost small', () => emit(this, 'sf-open-session', { sid: c.sid ?? '' }), 'button', `open:${c.card}`))
      box.append(line)
    }
    const note = this.#notes.get(c.card)
    if (note) box.append(el('p', `small ${note.tone}`, note.text))
    if (open && !editing && c.actions) box.append(this.#actions(v, c))
    return box
  }

  #actions(v: SplitPanelView, c: SplitCard): HTMLElement {
    const off = !!v.disabled || this.#busy.has(c.card)
    const offered = c.actions ?? {}
    const row = el('div', 'actions forge-actions')
    const add = (name: 'start' | 'unqueue' | 'edit' | 'dismiss', b: HTMLButtonElement) => {
      const why = offered[name]
      if (why === undefined) return
      b.setAttribute('aria-disabled', String(off || !!why))
      if (v.disabled) b.title = v.disabled
      else if (why) b.title = shown(why)
      if (this.#busy.get(c.card) === name) busy(b, true)
      row.append(b)
    }
    add('start', button('Start', 'primary small', () => {
      if (!off && !offered.start) void this.#start(c)
    }, 'button', `start:${c.card}`))
    add('unqueue', button('Remove from queue', 'small', () => {
      if (!off) void this.#call(c, { op: 'unqueue', sid: v.sid, card: c.card }, 'Taken out of the queue. Press Start to start it.')
    }, 'button', `unqueue:${c.card}`))
    add('edit', button('Edit', 'small', () => {
      if (off) return
      this.#editing.set(c.card, { title: c.title, message: c.message })
      this.#key = ''
      this.view = this.#view
    }, 'button', `edit:${c.card}`))
    add('dismiss', button('Dismiss', 'small ghost', () => {
      if (!off) void this.#call(c, { op: 'dismiss', sid: v.sid, card: c.card }, '')
    }, 'button', `dismiss:${c.card}`))
    return row
  }

  #editor(v: SplitPanelView, c: SplitCard, draft: { title: string; message: string }): HTMLElement {
    const title = textbox({ name: 'title', value: draft.title })
    title.setAttribute('aria-required', 'true')
    title.dataset.key = `title:${c.card}`
    const message = textarea({ rows: 8, name: 'message', value: draft.message })
    message.setAttribute('aria-required', 'true')
    message.dataset.key = `message:${c.card}`
    const inputs: Record<string, HTMLInputElement | HTMLTextAreaElement> = { title, message }
    for (const [name, input] of Object.entries(inputs)) {
      input.addEventListener('input', () => {
        this.#editing.set(c.card, { title: title.value, message: message.value })
        if (this.#refused.get(c.card)?.field !== name) return
        this.#refused.delete(c.card)
        labelError(input, '')
      })
    }
    const save = button('Save', 'primary small', undefined, 'submit', `save:${c.card}`)
    if (this.#busy.get(c.card) === 'edit') busy(save, true)
    const cancel = button('Cancel', 'small ghost', () => {
      this.#editing.delete(c.card)
      this.#key = ''
      this.view = this.#view
    })
    const edit = form({
      cls: 'stacked split-edit',
      children: [field('Task title', title), field('First message of the task', message), actions([cancel, save])],
      onSubmit: () => {
        void this.#call(c, { op: 'edit', sid: v.sid, card: c.card, title: title.value, message: message.value }, null).then(ok => {
          if (ok) this.#editing.delete(c.card)
          this.#key = ''
          this.view = this.#view
          const own = this.#refused.get(c.card)
          if (own) this.querySelector<HTMLElement>(`[data-key="${CSS.escape(`${own.field}:${c.card}`)}"]`)?.focus()
        })
      },
    })
    const refused = this.#refused.get(c.card)
    const bad = refused && Object.hasOwn(inputs, refused.field) ? inputs[refused.field] : undefined
    if (refused && bad) labelError(bad, refused.text)
    return edit
  }

  async #call(c: SplitCard, body: Record<string, unknown>, done: string | null): Promise<boolean> {
    this.#busy.set(c.card, typeof body.op === 'string' ? body.op : '')
    this.#key = ''
    this.view = this.#view
    this.#refused.delete(c.card)
    const a = await request('/api/split', body)
    this.#busy.delete(c.card)
    const onField = body.op === 'edit' && (a.field === 'title' || a.field === 'message') && a.message
    if (a.ok) {
      const text = done ?? a.message
      if (text) this.#notes.set(c.card, { text, tone: done === null ? savedTone(a) : 'muted' })
      else this.#notes.delete(c.card)
    } else if (onField) {
      this.#notes.delete(c.card)
      this.#refused.set(c.card, { field: a.field, text: a.message })
    } else this.#notes.set(c.card, { text: a.message, tone: 'bad' })
    this.#key = ''
    this.view = this.#view
    return a.ok
  }

  async #start(c: SplitCard) {
    const v = this.#view
    if (!v || !this.start || this.#busy.has(c.card)) return
    this.#busy.set(c.card, 'start')
    this.#notes.delete(c.card)
    this.#key = ''
    this.paint()
    const a = await this.start(v.sid, c.card)
    this.#busy.delete(c.card)
    if (a.ok) this.#notes.set(c.card, { text: 'Started. It gets this text as its first message once it connects.', tone: 'muted' })
    else if (a.error === 'goal full') this.#notes.delete(c.card)
    else this.#notes.set(c.card, { text: a.message, tone: 'bad' })
    this.#key = ''
    this.view = this.#view
  }
}

customElements.define('sf-split', SfSplit)

declare global {
  interface HTMLElementTagNameMap {
    'sf-split': SfSplit
  }
}
