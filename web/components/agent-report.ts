import { request, signedIn } from '../api.ts'
import { el, visible } from '../lib/dom.ts'
import { saidOf, type Said } from '../lib/json.ts'
import { emptyState } from '../lib/page.ts'
import { tipTag } from '../lib/tooltip.ts'

interface SettingsReport {
  sid: string
  session: string
  checkedAt: number
  sinceAt: number
  changed: string[]
  more: number
  first: boolean
  dropped: boolean
  running: boolean
  problem: string
  watching: number
}

interface Pin {
  name: string
  package: string
  version: string
  state: string
  repository: string
  workflow: string
  ref: string
  commit: string
  note: string
  link: string
  said: Said
}

interface PluginPin {
  name: string
  source: string
  link: string
  branch: string
  version: string
  commit: string
  refused: boolean
  refusedWhy: string
  installedAt: number
  checkedAt: number
  check: string
  head: string
  from: string
  problem: string
  checkLine: string
  checkTone: string
  checkStamped: boolean
  said: Said
}

interface Advisory {
  label: string
  id: string
  summary: string
  aliases: string[]
  link: string
}

interface Report {
  settings: { problem: string; sessions: SettingsReport[] }
  pins: Pin[]
  plugins: PluginPin[]
  advisories: { checkedAt: number; off: boolean; problem: string; found: Advisory[]; more: number }
}

const POLL_MS = 60000
const LINK = /^https:\/\/osv\.dev\/vulnerability\/[A-Za-z0-9][A-Za-z0-9._:-]{0,80}$/
const SOURCE = /^https:\/\/(www\.npmjs\.com\/package\/|github\.com\/)[A-Za-z0-9@._\/+-]{1,240}$/

function text(x: unknown): string {
  return typeof x === 'string' ? x : ''
}

function strings(x: unknown): string[] {
  return Array.isArray(x) ? x.filter((s): s is string => typeof s === 'string') : []
}

function record(x: unknown): Record<string, unknown> {
  return x && typeof x === 'object' ? x as Record<string, unknown> : {}
}

function list<T>(x: unknown, read: (r: Record<string, unknown>) => T): T[] {
  return Array.isArray(x) ? x.filter(v => v && typeof v === 'object').map(v => read(v as Record<string, unknown>)) : []
}

function readReport(body: unknown): Report | null {
  const v = record(body)
  if (!v.settings || !v.advisories) return null
  const s = record(v.settings)
  const a = record(v.advisories)
  return {
    settings: {
      problem: text(s.problem),
      sessions: list(s.sessions, h => ({ sid: text(h.sid), session: text(h.session), checkedAt: Number(h.checkedAt) || 0, sinceAt: Number(h.sinceAt) || 0, changed: strings(h.changed), more: Number(h.more) || 0, first: h.first === true, dropped: h.dropped === true, running: h.running === true, problem: text(h.problem), watching: Number(h.watching) || 0 })),
    },
    pins: list(v.pins, p => ({ name: text(p.name), package: text(p.package), version: text(p.version), state: text(p.state), repository: text(p.repository), workflow: text(p.workflow), ref: text(p.ref), commit: text(p.commit), note: text(p.note), link: text(p.link), said: saidOf(p.said) })),
    plugins: list(v.plugins, p => ({
      name: text(p.name), source: text(p.source), link: text(p.link), branch: text(p.branch), version: text(p.version), commit: text(p.commit), refused: p.refused === true, refusedWhy: text(p.refusedWhy), installedAt: Number(p.installedAt) || 0,
      checkedAt: Number(p.checkedAt) || 0, check: text(p.check), head: text(p.head), from: text(p.from), problem: text(p.problem), checkLine: text(p.checkLine), checkTone: text(p.checkTone), checkStamped: p.checkStamped === true, said: saidOf(p.said),
    })),
    advisories: {
      checkedAt: Number(a.checkedAt) || 0, off: a.off === true, problem: text(a.problem), more: Math.max(0, Math.floor(Number(a.more) || 0)),
      found: list(a.found, f => ({ label: text(f.label), id: text(f.id), summary: text(f.summary), aliases: strings(f.aliases), link: text(f.link) })),
    },
  }
}

