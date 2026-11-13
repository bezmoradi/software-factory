import { button, el, emit, focusedKey, refocus, remembered, visible } from '../lib/dom.ts'
import { diffText, lineDiff } from '../lib/diff.ts'
import type { HoldSetting, PlanApproval, ProjectAgents } from '../types.ts'
import './markdown.ts'

class SfApproval extends HTMLElement {
  #view: PlanApproval | null = null
  #key = ''
  #open = new Map<string, boolean>()
  #field = el('input', 'textbox')

  constructor() {
    super()
    this.#field.name = 'note'
    this.#field.maxLength = 2000
    this.#field.placeholder = 'What should the agent change?'
    this.#field.setAttribute('aria-label', 'What the agent should change in the plan')
    this.#field.dataset.key = 'note'
    this.#field.required = true
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

  sent(note: string) {
    if (this.#field.value.trim() === note) this.#field.value = ''
  }

  #changes(v: PlanApproval): HTMLElement {
    if (!v.previous) return el('p', 'small muted', 'The engine doesn\'t hold the text you approved before, so it can\'t show what changed. Read them again in full.')
    const lines = lineDiff(v.previous, v.text)
    if (!lines) return el('p', 'small muted', 'The requirements are too long to compare here. Read them again in full.')
    const { text, added, removed } = diffText(lines)
    if (!added && !removed) return el('p', 'small muted', 'Only invisible changes, such as spacing at the ends of lines. Read them again in full.')
    const pre = el('pre', 'diff small', visible(text))
    pre.tabIndex = 0
    pre.setAttribute('aria-label', 'What changed in the requirements since you approved them, as lines added and removed')
    const summary = el('summary', 'small', `What changed since you approved them: ${added} ${added === 1 ? 'line' : 'lines'} added, ${removed} removed`)
    summary.dataset.key = 'changes'
    return remembered(this.#open, `changes:${v.hash}`, true, summary, pre)
  }

  #why = (() => {
    const p = el('p', 'small warn approval-why')
    p.setAttribute('role', 'status')
    return p
  })()

