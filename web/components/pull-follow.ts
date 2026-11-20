import { api } from '../api.ts'
import { busy, button, el, emit, focusedKey, refocus, replaceKeeping, sayIfChanged, shown, statusLine } from '../lib/dom.ts'
import type { Tone } from '../lib/dom.ts'
import { DOUBLED_NOTE } from '../lib/format.ts'
import type { PullComment, PullFollow, PullFollowView, SessionInfo } from '../types.ts'

type Answer = { ok: boolean; status: number; body: Record<string, unknown> }

type Finding = { severity: string; title: string; location: string; text: string; reviewer: string; agent: string }

type Findings = { key: string; commit: string; items: Finding[]; held: number; opened: boolean }

type Settings = { project: string; bots: string[]; auto: boolean; assign: boolean }

const MAX_BOTS = 20
const BOT = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})(?:\[bot\])?$/

async function call(path: string, body: unknown): Promise<Answer> {
  const res = await api(path, body).catch(() => null)
  const parsed = res ? ((await res.json().catch(() => ({}))) as Record<string, unknown>) : {}
  return { ok: !!res && res.ok, status: res?.status ?? 0, body: parsed }
}

function message(a: Answer, fallback: string): string {
  return typeof a.body.message === 'string' && a.body.message ? shown(a.body.message) : fallback
}

function safeLink(url: string): string {
  try {
    const u = new URL(url)
    return u.protocol === 'https:' && !u.username && !u.password ? u.href : ''
  } catch {
    return ''
  }
}

function link(url: string, text: string): HTMLElement {
  const href = safeLink(url)
  if (!href) return el('span', '', text)
  const a = el('a', '', text)
  a.href = href
  a.target = '_blank'
  a.rel = 'noopener noreferrer'
  return a
}

function clock(ms: number): string {
  return ms ? new Date(ms).toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' }) : ''
}

function commentWhat(c: PullComment): string {
  if (c.kind === 'review') return `Review${c.state ? ` (${shown(c.state)})` : ''}`
  if (c.kind === 'inline') return `Inline comment on ${shown(c.path ?? '')}${c.line ? ` line ${c.line}` : ''}${c.outdated ? ' (outdated)' : ''}`
  return 'Conversation comment'
}

function reviewKey(v: PullFollowView): string {
  const r = v.review
  if (!r || r.running || !/^[0-9a-f]{40}$/.test(r.commit)) return ''
  const passed = r.roles.filter(x => x.state === 'pass')
  if (!passed.length) return ''
  return JSON.stringify([v.sid, r.commit, r.branch, passed.map(x => [x.agent, x.message.length])])
}

export function pullFollowOf(sid: string, info: SessionInfo, connected: boolean): PullFollowView | null {
  if (!info.verified) return null
  return { sid, follow: info.follow ?? null, review: info.review ?? null, running: !!info.running, disabled: info.doubled ? DOUBLED_NOTE : connected ? '' : 'Actions work once the page is connected again.', connected }
}

class SfPullFollow extends HTMLElement {
  #view: PullFollowView | null = null
  #key = ''
  #busy = ''
  #status = new Map<string, [string, Tone | undefined]>()
  #said = statusLine('small status-slot')
  #findings = new Map<string, Findings>()
  #loading = ''
  #settings = new Map<string, Settings>()
  #settingsOpen = false
  #draft = new Map<string, { bots: string; auto: boolean; assign: boolean }>()

  connectedCallback() {
    if (!this.#view) this.hidden = true
  }

  set view(value: PullFollowView | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    this.#view = value
    this.#paint()
    if (value) void this.#loadFindings(value)
  }

  #say(sid: string, text: string, tone?: Tone) {
    if (text) this.#status.set(sid, [text, tone])
    else this.#status.delete(sid)
  }

  async #run(op: string, path: string, body: Record<string, unknown>, ok: string, fail: string): Promise<Answer | null> {
    const v = this.#view
    if (!v || this.#busy || v.disabled) return null
    this.#busy = op
    this.#say(v.sid, '')
    this.#paint()
    const a = await call(path, body)
    this.#busy = ''
    this.#say(v.sid, a.ok ? ok : message(a, fail), a.ok ? 'muted' : 'bad')
    this.#paint()
    return a
  }

