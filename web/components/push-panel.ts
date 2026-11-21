import { download as downloadFile } from '../api.ts'
import { actionButton, busy, button, el, emit, labelError, replaceKeeping, sayIfChanged, statusLine, unspoofed, visible } from '../lib/dom.ts'
import { saveFile } from '../lib/download.ts'
import { actions, field as formField, form } from '../lib/form.ts'
import { shortSha } from '../lib/format.ts'
import { icon } from './icon.ts'
import { changeFacts } from './push-facts.ts'
import { coverageFacts } from './push-coverage.ts'
import './push-checked.ts'
import type { FlaggedFile, PushPanelView, SessionPush } from '../types.ts'

const FULL_SHA = /^[0-9a-f]{40}$/
const RECORD_ROUTE = '/api/push-record'
const RECORD_NAME = /filename="(software-factory-push-[0-9a-f]{7,12}\.json)"/

function recordName(disposition: string, commit: string): string {
  return RECORD_NAME.exec(disposition)?.[1] ?? `software-factory-push-${commit.slice(0, 12)}.json`
}

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

class SfPushPanel extends HTMLElement {
  #key = ''
  #checked = el('sf-push-checked')
  #expiry = 0
  #ends = 0
  #last: PushPanelView | null = null
  #card = ''
  #reason: HTMLInputElement | null = null
  #fileReasons = new Map<string, string>()
  #refused = new Map<string, string>()
  #watched = new WeakSet<HTMLInputElement>()
  #fileShown = new Map<string, string>()
  #fileChanged = new Set<string>()
  #answered = ''
  #heldCard = el('div', 'push-card')
  #passedSaid = statusLine('small muted status-slot')
  #recordFor = ''
  #recordBox = el('div', 'row push-line')

