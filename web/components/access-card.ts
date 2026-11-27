import { request, savedTone, type EngineAnswer } from '../api.ts'
import { button, el, focusedKey, refocus, statusLine, unspoofed, visible, visibleCommand, whileBusy } from '../lib/dom.ts'
import { actions, field, fieldRow, form, showAnswer } from '../lib/form.ts'
import { checkbox, dropdown, option } from '../lib/controls.ts'
import type { AccessCard, AccessRisk, GrantChoice } from '../types.ts'
import { codeBlock } from './code-block.ts'
import { toast } from './toasts.ts'
import { openSavePreset } from './credentials.ts'
import { offTip } from '../lib/tooltip.ts'

export const ACCESS_ROUTE = '/api/access'

export interface AccessChoice {
  scope: string
  expires: string
  confirmed: boolean
}

export interface AccessCardOptions {
  card: AccessCard
  choice: AccessChoice
  disabled: boolean
  heading: string
  onChoice: (c: AccessChoice) => void
  accept: (c: AccessChoice) => Promise<EngineAnswer>
  refuse?: () => Promise<EngineAnswer>
  savePreset?: (c: AccessChoice) => void
  done: (a: EngineAnswer) => void
}

const text = (v: unknown): string => (typeof v === 'string' ? v : '')
const record = (v: unknown): Record<string, unknown> => (v !== null && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {})
const strings = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [])

function choicesOf(v: unknown): GrantChoice[] {
  return Array.isArray(v) ? v.map(record).map(c => ({ value: text(c.value), label: text(c.label) })).filter(c => c.value) : []
}

export function accessCardOf(v: unknown): AccessCard | null {
  const c = record(v)
  if ((!text(c.host) && !text(c.credential) && !text(c.action)) || !Array.isArray(c.risks)) return null
  const risks: AccessRisk[] = c.risks.map(record).map(r => ({
    scope: text(r.scope), expires: text(r.expires), level: text(r.level), label: text(r.label),
    says: strings(r.says), accept: text(r.accept), off: text(r.off),
  }))
  const port = typeof c.port === 'number' && Number.isInteger(c.port) && c.port > 0 ? c.port : undefined
  const action = text(c.action) ? { action: text(c.action), command: text(c.command), target: text(c.target), files: c.files === true, minutes: Number(c.minutes) || 0 } : {}
  return { id: text(c.id), kind: text(c.kind), host: text(c.host), ...(port ? { port } : {}), credential: text(c.credential), title: text(c.title), why: text(c.why), reason: text(c.reason), asked: Number(c.asked) || 0, risks, scopes: choicesOf(c.scopes), expiries: choicesOf(c.expiries), ...action }
}

export function firstChoice(card: AccessCard): AccessChoice {
  return { scope: card.scopes[0]?.value ?? '', expires: card.expiries[0]?.value ?? '', confirmed: false }
}

function riskOf(card: AccessCard, c: AccessChoice): AccessRisk | undefined {
  return card.risks.find(r => r.scope === c.scope && r.expires === c.expires)
}

function picker(name: string, list: GrantChoice[], value: string, disabled: boolean, change: (v: string) => void): HTMLSelectElement {
  const pick = dropdown({ name, options: list.map(c => option(visible(c.label), c.value)) })
  pick.value = value
  pick.disabled = disabled
  pick.dataset.key = name
  pick.addEventListener('change', () => change(pick.value))
  return pick
}

