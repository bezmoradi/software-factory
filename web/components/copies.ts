import { request } from '../api.ts'
import { offTip } from '../lib/tooltip.ts'
import { busy, button, el, focusedKey, refocus, shown } from '../lib/dom.ts'
import type { Tone } from '../lib/dom.ts'
import { saidOf, tagClass } from '../lib/json.ts'
import type { CopyAction, CopyCard } from '../types.ts'
import { toast } from './toasts.ts'

export interface CopiesView {
  sid: string
  cards: CopyCard[]
  disabled: string
}

const LABELS: Record<CopyAction, string> = { approve: 'Approve', decline: 'Decline', remove: 'Remove' }
const CLASSES: Record<CopyAction, string> = { approve: 'small', decline: 'small ghost', remove: 'small' }

class SfCopies extends HTMLElement {
  #view: CopiesView | null = null
  #key = ''
  #busy = new Map<string, string>()
  #notes = new Map<string, { text: string; tone: Tone }>()

  connectedCallback() {
    if (!this.#view) this.hidden = true
  }

  set view(value: CopiesView | null) {
    this.#view = value
    const key = JSON.stringify([value?.sid, value?.cards, value?.disabled, [...this.#busy], [...this.#notes]])
    if (key !== this.#key) {
      this.#key = key
      this.paint()
    }
  }

  paint() {
    const v = this.#view
    const parts: Node[] = []
    if (v?.cards.length) {
      parts.push(el('h2', 'label', 'Copies of other repositories'))
      parts.push(el('p', 'small muted', 'The agent asked to read these repositories. Read each card before you answer: the engine fetches nothing until you approve.'))
      parts.push(...v.cards.map(c => this.#card(v, c)))
    }
    this.hidden = !parts.length
    const focused = focusedKey(this)
    this.replaceChildren(...parts)
    refocus(this, focused)
  }

  #card(v: CopiesView, c: CopyCard): HTMLElement {
    const box = el('div', 'forge-card copy-card')
    const said = saidOf(c.said, c.state)
    const head = el('div', 'row forge-head')
    head.append(el('span', tagClass(said), said.label), el('span', 'tag', 'asked by the agent'))
    if (c.token) head.append(el('span', 'tag', 'uses your saved token'))
    box.append(head)
    box.append(this.#fact('Address', c.url, true), this.#fact('Branch or tag', c.ref ? c.ref : 'its main branch', !!c.ref), this.#fact('Host', c.host, false))
    box.append(el('p', 'small', shown(c.tokenSaid)))
    if (c.state === 'waiting') box.append(el('p', 'small warn', shown(c.risk)))
    box.append(this.#fact('Files at', c.path, true))
    if (c.note) box.append(el('p', `small${c.state === 'failed' ? ' bad' : ''}`, shown(c.note)))
    const note = this.#notes.get(c.card)
    if (note) box.append(el('p', `small ${note.tone}`, note.text))
    const row = this.#actions(v, c)
    if (row.childElementCount) box.append(row)
    return box
  }

  #fact(name: string, value: string, code: boolean): HTMLElement {
    const line = el('p', 'small copy-fact')
    line.append(el('span', 'muted', `${name}: `))
    line.append(code ? el('code', 'copy-value', shown(value)) : el('span', 'copy-value', shown(value)))
    return line
  }

  #actions(v: CopiesView, c: CopyCard): HTMLElement {
    const off = !!v.disabled || this.#busy.has(c.card)
    const offered = c.actions ?? {}
    const row = el('div', 'actions forge-actions')
    for (const name of ['approve', 'decline', 'remove'] as const) {
      const why = offered[name]
      if (why === undefined) continue
      const b = button(LABELS[name], CLASSES[name], () => {
        if (!off && !why) void this.#call(v.sid, c, name)
      }, 'button', `${name}:${c.card}`)
      b.setAttribute('aria-label', `${LABELS[name]} the copy of ${shown(c.url)}`)
      b.setAttribute('aria-disabled', String(off || !!why))
      if (this.#busy.get(c.card) === name) busy(b, true)
      const reason = v.disabled || (why ? shown(why) : '')
      row.append(...(reason ? offTip(b, reason) : [b]))
    }
    return row
  }

  async #call(sid: string, c: CopyCard, op: CopyAction) {
    this.#busy.set(c.card, op)
    this.#notes.delete(c.card)
    this.#key = ''
    this.view = this.#view
    const a = await request('/api/copies', { op, sid, card: c.card })
    this.#busy.delete(c.card)
    if (a.ok) toast({ id: `copy:${c.card}`, title: a.message, tone: 'ok' })
    else this.#notes.set(c.card, { text: a.message, tone: 'bad' })
    this.#key = ''
    this.view = this.#view
  }
}

customElements.define('sf-copies', SfCopies)

declare global {
  interface HTMLElementTagNameMap {
    'sf-copies': SfCopies
  }
}
