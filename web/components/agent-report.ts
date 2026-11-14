import { api, signedIn } from '../api.ts'
import { el, visible } from '../lib/dom.ts'

interface SettingsReport {
  checkedAt: number
  sinceAt: number
  changed: string[]
  more: number
  first: boolean
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
}

interface PluginPin {
  name: string
  version: string
  commit: string
}

interface Advisory {
  label: string
  id: string
  summary: string
  aliases: string[]
  link: string
}

interface Report {
  settings: SettingsReport
  pins: Pin[]
  plugins: PluginPin[]
  advisories: { checkedAt: number; off: boolean; problem: string; found: Advisory[]; more: number; sent: string[] }
}

const POLL_MS = 60000
const LINK = /^https:\/\/osv\.dev\/vulnerability\/[A-Za-z0-9][A-Za-z0-9._:-]{0,80}$/

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
    settings: { checkedAt: Number(s.checkedAt) || 0, sinceAt: Number(s.sinceAt) || 0, changed: strings(s.changed), more: Number(s.more) || 0, first: s.first === true, running: s.running === true, problem: text(s.problem), watching: Number(s.watching) || 0 },
    pins: list(v.pins, p => ({ name: text(p.name), package: text(p.package), version: text(p.version), state: text(p.state), repository: text(p.repository), workflow: text(p.workflow), ref: text(p.ref), commit: text(p.commit), note: text(p.note) })),
    plugins: list(v.plugins, p => ({ name: text(p.name), version: text(p.version), commit: text(p.commit) })),
    advisories: {
      checkedAt: Number(a.checkedAt) || 0, off: a.off === true, problem: text(a.problem), sent: strings(a.sent), more: Math.max(0, Math.floor(Number(a.more) || 0)),
      found: list(a.found, f => ({ label: text(f.label), id: text(f.id), summary: text(f.summary), aliases: strings(f.aliases), link: text(f.link) })),
    },
  }
}

function when(ms: number): string {
  return ms ? new Date(ms).toLocaleString() : ''
}

function short(sha: string): string {
  return /^[0-9a-f]{40}$/.test(sha) ? sha.slice(0, 7) : sha
}

const PIN_STATES: Record<string, [string, string]> = {
  verified: ['Provenance verified', 'ok'],
  none: ['No provenance', 'warn'],
  unpinned: ['Provenance not checked against a source', 'warn'],
  failed: ['Provenance failed', 'bad'],
  unchecked: ['Provenance not checked yet', 'muted'],
}

class SfAgentReport extends HTMLElement {
  #timer = 0
  #report: Report | null = null

  connectedCallback() {
    this.hidden = !this.#report
    void this.#load()
  }

  disconnectedCallback() {
    window.clearTimeout(this.#timer)
    this.#timer = 0
  }

  async #load() {
    window.clearTimeout(this.#timer)
    const res = signedIn() ? await api('/api/agent-report', { op: 'read' }).catch(() => null) : null
    if (res && res.ok) {
      const r = readReport(await res.json().catch(() => null))
      if (r) this.#report = r
    }
    this.#paint()
    if (this.isConnected) this.#timer = window.setTimeout(() => void this.#load(), POLL_MS)
  }

  #settings(s: SettingsReport): HTMLElement[] {
    const out: HTMLElement[] = [el('h3', 'small', "The agents' user settings")]
    if (s.problem) out.push(el('p', 'small warn', visible(s.problem)))
    if (!s.checkedAt) out.push(el('p', 'small muted', s.running ? 'Listing them now…' : 'Not listed yet: the engine lists them at each session start or wake.'))
    else if (s.first) out.push(el('p', 'small muted', `Listed at the first start, ${when(s.checkedAt)}: ${s.watching} files and servers. The next start shows what changed.`))
    else if (s.changed.length) {
      out.push(el('p', 'small warn', `Changed since the last start (${when(s.sinceAt)}), seen at ${when(s.checkedAt)}:`))
      const ul = el('ul', 'list small')
      for (const c of s.changed) ul.append(el('li', '', visible(c)))
      if (s.more) ul.append(el('li', 'muted', `and ${s.more} more`))
      out.push(ul)
    } else out.push(el('p', 'small ok', `No change since the last start (${when(s.sinceAt)}), seen at ${when(s.checkedAt)}.`))
    out.push(el('p', 'small muted', "At each start agent-host lists and hashes the agents' user-level settings (Claude Code's settings, agents, skills, commands, hooks and instructions, user MCP servers as claude mcp list prints them, Codex's config, git config, shell start-up files and auto-memory folders). This is an agent report from inside the agent container: it catches changes made by an honest session or a third party, not by a hostile agent, which can change agent-host's answer too. It never reads Claude Code's account file or a sign-in file, and it compares ~/.npmrc and ~/.ssh by size and time only."))
    return out
  }

