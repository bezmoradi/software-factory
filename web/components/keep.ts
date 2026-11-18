import { api } from '../api.ts'
import { busy, button, el, focusedKey, refocus, replaceKeeping, say, statusLine, visible, whileBusy } from '../lib/dom.ts'
import { isRecord } from '../lib/json.ts'
import { shortSha } from '../lib/format.ts'
import { bytes } from './workspace.ts'

const ROUTE = '/api/keep'
const SHA = /^[0-9a-f]{40}$/
const VERDICTS = ['pass', 'block', 'overridden', 'none'] as const

type Verdict = typeof VERDICTS[number]

interface KeptRole {
  name: string
  verdict: Verdict
}

interface KeptHead {
  commit: string
  at: number
  holds: boolean
  restorable: boolean
  refusal: string
  reviewed: boolean
  rolesRead: boolean
  roles: KeptRole[]
  passed: boolean
}

interface KeepView {
  branch: string
  commit: string
  at: number
  bytes: number
  packs: number
  stopped: string
  kind: string
  why: string
  head: string
  holds: boolean
  copying: boolean
  restorable: boolean
  refusal: string
  sessionCap: number
  wasStopped: boolean
  heads: KeptHead[]
}

export interface KeepTarget {
  sid: string
  disabled: string
}

const text = (v: unknown) => (typeof v === 'string' ? v : '')
const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) && v >= 0 ? v : 0)
const sha = (v: unknown) => (typeof v === 'string' && SHA.test(v) ? v : '')

const verdict = (v: unknown): Verdict => VERDICTS.find(x => x === v) ?? 'none'

function readRole(v: unknown): KeptRole | null {
  if (!isRecord(v) || !text(v.name)) return null
  return { name: text(v.name), verdict: verdict(v.verdict) }
}

function readHead(v: unknown): KeptHead | null {
  if (!isRecord(v) || !sha(v.commit)) return null
  const roles = Array.isArray(v.roles) ? v.roles.map(readRole).filter(r => r !== null) : []
  return {
    commit: sha(v.commit), at: num(v.at), holds: v.holds === true, restorable: v.restorable === true, refusal: text(v.refusal),
    reviewed: v.reviewed === true, rolesRead: v.rolesRead === true, roles, passed: v.passed === true && roles.length > 0,
  }
}

function readView(v: unknown): KeepView | null {
  if (!isRecord(v)) return null
  const heads = Array.isArray(v.heads) ? v.heads.map(readHead).filter(h => h !== null) : []
  return {
    branch: text(v.branch), commit: sha(v.commit), at: num(v.at), bytes: num(v.bytes), packs: num(v.packs), stopped: sha(v.stopped), kind: text(v.kind), why: text(v.why),
    head: sha(v.head), holds: v.holds === true, copying: v.copying === true, restorable: v.restorable === true, refusal: text(v.refusal), sessionCap: num(v.sessionCap), wasStopped: v.wasStopped === true,
    heads,
  }
}

function ago(n: number): string {
  if (n === 0) return 'the newest kept commit'
  return `${n} kept ${n === 1 ? 'commit' : 'commits'} ago`
}

function reviewsOf(h: KeptHead): string {
  if (!h.reviewed) return 'No review recorded for this commit.'
  if (!h.rolesRead) return 'Reviews are recorded for this commit, but the engine couldn’t tell which roles it needs.'
  if (!h.roles.length) return 'This commit’s work type needs no review.'
  return `Recorded reviews: ${h.roles.map(r => `${visible(r.name)} ${r.verdict}`).join(' · ')}.`
}

async function problemOf(res: Response | null): Promise<string> {
  if (!res) return 'The engine didn’t answer. Try again.'
  const body = await res.json().catch(() => null) as { message?: unknown } | null
  return typeof body?.message === 'string' && body.message ? visible(body.message) : 'The engine didn’t restore the branch. Try again.'
}

function when(ms: number): string {
  return ms ? new Date(ms).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : ''
}

class SfKeep extends HTMLElement {
  #target: KeepTarget | null = null
  #view: KeepView | null = null
  #key = ''
  #loading = 0
  #busy = false
  #confirm: string | null = null
  #asked = ''
  #status = statusLine()
  #details = el('details', 'session-work')
  #summary = el('summary', 'small')
  #opened: boolean | null = null
  #shown = false