  connectedCallback() {
    if (!this.#key) this.hidden = true
    if (this.#ends && !this.#expiry) this.#arm()
  }

  disconnectedCallback() {
    clearTimeout(this.#expiry)
    this.#expiry = 0
  }

  #arm() {
    clearTimeout(this.#expiry)
    this.#expiry = window.setTimeout(() => {
      this.#expiry = 0
      this.#ends = 0
      this.#key = ''
      this.view = this.#last
    }, Math.min(Math.max(0, this.#ends - Date.now() + 500), 2 ** 31 - 1))
  }

  refuse(key: string, message: string): boolean {
    const target = () => this.querySelector<HTMLElement>(`[data-key="${CSS.escape(key)}"]`)
    if (!target()) return false
    this.#refused.set(key, message)
    this.#key = ''
    this.view = this.#last
    target()?.focus()
    return true
  }

  #refusedFor(input: HTMLInputElement) {
    if (!this.#watched.has(input)) {
      this.#watched.add(input)
      input.addEventListener('input', () => {
        if (this.#refused.delete(input.dataset.key ?? '')) labelError(input, '')
      })
    }
    labelError(input, this.#refused.get(input.dataset.key ?? '') ?? '')
  }

  #unrefuse(input: HTMLInputElement) {
    this.#refused.delete(input.dataset.key ?? '')
    labelError(input, '')
  }

  set view(value: PushPanelView | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    this.#last = value
    clearTimeout(this.#expiry)
    this.#expiry = 0
    const ends = value?.push?.result === 'held' ? value.push.expires ?? 0 : 0
    this.#ends = ends > Date.now() ? ends : 0
    if (this.#ends) this.#arm()
    const p = value?.push ?? null
    this.#checked.view = p ? { push: p } : null
    const flagged = (value?.flagged ?? []).filter(f => !f.allowed)
    this.hidden = !p && !flagged.length && value?.flagsRead !== false
    if (!value || this.hidden) {
      this.replaceChildren()
      this.#card = ''
      this.#reason = null
      sayIfChanged(this.#passedSaid, '', 'muted')
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
    if (!p.running && p.result === 'pushed' && FULL_SHA.test(p.commit) && value.sid) parts.push(this.#record(value.sid, p.commit))
    const facts = p.running ? null : changeFacts(p, value.typeChange ?? null)
    if (facts) parts.push(facts)
    const coverage = p.running ? null : coverageFacts(value.coverage ?? [])
    if (coverage) parts.push(coverage)
    if (!this.#checked.hidden) parts.push(this.#checked)
    if (p.card && !p.running) parts.push(this.#heldInPlace(p, value))
    else {
      this.#card = ''
      this.#reason = null
      sayIfChanged(this.#passedSaid, '', 'muted')
    }
    replaceKeeping(this, parts)
    if (focused) this.querySelector<HTMLElement>(`[data-key="${CSS.escape(focused)}"]`)?.focus()
  }

  #flagged(list: FlaggedFile[], value: PushPanelView): HTMLElement {
    const box = el('div', 'push-flagged')
    if (value.flagsRead === false) box.append(el('p', 'small warn', "The engine couldn't finish checking this branch's files and commits for secrets, and in a project that uses Proctor for changes to its specifications, so the push needs your Allow with a reason."))
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
      const digest = f.digest ?? ''
      const seen = `${value.sid ?? ''}\u0000${id}`
      const was = this.#fileShown.get(seen)
      if (was !== undefined && was !== digest) {
        this.#fileChanged.add(seen)
        this.#fileReasons.delete(id)
      }
      this.#fileShown.set(seen, digest)
      const size = !commit && f.size && f.size > 0 ? `, ${f.size >= 1048576 ? `${(f.size / 1048576).toFixed(1)} MiB` : `${Math.max(1, Math.round(f.size / 1024))} KiB`}` : ''
      if (commit) {
        const sha = el('code', '', shortSha(commit))
        sha.title = commit
        li.append(`The ${text(f.label ?? field)} of commit `, sha, ` is flagged: ${text(f.why)}.`)
      } else li.append(el('code', '', text(f.shown)), ` is flagged: ${text(f.why)}${size}.`)
      if (!commit && this.#fileChanged.has(seen)) li.append(el('p', 'small warn', 'It changed on the branch since the page first showed it, so your reason was cleared: look at the change again before you allow it.'))
      if (!commit && f.link) li.append(el('p', 'small warn', "This symbolic link is one the hold can't follow, under proctor/ and pointing outside it or a root Makefile that is a link, so it can't be allowed and the engine refuses the push: only asking the agent to take it out of the branch clears it."))
      const input = el('input', 'textbox')
      input.id = `flag-reason-${i}`
      input.dataset.key = `flag:${id}`
      input.setAttribute('aria-required', 'true')
      input.value = this.#fileReasons.get(id) ?? ''
      input.addEventListener('input', () => this.#fileReasons.set(id, input.value))
      const allow = button('Allow it', 'small', undefined, 'submit', `allow:${id}`)
      const allowIt = () => {
        if (off || f.full || f.link) return
        const reason = input.value
        this.#fileReasons.delete(id)
        this.#unrefuse(input)
        if (commit) emit(this, 'sf-allow-commit', { commit, field, reason })
        else emit(this, 'sf-allow-file', { path: f.path, digest, reason })
      }
      allow.setAttribute('aria-label', `Allow it: ${name}`)
      allow.setAttribute('aria-disabled', String(off || !!f.full || !!f.link))
      if (f.full) allow.title = 'It has more than 20 flagged versions on the branch, so it can\'t be allowed'
      if (f.link) allow.title = 'A link the hold on Proctor\'s specifications can\'t follow can\'t be allowed'
      if (commit) allow.title = 'Allows this commit only: another commit with the same text is flagged again'
      const spec = !commit && (!!f.spec || f.kind === 'specification')
      const removeText = commit ? 'Ask the agent to rewrite it' : spec ? 'Ask the agent to revert it' : 'Ask the agent to remove it'
      const remove = commit
        ? button(removeText, 'small', () => {
          if (!off) emit(this, 'sf-rewrite-commit', { commit, field })
        }, 'button', `rewrite:${id}`)
        : button(removeText, 'small', () => {
          if (!off) emit(this, 'sf-remove-file', { path: f.path, revert: spec })
        }, 'button', `remove:${id}`)
      remove.setAttribute('aria-label', `${removeText}: ${name}`)
      remove.setAttribute('aria-disabled', String(off))
      remove.title = commit
        ? 'Sends the session a message asking it to rewrite this commit if it isn\'t on the remote branch yet'
        : spec
          ? 'Sends the session a message asking it to take this change to a Proctor specification out of every commit on the branch, so the file is as the main branch has it'
          : 'Sends the session a message asking it to take this file out of every commit on the branch and add it to .gitignore'
      li.append(form({ children: [formField('Your reason for allowing it (the audit file keeps it)', input, { kind: 'inline', cls: 'field-row' }), actions([allow, remove])], onSubmit: allowIt }))
      this.#refusedFor(input)
      ul.append(li)
    })
    box.append(ul)
    return box
  }

  #result(p: SessionPush): HTMLElement {
    const { icon: name, tone, label: alone, lead } = p.look ?? { icon: 'warning', tone: 'warn', label: 'Push', lead: 'Push to' }
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
    const third = text(p.outcome ?? '')
    if (third) box.append(el('p', `push-line small${ok ? ' muted' : ''}`, third))
    const person = p.audit?.personReason ?? ''
    if (person && ok) box.append(el('p', 'push-line small muted', `Your reason: ${text(person)}`))
    return box
  }

  #record(sid: string, commit: string): HTMLElement {
    const key = `${sid}\u0000${commit}`
    if (this.#recordFor === key) return this.#recordBox
    this.#recordFor = key
    const said = statusLine('small muted')
    const download = actionButton('Download this push\'s record', 'ghost small', async () => {
      said.textContent = 'Building the record…'
      const got = await downloadFile(RECORD_ROUTE, { sid, commit })
      if (this.#recordFor !== key) return
      if (!got.answer.ok) {
        said.textContent = got.answer.message
        return
      }
      const blob = got.file
      if (!blob) {
        said.textContent = 'The record was cut off. Try again.'
        return
      }
      const name = recordName(got.disposition, commit)
      saveFile(blob, name)
      said.textContent = `Saved ${name}. Nothing was sent anywhere.`
    }, 'button', 'push-record')
    download.setAttribute('aria-label', `Download the record of the push of commit ${shortSha(commit)}`)
    download.title = 'One JSON file built from the engine’s own records: the commit, each review with its versions, the approvals, overrides and Allows with your reasons, the verification run, and the checks the agent reported, labelled as such'
    this.#recordBox.replaceChildren(download, said)
    return this.#recordBox
  }

  #heldInPlace(p: SessionPush, value: PushPanelView): HTMLElement {
    const fresh = this.#held(p, value)
    replaceKeeping(this.#heldCard, [this.#passedSaid, ...fresh.childNodes])
    return this.#heldCard
  }

  #held(p: SessionPush, value: PushPanelView): HTMLElement {
    const box = el('div', 'push-card')
    const items = p.missingItems ?? p.missing.map(text => ({ text, since: '' }))
    const done = items.filter(m => m.since === 'passed').map(m => m.text)
    const failed = items.filter(m => m.since === 'blocked').map(m => m.text)
    const still = items.filter(m => m.since !== 'passed' && m.since !== 'blocked').map(m => m.text)
    const expired = !!p.expires && p.expires <= Date.now()
    sayIfChanged(this.#passedSaid, done.length && !expired ? `${done.length === 1 ? 'A review this card lists has' : `${done.length} reviews this card lists have`} passed since it was made; the engine is checking the push again, so these lists are about to change.` : '', 'muted')
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
      this.#reason.dataset.key = 'reason'
      this.#reason.id = 'push-reason'
    }
    const input = this.#reason
    if (p.needsReason) input.setAttribute('aria-required', 'true')
    else input.removeAttribute('aria-required')
    const row = formField(p.needsReason ? 'Your reason for allowing it with items missing (the audit file keeps it), or a note for Deny' : 'A note', input, { kind: 'inline', cls: 'field-row', optional: !p.needsReason })
    const off = value.answering || !!value.disabled || p.running
    const answer = (allow: boolean) => () => {
      if (off) return
      const reason = input.value
      this.#unrefuse(input)
      this.#answered = allow ? 'allow' : 'deny'
      emit(this, 'sf-push-answer', { card: p.card, commit: p.commit, allow, reason })
    }
    const allow = button('Allow', 'primary small', answer(true), 'button', `push-allow:${p.card}`)
    const deny = button('Deny', 'small', answer(false), 'button', `push-deny:${p.card}`)
    const buttons = [allow, deny]
    if (p.handoff) {
      const handoff = button('Hand off', 'small', () => {
        if (!off) emit(this, 'sf-handoff', { card: p.card, commit: p.commit })
      }, 'button', 'handoff')
      handoff.title = 'Denies this push and asks the session for a handoff of the open findings: a reviewer reached this project\'s round limit and still blocks'
      buttons.push(handoff)
    }
    const sent = p.findingsSent ?? 0
    const findings = p.findings && !p.handoff ? button(sent ? 'Findings sent' : 'Send findings', 'small', () => {
      if (!off && !sent) emit(this, 'sf-send-findings', { card: p.card, commit: p.commit })
    }, 'button', 'findings') : null
    if (findings) {
      findings.title = 'Sends the session the blocking reviewers\' final messages, marked as untrusted findings to check. The push stays held: Allow and Deny still work.'
      buttons.push(findings)
    }
    for (const b of buttons) b.setAttribute('aria-disabled', String(off))
    if (findings && sent) findings.setAttribute('aria-disabled', 'true')
    if (value.answering && this.#answered === 'allow') busy(allow, true)
    if (value.answering && this.#answered === 'deny') busy(deny, true)
    box.append(form({ children: [row, actions(buttons)] }))
    this.#refusedFor(input)
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