  #pins(r: Report): HTMLElement[] {
    const out: HTMLElement[] = [el('h3', 'small', 'Pinned agents')]
    const ul = el('ul', 'list')
    for (const p of r.pins) {
      const li = el('li', 'small')
      const [label, tone] = PIN_STATES[p.state] ?? [visible(p.state), 'muted']
      li.append(el('strong', '', `${visible(p.name || p.package)} ${visible(p.version)}`), el('span', 'muted', ` ${visible(p.package)} `), el('span', `tag ${tone}`, label))
      if (p.state === 'verified') li.append(el('p', 'small muted', `Built by ${visible(p.repository)} with ${visible(p.workflow)} at ${visible(p.ref)}${p.commit ? `, commit ${visible(short(p.commit))}` : ''}.`))
      else if (p.note) li.append(el('p', 'small muted', visible(p.note)))
      ul.append(li)
    }
    for (const p of r.plugins) {
      const li = el('li', 'small')
      li.append(el('strong', '', `${visible(p.name)} ${visible(p.version)}`), el('p', 'small muted', `Pinned by git commit ${visible(short(p.commit))}. npm provenance doesn't apply to it: the commit itself is the pin.`))
      ul.append(li)
    }
    out.push(ul)
    return out
  }

  #advisories(r: Report): HTMLElement[] {
    const a = r.advisories
    const out: HTMLElement[] = [el('h3', 'small', 'Known vulnerabilities')]
    if (a.off) out.push(el('p', 'small muted', 'Advisory lookups are off (SF_OSV=off).'))
    else if (a.problem) out.push(el('p', 'small warn', visible(a.problem)))
    else if (!a.checkedAt) out.push(el('p', 'small muted', 'Not looked up yet: the engine asks OSV shortly after it starts, then every 24 hours.'))
    else if (a.found.length) {
      out.push(el('p', 'small bad', `A pinned version has a known advisory (looked up ${when(a.checkedAt)}). Read it and move the pin if it applies.`))
      const ul = el('ul', 'list small')
      for (const f of a.found) {
        const li = el('li')
        li.append(el('strong', '', visible(f.label)), ': ')
        if (LINK.test(f.link)) {
          const link = el('a', '', visible(f.id))
          link.href = f.link
          link.rel = 'noreferrer noopener'
          link.target = '_blank'
          li.append(link)
        } else li.append(el('span', '', visible(f.id)))
        if (f.aliases.length) li.append(el('span', 'muted', ` (${f.aliases.map(visible).join(', ')})`))
        if (f.summary) li.append(` ${visible(f.summary)}`)
        ul.append(li)
      }
      if (a.more) ul.append(el('li', 'muted', `and ${a.more} more: the list stops at ${a.found.length}, so read the rest at osv.dev`))
      out.push(ul)
    } else out.push(el('p', 'small ok', `No known advisory for a pinned version (looked up ${when(a.checkedAt)}).`))
    if (!a.off && a.sent.length) out.push(el('p', 'small muted', `The engine asks OSV (api.osv.dev) about ${a.sent.map(visible).join(', ')}: package names, versions and the commit, nothing else. The agent container never reaches OSV.`))
    return out
  }

  #paint() {
    const r = this.#report
    this.hidden = !r
    if (!r) return this.replaceChildren()
    this.replaceChildren(el('h2', '', 'Agents'), ...this.#advisories(r), ...this.#settings(r.settings), ...this.#pins(r))
  }
}

customElements.define('sf-agent-report', SfAgentReport)

declare global {
  interface HTMLElementTagNameMap {
    'sf-agent-report': SfAgentReport
  }
}