  async #loadFindings(v: PullFollowView) {
    const key = reviewKey(v)
    if (!key || this.#findings.has(key) || this.#loading === key || !v.connected) return
    this.#loading = key
    const a = await call('/api/finding-issues', { op: 'read', sid: v.sid })
    this.#loading = ''
    if (!a.ok) return
    const items = Array.isArray(a.body.items) ? (a.body.items as Finding[]) : []
    this.#findings.set(key, { key, commit: typeof a.body.commit === 'string' ? a.body.commit : '', items, held: typeof a.body.held === 'number' ? a.body.held : 0, opened: a.body.opened === true })
    if (this.#findings.size > 50) this.#findings.delete(this.#findings.keys().next().value!)
    this.#paint()
  }

  async #loadSettings(sid: string) {
    const a = await call('/api/pull-request', { op: 'settings', sid })
    if (!a.ok) {
      this.#say(sid, message(a, "The project's pull request settings couldn't be read."), 'bad')
      return this.#paint()
    }
    const s: Settings = { project: typeof a.body.project === 'string' ? a.body.project : '', bots: Array.isArray(a.body.bots) ? (a.body.bots as string[]) : [], auto: a.body.auto === true, assign: a.body.assign === true }
    this.#settings.set(sid, s)
    this.#draft.set(sid, { bots: s.bots.join(', '), auto: s.auto, assign: s.assign })
    this.#paint()
  }

