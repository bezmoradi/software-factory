import { busy, button, el, emit, unspoofed, visible } from '../lib/dom.ts'
import { shortSha } from '../lib/format.ts'
import { icon } from './icon.ts'
import { changeFacts } from './push-facts.ts'
import type { FlaggedFile, PushPanelView, SessionPush } from '../types.ts'

const RESULTS: Record<string, [string, string, string, string]> = {
  pushed: ['check', 'ok', 'Pushed', 'Pushed to'],
  'up-to-date': ['check', 'ok', 'Pushed', 'Pushed to'],
  held: ['warning', 'warn', 'Push held', 'Push held for'],
  denied: ['error', 'bad', 'Push denied', 'Push denied for'],
  refused: ['error', 'bad', 'Not pushed', 'Not pushed to'],
  unclear: ['warning', 'warn', 'Push outcome unclear', 'Push outcome unclear for'],
}

const FULL_SHA = /^[0-9a-f]{40}$/

function text(value: string): string {
  return visible(unspoofed(value))
}

export function when(ms: number): string {
  if (!ms) return ''
  const at = new Date(ms)
  const today = at.toDateString() === new Date().toDateString()
  return at.toLocaleString(undefined, { ...(today ? {} : { month: 'short', day: 'numeric' }), hour: 'numeric', minute: '2-digit' })
}

interface Remote {
  name: string
  href: string
  shown: string
  github: boolean
}

function remoteOf(url: string): Remote {
  try {
    const u = new URL(url)
    const path = u.pathname.replace(/\/+$/, '').replace(/\.git$/, '')
    const name = path.replace(/^\/+/, '') || u.host
    const https = u.protocol === 'https:'
    return { name, href: https ? `${u.origin}${path}` : '', shown: `${u.protocol}//${u.host}${path}`, github: https && u.hostname === 'github.com' && !u.port }
  } catch {
    return { name: url, href: '', shown: url, github: false }
  }
}

function outLink(label: string, href: string, title: string): HTMLElement {
  if (!href) {
    const span = el('span', '', label)
    span.title = title
    return span
  }
  const a = el('a', '', label)
  a.href = href
  a.target = '_blank'
  a.rel = 'noopener noreferrer'
  a.title = title
  return a
}

const HELD_UNTIL = 'held until it has: '

function reasonOf(p: SessionPush): string {
  const at = p.result === 'held' && p.missing.length ? p.reason.indexOf(HELD_UNTIL) : -1
  const said = at < 0 ? p.reason : p.reason.slice(0, at).replace(/[;,\s]+$/, '')
  return said.charAt(0).toUpperCase() + said.slice(1)
}

function how(p: SessionPush): string {
  const by = p.audit?.by ?? ''
  const who = by === 'agent' || by === 'agent, after reviews' ? 'Asked by the agent' : by === 'page' ? 'From the page' : ''
  const passed = p.passed ?? 0
  const overridden = p.overridden ?? 0
  const n = passed + overridden
  let after = ''
  if (p.missing.length) after = `allowed with ${p.missing.length === 1 ? '1 item' : `${p.missing.length} items`} missing`
  else if (n && !overridden) after = `after ${n === 1 ? 'its review' : n === 2 ? 'both reviews' : `all ${n} reviews`} passed`
  else if (n) after = `after ${passed} of ${n} reviews passed, ${overridden} overridden`
  return [who, after].filter(Boolean).join(' · ')
}

class SfPushPanel extends HTMLElement {
  #key = ''
  #expiry = 0
  #card = ''
  #reason: HTMLInputElement | null = null
  #fileReasons = new Map<string, string>()
  #answered = ''

