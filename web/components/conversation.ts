import { button, el, emit, shown, statusLine, unspoofed, visible } from '../lib/dom.ts'
import { plural } from '../lib/format.ts'
import type { CardEvent, CardHost, ClaimMark, ContextView, SessionEvent, TurnMarks } from '../types.ts'
import './markdown.ts'
import { answering, hiddenMark, hidesText } from './cards.ts'

const NOTE = '[Software Factory] '
const TOOL_NAMES = new Map([['WebFetch', 'Fetch']])
const FILE_TOOLS = new Set(['Read', 'Edit', 'MultiEdit', 'Write', 'NotebookEdit'])
const CARD_TAGS = { permission: 'sf-permission', ask: 'sf-ask', plan: 'sf-plan' } as const
const CARD_ORDER: string[] = Object.keys(CARD_TAGS)

export type Spot = { turn: Element; offset: number }

const tokens = (n: number) => (n >= 1e6 ? `${+(n / 1e6).toFixed(1)}m` : n >= 1000 ? `${+(n / 1000).toFixed(1)}k` : String(n))

function square(kind: string, color: string, fullness = 1, legend = false): HTMLSpanElement {
  const sq = el('span', 'ctx-sq', kind === 'free' ? '⛶' : kind === 'buffer' && !legend ? '⛀' : fullness >= 0.7 ? '⛁' : '⛀')
  sq.dataset.color = kind === 'free' ? 'free' : color
  return sq
}

function contextBody(v: ContextView): HTMLElement {
  const box = el('div', 'body ctx')
  const grid = el('div', 'ctx-grid')
  grid.setAttribute('aria-hidden', 'true')
  for (const row of v.grid) {
    const line = el('div', 'ctx-row')
    for (const [i, fullness] of row) {
      const c = v.categories[i]
      line.append(square(c?.kind ?? 'free', c?.color ?? '', fullness))
    }
    grid.append(line)
  }
  const side = el('div', 'ctx-legend')
  side.append(el('p', 'ctx-model', shown(v.model)), el('p', '', `${tokens(v.total)}/${tokens(v.max)} tokens (${v.percent}%)`), el('p', 'ctx-head', 'Estimated usage by category'))
  const list = el('ul', 'ctx-list')
  for (const c of v.categories) {
    if (c.kind === 'deferred' || (c.tokens <= 0 && c.kind !== 'free')) continue
    const share = v.max > 0 ? ((c.tokens / v.max) * 100).toFixed(1) : '0'
    const li = el('li')
    li.append(square(c.kind, c.color, 1, true), el('span', '', `${shown(c.name)}: ${tokens(c.tokens)}${c.kind === 'free' ? '' : ' tokens'} (${share}%)`))
    list.append(li)
  }
  side.append(list)
  box.append(el('p', 'ctx-title', 'Context Usage'), grid, side)
  return box
}

