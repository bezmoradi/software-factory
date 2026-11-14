import { api } from '../api.ts'
import { button, el, emit, focusedKey, refocus, shown, unspoofed, visibleCommand } from '../lib/dom.ts'
import { label, shortSha } from '../lib/format.ts'
import { money } from './consent.ts'
import { agentName } from './lineage.ts'
import type { Changes, SessionInfo } from '../types.ts'

const SHA = /^[0-9a-f]{40}$/

export interface CompareSession {
  sid: string
  info: SessionInfo
  cost: number
  total: number
}

export interface CompareView {
  sid: string
  info: SessionInfo
  sessions: CompareSession[]
  disabled: string
}

interface PairMember {
  agent: string
  model: string
  effort: string
  sid: string
}

interface PairRecord {
  id: string
  repo: string
  commit: string
  origin: string
  name: string
  message: string
  members: PairMember[]
  kept: string
  historyOnly: boolean
  benchmark: { number: number; title: string; url: string; base: string; head: string; merge: string } | null
  at: number
}

export type PairNext = (pair: string, agent: string) => Promise<string>

async function call(body: unknown): Promise<{ ok: boolean; body: Record<string, unknown> }> {
  const res = await api('/api/pair', body).catch(() => null)
  const parsed = res ? ((await res.json().catch(() => ({}))) as Record<string, unknown>) : {}
  return { ok: !!res && res.ok, body: parsed }
}

function said(body: Record<string, unknown>, fallback: string): string {
  return typeof body.message === 'string' && body.message ? shown(body.message) : fallback
}

function pairOf(raw: unknown): PairRecord | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as PairRecord
  if (typeof r.id !== 'string' || !Array.isArray(r.members) || typeof r.message !== 'string') return null
  return r
}

function elapsed(from: number | undefined): string {
  if (!from) return ''
  const minutes = Math.max(0, Math.round((Date.now() - from) / 60000))
  return minutes < 60 ? `${minutes} min` : `${Math.floor(minutes / 60)} h ${minutes % 60} min`
}

export function confirmBoth(): Promise<boolean> {
  return new Promise(resolve => {
    const d = el('dialog', 'consent surface')
    d.setAttribute('aria-label', 'Start both sessions')
    const done = (answer: boolean) => {
      d.close()
      d.remove()
      resolve(answer)
    }
    const actions = el('div', 'row consent-actions')
    actions.append(el('span', 'spacer'), button('Cancel', 'ghost small', () => done(false)), button('Start both', 'primary small', () => done(true)))
    d.append(el('h2', 'label', 'Start both sessions?'),
      el('p', 'small', 'Starting both runs two sessions on the same task at once, which doubles usage, even on a subscription: each agent spends its own sign-in or key. Each start still shows its own start-up files and cost checks.'),
      actions)
    d.addEventListener('cancel', e => {
      e.preventDefault()
      done(false)
    })
    document.body.append(d)
    d.showModal()
  })
}

function diffBox(c: Changes, what: string): HTMLElement {
  const box = el('div', 'compare-diff')
  box.append(el('p', 'small', `${c.files.length} ${c.files.length === 1 ? 'file' : 'files'} changed${c.more ? ', more not listed' : ''}${c.commits.length ? `, ${c.commits.length} ${c.commits.length === 1 ? 'commit' : 'commits'}` : ''}`))
  if (c.files.length) {
    const ul = el('ul', 'list small compare-files')
    for (const f of c.files.slice(0, 200)) ul.append(el('li', '', `${f.status === 'added' ? 'A' : f.status === 'deleted' ? 'D' : 'M'} ${shown(f.path)}`))
    box.append(ul)
  }
  if (c.diff) {
    const pre = el('pre', 'forge-body compare-text', visibleCommand(unspoofed(c.diff)))
    pre.tabIndex = 0
    pre.setAttribute('aria-label', what)
    box.append(pre)
  }
  if (c.clipped) box.append(el('p', 'small warn', 'The diff is cut here; the full change is on the branch.'))
  if (c.note) box.append(el('p', 'small muted', shown(c.note)))
  return box
}

class SfCompare extends HTMLElement {
  next: PairNext | null = null
  #view: CompareView | null = null
  #pair: PairRecord | null = null
  #pairKey = ''
  #loading = ''
  #key = ''
  #note = ''
  #diffs = new Map<string, Changes | string>()
  #busy = false

