import { button, el, emit, unspoofed, visible } from '../lib/dom.ts'
import { shortSha } from '../lib/format.ts'
import { icon } from './icon.ts'
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
  #card = ''
  #reason: HTMLInputElement | null = null
  #fileReasons = new Map<string, string>()

  connectedCallback() {
    if (!this.#key) this.hidden = true
  }

  set view(value: PushPanelView | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
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
    if (value.flagsRead === false) box.append(el('p', 'small warn', "The engine couldn't finish checking this branch's files for secrets, so the push needs your Allow with a reason."))
    if (!list.length) return box
    box.append(el('p', 'small warn', list.length === 1 ? 'A file the push would send is flagged:' : `${list.length} files the push would send are flagged:`))
    const ul = el('ul', 'small flagged')
    const off = !!value.disabled
    list.forEach((f, i) => {
      const li = el('li', 'warn')
      const size = f.size && f.size > 0 ? `, ${f.size >= 1048576 ? `${(f.size / 1048576).toFixed(1)} MiB` : `${Math.max(1, Math.round(f.size / 1024))} KiB`}` : ''
      li.append(el('code', '', text(f.path)), ` is flagged: ${text(f.why)}${size}.`)
      const input = el('input', 'textbox')
      input.maxLength = 500
      input.id = `flag-reason-${i}`
      input.dataset.key = `flag:${f.path}`
      input.value = this.#fileReasons.get(f.path) ?? ''
      input.addEventListener('input', () => this.#fileReasons.set(f.path, input.value))
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
        this.#fileReasons.delete(f.path)
        emit(this, 'sf-allow-file', { path: f.path, reason })
      }, 'button', `allow:${f.path}`)
      allow.setAttribute('aria-label', `Allow it: ${text(f.path)}`)
      allow.setAttribute('aria-disabled', String(off || !!f.full))
      if (f.full) allow.title = 'It has more than 20 flagged versions on the branch, so it can\'t be allowed'
      const remove = button('Ask the agent to remove it', 'small', () => {
        if (!off) emit(this, 'sf-remove-file', { path: f.path })
      }, 'button', `remove:${f.path}`)
      remove.setAttribute('aria-label', `Ask the agent to remove it: ${text(f.path)}`)
      remove.setAttribute('aria-disabled', String(off))
      remove.title = 'Sends the session a message asking it to take this file out of every commit on the branch and add it to .gitignore'
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
    if (p.missing.length) {
      box.append(el('p', 'small warn', 'Missing:'))
      const list = el('ul', 'small push-missing')
      for (const m of p.missing) list.append(el('li', '', text(m)))
      box.append(list)
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
