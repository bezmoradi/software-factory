import { el, replaceKeeping, visible } from '../lib/dom.ts'
import { agentWord, shortSha } from '../lib/format.ts'
import type { Lineage, SessionInfo, Starter } from '../types.ts'

const SHA = /^[0-9a-f]{40}$/
let starters: Starter[] = []

export function setBranchStarters(list: Starter[] | undefined): void {
  starters = Array.isArray(list) ? list.filter(a => typeof a?.id === 'string' && typeof a?.name === 'string') : []
}

export function agentName(id: string): string {
  return starters.find(a => a.id === id)?.name ?? agentWord(id)
}

function sha(value: string | undefined): string {
  return value && SHA.test(value) ? shortSha(value) : ''
}

export interface LineageView {
  info: SessionInfo
  disabled: string
}

class SfLineage extends HTMLElement {
  #view: LineageView | null = null
  #key = ''

  connectedCallback() {
    this.paint()
  }

  set view(value: LineageView | null) {
    this.#view = value
    this.paint()
  }

  paint() {
    const v = this.#view
    const key = JSON.stringify(v ? [v.info.sid, v.info.lineage] : null)
    if (key === this.#key) return
    this.#key = key
    const said = v?.info.lineage ? this.#history(v.info.lineage) : null
    this.hidden = !said
    replaceKeeping(this, said ? [said] : [])
  }

  #history(l: Lineage): HTMLElement | null {
    const start = sha(l.from)
    const origin = l.origin && l.origin !== 'main' ? visible(l.origin) : ''
    if (!origin) return null
    const box = el('div', 'notice small')
    box.append(el('p', '', `This session started from ${origin}${start ? ` at commit ${start}` : ''}, on its own branch ${visible(l.branch)}. Its reviews compare it against main.`))
    return box
  }
}

customElements.define('sf-lineage', SfLineage)

declare global {
  interface HTMLElementTagNameMap {
    'sf-lineage': SfLineage
  }
}