  connectedCallback() {
    if (!this.#view) this.hidden = true
  }

  set view(value: CompareView | null) {
    this.#view = value
    const l = value?.info.lineage
    const id = l?.groupKind === 'pair' && l.group ? l.group : ''
    const members = value && id ? value.sessions.filter(s => s.info.lineage?.groupKind === 'pair' && s.info.lineage.group === id).map(s => s.sid).sort() : []
    const pairKey = `${id}:${members.join(',')}`
    if (id && pairKey !== this.#pairKey && this.#loading !== pairKey) void this.#load(id, pairKey)
    if (!id) {
      this.#pair = null
      this.#pairKey = ''
      this.#diffs.clear()
    }
    const rows = value && id ? value.sessions.filter(s => members.includes(s.sid)).map(s => [s.sid, s.info.running, s.info.review, s.cost, s.total, label(s.info)]) : []
    const key = JSON.stringify([pairKey, rows, value?.disabled, this.#note, this.#busy, [...this.#diffs.keys()], this.#pair])
    if (key === this.#key) return
    this.#key = key
    this.paint()
  }

  async #load(id: string, pairKey: string) {
    this.#loading = pairKey
    const a = await call({ op: 'view', pair: id })
    if (this.#loading !== pairKey) return
    this.#loading = ''
    this.#pairKey = pairKey
    this.#pair = a.ok ? pairOf(a.body) : null
    this.#note = a.ok ? '' : said(a.body, "The comparison couldn't be read.")
    this.#key = ''
    this.view = this.#view
  }

  paint() {
    const v = this.#view
    const p = this.#pair
    if (!v || !p) {
      this.hidden = !(v && this.#note)
      this.replaceChildren(...(v && this.#note ? [el('p', 'small bad', this.#note)] : []))
      return
    }
    const focused = focusedKey(this)
    const parts: Node[] = [el('h2', 'label', p.benchmark ? 'Benchmark comparison' : 'Comparison of both sessions')]
    const from = SHA.test(p.commit) ? shortSha(p.commit) : ''
    parts.push(el('p', 'small muted', p.benchmark
      ? `Started at ${from}, the base of merged pull request #${p.benchmark.number} ${shown(p.benchmark.title)}, with its description as the first message. The merged diff is the change its author made.`
      : `${p.members.length} sessions started on their own branches from commit ${from} with one first message. Cost is each agent's own estimate, which may differ from the bill.`))
    const first = el('details', 'compare-message')
    first.append(el('summary', 'small', 'The first message they got'))
    const text = el('pre', 'forge-body', visibleCommand(unspoofed(p.message)))
    text.tabIndex = 0
    text.setAttribute('aria-label', 'The first message')
    first.append(text)
    parts.push(first)
    const grid = el('div', 'compare-grid')
    for (const m of p.members) grid.append(this.#column(v, p, m))
    if (p.benchmark) grid.append(this.#merged(p))
    parts.push(grid)
    if (this.#note) parts.push(el('p', 'small bad', this.#note))
    this.hidden = false
    this.replaceChildren(...parts)
    refocus(this, focused)
  }

  #column(v: CompareView, p: PairRecord, m: PairMember): HTMLElement {
    const col = el('section', 'compare-col')
    const who = `${agentName(m.agent)}${m.model ? ` ${shown(m.model)}` : ''}${m.effort ? `, ${shown(m.effort)} effort` : ''}`
    col.append(el('h3', 'label', who))
    if (!m.sid && p.historyOnly) {
      col.append(el('p', 'small muted', "This session didn't start, and this pair came from an export of another engine, so it can't start here."))
      return col
    }
    if (!m.sid) {
      col.append(el('p', 'small warn', "This session didn't start."))
      const b = button(this.#busy ? 'Starting…' : 'Start it', 'small', () => {
        if (!this.#busy && !v.disabled) void this.#startSecond(p, m)
      }, 'button', `next:${p.id}`)
      b.setAttribute('aria-disabled', String(this.#busy || !!v.disabled))
      col.append(b)
      return col
    }
    const s = v.sessions.find(x => x.sid === m.sid)
    if (!s) {
      col.append(el('p', 'small muted', p.kept && p.kept !== m.sid ? 'Deleted; you kept the other.' : 'This session was deleted.'))
      return col
    }
    const head = el('div', 'row compare-head')
    head.append(el('span', `tag ${s.info.running ? 'warn' : ''}`, s.info.running ? 'running' : 'stopped'))
    if (p.kept === m.sid) head.append(el('span', 'tag ok', 'kept'))
    head.append(el('span', 'small', shown(label(s.info))))
    col.append(head)
    const facts = el('ul', 'list small compare-facts')
    const time = elapsed(s.info.startedAt)
    if (time) facts.append(el('li', '', `Started ${time} ago`))
    const cost = money(s.cost)
    facts.append(el('li', '', cost ? `≈ ${cost}, the agent's estimate${s.total ? `, ${s.total.toLocaleString()} tokens` : ''}` : 'Cost: not reported yet (open the session to read it)'))
    const roles = s.info.review?.roles ?? []
    if (roles.length) for (const r of roles) facts.append(el('li', '', `${shown(r.name || r.role)}: ${shown(r.verdict || r.state)}`))
    else facts.append(el('li', '', 'No review yet'))
    col.append(facts)
    const row = el('div', 'actions')
    row.append(button('Open', 'ghost small', () => emit(this, 'sf-open-session', { sid: m.sid }), 'button', `open:${m.sid}`))
    if (p.kept !== m.sid && !p.historyOnly) row.append(this.#keep(v, p, m.sid))
    if (p.kept && p.kept !== m.sid && !s.info.running) row.append(button('Delete this session', 'small danger', () => {
      if (!v.disabled) emit(this, 'sf-delete-session', { sid: m.sid })
    }, 'button', `delete:${m.sid}`))
    row.append(this.#diffButton(m.sid, () => api('/api/changes', { sid: m.sid })))
    col.append(row)
    const d = this.#diffs.get(m.sid)
    if (typeof d === 'string') col.append(el('p', 'small bad', d))
    else if (d) col.append(diffBox(d, `The change of ${who}`))
    return col
  }

  #merged(p: PairRecord): HTMLElement {
    const col = el('section', 'compare-col')
    const b = p.benchmark!
    col.append(el('h3', 'label', `Merged pull request #${b.number}`))
    col.append(el('p', 'small muted', `What its author merged, from where it left its base ${shortSha(b.base)} up to ${shortSha(b.head)}.`))
    const key = `merged:${p.id}`
    col.append(this.#diffButton(key, () => api('/api/pair', { op: 'merged-diff', pair: p.id })))
    const d = this.#diffs.get(key)
    if (typeof d === 'string') col.append(el('p', 'small bad', d))
    else if (d) col.append(diffBox(d, `The merged change of pull request #${b.number}`))
    return col
  }

  #keep(v: CompareView, p: PairRecord, sid: string): HTMLButtonElement {
    const b = button('Keep this one', 'small', () => {
      if (this.#busy || v.disabled) return
      void (async () => {
        this.#busy = true
        const a = await call({ op: 'keep', pair: p.id, sid })
        this.#busy = false
        this.#note = a.ok ? '' : said(a.body, "The choice wasn't saved.")
        if (a.ok) p.kept = sid
        this.#key = ''
        this.view = this.#view
      })()
    }, 'button', `keep:${sid}`)
    b.setAttribute('aria-disabled', String(this.#busy || !!v.disabled))
    return b
  }

  #diffButton(key: string, load: () => Promise<Response>): HTMLButtonElement {
    const shownNow = this.#diffs.has(key)
    return button(shownNow ? 'Hide the diff' : 'Show the diff', 'small ghost', () => {
      if (this.#diffs.has(key)) this.#diffs.delete(key)
      else
        void (async () => {
          const res = await load().catch(() => null)
          const body = res ? ((await res.json().catch(() => ({}))) as Record<string, unknown>) : {}
          const changes = body.changes as Changes | undefined
          this.#diffs.set(key, res?.ok && changes && Array.isArray(changes.files) ? changes : said(body, "The diff couldn't be read."))
          this.#key = ''
          this.view = this.#view
        })()
      this.#key = ''
      this.view = this.#view
    }, 'button', `diff:${key}`)
  }

  async #startSecond(p: PairRecord, m: PairMember) {
    if (!this.next) return
    this.#busy = true
    this.#key = ''
    this.view = this.#view
    const sid = await this.next(p.id, m.agent)
    this.#busy = false
    this.#note = sid ? '' : "The second session didn't start."
    this.#pairKey = ''
    this.#key = ''
    this.view = this.#view
  }
}

customElements.define('sf-compare', SfCompare)

declare global {
  interface HTMLElementTagNameMap {
    'sf-compare': SfCompare
  }
  interface HTMLElementEventMap {
    'sf-delete-session': CustomEvent<{ sid: string }>
  }
}
