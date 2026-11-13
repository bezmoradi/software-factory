import { el, visible } from '../lib/dom.ts'
import type { Check, CheckStatus } from '../types.ts'

const NOTES: Record<string, [string, string] | undefined> & { none: [string, string] } = {
  none: ['Not reviewed on this commit yet: press Review', 'muted'],
  running: ['Reviewing…', 'muted'],
  stale: ['Reviewed an earlier commit: press Review again', 'warn'],
  failed: ["Didn't finish: it counts as not passed. Press Review again", 'bad'],
}
const UNKNOWN = "Finished, but its reply had no verdict the engine recognizes"

class SfCheck extends HTMLElement {
  #check: Check | null = null
  #note: HTMLDivElement | null = null
  #said: HTMLSpanElement | null = null
  #sid = ''

  set check(value: Check) {
    this.#check = value
    const row = el('div', 'row')
    row.append(el('span', '', visible(value.name)), el('span', 'spacer'))
    this.#note = el('div', 'small muted', NOTES.none[0])
    this.#said = el('span', 'sr-only')
    this.#said.setAttribute('role', 'status')
    const meta = el('div', 'small muted', [value.type, value.why].filter(Boolean).map(x => visible(x ?? '')).join(' · '))
    this.replaceChildren(row, meta, this.#note, this.#said)
  }

  set status(value: CheckStatus | undefined) {
    const { gate = null, sid = '' } = value ?? {}
    if (!this.#note) return
    const moved = sid !== this.#sid
    this.#sid = sid
    const state = gate?.state ?? 'none'
    const [text, tone] = state === 'pass'
      ? [gate!.verdict || 'Passed', 'ok']
      : state === 'block'
        ? [gate!.verdict || 'Blocked', 'bad']
        : state === 'unknown'
          ? [`${UNKNOWN}.`, 'warn']
          : (Object.hasOwn(NOTES, state) ? NOTES[state] : undefined) ?? NOTES.none
    this.#note.className = `small ${tone}`
    if (this.#note.textContent !== text) {
      this.#note.textContent = text
      if (!moved && ['pass', 'block', 'unknown', 'failed'].includes(state) && this.#said) this.#said.textContent = `${this.#check?.name ?? 'Review'}: ${text}`
    }
  }
}

customElements.define('sf-check', SfCheck)

declare global {
  interface HTMLElementTagNameMap {
    'sf-check': SfCheck
  }
}
