import { button, el, emit, remembered, visible, visibleCommand } from '../lib/dom.ts'
import { attachCommand, plural } from '../lib/format.ts'
import type { Answer, AskEvent, PermissionEvent, PlanEvent } from '../types.ts'
import './markdown.ts'

const readOnlyText = (attach?: string) => (attach ? `This session is read-only on this page, so answer in Claude Code itself: ${attachCommand(attach)}` : "This session is read-only on this page, so this can't be answered here.")
const alsoThere = (attach?: string) => (attach ? ` You can also answer in Claude Code itself: ${attachCommand(attach)}` : '')

function start(host: HTMLElement, meta: string, title?: string) {
  host.classList.add('card')
  host.replaceChildren(el('div', 'label meta', meta))
  if (title) host.append(el('h3', '', title))
}

const pending = new WeakSet<HTMLElement>()

export const answering = (host: HTMLElement) => pending.has(host)

function rerender(host: HTMLElement, render: () => void) {
  const focused = document.activeElement
  const key = focused instanceof HTMLElement && host.contains(focused) ? focused.dataset.key : undefined
  render()
  if (pending.has(host)) lock(host, true)
  if (!(focused instanceof HTMLElement) || focused === document.activeElement) return
  if (host.contains(focused)) focused.focus()
  else if (key) host.querySelector<HTMLElement>(`[data-key="${CSS.escape(key)}"]`)?.focus()
}

function lock(host: HTMLElement, locked: boolean) {
  for (const control of host.querySelectorAll<HTMLButtonElement | HTMLInputElement>('button, input')) if (!control.closest('sf-markdown')) control.disabled = locked
}

const watching = new WeakMap<HTMLElement, ResizeObserver>()

export function scrolledText(text: string, name: string, cls: string): [HTMLPreElement, HTMLParagraphElement, ResizeObserver] {
  const box = el('pre', cls, visibleCommand(text))
  box.tabIndex = 0
  box.setAttribute('aria-label', name)
  const more = el('p', 'small warn more')
  more.hidden = true
  const update = () => {
    const left = box.scrollHeight - box.clientHeight - box.scrollTop
    const line = parseFloat(getComputedStyle(box).lineHeight) || 20
    const lines = Math.ceil(left / line)
    more.hidden = box.clientHeight === 0 || lines < 1
    more.textContent = `${lines} more ${plural(lines, 'line')} below; scroll the box to read them`
  }
  const observer = new ResizeObserver(update)
  observer.observe(box)
  box.addEventListener('scroll', update, { passive: true })
  return [box, more, observer]
}

function showCommand(host: HTMLElement, text: string, name: string) {
  watching.get(host)?.disconnect()
  const [box, more, observer] = scrolledText(text, name, 'sum')
  watching.set(host, observer)
  host.append(box, more)
}

function reply(host: HTMLElement, id: string, answer: Answer) {
  if (host.contains(document.activeElement)) host.dataset.answered = ''
  pending.add(host)
  lock(host, true)
  emit(host, 'sf-answer', { id, answer })
}

function fail(host: HTMLElement, message: string) {
  pending.delete(host)
  delete host.dataset.answered
  lock(host, false)
  let note = host.querySelector('.card-error')
  if (!note) note = host.appendChild(el('p', 'small bad card-error'))
  note.textContent = message
}

interface NoteOptions {
  name: string
  placeholder: string
  label: string
  send: string
  required?: boolean
  maxLength?: number
  toggle?: string
  hideToggle?: boolean
}

class NoteForm {
  form = el('form', 'other')
  field = el('input', 'textbox')
  send: HTMLButtonElement
  toggle: HTMLButtonElement | null = null
  onSend: (text: string) => void = () => {}
  #hideToggle = false