  constructor() {
    super()
    this.#details.addEventListener('toggle', () => {
      if (this.#details.open !== this.#shown) {
        this.#opened = this.#details.open
        if (this.#details.open && this.#target?.sid) void this.refresh(this.#target.sid)
      }
      this.#shown = this.#details.open
    })
  }

  set view(value: KeepTarget | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    const changed = value?.sid !== this.#target?.sid
    this.#target = value
    if (!value?.sid) {
      this.#view = null
      this.hidden = true
      this.replaceChildren()
      return
    }
    if (changed) {
      this.#view = null
      this.#confirm = null
      this.#asked = ''
      this.#opened = null
      this.#status.textContent = ''
      this.hidden = true
      void this.refresh(value.sid)
    } else this.#paint()
  }

  async refresh(sid: string) {
    if (!sid || sid !== this.#target?.sid) return
    const n = ++this.#loading
    const res = await api(ROUTE, { op: 'read', sid }).catch(() => null)
    if (n !== this.#loading || sid !== this.#target?.sid) return
    if (!res || !res.ok) {
      if (res && res.status !== 404 && res.status !== 501) return
      this.#view = null
      this.#status.textContent = ''
      this.hidden = true
      this.replaceChildren()
      return
    }
    const v = readView(await res.json().catch(() => null))
    if (!v) return
    this.#view = v
    this.#paint()
  }

  async #ask(commit: string) {
    const sid = this.#target?.sid
    if (!sid || this.#busy) return
    this.#opened ??= this.#details.open
    this.#asked = commit
    await this.refresh(sid)
    if (sid !== this.#target?.sid) return
    const v = this.#view
    const h = commit ? v?.heads.find(x => x.commit === commit) : null
    const restorable = commit ? h?.restorable : v?.restorable
    if (restorable) {
      this.#confirm = commit
      this.#status.textContent = ''
    } else {
      const why = commit ? (h ? h.refusal : 'The engine’s copy no longer lists that commit.') : v?.refusal
      say(this.#status, visible(why || 'The branch can’t be restored now.'), 'warn')
    }
    this.#paint()
  }

  async #restore() {
    const sid = this.#target?.sid
    const commit = this.#confirm
    if (!sid || this.#busy || commit === null) return
    this.#busy = true
    this.#confirm = null
    say(this.#status, 'Restoring the branch…', 'muted')
    this.#paint()
    const res = await api(ROUTE, commit ? { op: 'restore', sid, commit } : { op: 'restore', sid }).catch(() => null)
    this.#busy = false
    if (sid !== this.#target?.sid) return
    if (!res || !res.ok) {
      say(this.#status, await problemOf(res), 'warn')
      void this.refresh(sid)
      return
    }
    const v = readView(await res.json().catch(() => null))
    if (v) this.#view = v
    const back = v?.head || commit || v?.commit || ''
    const at = back ? ` at ${shortSha(back)}` : ''
    say(this.#status, `The branch ${visible(v?.branch ?? '')} is back${at}, with its worktree.${v?.wasStopped ? ' The engine stopped the session to do it; wake it from the ⋯ menu to go on.' : ''}`, 'ok')
    this.#paint()
  }

  #summaryText(v: KeepView): string {
    if (!v.commit) return v.why ? 'Engine copy of the commits: stopped' : 'Engine copy of the commits: nothing yet'
    const parts = [`Engine copy of the commits: up to ${shortSha(v.commit)} (${bytes(v.bytes)})`]
    if (v.why) parts.push('stopped')
    if (!v.holds) parts.push(v.head ? 'the branch no longer holds it' : 'the branch is gone')
    return parts.join(' · ')
  }

  #paint() {
    const t = this.#target
    const v = this.#view
    if (!t?.sid || !v) return
    const focused = focusedKey(this)
    const parts: HTMLElement[] = [el('p', 'small muted', `When a turn ends, the engine copies the commits on ${visible(v.branch)} that main lacks into its own store, which no agent can reach, so Restore this on a kept commit can bring the branch and its worktree back to it if an agent deletes the branch or resets it.`)]
    if (v.commit) {
      parts.push(el('p', 'small', `Last copied commit: ${shortSha(v.commit)}${v.at ? `, ${when(v.at)}` : ''}. The copy holds ${bytes(v.bytes)} in ${v.packs} ${v.packs === 1 ? 'pack' : 'packs'}, of ${bytes(v.sessionCap)} for one session.`))
      if (!v.head) parts.push(el('p', 'small warn', `The branch ${visible(v.branch)} isn’t in the clone now.`))
      else if (v.holds) parts.push(el('p', 'small', `The branch holds that commit${v.head !== v.commit ? `; it is at ${shortSha(v.head)} now` : ''}.`))
      else if (v.heads.some(h => h.commit === v.head)) parts.push(el('p', 'small', `The branch is at ${shortSha(v.head)} now, an earlier kept commit.`))
      else parts.push(el('p', 'small warn', `The branch is at ${shortSha(v.head)} now, which doesn’t hold the last copied commit.`))
    }
    if (v.why) {
      const where = v.stopped ? `Copying stopped at commit ${shortSha(v.stopped)}: ` : 'Copying stopped: '
      const resume = v.stopped && v.kind !== 'session-cap' ? ` The copy stays at ${v.commit ? shortSha(v.commit) : 'no commit'}, and copying goes on once the branch no longer holds commit ${shortSha(v.stopped)}.` : ''
      parts.push(el('p', 'small warn', `${where}${visible(v.why)}.${resume}`))
    }
    if (v.copying) parts.push(el('p', 'small muted', 'Copying the commits of the last turn…'))
    const off = !!t.disabled || this.#busy || !v.commit
    if (!v.heads.length) {
      const go = button('Restore branch', 'small', () => {
        if (!off) void whileBusy(go, () => this.#ask(''))
      }, 'button', 'keep-restore')
      go.setAttribute('aria-disabled', String(off))
      if (this.#busy) busy(go, true)
      const why = t.disabled || (!v.commit ? v.refusal : '')
      if (why) {
        go.title = visible(why)
        go.setAttribute('aria-describedby', 'keep-why')
      }
      const row = el('div', 'row')
      row.append(go)
      if (why) {
        const said = el('span', 'small muted', visible(why))
        said.id = 'keep-why'
        row.append(said)
      }
      parts.push(row)
    } else parts.push(...this.#heads(t, v, off))
    parts.push(this.#status)
    this.#summary.textContent = this.#summaryText(v)
    this.#shown = this.#opened ?? (!!v.why || (!!v.commit && !v.holds))
    this.#details.open = this.#shown
    replaceKeeping(this.#details, [this.#summary, ...parts])
    if (this.#details.parentNode !== this) this.replaceChildren(this.#details)
    this.hidden = false
    refocus(this, focused, () => [this.#confirm !== null ? 'keep-confirm' : `keep-restore-${this.#asked}`, 'keep-restore'])
  }

  #confirmation(v: KeepView, commit: string, newest: boolean): HTMLElement[] {
    const later = newest ? '' : ' The later kept commits stay in the engine’s copy, so you can restore one of them again.'
    const said = el('p', 'small', `Restore moves ${visible(v.branch)} to ${shortSha(commit)} and has agent-host remove the session’s worktree and add it again at that commit. A running session is stopped first, and a worktree with changes that aren’t committed is left as it is.${later}`)
    const row = el('div', 'row')
    row.append(
      button('Cancel', 'ghost small', () => {
        this.#confirm = null
        this.#paint()
      }, 'button', 'keep-cancel'),
      button('Restore branch now', 'primary small', () => void this.#restore(), 'button', 'keep-confirm'),
    )
    return [said, row]
  }

  #heads(t: KeepTarget, v: KeepView, off: boolean): HTMLElement[] {
    const out: HTMLElement[] = [el('p', 'small muted', 'Kept commits, newest first, each with the reviews the engine recorded for that commit on this branch. A push also needs the approvals, the team rules and main still holding what was reviewed, which this list doesn’t check.')]
    const best = v.heads.find(h => h.passed)
    const passed = best ? v.heads.indexOf(best) : -1
    if (best) out.push(el('p', 'small ok', `Reviews recorded as passed at ${shortSha(best.commit)}, ${ago(passed)}.`))
    if (t.disabled) {
      const said = el('p', 'small muted', visible(t.disabled))
      said.id = 'keep-why'
      out.push(said)
    }
    const list = el('ul', 'list reviews')
    v.heads.forEach((h, i) => {
      const li = el('li')
      const row = el('div', 'row')
      const name = el('code', '', shortSha(h.commit))
      const facts = [i === 0 ? 'last copied' : '', h.at ? when(h.at) : '', h.holds ? 'the branch is here' : ''].filter(Boolean).join(', ')
      row.append(name)
      if (facts) row.append(el('span', 'small muted', facts))
      if (h.passed) row.append(el('span', 'small ok', i === passed ? 'Newest with recorded reviews passed' : 'Recorded reviews passed'))
      if (!h.holds || h.restorable) {
        const key = `keep-restore-${h.commit}`
        const go = button('Restore this', 'small', () => {
          if (!off) void whileBusy(go, () => this.#ask(h.commit))
        }, 'button', key)
        go.setAttribute('aria-label', `Restore this commit, ${shortSha(h.commit)}`)
        go.setAttribute('aria-disabled', String(off))
        if (this.#busy && this.#asked === h.commit) busy(go, true)
        if (t.disabled) {
          go.title = visible(t.disabled)
          go.setAttribute('aria-describedby', 'keep-why')
        }
        row.append(go)
      }
      li.append(row, el('p', 'small muted', reviewsOf(h)))
      if (this.#confirm === h.commit && !off && h.restorable) li.append(...this.#confirmation(v, h.commit, i === 0))
      list.append(li)
    })
    out.push(list)
    return out
  }
}

customElements.define('sf-keep', SfKeep)

declare global {
  interface HTMLElementTagNameMap {
    'sf-keep': SfKeep
  }
}