  #paint() {
    const v = this.#view
    this.hidden = !v
    if (!v) return void this.replaceChildren()
    const focused = focusedKey(this)
    const f = v.follow
    const parts: Node[] = [el('h2', 'label', 'Pull request')]
    parts.push(this.#state(v, f))
    if (f && f.state === 'open') parts.push(...this.#open(v, f))
    if (f?.issue) parts.push(el('p', 'small muted', f.issueSent ? `Started from issue #${f.issue}: the engine sent it as the first message. The pull request the engine opens says Closes #${f.issue}.` : `Started from issue #${f.issue}. The pull request the engine opens says Closes #${f.issue}.`))
    if (f?.note) parts.push(el('p', 'small warn', shown(f.note)))
    parts.push(...this.#findingsPart(v))
    const said = this.#status.get(v.sid)
    sayIfChanged(this.#said, said?.[0] ?? '', said?.[1])
    parts.push(this.#said)
    if (v.disabled) parts.push(el('p', 'small muted', v.disabled))
    parts.push(this.#settingsPart(v))
    replaceKeeping(this, parts)
    refocus(this, focused)
  }

  #off(): boolean {
    return !!this.#busy || !!this.#view?.disabled
  }

  #action(text: string, op: string, cls: string, key: string, run: () => void, title = ''): HTMLButtonElement {
    const b = button(text, cls, () => {
      if (!this.#off()) run()
    }, 'button', key)
    b.setAttribute('aria-disabled', String(this.#off()))
    if (this.#busy === op) busy(b, true)
    if (title) b.title = title
    return b
  }

  #state(v: PullFollowView, f: PullFollow | null): HTMLElement {
    const box = el('div', 'pull-state')
    const openPull = () => this.#action('Open pull request', 'open', 'small', `pull-open:${v.sid}`,
      () => void this.#run('open', '/api/pull-request', { op: 'open', sid: v.sid }, 'A pull request card waits under Issues and pull requests. Read it, then open it from there.', "The pull request card wasn't made."),
      'Makes a pull request card from the engine\'s records: the branch, its commits and the approved requirements\' title. It opens as a draft unless you clear that on the card.')
    if (!f || !f.state) {
      box.append(el('p', 'small muted', 'Once the branch is pushed, the engine follows its pull request: CI results, your own comments and the bots this project names.'))
      const row = el('div', 'actions')
      row.append(openPull())
      box.append(row)
      return box
    }
    if (f.state === 'looking') {
      box.append(el('p', 'small muted', 'The branch is pushed. No pull request is open for it yet; the engine looks once a minute.'))
      const row = el('div', 'actions')
      row.append(openPull())
      box.append(row)
      return box
    }
    if (f.done) {
      const line = el('p', 'small')
      line.append(el('span', 'tag ok', f.state === 'merged' ? 'merged' : 'closed'), ' ', link(f.url, `Pull request #${f.number}`), f.state === 'merged' ? ' was merged, so the session is done.' : ' was closed without merging, so the session is done.')
      box.append(line)
      box.append(el('p', 'small muted', 'Delete removes the session and its worktree. The engine refuses while the session runs or has commits that aren\'t pushed.'))
      const row = el('div', 'actions')
      row.append(this.#action('Delete', 'delete', 'small', `pull-delete:${v.sid}`, () => void this.#delete(v.sid)))
      box.append(row)
      return box
    }
    const head = el('p', 'small row pull-head')
    head.append(link(f.url, `Pull request #${f.number}`))
    if (f.draft) head.append(el('span', 'tag', 'draft'))
    if (f.conflict) head.append(el('span', 'tag bad', 'conflicts with main'))
    else if (f.mergeable === 'yes') head.append(el('span', 'tag ok', 'no conflict'))
    if (f.polled) head.append(el('span', 'small muted pull-time', `read ${clock(f.polled)}`))
    box.append(head)
    return box
  }

  async #delete(sid: string) {
    const a = await this.#run('delete', '/api/workspace', { op: 'delete', sid }, 'The session was deleted.', "The session wasn't deleted.")
    if (a?.ok) emit(this, 'sf-session-deleted', { sid })
  }

  #open(v: PullFollowView, f: PullFollow): Node[] {
    const out: Node[] = []
    const actions = el('div', 'actions pull-actions')
    if (f.draft && f.opened) actions.append(this.#action('Ready for review', 'ready', 'small', `pull-ready:${v.sid}`,
      () => void this.#run('ready', '/api/pull-request', { op: 'ready', sid: v.sid }, 'It is ready for review now.', "It wasn't marked ready for review."), 'Marks the engine\'s draft pull request ready for review on the git host, with your saved token'))
    if (f.conflict) {
      out.push(el('p', 'small warn', `The branch conflicts with main (GitHub says ${shown(f.mergeState || 'not mergeable')}).`))
      actions.append(this.#action('Ask the agent to bring in main', 'main', 'small', `pull-main:${v.sid}`,
        () => void this.#run('main', '/api/bring-main', { sid: v.sid }, 'The agent was asked to bring in main.', "The agent wasn't asked.")))
    }
    const total = f.failing + f.passing + f.running
    if (total) {
      out.push(el('p', `small ${f.failing ? 'bad' : 'muted'}`, `CI at ${shown(f.head.slice(0, 7))}: ${f.failing} failed, ${f.passing} passed, ${f.running} running or queued.`))
      const failed = f.checks.filter(c => c.failed)
      if (failed.length) {
        const ul = el('ul', 'small pull-list')
        for (const c of failed.slice(0, 20)) {
          const li = el('li', '')
          li.append(c.url ? link(c.url, shown(c.name)) : shown(c.name), el('span', 'muted', `: ${shown(c.result)}`))
          ul.append(li)
        }
        out.push(ul)
      }
    } else out.push(el('p', 'small muted', 'No CI results for its head commit yet.'))
    if (f.comments.length) {
      out.push(el('p', 'small', `${f.comments.length + f.more} new ${f.comments.length + f.more === 1 ? 'comment' : 'comments'} by you or this project's bots, written on the git host:`))
      for (const c of f.comments) {
        const card = el('div', 'forge-card')
        const line = el('p', 'small row pull-head')
        line.append(el('strong', '', shown(c.author)), el('span', 'muted', commentWhat(c)), c.url ? link(c.url, clock(c.created) || 'link') : el('span', 'muted', clock(c.created)))
        const body = el('pre', 'forge-body', c.body ? shown(c.body) : '(no text)')
        body.tabIndex = 0
        body.setAttribute('aria-label', `Comment by ${shown(c.author)}`)
        card.append(line, body)
        out.push(card)
      }
      if (f.more) out.push(el('p', 'small muted', `And ${f.more} older ones, sent with them.`))
    }
    if (f.dropped) out.push(el('p', 'small warn', `${f.dropped} new ${f.dropped === 1 ? 'comment holds' : 'comments hold'} what looks like a token, so the engine won't send ${f.dropped === 1 ? 'it' : 'them'} to the agent. Read ${f.dropped === 1 ? 'it' : 'them'} on the git host.`))
    if (f.checksNew || f.comments.length) {
      out.push(el('p', 'small muted', 'The agent gets this as text from the git host, marked untrusted: feedback to weigh, not instructions.'))
      actions.append(this.#action('Send to the agent', 'send', 'primary small', `pull-send:${v.sid}`,
        () => void this.#run('send', '/api/pull-request', { op: 'send', sid: v.sid }, 'Sent to the agent.', "It wasn't sent.")))
    } else if (f.sent) out.push(el('p', 'small muted', `Last sent to the agent ${clock(f.sent)}. Nothing new since.`))
    if (f.auto) out.push(el('p', `small ${f.autoStop ? 'warn' : 'muted'}`, f.autoStop ? `Sending on its own stopped: ${shown(f.autoStop)}. Send it yourself to start again.` : `This project sends what's new to the agent on its own: ${f.rounds} of 5 rounds used.`))
    if (actions.childElementCount) out.push(actions)
    return out
  }

  #findingsPart(v: PullFollowView): Node[] {
    const key = reviewKey(v)
    const found = key ? this.#findings.get(key) : undefined
    if (!found || (!found.items.length && !found.held)) return []
    const box = el('div', 'pull-findings')
    const n = found.items.length
    box.append(el('p', 'small', `The passing review of ${shown(found.commit.slice(0, 7))} has ${n} ${n === 1 ? 'finding' : 'findings'} that didn't block:`))
    const ul = el('ul', 'small pull-list')
    for (const it of found.items) {
      const li = el('li', '')
      li.append(el('span', 'tag', shown(it.severity)), ' ', shown(it.title))
      if (it.location) li.append(el('span', 'muted', ` (${shown(it.location)})`))
      ul.append(li)
    }
    box.append(ul)
    if (found.held) box.append(el('p', 'small warn', `${found.held} more hold what looks like a token, so no card is offered for ${found.held === 1 ? 'it' : 'them'}.`))
    if (found.opened) box.append(el('p', 'small muted', 'Their issue cards were offered under Issues and pull requests.'))
    else if (n) {
      const row = el('div', 'actions')
      row.append(this.#action('Open issues for these', 'findings', 'small', `pull-findings:${v.sid}`, () => void this.#openFindings(v, found),
        'Makes one issue card per finding, pre-filled from it. Each opens only when you approve its own card.'))
      box.append(row)
    }
    return [box]
  }

  async #openFindings(v: PullFollowView, found: Findings) {
    const a = await this.#run('findings', '/api/finding-issues', { op: 'open', sid: v.sid, commit: found.commit }, 'One issue card per finding waits under Issues and pull requests. Approve each one on its own.', 'No issue card was made.')
    if (a?.ok || a?.status === 409) {
      found.opened = true
      this.#paint()
    }
  }

  #settingsPart(v: PullFollowView): HTMLElement {
    const d = el('details', 'pull-settings')
    d.open = this.#settingsOpen
    const summary = el('summary', 'small', "This project's pull request settings")
    summary.dataset.key = `pull-settings:${v.sid}`
    d.append(summary)
    d.addEventListener('toggle', () => {
      this.#settingsOpen = d.open
      if (d.open && !this.#settings.has(v.sid)) void this.#loadSettings(v.sid)
    })
    const saved = this.#settings.get(v.sid)
    const draft = this.#draft.get(v.sid)
    if (!saved || !draft) {
      d.append(el('p', 'small muted', 'Reading…'))
      return d
    }
    const form = el('form', 'stacked pull-settings-form')
    form.autocomplete = 'off'
    const botsId = `pull-bots-${v.sid}`
    const label = el('label', 'small', 'Bot logins whose comments the engine follows, exactly as GitHub shows them (none by default)')
    label.htmlFor = botsId
    const bots = el('input', 'textbox')
    bots.id = botsId
    bots.value = draft.bots
    bots.placeholder = 'e.g. coderabbitai[bot], sonarcloud[bot]'
    bots.dataset.key = `pull-bots:${v.sid}`
    bots.addEventListener('input', () => { draft.bots = bots.value })
    const auto = el('input')
    auto.type = 'checkbox'
    auto.checked = draft.auto
    auto.dataset.key = `pull-auto:${v.sid}`
    auto.addEventListener('change', () => { draft.auto = auto.checked })
    const autoLabel = el('label', 'check small')
    autoLabel.append(auto, 'Send what\'s new to the agent on its own, only when it is signed in on a subscription, at most 5 rounds per pull request')
    const assign = el('input')
    assign.type = 'checkbox'
    assign.checked = draft.assign
    assign.dataset.key = `pull-assign:${v.sid}`
    assign.addEventListener('change', () => { draft.assign = assign.checked })
    const assignLabel = el('label', 'check small')
    assignLabel.append(assign, 'Assign the issues and pull requests the engine opens to the token\'s own user')
    const why = el('p', 'small bad')
    why.hidden = true
    const save = button('Save', 'small', undefined, 'submit', `pull-save:${v.sid}`)
    save.setAttribute('aria-disabled', String(this.#off()))
    if (this.#busy === 'settings') busy(save, true)
    form.append(el('p', 'small muted', `For ${shown(saved.project)}. Comments by anyone else never reach the agent.`), label, bots, autoLabel, assignLabel, why, save)
    form.addEventListener('submit', e => {
      e.preventDefault()
      if (this.#off()) return
      const list = draft.bots.split(/[\s,]+/).filter(Boolean)
      const bad = list.find(b => !BOT.test(b))
      if (bad || list.length > MAX_BOTS) {
        why.textContent = bad ? `${shown(bad)} isn't a GitHub login.` : `At most ${MAX_BOTS} bot logins.`
        why.hidden = false
        return void bots.focus()
      }
      void this.#saveSettings(v.sid, list, draft.auto, draft.assign)
    })
    d.append(form)
    return d
  }

  async #saveSettings(sid: string, bots: string[], auto: boolean, assign: boolean) {
    const a = await this.#run('settings', '/api/pull-request', { op: 'set', sid, bots, auto, assign }, 'Saved.', "The settings weren't saved.")
    if (a?.ok) {
      const s = this.#settings.get(sid)
      if (s) this.#settings.set(sid, { ...s, bots, auto, assign })
    }
  }
}

customElements.define('sf-pull-follow', SfPullFollow)

declare global {
  interface HTMLElementTagNameMap {
    'sf-pull-follow': SfPullFollow
  }
}