function outLink(label: string, href: string, pattern: RegExp): HTMLElement {
  if (!pattern.test(href)) return el('span', '', label)
  const a = el('a', '', label)
  a.href = href
  a.rel = 'noreferrer noopener'
  a.target = '_blank'
  return a
}

function when(ms: number): string {
  return ms ? new Date(ms).toLocaleString() : ''
}

function short(sha: string): string {
  return /^[0-9a-f]{40}$/.test(sha) ? sha.slice(0, 7) : sha
}


class SfAgentReport extends HTMLElement {
  #timer = 0
  #gen = 0
  #report: Report | null = null

  connectedCallback() {
    this.hidden = !this.#report
    void this.#load()
  }

  disconnectedCallback() {
    this.#gen++
    window.clearTimeout(this.#timer)
    this.#timer = 0
  }

  async #load() {
    window.clearTimeout(this.#timer)
    this.#timer = 0
    const gen = ++this.#gen
    const a = signedIn() ? await request('/api/agent-report', { op: 'read' }) : null
    if (gen !== this.#gen) return
    if (a?.ok) {
      const r = readReport(a.body)
      if (r) this.#report = r
    }
    this.#paint()
    if (this.isConnected) this.#timer = window.setTimeout(() => void this.#load(), POLL_MS)
  }

  #settings(r: Report['settings']): HTMLElement[] {
    const out: HTMLElement[] = [el('h3', 'label', 'Agent settings')]
    if (r.problem) out.push(el('p', 'small warn', visible(r.problem)))
    else if (!r.sessions.length) out.push(emptyState('No session is running.'))
    for (const s of r.sessions) out.push(el('h4', 'label', `Session ${visible(s.session || s.sid)}`), ...this.#session(s))
    return out
  }

  #session(s: SettingsReport): HTMLElement[] {
    const out: HTMLElement[] = []
    if (s.problem) out.push(el('p', 'small warn', visible(s.problem)))
    if (!s.checkedAt) out.push(el('p', 'small muted', s.running ? 'Listing them now…' : "Not listed yet: the engine lists them at this session's next start or wake."))
    else if (s.first && s.dropped) out.push(el('p', 'small warn', `Listed at ${when(s.checkedAt)}: ${s.watching} ${s.watching === 1 ? 'file or server' : 'files and servers'}. The list from this container's previous start was dropped because the engine's record of these lists was full, so what changed since then can't be shown. The next start shows what changed after this one.`))
    else if (s.first) out.push(el('p', 'small muted', `Listed at this container's first start, ${when(s.checkedAt)}: ${s.watching} ${s.watching === 1 ? 'file or server' : 'files and servers'}. The next start shows what changed.`))
    else if (s.changed.length) {
      out.push(el('p', 'small warn', `Changed since the last start (${when(s.sinceAt)}), seen at ${when(s.checkedAt)}:`))
      const ul = el('ul', 'list small')
      for (const c of s.changed) ul.append(el('li', '', visible(c)))
      if (s.more) ul.append(el('li', 'muted', `and ${s.more} more`))
      out.push(ul)
    } else out.push(el('p', 'small ok', `No change since the last start (${when(s.sinceAt)}), seen at ${when(s.checkedAt)}.`))
    return out
  }

  #pins(r: Report): HTMLElement[] {
    const out: HTMLElement[] = [el('h3', 'label', 'Agent versions')]
    const ul = el('ul', 'list flat')
    for (const p of r.pins) {
      const note = p.state === 'verified' ? `Built by ${visible(p.repository)} with ${visible(p.workflow)} at ${visible(p.ref)}${p.commit ? `, commit ${visible(short(p.commit))}` : ''}.` : visible(p.note)
      const { label, tone } = p.said.label ? p.said : { label: visible(p.state), tone: 'muted' }
      const li = el('li', 'small')
      li.append(outLink(visible(p.package), p.link, SOURCE), el('span', 'muted', ` ${visible(p.version)} `), ...tipTag(label, tone, note))
      ul.append(li)
    }
    for (const p of r.plugins) ul.append(this.#plugin(p))
    out.push(ul)
    return out
  }

  #plugin(p: PluginPin): HTMLElement {
    const li = el('li', 'small')
    const source = visible(p.source.replace(/^https:\/\//, '').replace(/\.git$/, ''))
    const check = p.checkStamped ? `Checked at this start (${when(p.checkedAt)}): ${visible(p.checkLine)}` : visible(p.checkLine)
    const installed = p.commit ? `Installed at commit ${visible(short(p.commit))}${p.installedAt ? `, ${when(p.installedAt)}` : ''}.` : ''
    const tip = [`Follows the ${visible(p.branch)} branch of ${source}.`, installed, check].filter(Boolean).join(' ')
    const { label, tone } = p.said.label ? p.said : { label: visible(p.check), tone: 'muted' }
    li.append(outLink(visible(p.name), p.link, SOURCE))
    if (p.version || p.commit) li.append(el('span', 'muted', ` ${visible(p.version || short(p.commit))}`))
    li.append(' ', ...tipTag(label, tone, tip))
    if (!p.commit) li.append(el('p', 'small bad', `No ${visible(p.name)} is installed: reviews can't start, so every push needs your Allow with a reason.`))
    const unused = `reviews can't start, so every push needs your Allow with a reason, and new sessions start without ${visible(p.name)}`
    if (p.commit && p.refusedWhy) li.append(el('p', 'small bad', `Commit ${visible(short(p.commit))} doesn't pass this engine's install checks, so it isn't used: ${unused}. ${visible(p.refusedWhy)} At each start the engine installs the head of ${visible(p.branch)} if it passes them.`))
    else if (p.commit && p.refused) li.append(el('p', 'small bad', `This engine's list of refused ${visible(p.name)} commits names ${visible(short(p.commit))}, so it isn't used: ${unused}. At each start the engine installs the head of ${visible(p.branch)} if it isn't refused.`))
    return li
  }

  #advisories(r: Report): HTMLElement[] {
    const a = r.advisories
    const out: HTMLElement[] = [el('h3', 'label', 'Vulnerabilities')]
    if (a.off) out.push(el('p', 'small muted', 'Advisory lookups are off (SF_OSV=off).'))
    else if (a.problem) out.push(el('p', 'small warn', visible(a.problem)))
    else if (!a.checkedAt) out.push(el('p', 'small muted', 'Not looked up yet: the engine asks OSV shortly after it starts, then every 24 hours.'))
    else if (a.found.length) {
      out.push(el('p', 'small bad', `A pinned version or the installed knackpack commit has a known advisory (looked up ${when(a.checkedAt)}). Read it, and move the pin or wait for a fixed knackpack if it applies.`))
      const ul = el('ul', 'list small')
      for (const f of a.found) {
        const li = el('li')
        li.append(el('strong', '', visible(f.label)), ': ')
        li.append(outLink(visible(f.id), f.link, LINK))
        if (f.aliases.length) li.append(el('span', 'muted', ` (${f.aliases.map(visible).join(', ')})`))
        if (f.summary) li.append(` ${visible(f.summary)}`)
        ul.append(li)
      }
      if (a.more) ul.append(el('li', 'muted', `and ${a.more} more: the list stops at ${a.found.length}, so read the rest at osv.dev`))
      out.push(ul)
    } else out.push(el('p', 'small ok', `No known advisory for a pinned version or the installed knackpack commit (looked up ${when(a.checkedAt)}).`))
    return out
  }

  #paint() {
    const r = this.#report
    this.hidden = !r
    if (!r) return this.replaceChildren()
    this.replaceChildren(...this.#advisories(r), ...this.#settings(r.settings), ...this.#pins(r))
  }
}

customElements.define('sf-agent-report', SfAgentReport)

declare global {
  interface HTMLElementTagNameMap {
    'sf-agent-report': SfAgentReport
  }
}