  connectedCallback() {
    if (!this.#key) this.hidden = true
  }

  set view(value: PushPanelView | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    clearTimeout(this.#expiry)
    const ends = value?.push?.result === 'held' ? value.push.expires ?? 0 : 0
    if (ends > Date.now()) {
      this.#expiry = window.setTimeout(() => {
        this.#key = ''
        this.view = value
      }, Math.min(ends - Date.now() + 500, 2 ** 31 - 1))
    }
    const p = value?.push ?? null
    const flagged = (value?.flagged ?? []).filter(f => !f.allowed)
    this.hidden = !p && !flagged.length && value?.flagsRead !== false
    if (!value || this.hidden) {
      this.replaceChildren()
      this.#card = ''
      this.#reason = null
      return
    }
    const focused = this.contains(document.activeElement) ? (document.activeElement as HTMLElement).dataset.key ?? '' : ''
    const parts: Node[] = [el('h2', 'label', 'Push')]
    if (flagged.length || value.flagsRead === false) parts.push(this.#flagged(flagged, value))
    if (!p) {
      this.replaceChildren(...parts)
      if (focused) this.querySelector<HTMLElement>(`[data-key="${CSS.escape(focused)}"]`)?.focus()
      return
    }
    if (p.running) parts.push(el('p', 'small muted', 'The engine is checking the gate and the remote…'))
    else if (p.result) parts.push(this.#result(p))
    const facts = p.running ? null : changeFacts(p, value.checks ?? [])
    if (facts) parts.push(facts)
    if (p.card && !p.running) parts.push(this.#held(p, value))
    else {
      this.#card = ''
      this.#reason = null
    }
    this.replaceChildren(...parts)
    if (focused) this.querySelector<HTMLElement>(`[data-key="${CSS.escape(focused)}"]`)?.focus()
  }

  #flagged(list: FlaggedFile[], value: PushPanelView): HTMLElement {
    const box = el('div', 'push-flagged')
    if (value.flagsRead === false) box.append(el('p', 'small warn', "The engine couldn't finish checking this branch's files and commits for secrets, so the push needs your Allow with a reason."))
    if (!list.length) return box
    const commits = list.filter(f => f.commit).length
    const files = list.length - commits
    const distinct = new Set(list.map(f => f.commit).filter(Boolean)).size
    let lead = files === 1 ? 'A file the push would send is flagged:' : `${files} files the push would send are flagged:`
    if (commits && !files) lead = `${distinct === 1 ? 'A commit the push would send is' : `${distinct} commits the push would send are`} flagged${commits > distinct ? ` in ${commits} places` : ''}:`
    else if (commits) lead = `${list.length} items the push would send are flagged:`
    box.append(el('p', 'small warn', lead))
    if (commits) box.append(el('p', 'small muted', 'The engine never forces a push, so the agent can rewrite only commits it hasn\'t pushed. If a flagged commit is already on the remote branch, allow it instead.'))
    const ul = el('ul', 'small flagged')
    const off = !!value.disabled
    list.forEach((f, i) => {
      const commit = f.commit ?? ''
      const field = f.field ?? ''
      const id = commit ? `${commit}:${field}` : f.path
      const name = commit ? `the ${text(f.label ?? field)} of commit ${shortSha(commit)}` : text(f.shown)
      const li = el('li', 'warn')
      const size = !commit && f.size && f.size > 0 ? `, ${f.size >= 1048576 ? `${(f.size / 1048576).toFixed(1)} MiB` : `${Math.max(1, Math.round(f.size / 1024))} KiB`}` : ''
      if (commit) {
        const sha = el('code', '', shortSha(commit))
        sha.title = commit
        li.append(`The ${text(f.label ?? field)} of commit `, sha, ` is flagged: ${text(f.why)}.`)
      } else li.append(el('code', '', text(f.shown)), ` is flagged: ${text(f.why)}${size}.`)
      const input = el('input', 'textbox')
      input.maxLength = 500
      input.id = `flag-reason-${i}`
      input.dataset.key = `flag:${id}`
      input.value = this.#fileReasons.get(id) ?? ''
      input.addEventListener('input', () => this.#fileReasons.set(id, input.value))
      const label = el('label', 'small', 'Your reason for allowing it (the audit file keeps it)')
      label.htmlFor = input.id
      const allow = button('Allow it', 'small', () => {
        if (off || f.full) return
        const reason = input.value.trim()
        if (!reason) {
          input.setAttribute('aria-invalid', 'true')
          input.focus()
          return
        }
        input.removeAttribute('aria-invalid')
        this.#fileReasons.delete(id)
        if (commit) emit(this, 'sf-allow-commit', { commit, field, reason })
        else emit(this, 'sf-allow-file', { path: f.path, reason })
      }, 'button', `allow:${id}`)
      allow.setAttribute('aria-label', `Allow it: ${name}`)
      allow.setAttribute('aria-disabled', String(off || !!f.full))
      if (f.full) allow.title = 'It has more than 20 flagged versions on the branch, so it can\'t be allowed'
      if (commit) allow.title = 'Allows this commit only: another commit with the same text is flagged again'
      const remove = commit
        ? button('Ask the agent to rewrite it', 'small', () => {
          if (!off) emit(this, 'sf-rewrite-commit', { commit, field })
        }, 'button', `rewrite:${id}`)
        : button('Ask the agent to remove it', 'small', () => {
          if (!off) emit(this, 'sf-remove-file', { path: f.path })
        }, 'button', `remove:${id}`)
      remove.setAttribute('aria-label', `${commit ? 'Ask the agent to rewrite it' : 'Ask the agent to remove it'}: ${name}`)
      remove.setAttribute('aria-disabled', String(off))
      remove.title = commit
        ? 'Sends the session a message asking it to rewrite this commit if it isn\'t on the remote branch yet'
        : 'Sends the session a message asking it to take this file out of every commit on the branch and add it to .gitignore'
      const row = el('div', 'row field-row')
      row.append(label, input)
      const actions = el('div', 'actions')
      actions.append(allow, remove)
      li.append(row, actions)
      ul.append(li)
    })
    box.append(ul)
    return box
  }

  #result(p: SessionPush): HTMLElement {
    const [name, tone, alone, lead] = RESULTS[p.result] ?? ['warning', 'warn', 'Push', 'Push to']
    const remote = remoteOf(p.remote)
    const box = el('div', 'push-result')
    box.id = 'push-result'
    box.tabIndex = -1
    const head = el('div', 'push-line push-head')
    const title = el('span', 'push-title')
    if (p.remote) title.append(`${lead} `, outLink(text(remote.name), remote.href, text(remote.shown)))
    else title.append(alone)
    const time = el('span', 'small muted push-time', when(p.at))
    if (p.at) time.title = new Date(p.at).toLocaleString()
    head.append(icon(name, `icon push-icon ${tone}`), title, time)
    box.append(head)
    const refs = el('div', 'push-line small muted push-refs')
    if (p.branch) refs.append(el('code', 'push-ref', text(p.branch)))
    if (p.commit) {
      if (p.branch) refs.append(' · ')
      const sha = FULL_SHA.test(p.commit) ? p.commit : ''
      const code = el('code', 'push-ref')
      code.append(outLink(shortSha(p.commit), remote.github && sha ? `${remote.href}/commit/${sha}` : '', p.commit))
      refs.append(code)
    }
    if (refs.childNodes.length) box.append(refs)
    const ok = p.result === 'pushed' || p.result === 'up-to-date'
    const third = p.result === 'up-to-date' ? 'The remote already had this commit, so nothing was sent' : ok ? how(p) : text(reasonOf(p))
    if (third) box.append(el('p', `push-line small${ok ? ' muted' : ''}`, third))
    const person = p.audit?.personReason ?? ''
    if (person && ok) box.append(el('p', 'push-line small muted', `Your reason: ${text(person)}`))
    return box
  }

  #held(p: SessionPush, value: PushPanelView): HTMLElement {
    const box = el('div', 'push-card')
    const passed = new Set(value.passedSince ?? [])
    const blocked = new Set((value.blockedSince ?? []).filter(m => !passed.has(m)))
    const done = p.missing.filter(m => passed.has(m))
    const failed = p.missing.filter(m => blocked.has(m))
    const still = p.missing.filter(m => !passed.has(m) && !blocked.has(m))
    const expired = !!p.expires && p.expires <= Date.now()
    if (done.length && !expired) {
      const note = el('p', 'small muted', `${done.length === 1 ? 'A review this card lists has' : `${done.length} reviews this card lists have`} passed since it was made; the engine is checking the push again, so these lists are about to change.`)
      note.setAttribute('role', 'status')
      box.append(note)
    }
    const listed = (heading: string, tone: string, items: string[]) => {
      if (!items.length) return
      box.append(el('p', `small ${tone}`, heading))
      const list = el('ul', 'small push-missing')
      for (const m of items) list.append(el('li', '', text(m)))
      box.append(list)
    }
    listed('Missing:', 'warn', still)
    listed('Passed since this card was made:', 'muted', done)
    listed('Blocked since this card was made:', 'bad', failed)
    if (failed.length) box.append(el('p', 'small muted', `${failed.length === 1 ? 'Its findings are' : 'Their findings are'} in the Reviews section above.`))
    if (p.expires && expired) {
      const recheck = done.length ? ', and nothing checks it again by itself' : ''
      box.append(el('p', 'small warn', `This card expired at ${when(p.expires)}, so Allow and Deny no longer apply to it${recheck}. Press Push in the Reviews section above to check the push again.`))
      if (value.disabled) box.append(el('p', 'small muted', text(value.disabled)))
      return box
    }
    if (p.expires) box.append(el('p', 'small muted', `Allow works until ${when(p.expires)}, once, for this commit only.`))
    if (this.#card !== p.card || !this.#reason) {
      this.#card = p.card
      this.#reason = el('input', 'textbox push-reason')
      this.#reason.maxLength = 500
      this.#reason.dataset.key = 'reason'
      this.#reason.id = 'push-reason'
    }
    const input = this.#reason
    const label = el('label', 'small', p.needsReason ? 'Your reason for allowing it with items missing (the audit file keeps it), or a note for Deny' : 'A note (optional)')
    label.htmlFor = input.id
    const row = el('div', 'row field-row')
    row.append(label, input)
    const off = value.answering || !!value.disabled || p.running
    const answer = (allow: boolean) => () => {
      if (off) return
      const reason = input.value.trim()
      if (allow && p.needsReason && !reason) {
        input.setAttribute('aria-invalid', 'true')
        input.focus()
        return
      }
      input.removeAttribute('aria-invalid')
      this.#answered = allow ? 'allow' : 'deny'
      emit(this, 'sf-push-answer', { card: p.card, commit: p.commit, allow, reason })
    }
    const allow = button('Allow', 'primary small', answer(true), 'button', 'allow')
    const deny = button('Deny', 'small', answer(false), 'button', 'deny')
    const actions = el('div', 'actions')
    actions.append(allow, deny)
    if (p.handoff) {
      const handoff = button('Hand off', 'small', () => {
        if (!off) emit(this, 'sf-handoff', { card: p.card, commit: p.commit })
      }, 'button', 'handoff')
      handoff.title = 'Denies this push and asks the session for a handoff of the open findings: a reviewer reached this project\'s round limit and still blocks'
      actions.append(handoff)
    }
    const sent = p.findingsSent ?? 0
    const findings = p.findings && !p.handoff ? button(sent ? 'Findings sent' : 'Send findings', 'small', () => {
      if (!off && !sent) emit(this, 'sf-send-findings', { card: p.card, commit: p.commit })
    }, 'button', 'findings') : null
    if (findings) {
      findings.title = 'Sends the session the blocking reviewers\' final messages, marked as untrusted findings to check. The push stays held: Allow and Deny still work.'
      actions.append(findings)
    }
    for (const b of actions.querySelectorAll('button')) b.setAttribute('aria-disabled', String(off))
    if (findings && sent) findings.setAttribute('aria-disabled', 'true')
    if (value.answering && this.#answered === 'allow') busy(allow, true)
    if (value.answering && this.#answered === 'deny') busy(deny, true)
    box.append(row, actions)
    if (findings && sent) box.append(el('p', 'small muted', `Findings sent to the session at ${when(sent)}; the push stays held.`))
    if (value.disabled) box.append(el('p', 'small muted', text(value.disabled)))
    return box
  }
}

customElements.define('sf-push-panel', SfPushPanel)

declare global {
  interface HTMLElementTagNameMap {
    'sf-push-panel': SfPushPanel
  }
}
