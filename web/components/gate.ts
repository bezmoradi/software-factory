import { button, el, emit, focusedKey, labelError, refocus, remembered, replaceKeeping, statusLine, visible } from '../lib/dom.ts'
import { diffText, lineDiff } from '../lib/diff.ts'
import { actions, field, form } from '../lib/form.ts'
import type { AsideRun, HoldSetting, HoldTyped, PlanApproval, ProjectAgents } from '../types.ts'
import './markdown.ts'
import { asideBox } from './aside-run.ts'
import { textbox, dropdown, option, checkbox } from '../lib/controls.ts'
import { codeBlock } from './code-block.ts'

class SfApproval extends HTMLElement {
  #view: PlanApproval | null = null
  #key = ''
  #open = new Map<string, boolean>()
  #doc: { details: HTMLDetailsElement; summary: HTMLElement; hash: string } | null = null
  #field = textbox({ name: 'note' })
  #fieldWords = document.createTextNode('What the agent should change in the plan')
  #fieldBox = field(this.#fieldWords, this.#field)

  constructor() {
    super()
    this.#field.dataset.key = 'note'
    this.#field.setAttribute('aria-required', 'true')
    this.#field.addEventListener('input', () => this.refuse(''))
  }

  set view(value: PlanApproval | null) {
    const key = JSON.stringify(value && { ...value, text: value.text.length, previous: value.previous?.length })
    if (key === this.#key) return
    this.#key = key
    this.#view = value
    this.hidden = !value
    const focused = focusedKey(this)
    this.#render()
    refocus(this, focused)
  }

  openDocument() {
    const doc = this.#doc
    if (!doc?.details.isConnected) return
    this.#open.set(doc.hash, true)
    doc.details.open = true
    doc.summary.focus({ preventScroll: true })
    doc.details.scrollIntoView({ block: 'start' })
  }

  sent(note: string) {
    if (this.#field.value === note) this.#field.value = ''
    this.refuse('')
  }

  refuse(why: string, field = '') {
    const mine = !!why && field === 'note'
    labelError(this.#field, mine ? why : '')
    this.#noteWhy.textContent = mine ? '' : why
    if (mine && !this.#field.disabled) this.#field.focus()
  }

  #changes(v: PlanApproval): HTMLElement {
    if (!v.previous) return el('p', 'small muted', 'The engine doesn\'t hold the text you approved before, so it can\'t show what changed. Read them again in full.')
    const lines = lineDiff(v.previous, v.text)
    if (!lines) return el('p', 'small muted', 'The requirements are too long to compare here. Read them again in full.')
    const { text, added, removed } = diffText(lines)
    if (!added && !removed) return el('p', 'small muted', 'Only invisible changes, such as spacing at the ends of lines. Read them again in full.')
    const diff = codeBlock({ text: visible(text), label: 'What changed in the requirements since you approved them, as lines added and removed', diff: true, wrap: true })
    const summary = el('summary', 'small', `What changed since you approved them: ${added} ${added === 1 ? 'line' : 'lines'} added, ${removed} removed`)
    summary.dataset.key = 'changes'
    return remembered(this.#open, `changes:${v.hash}`, true, summary, diff)
  }

  #planBox: HTMLElement | null = null
  #planState: { run: AsideRun | null; asking: boolean } = { run: null, asking: false }
  #planKey = ''

  set planReview(value: { run: AsideRun | null; asking: boolean } | null) {
    const next = value ?? { run: null, asking: false }
    const key = JSON.stringify(next)
    if (key === this.#planKey) return
    this.#planKey = key
    this.#planState = next
    const v = this.#view
    if (!v || !this.#planBox?.isConnected) return
    const focused = document.activeElement?.id === 'plan-review-button'
    const box = this.#planReview(v)
    this.#planBox.replaceWith(box)
    this.#planBox = box
    if (focused) document.getElementById('plan-review-button')?.focus({ preventScroll: true })
  }

  #planReview(v: PlanApproval): HTMLElement {
    const t = this.#planState.run
    const stale = t && t.planHash && t.planHash !== v.hash ? ', of an earlier version of the plan' : ''
    return asideBox({
      id: 'plan-review-button', title: 'Second opinion on the plan', start: 'Review this plan', busy: 'Reviewing the plan…', label: 'The review of the plan',
      hint: 'Asks Claude Code in a reviewer container of its own, with read-only tools, to review this version of the plan against the code. It approves nothing and changes no gate state',
      intro: 'Before you approve, a reviewer run can read the plan against the code and list what looks wrong or missing. It costs one run, and nothing reads it but you.',
      run: t, asking: this.#planState.asking, disabled: v.disabled, stale,
      press: () => emit(this, 'sf-plan-review', { hash: v.hash }),
    })
  }

  #why = statusLine('small warn approval-why')
  #noteWhy = statusLine('small warn')

  #render() {
    const v = this.#view
    if (!v) {
      this.#why.textContent = ''
      this.#doc = null
      return this.replaceChildren()
    }
    const reqs = v.kind === 'requirements'
    const cause = v.kind === 'root-cause'
    const what = reqs ? 'the requirements' : cause ? 'the root cause' : 'the plan'
    this.#fieldWords.data = `What the agent should change in ${what}`
    const status = v.status
    const body = el('div', 'reply')
    const text = el('sf-markdown', 'body')
    text.exact = true
    text.text = v.text || (reqs ? 'Nothing is agreed yet.' : cause ? 'The root cause is empty.' : 'The plan is empty.')
    body.append(text)
    const file = visible(v.path.split('/').pop() ?? 'the plan')
    const draft = !!v.draft && !v.approved
    const words = draft ? (reqs ? 'Read the draft requirements' : `Read the draft of ${file}`) : reqs ? 'Read the agreed requirements' : `Read ${file}`
    const summary = el('summary', 'small', words)
    summary.dataset.key = 'read'
    const details = remembered(this.#open, v.hash, false, summary, body)
    details.classList.add('approval-doc')
    this.#doc = { details, summary, hash: v.hash }
    const read = button(words, 'link small approval-read', () => emit(this, 'sf-read-document'), 'button', 'read-link')
    if (draft) return this.#quiet([read, details])
    const parts: HTMLElement[] = [el('p', `small ${v.approved ? 'ok' : 'warn'}`, status)]
    if (reqs && v.changed && !v.approved) parts.push(this.#changes(v))
    parts.push(read, details)
    if (!reqs && !cause) {
      this.#planBox = this.#planReview(v)
      parts.push(this.#planBox)
    }
    if (v.approved) {
      if (reqs && v.planSwitch) parts.push(this.#planSwitch(v, v.planSwitch))
      return this.#quiet(parts)
    }
    const approveButton = (label: string, key: string, plan?: boolean) => button(label, 'ghost', () => {
      if (!this.#open.has(v.hash)) {
        this.#why.textContent = `Read this version of ${what} first: it is open above. Then approve.`
        if (!details.getClientRects().length) return emit(this, 'sf-read-document')
        details.open = true
        summary.focus()
        return
      }
      if (!v.hold) emit(this, 'sf-approve', plan === undefined ? { hash: v.hash } : { hash: v.hash, plan })
    }, 'button', key)
    const approves = reqs && v.planChoice
      ? [approveButton('Approve and plan', 'approve-plan', true), approveButton('Approve and implement', 'approve-implement', false)]
      : [approveButton(reqs ? 'Approve requirements' : cause ? 'Approve root cause' : 'Approve plan', 'approve')]
    const ask = button('Ask for changes', 'ghost', undefined, 'submit', 'ask')
    const revise = form({
      inline: true,
      cls: 'other field-row',
      children: [this.#fieldBox, actions([ask])],
      onSubmit: () => {
        this.refuse('')
        emit(this, 'sf-revise', { note: this.#field.value })
      },
    })
    for (const control of [...approves, ask, this.#field]) control.disabled = v.disabled
    if (v.hold) for (const approve of approves) approve.setAttribute('aria-disabled', 'true')
    const row = el('div', 'choices field-row')
    row.append(...approves)
    parts.push(row)
    if (v.note) parts.push(el('p', 'small muted', v.note))
    const why = v.refused || v.hold || ''
    this.#why.id = `${reqs ? 'req' : cause ? 'cause' : 'plan'}-approval-why`
    this.#noteWhy.id = `${reqs ? 'req' : cause ? 'cause' : 'plan'}-note-why`
    if (why) for (const approve of approves) approve.setAttribute('aria-describedby', this.#why.id)
    this.#why.classList.remove('status-slot')
    replaceKeeping(this, [...parts, this.#why, revise, this.#noteWhy])
    if (this.#why.textContent !== why) this.#why.textContent = why
  }

  #planSwitch(v: PlanApproval, offered: 'add' | 'skip'): HTMLElement {
    const add = offered === 'add'
    const change = button(add ? 'Add a plan' : 'Skip the plan', 'ghost small', () => emit(this, 'sf-approve', { hash: v.hash, plan: add }), 'button', 'plan-switch')
    change.disabled = v.disabled
    const row = el('div', 'choices field-row')
    row.append(change)
    return row
  }

  #quiet(parts: HTMLElement[]) {
    this.refuse('')
    this.#why.textContent = ''
    this.#why.classList.add('status-slot')
    replaceKeeping(this, [...parts, this.#why])
  }
}

class HoldFields {
  #after = checkbox()
  #files = textbox({ type: 'number', inputMode: 'numeric' })
  #lines = textbox({ type: 'number', inputMode: 'numeric' })
  #refused = new Map<HTMLInputElement, string>()

  constructor(changed: () => void) {
    this.#after.id = 'project-hold-after'
    this.#after.dataset.key = 'hold-after'
    this.#after.setAttribute('aria-describedby', 'project-hold-help')
    for (const [input, id] of [[this.#files, 'project-hold-files'], [this.#lines, 'project-hold-lines']] as const) {
      input.id = id
      input.dataset.key = id
      input.setAttribute('aria-describedby', 'project-hold-help')
      input.addEventListener('input', () => this.#clear(input))
    }
    for (const input of [this.#after, this.#files, this.#lines]) {
      input.addEventListener('change', () => {
        this.#clearAll()
        changed()
      })
    }
  }

  #clear(input: HTMLInputElement) {
    if (this.#refused.delete(input)) labelError(input, '')
  }

  #clearAll() {
    for (const input of [...this.#refused.keys()]) this.#clear(input)
  }

  refuse(field: string, message: string): boolean {
    const input = new Map([['afterBlock', this.#after], ['maxFiles', this.#files], ['maxLines', this.#lines]]).get(field)
    if (!input || !message) return false
    this.#clearAll()
    this.#refused.set(input, message)
    labelError(input, message)
    if (!input.disabled) input.focus()
    return true
  }

  get value(): HoldTyped {
    return { afterBlock: this.#after.checked, maxFiles: this.#files.value, maxLines: this.#lines.value }
  }

  show(h: HoldSetting, disabled: boolean) {
    this.#clearAll()
    this.#after.checked = h.afterBlock
    this.#files.value = String(h.maxFiles)
    this.#lines.value = String(h.maxLines)
    for (const c of [this.#after, this.#files, this.#lines]) c.disabled = disabled
  }

  rows(): HTMLElement[] {
    const row = (label: string, control: HTMLElement) => field(label, control, { kind: 'inline', cls: 'field-row' })
    const after = field('Hold the agent\'s own push after a reviewer blocked on its branch', this.#after, { kind: 'check' })
    const help = el('p', 'small muted', 'Off by default; 0 means no limit. Only the agent\'s own push waits for your Push, with the reason on its card; your Push on the page isn\'t held. The engine counts files and lines, never which files.')
    help.id = 'project-hold-help'
    return [
      after,
      row('Hold it above this many changed files', this.#files),
      row('Or above this many changed lines', this.#lines),
      help,
    ]
  }
}

class SfProjectAgents extends HTMLElement {
  #view: ProjectAgents | null = null
  #key = ''
  #rounds = dropdown()
  #type = dropdown()
  #findings = checkbox()
  #comment = checkbox()
  #hold = new HoldFields(() => {
    const v = this.#view
    if (v?.project) emit(this, 'sf-project-setting', { path: v.project, op: 'hold', hold: this.#hold.value })
  })

  constructor() {
    super()
    this.#comment.id = 'project-pr-comment'
    this.#comment.dataset.key = 'prComment'
    this.#comment.setAttribute('aria-describedby', 'project-pr-comment-help')
    this.#comment.addEventListener('change', () => {
      const v = this.#view
      if (v?.project) emit(this, 'sf-project-setting', { path: v.project, op: 'prComment', on: this.#comment.checked })
    })
    this.#findings.id = 'project-findings'
    this.#findings.dataset.key = 'findings'
    this.#findings.setAttribute('aria-describedby', 'project-findings-help')
    this.#findings.addEventListener('change', () => {
      const v = this.#view
      if (v?.project) emit(this, 'sf-project-setting', { path: v.project, op: 'findings', findings: this.#findings.checked })
    })
    this.#rounds.id = 'project-rounds'
    this.#rounds.dataset.key = 'rounds'
    this.#rounds.addEventListener('change', () => {
      const v = this.#view
      if (v?.project) emit(this, 'sf-project-setting', { path: v.project, op: 'rounds', rounds: Number(this.#rounds.value) })
    })
    this.#type.id = 'project-type'
    this.#type.dataset.key = 'type'
    this.#type.addEventListener('change', () => {
      const v = this.#view
      if (v?.project) emit(this, 'sf-project-setting', { path: v.project, op: 'type', type: this.#type.value })
    })
  }

  reset() {
    this.#key = ''
    this.view = this.#view
  }

  refuse(field: string, message: string): boolean {
    return this.#hold.refuse(field, message)
  }

  set view(value: ProjectAgents | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    this.#view = value
    this.hidden = !value
    const focused = focusedKey(this)
    this.#render()
    refocus(this, focused)
  }

  #render() {
    const v = this.#view
    if (!v) return this.replaceChildren()
    this.replaceChildren(el('p', 'small', 'This project\'s settings'))
    if (!v.project) return this.append(el('p', 'small muted', 'Add this repository on the Projects page to keep settings for it.'))
    this.#type.replaceChildren(...[['', 'The team file\'s default, else Code change'], ...v.types].map(([value, label]) => option(visible(label ?? ''), value ?? '')))
    if (this.#rounds.options.length !== v.roundsMax) {
      this.#rounds.replaceChildren(...Array.from({ length: v.roundsMax }, (_, i) => option(String(i + 1), String(i + 1))))
    }
    this.#rounds.value = String(v.rounds)
    this.#type.value = v.types.some(([id]) => id === v.type) ? v.type : ''
    this.#findings.checked = v.autoFindings
    this.#comment.checked = v.prComment
    for (const c of [this.#rounds, this.#type, this.#findings, this.#comment]) c.disabled = v.disabled
    this.#hold.show(v.hold ?? { afterBlock: false, maxFiles: 0, maxLines: 0 }, v.disabled)
    const rounds = field('Review rounds before you decide', this.#rounds, { kind: 'inline', cls: 'field-row' })
    const type = field('New sessions start as', this.#type, { kind: 'inline', cls: 'field-row' })
    const findings = field('Send blocking findings to the agent automatically', this.#findings, { kind: 'check' })
    const findingsHelp = el('p', 'small muted', 'When reviews the agent asked for block its push, the engine sends the agent the reviewers\' final messages, marked as untrusted, so it can fix and push again. Only when the agent and the reviewers use a subscription, and never past the round limit or 12 automatic review runs on a branch.')
    findingsHelp.id = 'project-findings-help'
    const comment = field('Comment on the pull request after each push', this.#comment, { kind: 'check' })
    const commentHelp = el('p', 'small muted', 'Off by default. When on, the engine posts a short note under your token on the branch\'s open pull request after each push: the commit, who caused the push and which reviews passed, from its own records only. The page shows each note.')
    commentHelp.id = 'project-pr-comment-help'
    this.append(form({ children: [rounds, el('p', 'small muted', 'Kept for the round limit: after this many rounds of one reviewer blocking on a branch, you decide on the push.'), type, findings, findingsHelp, comment, commentHelp, ...this.#hold.rows()] }))
  }
}

customElements.define('sf-approval', SfApproval)
customElements.define('sf-project-agents', SfProjectAgents)

declare global {
  interface HTMLElementTagNameMap {
    'sf-approval': SfApproval
    'sf-project-agents': SfProjectAgents
  }
}