export function accessCardView(o: AccessCardOptions): HTMLElement {
  const { card, choice, disabled } = o
  const box = el('section', 'card access-card')
  const head = el('h3', '', visible(card.title))
  head.id = o.heading
  box.setAttribute('aria-labelledby', head.id)
  box.append(el('div', 'label meta', card.action ? 'Action' : card.credential ? 'Credential' : 'Network access'), head)
  if (card.action) {
    box.append(codeBlock({ text: visibleCommand(card.command ?? ''), label: `${visible(card.action)} command line`, look: 'terminal', wrap: true }))
    const facts = el('p', 'small muted')
    facts.append('Against ', el('code', '', visible(card.target ?? '')), ' with ', el('strong', '', visible(card.credential)), card.files ? ', with this session’s files read-only' : ', without the session’s files', `, stopped after ${card.minutes ?? 0} min`)
    box.append(facts)
  }
  if (card.why) box.append(el('p', 'small', visible(card.why)))
  if (card.reason) box.append(el('blockquote', 'agent-words small', visibleCommand(unspoofed(card.reason))))
  const risk = riskOf(card, choice)
  const level = risk?.level ?? ''
  const riskBox = el('div', 'notice small access-risk')
  riskBox.dataset.level = level
  if (risk) {
    const label = el('p')
    label.append(el('strong', '', visible(risk.label)))
    riskBox.append(label)
    const says = el('ul', 'access-says')
    for (const line of risk.says) says.append(el('li', '', visible(line)))
    riskBox.append(says)
  }
  const said = statusLine('small bad')
  const accept = button('Accept the risk', 'small', undefined, 'submit', 'accept')
  const confirm = checkbox({ name: 'confirmed', checked: choice.confirmed })
  confirm.dataset.key = 'confirmed'
  accept.disabled = disabled || !risk || (!!risk.accept && !choice.confirmed)
  const off = disabled ? '' : visible(risk?.off ?? '')
  confirm.disabled = disabled
  confirm.addEventListener('change', () => o.onChoice({ ...choice, confirmed: confirm.checked }))
  const scope = picker('scope', card.scopes, choice.scope, disabled, v => o.onChoice({ ...choice, scope: v, confirmed: false }))
  const expires = picker('expires', card.expiries, choice.expires, disabled, v => o.onChoice({ ...choice, expires: v, confirmed: false }))
  const buttons: HTMLButtonElement[] = []
  const answered = (a: EngineAnswer, place: HTMLElement) => {
    if (showAnswer(place, a, said)) o.done(a)
  }
  let f: HTMLFormElement
  if (o.refuse) {
    const refuse = o.refuse
    const no = button("Don't allow", 'small', undefined, 'button', 'refuse')
    no.disabled = disabled
    no.addEventListener('click', () => void whileBusy(no, async () => answered(await refuse(), f)))
    buttons.push(no)
  }
  if (o.savePreset) {
    const save = o.savePreset
    const keep = button('Save as preset', 'small', () => save(choice), 'button', 'preset')
    keep.disabled = accept.disabled
    buttons.push(keep)
  }
  buttons.push(accept)
  const offTips = off && !accept.disabled ? offTip(accept, off).slice(1) : []
  if (risk) box.append(riskBox)
  const parts: Node[] = []
  if (risk?.accept) parts.push(field(visible(risk.accept), confirm, { kind: 'check' }))
  const choose = el('div', 'access-choose')
  choose.append(card.action ? field('For how long', expires) : fieldRow([field('Who can reach it', scope), field('For how long', expires)]), actions([...buttons, ...offTips]))
  parts.push(choose, said)
  f = form({ cls: 'access-form', children: parts, onSubmit: () => {
    if (accept.getAttribute('aria-disabled') === 'true') return
    void whileBusy(accept, async () => answered(await o.accept(choice), f))
  } })
  box.append(f)
  return box
}

export interface AccessCardsView {
  sid: string
  cards: AccessCard[]
  disabled: boolean
}

let headings = 0

class SfAccessCards extends HTMLElement {
  #view: AccessCardsView | null = null
  #choices = new Map<string, AccessChoice>()
  #key = ''

  connectedCallback() {
    if (!this.#view) this.hidden = true
  }

  set view(value: AccessCardsView | null) {
    if (value?.sid !== this.#view?.sid) this.#choices.clear()
    this.#view = value
    this.paint()
  }

  paint() {
    const v = this.#view
    const ids = new Set((v?.cards ?? []).map(c => c.id ?? ''))
    for (const id of this.#choices.keys()) if (!ids.has(id)) this.#choices.delete(id)
    const key = JSON.stringify(v ? [v.sid, v.cards, v.disabled, [...this.#choices]] : null)
    if (key === this.#key) return
    this.#key = key
    this.hidden = !v || !v.cards.length
    const focused = focusedKey(this)
    this.replaceChildren(...(v ? v.cards.map(c => this.#card(v, c)) : []))
    refocus(this, focused)
  }

  #card(v: AccessCardsView, card: AccessCard): HTMLElement {
    const id = card.id ?? ''
    const choice = this.#choices.get(id) ?? firstChoice(card)
    return accessCardView({
      card, choice, disabled: v.disabled, heading: `access-card-${++headings}`,
      onChoice: c => {
        this.#choices.set(id, c)
        this.paint()
      },
      accept: c => request(ACCESS_ROUTE, { op: 'allow', sid: v.sid, card: id, scope: c.scope, expires: c.expires, confirmed: c.confirmed }),
      refuse: () => request(ACCESS_ROUTE, { op: 'refuse', sid: v.sid, card: id }),
      savePreset: card.action ? undefined : c => openSavePreset(request, { sid: v.sid, ...(card.credential ? { credential: card.credential } : card.port ? { host: card.host, port: card.port } : { host: card.host }), scope: c.scope, expires: c.expires, confirmed: c.confirmed }),
      done: a => {
        this.#choices.delete(id)
        toast({ id: `access:${v.sid}`, title: a.message, tone: savedTone(a) })
      },
    })
  }
}

customElements.define('sf-access-cards', SfAccessCards)

declare global {
  interface HTMLElementTagNameMap {
    'sf-access-cards': SfAccessCards
  }
}
