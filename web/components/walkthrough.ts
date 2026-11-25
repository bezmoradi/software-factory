import { el, emit, shown } from '../lib/dom.ts'
import { field, form } from '../lib/form.ts'
import type { WalkStep } from '../types.ts'
import { checkbox } from '../lib/controls.ts'

export interface WalkthroughView {
  commit: string
  steps: WalkStep[]
  readOnly?: string
}

class SfWalkthrough extends HTMLElement {
  #key = ''
  #pending = new Map<number, boolean>()

  set view(value: WalkthroughView | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    this.#pending.clear()
    this.replaceChildren()
    if (!value || !value.steps.length) return
    const checked = value.steps.filter(s => s.done).length
    const count = el('p', 'small muted', `${checked} of ${value.steps.length} ${value.steps.length === 1 ? 'step' : 'steps'} checked off. A check-off only records that you tried the step; it isn't a verdict, and nothing reads it but you.`)
    count.id = `walkthrough-count-${value.commit.slice(0, 12)}`
    const list = el('ol', 'walkthrough-steps')
    list.setAttribute('aria-describedby', count.id)
    value.steps.forEach((step, i) => list.append(this.#step(value.commit, step, i + 1, !!value.readOnly)))
    if (value.readOnly) count.append(' ', value.readOnly)
    this.append(count, form({ children: [list] }))
  }

  #step(commit: string, step: WalkStep, n: number, readOnly: boolean): HTMLLIElement {
    const li = el('li', 'walkthrough-step')
    const box = checkbox({ checked: step.done })
    box.id = `walk-${commit.slice(0, 12)}-${n}`
    box.dataset.key = box.id
    box.disabled = readOnly
    li.append(field(`Step ${n}: ${shown(step.do)}`, box, { kind: 'check', labelCls: 'walkthrough-do' }))
    if (step.expect) li.append(el('div', 'small', `Expected: ${shown(step.expect)}`))
    if (step.input) li.append(el('div', 'small muted', `Sample input: ${shown(step.input)}`))
    box.addEventListener('change', () => {
      if (this.#pending.has(n)) {
        box.checked = !box.checked
        return
      }
      const done = box.checked
      this.#pending.set(n, done)
      box.setAttribute('aria-busy', 'true')
      emit(this, 'sf-walkthrough-check', {
        commit, step: n, done,
        settle: ok => {
          this.#pending.delete(n)
          box.removeAttribute('aria-busy')
          if (!ok) box.checked = !done
        },
      })
    })
    return li
  }
}

customElements.define('sf-walkthrough', SfWalkthrough)

declare global {
  interface HTMLElementTagNameMap {
    'sf-walkthrough': SfWalkthrough
  }
}
