import { api } from '../api.ts'
import { button, el, emit, fileLink, repoPath, unspoofed, visible } from '../lib/dom.ts'
import { shortSha } from '../lib/format.ts'
import { money } from './consent.ts'
import './walkthrough.ts'
import { asideBox } from './aside-run.ts'
import type { AsideRun, CitedIDs, FindingRef, ReviewGiven, ReviewRole, ReviewsView, SessionReview, TestList } from '../types.ts'


const STATES: Record<string, [string, string]> = {
  running: ['running', ''],
  pass: ['pass', 'ok'],
  block: ['block', 'bad'],
  unknown: ['no verdict', 'warn'],
  failed: ['failed', 'bad'],
}

function text(value: string): string {
  return visible(unspoofed(value))
}

function provenance(role: ReviewRole): string {
  const parts = [
    role.pluginCommit ? `knackpack ${shortSha(role.pluginCommit)}` : '',
    role.claudeCode ? (role.claudeCode === 'sf-fake-claude' ? 'the development fake reviewer' : `Claude Code ${text(role.claudeCode)}`) : '',
    ...(role.models ?? []).map(text),
  ]
  return parts.filter(Boolean).join(' · ')
}

function duration(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  const pad = (n: number) => String(n).padStart(2, '0')
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${pad(s % 60)}s`
  return `${Math.floor(s / 3600)}h ${pad(Math.floor(s / 60) % 60)}m`
}

function runName(role: ReviewRole): string {
  return role.runs && role.runs > 1 ? `${text(role.name)}, run ${role.run ?? 1} of ${role.runs}` : text(role.name)
}

function cited(c: CitedIDs): HTMLElement {
  if (c.failed) return el('div', 'small warn', `The engine couldn't look up the vulnerability IDs this review cites at OSV (${text(c.failed)}), so it marks none of them.`)
  const more = c.more ? ` It looked up the first ${c.looked.length}; ${c.more} more ${c.more === 1 ? "wasn't" : "weren't"} looked up.` : ''
  if (!c.notInOsv.length) return el('div', 'small muted', `OSV holds ${c.looked.length === 1 ? 'the vulnerability ID' : `all ${c.looked.length} vulnerability IDs`} this review cites.${more}`)
  return el('div', 'small warn', `Not in OSV: ${c.notInOsv.map(text).join(', ')}. OSV holds the vulnerabilities of open-source software only, so a real ID can be missing from it; check ${c.notInOsv.length === 1 ? 'it' : 'them'} before you rely on this review.${more}`)
}

function calls(n: number): string {
  return `${n} tool ${n === 1 ? 'call' : 'calls'}`
}

function sentence(value: string): string {
  const t = value.trim()
  return /[.!?)]$/.test(t) ? t : `${t}.`
}

function failure(r: SessionReview, note: string): { said: string; shared: string } | null {
  if (r.running) return null
  const failed = r.roles.filter(x => x.state === 'failed')
  if (!failed.length) return null
  const reasons = [...new Set(failed.map(x => x.reason.trim()))]
  const shared = reasons.length === 1 && reasons[0] && !note ? reasons[0] : ''
  const one = r.roles.length === 1
  const what = failed.length === r.roles.length ? (one ? "The review couldn't run" : "The reviews couldn't run") : `${failed.length} of ${r.roles.length} reviews couldn't run`
  const next = `Press Review to try again. If ${failed.length === 1 ? 'it keeps' : 'they keep'} failing, you can still push: Allow the push with your reason.`
  return { said: `${what}${shared ? `: ${sentence(text(shared))}` : '.'} ${next}`, shared }
}

interface WholeReport {
  text: string
  clipped: boolean
}

async function wholeReport(ask: { sid: string; agent: string; commit: string; run: number }): Promise<WholeReport | string> {
  try {
    const res = await api('/api/review-reply', ask)
    const body: unknown = await res.json().catch(() => null)
    const got = body && typeof body === 'object' ? body as Record<string, unknown> : {}
    if (res.ok && typeof got.text === 'string' && typeof got.clipped === 'boolean') return { text: got.text, clipped: got.clipped }
    return typeof got.message === 'string' && got.message ? got.message : `the engine answered ${res.status}`
  } catch {
    return 'the engine didn\'t answer'
  }
}

