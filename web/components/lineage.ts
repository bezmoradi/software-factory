import { button, el, emit, replaceKeeping, sayIfChanged, statusLine, visible } from '../lib/dom.ts'
import { agentWord, shortSha } from '../lib/format.ts'
import { limitsNow, watchLimits, when } from './usage.ts'
import type { Lineage, SessionInfo, Starter } from '../types.ts'

const SHA = /^[0-9a-f]{40}$/
let starters: Starter[] = []

export function setBranchStarters(list: Starter[] | undefined): void {
  starters = Array.isArray(list) ? list.filter(a => typeof a?.id === 'string' && typeof a?.name === 'string') : []
}

export function otherAgents(agent: string | undefined): Starter[] {
  const own = agent || 'claude'
  return starters.filter(a => a.enabled && a.id !== own)
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
  #stop = () => {}
  #handedSaid = statusLine('status-slot')
  #handedBox = el('div', 'notice small status-box')

  connectedCallback() {
    this.#stop = watchLimits(this)
    this.paint()
  }

  disconnectedCallback() {
    this.#stop()
  }

  set view(value: LineageView | null) {
    this.#view = value
    this.paint()
  }

  paint() {
    const v = this.#view
    const l = v?.info.lineage
    const handed = l?.handedTo ? l : null
    const parts: HTMLElement[] = []
    if (v && !handed) {
      if (l) {
        const said = this.#history(v.info, l)
        if (said) parts.push(said)
      }
      const offer = this.#limitOffer(v)
      if (offer) parts.push(offer)
    }
    this.#handed(handed)
    this.hidden = !parts.length && !handed
    replaceKeeping(this, [this.#handedBox, ...parts])
  }

  #handed(l: Lineage | null): void {
    if (!l?.handedTo) {
      sayIfChanged(this.#handedSaid, '')
      replaceKeeping(this.#handedBox, [this.#handedSaid])
      return
    }
    const to = l.handedTo
    const at = sha(l.last)
    sayIfChanged(this.#handedSaid, `This branch, ${visible(l.branch)}, continues in another session${at ? ` from commit ${at}` : ''}. This session is stopped and its worktree removed, so it can't wake; it keeps its conversation for reading.`)
    replaceKeeping(this.#handedBox, [this.#handedSaid, button('Open that session', 'ghost small', () => emit(this, 'sf-open-session', { sid: to }))])
  }

  #history(info: SessionInfo, l: Lineage): HTMLElement | null {
    const lines: HTMLElement[] = []
    for (const t of l.earlier ?? []) {
      const from = sha(t.from)
      const to = sha(t.to)
      if (!from || !to) continue
      const who = agentName(t.agent)
      const named = t.name ? ` (session ${visible(t.name)})` : ''
      lines.push(el('li', '', from === to ? `${who}${named} made no commits; it started and stopped at ${to}.` : `${who}${named} made the commits after ${from}, up to ${to}.`))
    }
    const start = sha(l.from)
    const origin = l.origin && l.origin !== 'hand-over' && l.origin !== 'main' ? visible(l.origin) : ''
    if (!lines.length && !origin) return null
    const box = el('div', 'notice small')
    if (lines.length) {
      box.append(el('p', '', `Branch ${visible(l.branch)}: who made which commits.`))
      lines.push(el('li', '', `${agentName(info.agent || 'claude')}, this session, makes the commits after ${start || 'that'}.`))
      const ul = el('ul', 'small list')
      ul.append(...lines)
      box.append(ul)
    } else box.append(el('p', '', `This session started from ${origin}${start ? ` at commit ${start}` : ''}, on its own branch ${visible(l.branch)}. Its reviews compare it against main.`))
    return box
  }

  #limitOffer(v: LineageView): HTMLElement | null {
    const info = v.info
    const s = limitsNow().sessions.get(info.sid)
    if (!info.verified || !s?.wakeOffer) return null
    const others = otherAgents(info.agent)
    if (!others.length) return null
    const box = el('div', 'notice warn small')
    box.append(el('p', '', `${agentName(info.agent || 'claude')} used up a usage limit, which resets ${when(s.wakeOffer)}. Another agent can go on with this branch now: the engine stops this session, and the new one starts on the same branch with the approvals you gave.`))
    const row = el('div', 'field-row')
    for (const a of others) {
      const b = button(`Continue with ${a.name}`, 'small', () => {
        if (!v.disabled) emit(this, 'sf-handover', { sid: info.sid, agent: a.id })
      })
      b.setAttribute('aria-disabled', String(!!v.disabled))
      if (v.disabled) b.title = v.disabled
      row.append(b)
    }
    box.append(row)
    return box
  }
}

customElements.define('sf-lineage', SfLineage)

declare global {
  interface HTMLElementTagNameMap {
    'sf-lineage': SfLineage
  }
}