  constructor(o: NoteOptions) {
    this.field.name = o.name
    this.field.placeholder = o.placeholder
    this.field.setAttribute('aria-label', o.label)
    if (o.maxLength) this.field.maxLength = o.maxLength
    this.send = button(o.send, '', undefined, 'submit')
    this.form.append(this.field, this.send)
    this.form.addEventListener('submit', e => {
      e.preventDefault()
      const text = this.field.value.trim()
      if (text || !o.required) this.onSend(text)
    })
    if (o.toggle === undefined) return
    this.#hideToggle = !!o.hideToggle
    this.toggle = button(o.toggle, '', () => this.#show(true))
    if (!this.#hideToggle) this.toggle.setAttribute('aria-expanded', 'false')
    this.form.hidden = true
    this.field.addEventListener('keydown', e => {
      if (e.key !== 'Escape') return
      e.preventDefault()
      this.#show(false)
      this.toggle!.focus()
    })
  }

  #show(open: boolean) {
    this.form.hidden = !open
    if (this.#hideToggle) this.toggle!.hidden = open
    else this.toggle!.setAttribute('aria-expanded', String(open))
    if (open) this.field.focus()
  }

  ready(): this {
    for (const control of [this.field, this.send, this.toggle]) if (control) control.disabled = false
    return this
  }
}

class SfAsk extends HTMLElement {
  #card: AskEvent | null = null
  #picks = new Map<string, string[]>()
  #notes = new Map<string, NoteForm>()
  #refresh = () => {}

  set card(value: AskEvent) {
    if (value.id !== this.#card?.id) {
      this.#notes.clear()
      this.#picks = new Map()
      pending.delete(this)
    }
    this.#card = value
    rerender(this, () => this.#render())
  }

  set error(message: string) {
    fail(this, message)
    this.#refresh()
  }

  #render() {
    const { id, questions, readOnly, attach, who = 'Claude' } = this.#card!
    start(this, `${who} asks`)
    if (readOnly) {
      for (const q of questions) {
        this.append(el('h3', '', visible(q.question)))
        if (q.description) this.append(el('p', 'small muted', visible(q.description)))
      }
      return this.append(el('p', 'small muted', readOnlyText(attach)))
    }
    const picks = this.#picks
    const single = questions.length === 1 && !questions[0]!.multiSelect
    const submit = button('Send answers', 'primary', undefined, 'button', 'submit')
    const refresh = () => (submit.disabled = !questions.some(q => picks.get(q.question)?.length))
    this.#refresh = refresh
    questions.forEach((q, qi) => {
      const box = el('div', 'q')
      box.append(el('h3', '', visible(q.question)))
      if (q.description) box.append(el('p', 'small muted', visible(q.description)))
      const opts = el('div', 'options')
      opts.setAttribute('role', 'group')
      opts.setAttribute('aria-label', q.multiSelect ? `${visible(q.question)} (pick any)` : visible(q.question))
      const mark = () => [...opts.children].forEach((b, i) => b.setAttribute('aria-pressed', String((picks.get(q.question) || []).includes(q.options[i]!.label))))
      q.options.forEach((o, oi) => {
        const b = button(visible(o.label), '', undefined, 'button', `opt:${qi}:${oi}`)
        if (o.description) b.append(el('small', '', visible(o.description)))
        b.addEventListener('click', () => {
          if (single) return reply(this, id, { [q.question]: o.label })
          const now = picks.get(q.question) || []
          picks.set(q.question, q.multiSelect ? (now.includes(o.label) ? now.filter(l => l !== o.label) : [...now, o.label]) : [o.label])
          mark()
          refresh()
        })
        opts.append(b)
      })
      let note = this.#notes.get(q.question)
      if (!note) {
        const unit = q.unit ? ` ${visible(q.unit)}` : ''
        const range = q.kind === 'number' && q.min !== undefined && q.max !== undefined ? `${q.min} to ${q.max}${unit}` : ''
        const placeholder = q.placeholder ? visible(q.placeholder) : q.kind === 'number' ? range || `A number${unit}` : q.kind === 'text' || !q.options.length ? 'Type your answer' : 'Or type your own answer'
        note = new NoteForm({ name: 'answer', placeholder, label: `${q.options.length ? 'Your own answer' : 'Your answer'} to: ${visible(q.question)}${range ? ` (${range})` : ''}`, send: single ? 'Send' : 'Use', required: true })
        if (q.kind === 'number') {
          note.field.type = 'number'
          if (q.min !== undefined) note.field.min = String(q.min)
          if (q.max !== undefined) note.field.max = String(q.max)
          note.field.step = q.step ? String(q.step) : 'any'
          if (q.defaultValue !== undefined) note.field.value = String(q.defaultValue)
        }
        this.#notes.set(q.question, note)
      }
      note.onSend = text => {
        if (single) return reply(this, id, { [q.question]: text })
        picks.set(q.question, [text])
        mark()
        refresh()
      }
      box.append(...(q.options.length ? [opts] : []), note.ready().form)
      this.append(box)
      if (!single) mark()
    })
    if (single) return attach ? this.append(el('p', 'small muted', alsoThere(attach).trim())) : undefined
    const actions = el('div', 'actions')
    submit.addEventListener('click', () => {
      const answered = questions.filter(q => picks.get(q.question)?.length)
      if (answered.length) reply(this, id, Object.fromEntries(answered.map(q => [q.question, (picks.get(q.question) ?? []).join(', ')])))
    })
    actions.append(submit, el('span', 'small muted', `A question you leave unanswered is sent without an answer.${alsoThere(attach)}`))
    this.append(actions)
    refresh()
  }
}

class SfPermission extends HTMLElement {
  #card: PermissionEvent | null = null
  #note: NoteForm | null = null

  set card(value: PermissionEvent) {
    if (value.id !== this.#card?.id) {
      this.#note = null
      pending.delete(this)
    }
    this.#card = value
    rerender(this, () => this.#render())
  }

  set error(message: string) {
    fail(this, message)
  }

  #render() {
    const { id, tool, detail, summary, agentId, complete, always, auto, readOnly, attach, who = 'Claude' } = this.#card!
    start(this, agentId ? 'A subagent wants to run a tool' : `${who} wants to run a tool`, `Allow ${visible(tool)}?`)
    if (detail || summary) showCommand(this, detail || summary, `What ${visible(tool)} would run`)
    if (readOnly) return this.append(el('p', 'small muted', readOnlyText(attach)))
    const choices = el('div', 'choices')
    if (complete === false) {
      choices.append(el('p', 'small muted', attach ? `Too long to show in full here, so allow it in Claude Code itself: ${attachCommand(attach)}` : "The page can't show the whole call, so it can only deny it."))
    } else {
      const yes = button('Yes', 'primary', undefined, 'button', 'yes')
      yes.addEventListener('click', () => reply(this, id, { choice: 'yes' }))
      choices.append(yes)
      if (always) {
        const again = button(visible(always), '', undefined, 'button', 'always')
        again.addEventListener('click', () => reply(this, id, { choice: 'always' }))
        choices.append(again)
      }
      if (auto) {
        const switching = button('Yes, and switch to auto mode', '', undefined, 'button', 'auto')
        switching.addEventListener('click', () => reply(this, id, { choice: 'auto' }))
        choices.append(switching)
      }
    }
    const name = who === 'Claude' || who === 'Codex' ? who : 'the agent'
    const note = (this.#note ??= new NoteForm({ name: 'note', placeholder: `What should ${name} do instead? Optional`, label: `What ${name} should do instead`, send: 'Deny', maxLength: 2000, toggle: `No, and tell ${name} what to do differently` })).ready()
    note.onSend = text => reply(this, id, text ? { choice: 'no', note: text } : { choice: 'no' })
    choices.append(note.toggle!)
    const footer = attach ? `No without a note also stops Claude’s current turn.${alsoThere(attach)}` : ''
    this.append(choices, note.form, ...(footer ? [el('p', 'small muted', footer)] : []))
  }
}

class SfPlan extends HTMLElement {
  #card: PlanEvent | null = null
  #note: NoteForm | null = null
  #open = new Map<string, boolean>()

  set card(value: PlanEvent) {
    if (value.id !== this.#card?.id) {
      this.#note = null
      pending.delete(this)
    }
    this.#card = value
    rerender(this, () => this.#render())
  }

  set error(message: string) {
    fail(this, message)
  }

  #render() {
    const { id, plan, readOnly, attach } = this.#card!
    start(this, 'Plan ready', 'Claude is waiting for you to approve its plan.')
    if (plan) {
      const body = el('div', 'reply')
      const text = el('sf-markdown', 'body')
      text.text = plan
      body.append(text)
      this.append(remembered(this.#open, 'plan', true, el('summary', 'small', 'The plan'), body))
    }
    const where = attach ? `run ${attachCommand(attach)} and answer there` : 'answer in Claude Code itself'
    if (readOnly) return this.append(el('p', 'small muted', `To approve it or ask for changes, ${where}.`))
    const note = (this.#note ??= new NoteForm({ name: 'note', placeholder: 'What should Claude change?', label: 'What Claude should change in the plan', send: 'Tell Claude what to change', maxLength: 2000, required: true })).ready()
    note.onSend = text => reply(this, id, { choice: 'revise', note: text })
    const approve = button('Approve in Claude Code', '', undefined, 'button', 'approve')
    approve.addEventListener('click', () => reply(this, id, { choice: 'terminal' }))
    const choices = el('div', 'choices')
    choices.append(approve)
    this.append(note.form, choices, el('p', 'small muted', `Claude Code lets only its own prompt approve a plan. Approve in Claude Code shows the usual prompt there; then ${where}.`))
  }
}

customElements.define('sf-ask', SfAsk)
customElements.define('sf-permission', SfPermission)
customElements.define('sf-plan', SfPlan)

declare global {
  interface HTMLElementTagNameMap {
    'sf-ask': SfAsk
    'sf-permission': SfPermission
    'sf-plan': SfPlan
  }
}