class SfConversation extends HTMLElement {
  #timeline: HTMLOListElement | null = null
  #needs: HTMLElement | null = null
  #blank = true
  #heading: HTMLHeadingElement | null = null
  #said: HTMLParagraphElement | null = null
  #reply: { turnId: string; body: HTMLElementTagNameMap['sf-markdown'] } | null = null
  #tools = new Map<string, HTMLLIElement>()
  #groups = new Map<string, { list: HTMLOListElement; label: HTMLElement; count: number; name: string }>()
  #cards = new Map<string, CardHost>()
  #pinned = true
  #top = 0
  #resized = new ResizeObserver(() => {
    this.#clip()
    this.#stick()
  })
  #width = 0
  #fresh = new Set<HTMLElement>()
  #unmeasured = new Set<HTMLElement>()
  #marks = new Map<string, HTMLLIElement>()
  #marksKey = ''
  scroller: HTMLElement | null = null
  who = 'Claude'

  #scrolled = () => {
    const s = this.scroller
    if (!s || this.away) return
    const gap = s.scrollHeight - s.clientHeight - s.scrollTop
    if (s.scrollTop < this.#top && gap > 1) this.#pinned = false
    else if (gap <= 80) this.#pinned = true
    this.#top = s.scrollTop
  }

  disconnectedCallback() {
    this.scroller?.removeEventListener('scroll', this.#scrolled)
    this.#resized.disconnect()
  }

  connectedCallback() {
    this.scroller?.addEventListener('scroll', this.#scrolled, { passive: true })
    this.#resized.observe(this)
    if (this.scroller) this.#resized.observe(this.scroller)
    if (this.#timeline) return
    this.#timeline = el('ol', 'timeline')
    this.#timeline.addEventListener('contentvisibilityautostatechange', e => {
      if (e.skipped || !(e.target instanceof HTMLElement) || !this.#unmeasured.size) return
      this.#measure([...e.target.querySelectorAll<HTMLElement>('.tool .call')].filter(c => this.#unmeasured.has(c)))
    }, { capture: true })
    this.#needs = el('section')
    this.#heading = el('h2', 'sr-only', 'Waiting on you')
    this.#heading.hidden = true
    this.#needs.append(this.#heading)
    this.#said = statusLine('sr-only')
    this.append(this.#timeline, this.#needs, this.#said)
  }

  get waiting(): CardEvent[] {
    return [...this.#cards.values()].flatMap(n => (n.source && !answering(n) ? [n.source] : []))
  }

  get pinned(): boolean {
    return this.#pinned
  }

  get away(): boolean {
    return this.classList.contains('away')
  }

  set away(on: boolean) {
    if (on !== this.away) this.classList.toggle('away', on)
  }

  spot(): Spot | null {
    const s = this.scroller
    if (!s || !this.#timeline || this.away) return null
    const top = s.getBoundingClientRect().top
    const turns = this.#timeline.children
    let lo = 0
    let hi = turns.length - 1
    let first: Element | null = null
    while (lo <= hi) {
      const mid = (lo + hi) >> 1
      const turn = turns[mid]!
      if (turn.getBoundingClientRect().bottom > top) {
        first = turn
        hi = mid - 1
      } else lo = mid + 1
    }
    return first ? { turn: first, offset: first.getBoundingClientRect().top - top } : null
  }

  restore(spot: Spot | null | undefined): void {
    const s = this.scroller
    if (!s || !spot || this.away || spot.turn.parentElement !== this.#timeline) return
    s.scrollTop += spot.turn.getBoundingClientRect().top - s.getBoundingClientRect().top - spot.offset
  }

  #stick() {
    if (this.#pinned && !this.away && this.scroller) this.scroller.scrollTop = this.scroller.scrollHeight
  }

  #pin() {
    this.#pinned = true
    this.#stick()
  }

  #entry(cls: string, who?: string) {
    const li = el('li', cls)
    if (who) li.append(el('span', 'who', who))
    this.#timeline!.append(li)
    this.#blank = false
    return li
  }

  #group(id: string, name?: string): HTMLOListElement {
    let g = this.#groups.get(id)
    if (!g) {
      const li = this.#entry('subagent')
      const box = el('details')
      box.open = true
      const label = el('summary', 'small')
      const list = el('ol', 'sub-list')
      box.append(label, list)
      li.append(box)
      g = { list, label, count: 0, name: '' }
      this.#groups.set(id, g)
    }
    if (name) g.name = name
    g.count++
    g.label.textContent = `${g.name ? `Subagent ${shown(g.name)}` : 'A subagent'}: ${g.count} ${plural(g.count, 'tool call')}`
    return g.list
  }

  #clip() {
    if (this.away || !this.#timeline) return
    const width = this.clientWidth
    if (!width) return
    const calls = width === this.#width ? [...this.#fresh] : [...this.#timeline.querySelectorAll<HTMLElement>('.tool .call')]
    if (width !== this.#width) this.#unmeasured.clear()
    this.#width = width
    this.#fresh.clear()
    this.#measure(calls)
  }

  #measure(calls: HTMLElement[]) {
    const laidOut: HTMLElement[] = []
    for (const call of calls) {
      this.#unmeasured.delete(call)
      if (!call.isConnected) continue
      if (call.checkVisibility({ contentVisibilityAuto: true })) laidOut.push(call)
      else this.#unmeasured.add(call)
    }
    const cut = laidOut.map(c => c.scrollHeight > c.clientHeight + 1)
    laidOut.forEach((call, i) => {
      const li = call.parentElement!
      const more = li.querySelector(':scope > .call-more')
      if (cut[i] && !more) call.after(this.#more(li))
      else if (!cut[i] && more && !li.classList.contains('whole')) more.remove()
    })
  }

  #more(li: HTMLElement) {
    const more = button('Show all', 'link call-more', () => {
      const whole = li.classList.toggle('whole')
      more.textContent = whole ? 'Show less' : 'Show all'
      more.setAttribute('aria-expanded', String(whole))
    })
    more.setAttribute('aria-expanded', 'false')
    return more
  }

  #outLine(text: string) {
    const out = el('div', 'tool-out')
    const elbow = el('span', '', '⎿')
    elbow.setAttribute('aria-hidden', 'true')
    out.append(elbow, el('span', '', text))
    return out
  }

  get blank(): boolean {
    return this.#blank
  }

  reset(note: string): void {
    this.#timeline!.replaceChildren()
    this.#needs!.replaceChildren(this.#heading!)
    this.#heading!.hidden = true
    this.#tools.clear()
    this.#unmeasured.clear()
    this.#groups.clear()
    this.#cards.clear()
    this.#reply = null
    this.#blank = !note
    if (note) this.#timeline!.append(el('li', 'reset small muted', note))
  }

  apply(ev: SessionEvent, controlled?: boolean): void {
    switch (ev.type) {
      case 'prompt': {
        this.#reply = null
        const who = ev.from === 'page' ? 'You, from this page' : ev.from === 'history' ? 'You, earlier' : ev.from === 'task' ? 'Background task' : `You, in ${this.who === 'Claude' ? 'Claude Code' : 'the agent'}`
        let text = ev.text || ''
        const noted = text.startsWith(NOTE)
        while (text.startsWith(NOTE)) {
          const cut = text.indexOf('\n\n')
          const note = cut < 0 ? text.slice(NOTE.length) : text.slice(NOTE.length, cut)
          this.#timeline!.append(el('li', 'reset small muted', `${ev.reported ? 'The session reports that ' : ''}Software Factory told ${this.who === 'Claude' ? 'Claude Code' : 'the agent'}: ${unspoofed(note)}`))
          text = cut < 0 ? '' : text.slice(cut + 2)
        }
        if (noted && !text && !ev.images && !ev.files?.length) break
        const item = this.#entry(ev.from === 'task' ? 'prompt task' : 'prompt', who)
        if (text) item.append(el('div', 'body', unspoofed(text)))
        if (ev.images) item.append(el('span', 'who count', `${ev.images} ${plural(ev.images, 'image')}`))
        if (ev.files?.length) item.append(el('span', 'who count', `${ev.files.length === 1 ? 'Copy' : 'Copies'} of ${ev.files.join(', ')}`))
        if (ev.lost) item.append(el('span', 'who count bad', `${ev.lost} ${plural(ev.lost, 'image')} couldn't be passed on to ${this.who}`))
        if (ev.from === 'page') this.#pin()
        break
      }
      case 'command': {
        this.#reply = null
        const view = ev.context && Array.isArray(ev.context.grid) && Array.isArray(ev.context.categories) ? ev.context : null
        const item = this.#entry(`reply command${ev.failed ? ' failed' : ''}`, view ? '' : `/${ev.name}`)
        item.append(view ? contextBody(view) : el('div', 'body', unspoofed(ev.text || '')))
        break
      }
      case 'text': {
        if (!this.#reply || this.#reply.turnId !== ev.turnId) {
          const li = this.#entry('reply', this.who)
          if (typeof ev.turnId === 'string') li.dataset.turn = ev.turnId
          this.#reply = { turnId: ev.turnId, body: li.appendChild(el('sf-markdown', 'body')) }
        }
        this.#reply.body.text += ev.text
        break
      }
      case 'tool': {
        this.#reply = null
        let li: HTMLLIElement
        if (ev.agentId) {
          li = el('li', 'tool run sub')
          this.#group(ev.agentId, ev.agentName).append(li)
        } else li = this.#entry('tool run')
        const mark = el('span', 'mark', '●')
        mark.setAttribute('aria-hidden', 'true')
        const call = el('div', 'call')
        call.append(el('b', '', TOOL_NAMES.get(ev.tool) || visible(ev.tool)))
        if (ev.summary && FILE_TOOLS.has(ev.tool) && ev.summary.startsWith('/') && !ev.summary.endsWith('…')) {
          const path = el('span', 'tool-path', visible(ev.summary))
          path.dataset.path = ev.summary
          call.append('(', path, ')')
        } else if (ev.summary) call.append(`(${visible(ev.summary)})`)
        li.append(mark, call, this.#outLine('Running…'))
        if (hidesText(ev.summary)) li.append(hiddenMark(ev.summary, 'This call'))
        this.#fresh.add(call)
        li.title = visible(ev.summary || ev.tool)
        if (ev.id) this.#tools.set(ev.id, li)
        if (call.querySelector('.tool-path')) emit(li, 'sf-tool-path')
        break
      }
      case 'tool_done': {
        const li = this.#tools.get(ev.id)
        if (li) {
          li.classList.remove('run')
          li.classList.add(ev.ok ? 'ok' : 'fail')
          li.querySelector('.tool-out')?.remove()
          if (ev.outcome) li.append(this.#outLine(visible(ev.outcome)))
          if (hidesText(ev.outcome)) li.append(hiddenMark(ev.outcome, 'This output'))
        }
        break
      }
      case 'idle':
        this.#reply = null
        for (const li of this.#timeline!.querySelectorAll('li.tool.run:not(.sub)')) {
          li.classList.remove('run')
          li.querySelector('.tool-out')?.remove()
        }
        if (ev.reason === 'aborted') this.#entry('reset small muted').textContent = 'Stopped.'
        else if (ev.reason === 'error') this.#entry('reset small muted').textContent = this.who === 'Claude' ? "The turn ended with an error. The session's log shows the details." : 'The turn ended with an error.'
        break
      case 'ask':
      case 'permission':
      case 'plan':
        if (ev.type === 'permission' && ev.away === true) {
          this.#entry('reset small muted').textContent = `Away mode is on, so Software Factory denied ${visible(ev.tool)} for you, with the note to the agent “the person is away; continue without this or stop”.`
          break
        }
        this.#showCard(ev, controlled)
        break
      case 'resolved':
        this.#dropCard(ev.id)
        break
      case 'ended':
        this.#entry('reset small muted').textContent = 'The session ended.'
        break
      case 'notice':
        if (typeof ev.text === 'string' && ev.text) this.#entry('reset small muted').textContent = `${ev.reported ? 'Reported by the session: ' : ''}${shown(ev.text)}`
        break
      case 'compact': {
        if (ev.state === 'start') break
        this.#reply = null
        const k = (n: number) => `${Math.round(n / 1000)}k`
        this.#entry('reset small muted').textContent = ev.state === 'done' ? `Conversation compacted${ev.before && ev.after ? `: about ${k(ev.before)} to ${k(ev.after)} tokens` : ''}.` : ev.state === 'skipped' ? `Compaction skipped${ev.reason ? `: ${visible(ev.reason)}` : ''}.` : "Compaction failed. The session's log shows the details."
        break
      }
    }
  }

  marks(list: TurnMarks[] | undefined, epoch: string): void {
    if (!this.#timeline) return
    const mine = (Array.isArray(list) ? list : []).filter(t => t && typeof t.turnId === 'string' && t.turnId && t.epoch === epoch && Array.isArray(t.marks) && Array.isArray(t.notes))
    const key = JSON.stringify(mine)
    if (key !== this.#marksKey) {
      for (const li of this.#marks.values()) li.remove()
      this.#marks.clear()
      this.#marksKey = key
      for (const t of mine) this.#marks.set(t.turnId, this.#markItem(t))
    }
    let moved = false
    for (const [turn, li] of this.#marks) {
      const replies = this.#timeline.querySelectorAll<HTMLLIElement>(`li.reply[data-turn="${CSS.escape(turn)}"]`)
      const last = replies[replies.length - 1]
      if (!last) li.remove()
      else if (last.nextElementSibling !== li) {
        last.after(li)
        moved = true
      }
    }
    if (moved) this.#stick()
  }

  #markItem(t: TurnMarks): HTMLLIElement {
    const li = el('li', 'unbacked')
    const marks = t.marks.filter((m: ClaimMark) => m && typeof m.quote === 'string' && typeof m.why === 'string')
    if (marks.length) {
      li.append(el('p', 'unbacked-head', "Not backed by this turn's actions"))
      const list = el('ul')
      for (const m of marks) {
        const item = el('li')
        item.append(el('q', '', visible(m.quote)), el('span', 'why', visible(m.why)))
        list.append(item)
      }
      li.append(list)
    }
    for (const note of t.notes) if (typeof note === 'string') li.append(el('p', 'unbacked-note', visible(note)))
    return li
  }

  refreshCards(controlled?: boolean): void {
    for (const node of this.#cards.values()) if (node.source) node.card = { ...node.source, readOnly: !!node.source.readOnly || !controlled }
  }

  #showCard(ev: CardEvent, controlled?: boolean) {
    if (this.#cards.has(ev.id)) return
    const node: CardHost = el(CARD_TAGS[ev.type])
    node.dataset.kind = ev.type
    node.source = ev
    node.card = { ...ev, readOnly: !!ev.readOnly || !controlled }
    const rank = CARD_ORDER.indexOf(ev.type)
    const before = [...this.#cards.values()].find(n => CARD_ORDER.indexOf(n.dataset.kind!) > rank)
    this.#needs!.insertBefore(node, before || null)
    this.#cards.set(ev.id, node)
    this.#heading!.hidden = false
    const who = ev.agentId ? (ev.agentName ? `Subagent ${shown(ev.agentName)}` : 'A subagent') : this.who
    this.#said!.textContent = ev.type === 'ask' ? `${who} asks a question` : ev.type === 'permission' ? `${who} asks to use ${visible(ev.tool)}` : 'Plan ready'
    this.#pin()
  }

  #dropCard(id: string) {
    const node = this.#cards.get(id)
    if (!node) return
    const had = 'answered' in node.dataset || node.contains(document.activeElement)
    node.remove()
    this.#cards.delete(id)
    this.#heading!.hidden = !this.#cards.size
    if (!had || (document.activeElement && document.activeElement !== document.body)) return
    const next = [...this.#cards.values()].map(n => n.querySelector<HTMLElement>('button:not(:disabled), input:not(:disabled)')).find(Boolean)
    if (next) next.focus()
    else emit(this, 'sf-settled')
  }
}

customElements.define('sf-conversation', SfConversation)

declare global {
  interface HTMLElementTagNameMap {
    'sf-conversation': SfConversation
  }
  interface HTMLElementEventMap {
    contentvisibilityautostatechange: ContentVisibilityAutoStateChangeEvent
  }
}
