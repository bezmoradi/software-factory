import { request } from '../api.ts'
import { busy, button, el, focusedKey, labelError, refocus, say, shown, statusLine, unspoofed, visibleCommand, whileBusy } from '../lib/dom.ts'
import { field } from '../lib/form.ts'
import { textbox } from '../lib/controls.ts'
import { pickRow } from '../lib/page.ts'

type Issue = { number: number; title: string; body?: string; url: string; author: string; labels: string[]; created: number; open: boolean }

type Picked = { issue: Issue; refused: string; clipped: boolean; hash: string }

let serial = 0

const NAV_KEYS: Record<string, string[]> = { 'issues:newer': ['issues:older'], 'issues:older': ['issues:newer'] }

class SfIssuePicker extends HTMLElement {
  #path = ''
  #gen = 0
  #asked = 0
  #built = false
  #wanted = false
  #number = textbox()
  #list = el('div', 'issue-list')
  #picked = el('div', 'issue-picked')
  #status = statusLine('small muted status-slot')
  #page = 1
  #more = false
  #busy = false
  #loaded = false
  #issues: Issue[] = []
  #chosen: Picked | null = null
  #pickButton = button('Pick', 'small', () => this.#run(this.#pickButton, () => this.#pick(this.#number.value)))
  readonly numberField = this.#numberField()

  get path(): string {
    return this.#path
  }

  set path(value: string) {
    if (value === this.#path) return
    this.#path = value
    this.#gen++
    this.#busy = false
    this.#page = 1
    this.#more = false
    this.#loaded = false
    this.#issues = []
    this.#chosen = null
    say(this.#status, '', 'muted')
    labelError(this.#number, '')
    this.#list.replaceChildren()
    this.#paintPicked()
    if (this.#wanted) void this.#load(1)
  }

  #run(b: HTMLButtonElement, work: () => Promise<void>): void {
    if (this.#busy || !this.path) return
    void whileBusy(b, work)
  }

  connectedCallback() {
    if (this.#built) return
    this.#built = true
    this.append(this.#status, this.#list, this.#picked)
  }

  #numberField(): HTMLDivElement {
    this.#number.id = `issue-number-${++serial}`
    this.#number.placeholder = '123'
    this.#number.addEventListener('input', () => labelError(this.#number, ''))
    this.#number.addEventListener('keydown', e => {
      if (e.key !== 'Enter') return
      e.preventDefault()
      this.#run(this.#pickButton, () => this.#pick(this.#number.value))
    })
    const pair = el('div', 'issue-number')
    pair.append(this.#number, this.#pickButton)
    return field('Issue #', pair)
  }

  opened() {
    this.#wanted = true
    if (!this.#loaded && !this.#busy) void this.#load(1)
  }

  chosen(): { issue: number; issueHash: string } | null {
    return this.#chosen ? { issue: this.#chosen.issue.number, issueHash: this.#chosen.hash } : null
  }

  refuse(text: string): boolean {
    say(this.#status, text, 'bad')
    return true
  }

  async #load(page: number, b?: HTMLButtonElement) {
    if (this.#busy || !this.path) return
    this.#busy = true
    const gen = this.#gen
    say(this.#status, '', 'muted')
    if (b) busy(b, true)
    const a = await request('/api/issues', { op: 'list', path: this.path, page })
    if (gen !== this.#gen) return
    if (b) busy(b, false)
    this.#busy = false
    if (!a.ok) {
      say(this.#status, a.message, 'bad')
      return
    }
    this.#page = typeof a.body.page === 'number' ? a.body.page : page
    this.#more = a.body.more === true
    this.#issues = Array.isArray(a.body.issues) ? (a.body.issues as Issue[]) : []
    this.#loaded = true
    this.#sayEmpty()
    this.#paintList()
  }

  #paintList() {
    const focused = focusedKey(this.#list)
    const ul = el('ul', 'list issue-items')
    for (const i of this.#issues) {
      const on = this.#chosen?.issue.number === i.number
      ul.append(pickRow({ text: `#${i.number} ${shown(i.title)}`, tip: `#${i.number} ${shown(i.title)}, by ${shown(i.author)}${i.labels.length ? `, ${i.labels.map(shown).join(', ')}` : ''}`, name: `#${i.number}`, on, key: `issue:${i.number}`, onPick: b => on ? this.clear() : this.#run(b, () => this.#pick(i.number)) }))
    }
    const nav = el('div', 'actions')
    if (this.#page > 1) {
      const newer = button('Newer', 'small ghost', () => void this.#load(this.#page - 1, newer), 'button', 'issues:newer')
      nav.append(newer)
    }
    if (this.#more) {
      const older = button('Older', 'small ghost', () => void this.#load(this.#page + 1, older), 'button', 'issues:older')
      nav.append(older)
    }
    this.#list.replaceChildren(ul, ...(nav.childElementCount ? [nav] : []))
    refocus(this.#list, focused, key => NAV_KEYS[key] ?? [])
  }

  async #pick(number: number | string) {
    if (this.#busy) return
    this.#busy = true
    const gen = this.#gen
    const asked = ++this.#asked
    const a = await request('/api/issues', { op: 'read', path: this.path, number })
    if (gen !== this.#gen) return
    this.#busy = false
    if (asked !== this.#asked) return
    if (!a.ok) {
      if (a.field === 'number' && typeof number === 'string') labelError(this.#number, a.message)
      else say(this.#status, a.message, 'bad')
      return
    }
    labelError(this.#number, '')
    const issue = a.body.issue as Issue
    this.#chosen = { issue, refused: typeof a.body.refused === 'string' ? a.body.refused : '', clipped: a.body.clipped === true, hash: typeof a.body.hash === 'string' ? a.body.hash : '' }
    say(this.#status, '', 'muted')
    if (this.#loaded) this.#paintList()
    this.#paintPicked()
  }

  #paintPicked() {
    const c = this.#chosen
    if (!c) return void this.#picked.replaceChildren()
    const body = el('pre', 'forge-body', c.issue.body ? visibleCommand(unspoofed(c.issue.body)) : '(no text)')
    body.tabIndex = 0
    body.setAttribute('aria-label', `Text of issue #${c.issue.number}`)
    const parts: Node[] = [body]
    if (c.clipped) parts.push(el('p', 'small warn', 'The issue is longer than one message, so the agent gets its start, marked as cut.'))
    if (c.refused) parts.push(el('p', 'small bad', shown(c.refused)))
    this.#picked.replaceChildren(...parts)
  }

  #sayEmpty() {
    say(this.#status, this.#loaded && !this.#issues.length ? 'No open issues.' : '', 'muted')
  }

  reset() {
    this.#gen++
    this.#asked++
    this.#busy = false
    busy(this.#pickButton, false)
    this.#chosen = null
    this.#number.value = ''
    labelError(this.#number, '')
    this.#sayEmpty()
    if (this.#loaded) this.#paintList()
    this.#paintPicked()
  }

  clear() {
    this.#asked++
    if (!this.#chosen) return
    this.#chosen = null
    this.#paintList()
    this.#paintPicked()
  }
}

customElements.define('sf-issue-picker', SfIssuePicker)

declare global {
  interface HTMLElementTagNameMap {
    'sf-issue-picker': SfIssuePicker
  }
}