class SfReviews extends HTMLElement {
  #key = ''
  #button: HTMLButtonElement | null = null
  #push: HTMLButtonElement | null = null
  #again: HTMLButtonElement | null = null
  #said: HTMLSpanElement | null = null
  #reasons = new Map<string, string>()
  #opened = new Set<string>()
  #wholes = new Map<string, Promise<WholeReport | string>>()
  #timer = 0

  connectedCallback() {
    if (!this.#button) this.hidden = true
    this.#ticking()
  }

  disconnectedCallback() {
    clearInterval(this.#timer)
    this.#timer = 0
  }

  #ticking() {
    const running = this.isConnected && !!this.querySelector('.review-progress[data-since]')
    if (running && !this.#timer) this.#timer = window.setInterval(() => this.#tick(), 1000)
    if (!running && this.#timer) {
      clearInterval(this.#timer)
      this.#timer = 0
    }
  }

  #tick() {
    for (const node of this.querySelectorAll<HTMLElement>('.review-progress[data-since]')) {
      const ms = Number(node.dataset.elapsed) + performance.now() - Number(node.dataset.since)
      node.textContent = `Running for ${duration(ms)} · ${calls(Number(node.dataset.calls))}`
    }
  }

  set view(value: ReviewsView | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    this.hidden = !value
    if (!value) {
      this.replaceChildren()
      return
    }
    const focused = document.activeElement === this.#button
    const againFocused = !!this.#again && document.activeElement === this.#again
    const typing = this.contains(document.activeElement) ? (document.activeElement as HTMLElement).dataset.key ?? '' : ''
    const testFocused = document.activeElement?.id === 'test-list-button'
    const explainFocused = document.activeElement?.id === 'explain-button'
    const pushFocused = document.activeElement === this.#push
    const r = value.review
    const running = !!r?.running || value.asking
    const off = running || !!value.disabled
    const b = button(running ? 'Reviewing…' : 'Review', 'primary small', () => {
      if (b.getAttribute('aria-disabled') === 'true') return
      emit(this, 'sf-review', { all: false })
    })
    b.setAttribute('aria-disabled', String(off))
    b.title = 'Runs the required reviewers that have no verdict on this session branch\'s latest commit yet, the work type\'s roles and those the changed files call for, in the reviewer container, compared with what the remote\'s main branch lacks'
    const decided = (state: string) => state === 'pass' || state === 'block' || state === 'overridden'
    const verdicts = !r?.running && ((value.gate ?? []).some(x => decided(x.state)) || (!!r && (!value.head || value.head === r.commit) && r.roles.some(x => decided(x.state))))
    const again = verdicts ? button('Run all again', 'small', () => {
      if (again?.getAttribute('aria-disabled') === 'true') return
      emit(this, 'sf-review', { all: true })
    }) : null
    if (again) {
      again.setAttribute('aria-disabled', String(off))
      again.title = 'Runs every required reviewer on the latest commit again, those that already passed or blocked it included, for fresh verdicts: each one is a run in the reviewer container'
    }
    const pushOff = value.pushing || !!value.disabled
    const p = button(value.pushing ? 'Pushing…' : 'Push', 'small', () => {
      if (p.getAttribute('aria-disabled') === 'true') return
      emit(this, 'sf-push-request')
    })
    p.setAttribute('aria-disabled', String(pushOff))
    p.title = 'Asks the engine to push this session\'s branch at its latest commit: the gate checks the reviews and the remote\'s main branch first'
    const head = el('div', 'row reviews-head')
    head.append(el('h2', 'label', 'Reviews'), el('span', 'spacer'), b)
    if (again) head.append(again)
    if (value.pushSaid) {
      const said = el('span', `small ${value.pushSaid.tone} push-said`, value.pushSaid.text)
      said.setAttribute('role', 'status')
      head.append(said)
    }
    if (!value.pushed) head.append(p)
    const parts: Node[] = [head]
    const note = value.disabled || r?.note || ''
    const described: string[] = []
    if (note) {
      const p = el('p', `small ${value.disabled ? 'muted' : 'warn'}`, text(note))
      p.id = 'reviews-note'
      described.push(p.id)
      parts.push(p)
    }
    const absent = (value.gate ?? []).filter(g => g.state === 'not-installed').map(g => text(g.type))
    const absentNote = absent.length ? `No plugin installed in the reviewer image provides ${absent.join(' or ')}, so the engine doesn't start ${absent.length === 1 ? 'that review' : 'those reviews'} and the push stays held.` : ''
    if (absentNote && !text(note).includes(absentNote)) {
      const p = el('p', 'small warn', absentNote)
      p.id = 'reviews-absent'
      described.push(p.id)
      parts.push(p)
    }
    const failed = r && !value.asking ? failure(r, note) : null
    if (failed) {
      const p = el('p', 'small bad', r?.req ? `${failed.said} Request id ${text(r.req)}.` : failed.said)
      p.id = 'reviews-failed'
      described.push(p.id)
      parts.push(p)
    }
    if (described.length) b.setAttribute('aria-describedby', described.join(' '))
    const who = value.reviewer
    if (who) {
      const si = r?.signIn ?? who.signIn
      const said = !si ? "The reviewers' sign-in hasn't been checked yet."
        : si.method === 'not-signed-in' ? 'The reviewers aren’t signed in. Sign them in under Finish setting up on the Welcome page.'
        : `The reviewers are signed in with ${text(si.words)}${si.costs ? '. Reviews cost money; the page asks first, and the engine starts none by itself' : ''}.`
      parts.push(el('p', `small ${si?.costs || si?.method === 'not-signed-in' ? 'warn' : 'muted'}`, said))
    }
    if (value.planned?.length) {
      parts.push(el('p', 'small muted', `Review runs ${value.planned.length === 1 ? 'this reviewer' : `these ${value.planned.length} reviewers`} on the latest commit, each one a run in the reviewer container, except one that already passed or blocked it:`))
      const list = el('ul', 'small planned')
      for (const x of value.planned) {
        const li = el('li', '', `${text(x.name)} (${text(x.agent)})${x.why ? `: ${text(x.why)}` : ''}`)
        if (x.summary) li.append(el('div', 'muted', text(x.summary)))
        list.append(li)
      }
      parts.push(list)
    }
    if (value.catalog?.state === 'refused') parts.push(el('p', 'small warn', `knackpack's reviewer catalog was refused: ${sentence(text(value.catalog.why || 'it could not be read'))} The engine's built-in list chooses the file reviewers.`))
    else if (value.catalog?.state === 'used') parts.push(el('p', 'small muted', "knackpack's reviewer catalog chooses the file reviewers. It names reviewers and the files they cover; it grants no tool or permission."))
    if (r) {
      const cost = money(r.costUsd)
      const total = money(r.sessionCostUsd)
      if (cost || total) parts.push(el('p', 'small muted', [cost && `This review ≈ ${cost}`, total && `this session's reviews ≈ ${total}`].filter(Boolean).join('; ') + ": Claude Code's own estimate, which may differ from the bill."))
      parts.push(el('p', 'small muted', `Commit ${shortSha(r.commit)} on ${text(r.branch)}, ${r.base ? `against ${shortSha(r.base)}, where it leaves the remote's main branch` : 'against an empty tree: it shares no history with the remote\'s main branch'}`))
      const list = el('ul', 'list reviews')
      const current = !value.head || value.head === r.commit
      const offered = new Set<string>()
      for (const role of r.roles) {
        const override = role.state === 'block' && !role.override && !role.advisory && !offered.has(role.agent)
        if (override) offered.add(role.agent)
        list.append(this.#role(role, current && !r.running ? r.commit : '', value, failed?.shared ?? '', override, r.running ? '' : r.commit))
      }
      parts.push(list)
    } else if (!value.disabled) {
      parts.push(el('p', 'small muted', 'No review of this session yet.'))
    }
    parts.push(this.#testList(value))
    this.#last = value
    this.#explainBox = this.#explain(value)
    parts.push(this.#explainBox)
    this.#said ??= el('span', 'sr-only')
    this.#said.setAttribute('role', 'status')
    parts.push(this.#said)
    this.replaceChildren(...parts)
    this.#button = b
    this.#push = p
    this.#again = again
    if (focused) b.focus()
    if (againFocused) (again ?? b).focus({ preventScroll: true })
    if (typing) this.querySelector<HTMLElement>(`[data-key="${CSS.escape(typing)}"]`)?.focus()
    if (pushFocused) p.focus({ preventScroll: true })
    if (testFocused) document.getElementById('test-list-button')?.focus({ preventScroll: true })
    if (explainFocused) document.getElementById('explain-button')?.focus({ preventScroll: true })
    this.#ticking()
    const done = r && !r.running ? [failed?.said ?? '', r.roles.map(x => `${x.name}: ${STATES[x.state]?.[0] ?? x.state}`).join(', ')].filter(Boolean).join(' ') : ''
    if (this.#said.textContent !== done) this.#said.textContent = done
  }

  #last: ReviewsView | null = null
  #explainBox: HTMLElement | null = null
  #explainState: { run: AsideRun | null; asking: boolean } = { run: null, asking: false }
  #explainKey = ''

  set explain(value: { run: AsideRun | null; asking: boolean } | null) {
    const next = value ?? { run: null, asking: false }
    const key = JSON.stringify(next)
    if (key === this.#explainKey) return
    this.#explainKey = key
    this.#explainState = next
    if (!this.#last || !this.#explainBox?.isConnected) return
    const focused = document.activeElement?.id === 'explain-button'
    const box = this.#explain(this.#last)
    this.#explainBox.replaceWith(box)
    this.#explainBox = box
    if (focused) document.getElementById('explain-button')?.focus({ preventScroll: true })
  }

  #explain(value: ReviewsView): HTMLElement {
    const t = this.#explainState.run
    return asideBox({
      id: 'explain-button', title: 'Explain this change', start: 'Explain this change', busy: 'Explaining…', label: 'The explanation of the change',
      hint: "Asks Claude Code in the reviewer container, with read-only tools, to explain the session branch's change against the remote's main branch. It isn't a review: the push gate and the agent never see it",
      intro: 'On request, a reviewer run explains what the branch changes and where to look closely. It costs one run, and nothing reads it but you.',
      run: t, asking: this.#explainState.asking, disabled: !!value.disabled,
      stale: t && value.head && value.head !== t.commit ? ", which isn't the branch's latest commit any more" : '',
      press: () => emit(this, 'sf-explain'),
    })
  }

  #testList(value: ReviewsView): HTMLElement {
    const box = el('div', 'test-list')
    const t: TestList | null = value.testList ?? null
    const running = t?.state === 'running' || !!value.testAsking
    const off = running || !!value.disabled
    const b = button(running ? 'Writing the test list…' : 'Write a manual test list', 'small', () => {
      if (b.getAttribute('aria-disabled') === 'true') return
      emit(this, 'sf-test-list')
    })
    b.id = 'test-list-button'
    b.setAttribute('aria-disabled', String(off))
    b.title = 'Asks Claude Code in the reviewer container, with read-only tools, for steps to try by hand on this session branch\'s latest commit. It isn\'t a review: the push gate and the agent never see it'
    const head = el('div', 'row test-list-head')
    head.append(el('span', 'small', 'Manual test list'), el('span', 'spacer'), b)
    box.append(head)
    if (!t) {
      box.append(el('p', 'small muted', 'On request, a reviewer run lists what to try by hand before you merge. It costs one run, and nothing reads it but you.'))
      return box
    }
    const cost = money(t.costUsd)
    const about = `For commit ${shortSha(t.commit)} on ${text(t.branch)}${value.head && value.head !== t.commit ? ', which isn\'t the branch\'s latest commit any more' : ''}${cost ? `; ≈ ${cost}, Claude Code's own estimate` : ''}.`
    box.append(el('p', 'small muted', about))
    if (t.state === 'running') box.append(el('p', 'small muted', 'Writing it in the reviewer container…'))
    if (t.reason) box.append(el('p', `small ${t.state === 'withheld' ? 'warn' : 'bad'}`, sentence(`${t.state === 'withheld' ? 'Not shown' : 'It failed'}: ${text(t.reason)}`)))
    if (t.state === 'done' && t.steps?.length) {
      const walk = el('sf-walkthrough')
      walk.view = { commit: t.commit, steps: t.steps }
      box.append(walk, el('p', 'small muted', 'From a reviewer run that read the commit; check each step makes sense before you rely on it.'))
    } else if (t.state === 'done' && t.text) {
      const pre = el('pre', 'review-message small', text(t.text))
      pre.tabIndex = 0
      pre.setAttribute('aria-label', 'The manual test list')
      box.append(pre, el('p', 'small muted', 'From a reviewer run that read the commit; check each step makes sense before you rely on it.'))
    }
    return box
  }

  #role(role: ReviewRole, commit: string, value: ReviewsView, said: string, offerOverride: boolean, reviewed: string): HTMLLIElement {
    const li = el('li')
    const row = el('div', 'row')
    const overridden = role.state === 'block' && !!role.override
    const warned = !!role.advisory && role.state === 'block'
    const [word, tone] = overridden ? ['overridden', 'warn'] : warned ? ['warning', 'warn'] : STATES[role.state] ?? [role.state, 'warn']
    row.append(el('span', '', runName(role)), el('span', 'spacer'), el('span', `tag${tone ? ` ${tone}` : ''}`, word))
    const cost = money(role.costUsd)
    const round = role.round ? `round ${role.round}${role.limit ? ` of ${role.limit}` : ''} on this branch` : ''
    li.append(row, el('div', 'small muted', [text(role.agent), role.why ? text(role.why) : '', round, cost ? `≈ ${cost} (estimate)` : ''].filter(Boolean).join(' · ')))
    const summary = value.gate?.find(g => g.type === role.agent)?.summary
    if (typeof summary === 'string' && summary) li.append(el('div', 'small muted', text(summary)))
    if (role.advisory) li.append(el('div', 'small muted', 'Advisory for this project: its findings are warnings, and it never counts for the push, the push hold or automatic findings sends.'))
    const made = provenance(role)
    if (made) li.append(el('div', 'small muted', made))
    const progress = this.#progress(role)
    if (progress) li.append(progress)
    li.append(el('div', 'small muted', role.incremental && role.since ? `Reviewed only what changed since ${shortSha(role.since)}, which its previous round reviewed${role.previous ? ', and was asked to check that round\'s findings' : ''}` : 'Reviewed the whole branch from where it leaves the remote\'s main branch'))
    if (role.verdict) li.append(el('div', `small ${tone}`, text(role.verdict)))
    if (role.reason && !(role.state === 'failed' && role.reason.trim() === said)) li.append(el('div', 'small warn', text(role.reason)))
    if (overridden) li.append(el('div', 'small warn', `You overrode this verdict for commit ${shortSha(commit)}: ${text(role.override ?? '')}`))
    else if (role.override) li.append(el('div', 'small warn', `You overrode an earlier blocking verdict of this reviewer for commit ${shortSha(commit)}, and it still allows this commit: ${text(role.override)}`))
    const capped = value.gate?.find(g => g.type === role.agent)?.capped
    if (capped && !overridden) li.append(el('div', 'small warn', `${text(role.name)} reached this project's round limit, so the engine doesn't start it again: you decide on the push.`))
    if (offerOverride && commit) li.append(this.#override(role, commit, value))
    if (role.message) {
      const pre = el('pre', 'review-message small', text(role.message))
      pre.tabIndex = 0
      pre.setAttribute('aria-label', `${runName(role)}'s final message`)
      li.append(pre)
    }
    const found = this.#findings(role, value, reviewed)
    if (found) li.append(found)
    if (role.hasFull) li.append(this.#full(role, value))
    if (role.cited?.looked.length) li.append(cited(role.cited))
    if (role.given) li.append(this.#given(role, role.given))
    return li
  }

  #findings(role: ReviewRole, value: ReviewsView, reviewed: string): HTMLElement | null {
    const list = (Array.isArray(role.findings) ? role.findings : []).filter((f): f is FindingRef => !!f && typeof f.severity === 'string' && typeof f.title === 'string')
    if (!list.length) return null
    const linked = reviewed && value.linked?.commit === reviewed ? new Set(value.linked.paths) : new Set<string>()
    const ul = el('ul', 'small review-findings')
    ul.setAttribute('aria-label', `${runName(role)}'s findings`)
    list.forEach((f, i) => {
      const li = el('li')
      li.append(el('span', 'muted', `[${text(f.severity)}] `), el('span', '', text(f.title)))
      const path = typeof f.path === 'string' && repoPath(f.path) ? f.path : ''
      const line = typeof f.line === 'number' && Number.isInteger(f.line) && f.line > 0 ? f.line : 0
      if (path && linked.has(path)) li.append(el('span', 'muted', ' · '), fileLink(path, `finding:${role.agent}:${role.run ?? 1}:${i}`, line ? { commit: reviewed, line } : { commit: reviewed }))
      else if (typeof f.where === 'string' && f.where) li.append(el('span', 'muted', ` · ${text(f.where)}`))
      ul.append(li)
    })
    return ul
  }

  #progress(role: ReviewRole): HTMLElement | null {
    const p = role.progress
    if (role.state === 'running') {
      const node = el('div', 'small muted review-progress', p ? `Running for ${duration(p.elapsedMs)} · ${calls(p.toolCalls)}` : 'Starting in the reviewer container…')
      if (p) {
        node.dataset.elapsed = String(p.elapsedMs)
        node.dataset.since = String(performance.now())
        node.dataset.calls = String(p.toolCalls)
      }
      return node
    }
    return p ? el('div', 'small muted', `Ran for ${duration(p.elapsedMs)} · ${calls(p.toolCalls)}`) : null
  }

  #full(role: ReviewRole, value: ReviewsView): HTMLElement {
    const ask = { sid: value.sid ?? '', agent: role.agent, commit: value.review?.commit ?? '', run: role.run ?? 1 }
    const key = `full:${ask.sid}:${ask.commit}:${ask.agent}:${ask.run}:${role.message}`
    const box = el('details', 'review-given small')
    const body = el('div')
    const fill = () => {
      body.replaceChildren(el('div', 'small muted', 'Reading the whole report…'))
      let asked = this.#wholes.get(key)
      if (!asked) {
        asked = wholeReport(ask)
        this.#wholes.set(key, asked)
      }
      void asked.then(got => {
        if (typeof got === 'string') {
          this.#wholes.delete(key)
          body.replaceChildren(el('div', 'small warn', `The whole report couldn't be read: ${sentence(text(got))}`))
          return
        }
        const pre = el('pre', 'review-message review-full small', text(got.text))
        pre.tabIndex = 0
        pre.setAttribute('aria-label', `${runName(role)}'s whole report`)
        body.replaceChildren(el('div', 'small muted', got.clipped ? 'The reply was longer than 64 KiB: the engine kept its start and its last 16 KiB, which hold its findings and closing lines, and left out the middle. The message above is its summary.' : 'The message above is its summary: its start and its closing lines.'), pre)
      })
    }
    box.open = this.#opened.has(key)
    box.addEventListener('toggle', () => {
      if (box.open) {
        this.#opened.add(key)
        fill()
      } else this.#opened.delete(key)
    })
    box.append(el('summary', '', 'The whole report'), body)
    if (box.open) fill()
    return box
  }

  #given(role: ReviewRole, given: ReviewGiven): HTMLElement {
    const key = `${role.agent}:${given.prompt.length}:${given.files.join('|')}`
    const box = el('details', 'review-given small')
    box.open = this.#opened.has(key)
    box.addEventListener('toggle', () => {
      if (box.open) this.#opened.add(key)
      else this.#opened.delete(key)
    })
    box.append(el('summary', '', 'What this review was given'))
    const prompt = el('pre', 'review-message small', text(given.prompt))
    prompt.tabIndex = 0
    prompt.setAttribute('aria-label', `The prompt ${text(role.name)} was given`)
    const flags = el('pre', 'review-message small', given.flags.map(text).join('\n'))
    flags.tabIndex = 0
    flags.setAttribute('aria-label', `The command-line flags ${text(role.name)} ran with`)
    const files = el('ul', 'small review-files')
    for (const f of given.files) files.append(el('li', '', text(f)))
    box.append(el('div', 'small muted', 'The prompt'), prompt, el('div', 'small muted', "Claude Code's flags, in order (no environment values are shown)"), flags, el('div', 'small muted', 'The review folder it read'), files)
    return box
  }

  #override(role: ReviewRole, commit: string, value: ReviewsView): HTMLElement {
    const box = el('div', 'row field-row override')
    const input = el('input', 'textbox')
    input.maxLength = 500
    input.id = `override-${role.agent.replace(/[^A-Za-z0-9-]/g, '-')}`
    input.dataset.key = input.id
    const kept = this.#reasons.get(`${commit}:${role.agent}`)
    if (kept) input.value = kept
    input.addEventListener('input', () => this.#reasons.set(`${commit}:${role.agent}`, input.value))
    const label = el('label', 'small', 'Your reason for overriding it (the audit file keeps it)')
    label.htmlFor = input.id
    const busy = value.overriding === role.agent || !!value.disabled
    const b = button('Override', 'small', () => {
      if (busy) return
      const reason = input.value.trim()
      if (!reason) {
        input.setAttribute('aria-invalid', 'true')
        input.focus()
        return
      }
      input.removeAttribute('aria-invalid')
      emit(this, 'sf-override', { agent: role.agent, commit, reason })
    })
    b.setAttribute('aria-disabled', String(busy))
    b.title = role.runs && role.runs > 1 ? 'Counts every blocking run of this role as passed for this commit only. A new commit needs a new review.' : 'Counts this role as passed for this commit only. A new commit needs a new review.'
    box.append(label, input, b)
    return box
  }
}

customElements.define('sf-reviews', SfReviews)

declare global {
  interface HTMLElementTagNameMap {
    'sf-reviews': SfReviews
  }
}
