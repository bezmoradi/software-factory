import { button, el, ensureId, shown, statusLine } from '../lib/dom.ts'
import { duration } from '../lib/format.ts'
import type { TaskRow, TasksView } from '../types.ts'
import { icon } from './icon.ts'

const TAGS: Record<TaskRow['state'], string> = { running: 'tag busy', done: 'tag ok', failed: 'tag bad', stopped: 'tag warn' }

class SfBackgroundTasks extends HTMLElement {
  #said = statusLine('sr-only')
  #box = el('section', 'subagents')
  #toggle = button('', 'subagents-toggle', () => {
    this.#open = !this.#open
    this.#render()
  })
  #list = el('ul', 'subagents-list')
  #sid = ''
  #rows: TaskRow[] = []
  #states = new Map<string, string>()
  #open = false
  #key = ''
  #timer = 0

  connectedCallback() {
    if (this.#box.isConnected) return
    const head = el('h2', 'subagents-head')
    head.append(this.#toggle)
    this.#toggle.setAttribute('aria-controls', ensureId(this.#list))
    this.#box.setAttribute('aria-labelledby', ensureId(head))
    this.#box.hidden = true
    this.#box.append(head, this.#list)
    this.append(this.#said, this.#box)
  }

  disconnectedCallback() {
    this.#stop()
  }

  set view(value: TasksView | null) {
    const rows = value?.rows ?? []
    const sid = value?.sid ?? ''
    const ran = this.#rows.some(r => r.state === 'running')
    const runs = rows.some(r => r.state === 'running')
    if (sid !== this.#sid) {
      this.#sid = sid
      this.#states.clear()
      this.#open = runs
      this.#said.textContent = ''
    } else {
      this.#announce(rows)
      if (runs && !ran) this.#open = true
      else if (!runs && ran) this.#open = false
    }
    this.#states = new Map(rows.map(r => [r.id, r.state]))
    this.#rows = rows
    this.#render()
    if (runs && !this.#timer) this.#timer = window.setInterval(() => this.#tick(), 1000)
    else if (!runs) this.#stop()
  }

  #stop() {
    if (this.#timer) window.clearInterval(this.#timer)
    this.#timer = 0
  }

  #announce(rows: TaskRow[]) {
    const said: string[] = []
    for (const r of rows) {
      const was = this.#states.get(r.id)
      if (was === r.state) continue
      if (r.state === 'running') said.push(`${shown(r.kind)} started in the background${r.what ? `: ${shown(r.what)}` : ''}`)
      else if (was) said.push(`${shown(r.kind)}: ${r.label}`)
    }
    if (said.length) this.#said.textContent = said.join('. ')
  }

  #render() {
    const key = JSON.stringify([this.#rows, this.#open])
    if (key === this.#key) return
    this.#key = key
    this.#box.hidden = !this.#rows.length
    this.toggleAttribute('data-rows', !!this.#rows.length)
    const counts = (['running', 'done', 'failed', 'stopped'] as const).map(state => [this.#rows.filter(r => r.state === state), state] as const).filter(([list]) => list.length).map(([list, state]) => `${list.length} ${state}`)
    this.#toggle.replaceChildren(icon('chevron', 'icon subagents-chevron'), el('span', 'subagents-title', 'Background tasks'), el('span', 'small muted', counts.join(', ')))
    this.#toggle.setAttribute('aria-expanded', String(this.#open))
    this.#list.hidden = !this.#open
    this.#list.replaceChildren(...this.#rows.map(r => this.#row(r)))
  }

  #row(r: TaskRow): HTMLLIElement {
    const li = el('li', 'subagent-row')
    const body = el('div', 'subagent-open')
    const head = el('span', 'subagent-head')
    head.append(el('b', '', shown(r.kind)))
    if (r.what) head.append(el('span', 'subagent-task', shown(r.what)))
    const facts = el('span', 'subagent-facts small')
    const time = el('span', 'subagent-time muted', duration((r.end || Date.now()) - r.start))
    time.dataset.start = String(r.start)
    if (r.end) time.dataset.end = String(r.end)
    facts.append(el('span', TAGS[r.state], r.label), time)
    body.append(head, facts)
    li.append(body)
    return li
  }

  #tick() {
    for (const t of this.#list.querySelectorAll<HTMLElement>('.subagent-time')) {
      if (t.dataset.end) continue
      t.textContent = duration(Date.now() - Number(t.dataset.start))
    }
  }
}

customElements.define('sf-background-tasks', SfBackgroundTasks)

declare global {
  interface HTMLElementTagNameMap {
    'sf-background-tasks': SfBackgroundTasks
  }
}