  #render() {
    const v = this.#view
    if (!v) return this.replaceChildren()
    const reqs = v.kind === 'requirements'
    const what = reqs ? 'the requirements' : 'the plan'
    this.#field.setAttribute('aria-label', `What the agent should change in ${what}`)
    const status = v.approved ? `You approved this version of ${what}.` : v.changed ? `${reqs ? 'The requirements' : 'The plan'} changed after you approved ${reqs ? 'them' : 'it'}. Read ${reqs ? 'them' : 'it'} and approve again.` : reqs ? 'Plan starts once you read the agreed requirements and approve them.' : 'Implement starts once you read the plan and approve it.'
    const body = el('div', 'reply')
    const text = el('sf-markdown', 'body')
    text.exact = true
    text.text = v.text || (reqs ? 'Nothing is agreed yet.' : 'The plan is empty.')
    body.append(text)
    const file = visible(v.path.split('/').pop() ?? 'the plan')
    const draft = !!v.draft && !v.approved
    const summary = el('summary', 'small', draft ? (reqs ? 'Read the draft requirements' : `Read the draft of ${file}`) : reqs ? 'Read the agreed requirements' : `Read ${file}`)
    summary.dataset.key = 'read'
    const details = remembered(this.#open, v.hash, false, summary, body)
    if (draft) return this.replaceChildren(details)
    this.replaceChildren(el('p', `small ${v.approved ? 'ok' : 'warn'}`, status))
    if (reqs && v.changed && !v.approved) this.append(this.#changes(v))
    this.append(details)
    if (v.approved) return
    const approve = button(reqs ? 'Approve requirements' : 'Approve plan', 'ghost', () => {
      if (!this.#open.has(v.hash)) {
        details.open = true
        this.#why.textContent = `Read this version of ${what} first: it is open above. Then approve.`
        summary.focus()
        return
      }
      if (!v.hold) emit(this, 'sf-approve', { hash: v.hash })
    }, 'button', 'approve')
    const form = el('form', 'other field-row')
    const ask = button('Ask for changes', 'ghost', undefined, 'submit', 'ask')
    form.append(this.#field, ask)
    form.addEventListener('submit', e => {
      e.preventDefault()
      const note = this.#field.value.trim()
      if (!note) {
        this.#field.value = ''
        this.#field.reportValidity()
        return this.#field.focus()
      }
      emit(this, 'sf-revise', { note })
    })
    for (const control of [approve, ask, this.#field]) control.disabled = v.disabled
    if (v.hold) approve.setAttribute('aria-disabled', 'true')
    const row = el('div', 'choices field-row')
    row.append(approve)
    this.append(row)
    if (v.note) this.append(el('p', 'small muted', v.note))
    const why = v.refused || v.hold || ''
    this.#why.id = `${reqs ? 'req' : 'plan'}-approval-why`
    if (why) approve.setAttribute('aria-describedby', this.#why.id)
    this.append(this.#why, form)
    if (this.#why.textContent !== why) this.#why.textContent = why
  }
}

class HoldFields {
  #after = el('input')
  #files = el('input', 'textbox')
  #lines = el('input', 'textbox')
  #shown: HoldSetting = { afterBlock: false, maxFiles: 0, maxLines: 0 }

  constructor(changed: () => void) {
    this.#after.type = 'checkbox'
    this.#after.id = 'project-hold-after'
    this.#after.dataset.key = 'hold-after'
    this.#after.setAttribute('aria-describedby', 'project-hold-help')
    this.#after.addEventListener('change', changed)
    for (const [field, id, max] of [[this.#files, 'project-hold-files', 100000], [this.#lines, 'project-hold-lines', 10000000]] as const) {
      field.id = id
      field.dataset.key = id
      field.type = 'number'
      field.inputMode = 'numeric'
      field.min = '0'
      field.max = String(max)
      field.step = '1'
      field.setAttribute('aria-describedby', 'project-hold-help')
      field.addEventListener('change', () => {
        const n = Number(field.value)
        if (!Number.isInteger(n) || n < 0 || n > max) {
          this.show(this.#shown, false)
          return
        }
        changed()
      })
    }
  }

  get value(): HoldSetting {
    const count = (f: HTMLInputElement) => Math.max(0, Math.trunc(Number(f.value) || 0))
    return { afterBlock: this.#after.checked, maxFiles: count(this.#files), maxLines: count(this.#lines) }
  }

  show(h: HoldSetting, disabled: boolean) {
    this.#shown = h
    this.#after.checked = h.afterBlock
    this.#files.value = String(h.maxFiles)
    this.#lines.value = String(h.maxLines)
    for (const c of [this.#after, this.#files, this.#lines]) c.disabled = disabled
  }

  rows(): HTMLElement[] {
    const row = (label: string, field: HTMLElement) => {
      const r = el('div', 'row field-row')
      const l = el('label', 'small', label)
      l.htmlFor = field.id
      r.append(l, field)
      return r
    }
    const after = el('div', 'row check-row')
    const afterLabel = el('label', 'small', 'Hold the agent\'s own push after a reviewer blocked on its branch')
    afterLabel.htmlFor = this.#after.id
    after.append(this.#after, afterLabel)
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
  #rounds = el('select', 'textbox')
  #type = el('select', 'textbox')
  #findings = el('input')
  #comment = el('input')
  #hold = new HoldFields(() => {
    const v = this.#view
    if (v?.project) emit(this, 'sf-project-setting', { path: v.project, op: 'hold', hold: this.#hold.value })
  })

  constructor() {
    super()
    this.#comment.type = 'checkbox'
    this.#comment.id = 'project-pr-comment'
    this.#comment.dataset.key = 'prComment'
    this.#comment.setAttribute('aria-describedby', 'project-pr-comment-help')
    this.#comment.addEventListener('change', () => {
      const v = this.#view
      if (v?.project) emit(this, 'sf-project-setting', { path: v.project, op: 'prComment', on: this.#comment.checked })
    })
    this.#findings.type = 'checkbox'
    this.#findings.id = 'project-findings'
    this.#findings.dataset.key = 'findings'
    this.#findings.setAttribute('aria-describedby', 'project-findings-help')
    this.#findings.addEventListener('change', () => {
      const v = this.#view
      if (v?.project) emit(this, 'sf-project-setting', { path: v.project, op: 'findings', findings: this.#findings.checked })
    })
    this.#rounds.id = 'project-rounds'
    this.#rounds.dataset.key = 'rounds'
    for (let n = 1; n <= 20; n++) {
      const o = el('option', '', String(n))
      o.value = String(n)
      this.#rounds.append(o)
    }
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
    this.#type.replaceChildren(...[['', 'The team file\'s default, else Fix'], ...v.types].map(([value, label]) => {
      const o = el('option', '', visible(label ?? ''))
      o.value = value ?? ''
      return o
    }))
    this.#rounds.value = String(v.rounds)
    this.#type.value = v.types.some(([id]) => id === v.type) ? v.type : ''
    this.#findings.checked = v.autoFindings
    this.#comment.checked = v.prComment
    for (const c of [this.#rounds, this.#type, this.#findings, this.#comment]) c.disabled = v.disabled
    this.#hold.show(v.hold ?? { afterBlock: false, maxFiles: 0, maxLines: 0 }, v.disabled)
    const rounds = el('div', 'row field-row')
    const roundsLabel = el('label', 'small', 'Review rounds before you decide')
    roundsLabel.htmlFor = this.#rounds.id
    rounds.append(roundsLabel, this.#rounds)
    const type = el('div', 'row field-row')
    const typeLabel = el('label', 'small', 'New sessions start as')
    typeLabel.htmlFor = this.#type.id
    type.append(typeLabel, this.#type)
    const findings = el('div', 'row check-row')
    const findingsLabel = el('label', 'small', 'Send blocking findings to the agent automatically')
    findingsLabel.htmlFor = this.#findings.id
    findings.append(this.#findings, findingsLabel)
    const findingsHelp = el('p', 'small muted', 'When reviews the agent asked for block its push, the engine sends the agent the reviewers\' final messages, marked as untrusted, so it can fix and push again. Only when the agent and the reviewers use a subscription, and never past the round limit or 12 automatic review runs on a branch.')
    findingsHelp.id = 'project-findings-help'
    const comment = el('div', 'row check-row')
    const commentLabel = el('label', 'small', 'Comment on the pull request after each push')
    commentLabel.htmlFor = this.#comment.id
    comment.append(this.#comment, commentLabel)
    const commentHelp = el('p', 'small muted', 'Off by default. When on, the engine posts a short note under your token on the branch\'s open pull request after each push: the commit, who caused the push and which reviews passed, from its own records only. The page shows each note.')
    commentHelp.id = 'project-pr-comment-help'
    this.append(rounds, el('p', 'small muted', 'Kept for the round limit: after this many rounds of one reviewer blocking on a branch, you decide on the push.'), type, findings, findingsHelp, comment, commentHelp, ...this.#hold.rows())
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
