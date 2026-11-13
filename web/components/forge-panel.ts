import { button, el, emit, focusedKey, refocus, shown } from '../lib/dom.ts'
import type { ForgePanelView, SessionForge } from '../types.ts'

const STATES: Record<string, [string, string]> = {
  waiting: ['waiting for you', 'warn'],
  sending: ['opening', 'warn'],
  opened: ['opened', 'ok'],
  exists: ['already open', 'ok'],
  denied: ['denied', 'bad'],
  failed: ['failed', 'bad'],
  posting: ['posting', 'warn'],
  commented: ['posted', 'ok'],
  unclear: ['check on the host', 'warn'],
}

const KINDS: Record<string, string> = { issue: 'Issue', 'pull-request': 'Pull request', comment: 'Comment' }

function clock(ms: number): string {
  return ms ? new Date(ms).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' }) : ''
}

function safeLink(url: string): string {
  try {
    const u = new URL(url)
    return u.protocol === 'https:' && !u.username && !u.password ? u.href : ''
  } catch {
    return ''
  }
}

class SfForgePanel extends HTMLElement {
  #key = ''
  #drafts = new Map<string, boolean>()

  connectedCallback() {
    if (!this.#key) this.hidden = true
  }

  set view(value: ForgePanelView | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    const list = value?.forge ?? []
    this.hidden = !list.length
    if (!value || !list.length) {
      this.replaceChildren()
      return
    }
    const focused = focusedKey(this)
    for (const card of [...this.#drafts.keys()]) if (!list.some(f => f.card === card && f.state === 'waiting')) this.#drafts.delete(card)
    const parts: Node[] = [el('h2', 'label', 'Issues and pull requests')]
    for (const f of [...list].reverse()) parts.push(this.#card(f, value))
    this.replaceChildren(...parts)
    refocus(this, focused)
  }

  #comment(f: SessionForge): HTMLElement {
    const box = el('div', 'forge-card')
    const [word, tone] = STATES[f.state] ?? [f.state, 'warn']
    const head = el('div', 'row forge-head')
    head.append(el('span', `tag ${tone}`, word), el('span', 'small', `Comment on pull request #${f.number} in ${shown(f.repo)}`), el('span', 'small muted forge-time', clock(f.at || f.asked)))
    box.append(head, el('p', 'small muted', f.state === 'posting' ? 'Software Factory is posting this under your token after the push, as this project\'s settings ask.' : 'Software Factory posted this under your token after the push, as this project\'s settings ask.'))
    const body = el('pre', 'forge-body', shown(f.body))
    body.tabIndex = 0
    body.setAttribute('aria-label', `The comment on pull request #${f.number}`)
    box.append(body)
    if (f.url && f.state === 'commented') {
      const line = el('p', 'small forge-link')
      const href = safeLink(f.url)
      if (href) {
        const a = el('a', '', 'The comment on GitHub')
        a.href = href
        a.target = '_blank'
        a.rel = 'noopener noreferrer'
        line.append(a)
      } else line.append(shown(f.url))
      box.append(line)
    }
    if (f.reason && f.state === 'failed') box.append(el('p', 'small bad', shown(f.reason)))
    return box
  }

  #card(f: SessionForge, value: ForgePanelView): HTMLElement {
    if (f.kind === 'comment') return this.#comment(f)
    const box = el('div', 'forge-card')
    const pull = f.kind === 'pull-request'
    const [word, tone] = STATES[f.state] ?? [f.state, 'warn']
    const head = el('div', 'row forge-head')
    head.append(el('span', `tag ${tone}`, word))
    if (pull && f.draft && (f.state === 'opened' || f.state === 'sending')) head.append(el('span', 'tag', 'draft'))
    head.append(el('span', 'small', `${KINDS[f.kind] ?? 'Issue'} in ${shown(f.repo)}`), el('span', 'small muted forge-time', clock(f.at || f.asked)))
    box.append(head, el('p', 'forge-title', shown(f.title)))
    if (pull) box.append(el('p', 'small muted forge-refs', `From ${shown(f.head)} into ${shown(f.base)}`))
    if (f.state === 'waiting' || f.state === 'sending') {
      box.append(el('p', 'small muted', 'The agent asked for this. Its text is shown exactly as written; read it before you approve, since the engine posts it under your token.'))
      const body = el('pre', 'forge-body', f.body ? shown(f.body) : '(no body)')
      body.tabIndex = 0
      body.setAttribute('aria-label', `Body of the ${pull ? 'pull request' : 'issue'}`)
      box.append(body)
    }
    if (f.number && f.url) {
      const line = el('p', 'small forge-link')
      const href = safeLink(f.url)
      const label = `${pull ? 'Pull request' : 'Issue'} #${f.number}`
      if (href) {
        const a = el('a', '', label)
        a.href = href
        a.target = '_blank'
        a.rel = 'noopener noreferrer'
        line.append(a)
      } else line.append(label)
      box.append(line)
    }
    if (f.reason && (f.state === 'failed' || f.state === 'denied' || f.state === 'opened' || f.state === 'unclear')) box.append(el('p', `small ${f.state === 'failed' ? 'bad' : 'muted'}`, shown(f.reason)))
    if (pull && f.draft && f.state === 'opened') box.append(el('p', 'small muted', 'It opened as a draft: mark it ready for review on GitHub when you want reviews.'))
    if (f.state === 'unclear') box.append(this.#matches(f))
    if (f.state === 'waiting') {
      const off = value.answering || !!value.disabled
      const draft = pull ? this.#draftBox(f, off) : null
      const approve = button(pull ? 'Open pull request' : 'Open issue', 'primary small', () => {
        if (off) return
        emit(this, 'sf-forge-answer', pull ? { card: f.card, approve: true, draft: this.#drafts.get(f.card) ?? true } : { card: f.card, approve: true })
      }, 'button', `approve:${f.card}`)
      approve.title = `The engine opens it on ${shown(f.host)} with your saved token`
      const deny = button('Deny', 'small', () => {
        if (!off) emit(this, 'sf-forge-answer', { card: f.card, approve: false })
      }, 'button', `deny:${f.card}`)
      for (const b of [approve, deny]) b.setAttribute('aria-disabled', String(off))
      const actions = el('div', 'actions forge-actions')
      if (draft) actions.append(draft)
      actions.append(approve, deny)
      box.append(actions)
      if (value.disabled) box.append(el('p', 'small muted', shown(value.disabled)))
    }
    return box
  }

  #draftBox(f: SessionForge, off: boolean): HTMLLabelElement {
    const label = el('label', 'check small')
    const box = el('input')
    box.type = 'checkbox'
    box.checked = this.#drafts.get(f.card) ?? true
    box.disabled = off
    box.dataset.key = `draft:${f.card}`
    box.addEventListener('change', () => this.#drafts.set(f.card, box.checked))
    label.title = 'A draft pull request starts no review requests; you mark it ready on GitHub. If GitHub doesn\'t offer drafts for this repository, it opens ready for review.'
    label.append(box, 'Open as a draft')
    return label
  }

  #matches(f: SessionForge): HTMLElement {
    const list = f.matches ?? []
    if (!list.length) return el('p', 'small muted', 'Check the repository on the git host before you or the agent ask for it again.')
    const box = el('div', 'forge-matches')
    box.append(el('p', 'small', 'Opened since you approved it. Check these before anyone asks for it again:'))
    const ul = el('ul', 'small forge-match-list')
    for (const m of list) {
      const li = el('li', '')
      const href = safeLink(m.url)
      const label = `#${m.number} ${shown(m.title)}`
      if (href) {
        const a = el('a', '', label)
        a.href = href
        a.target = '_blank'
        a.rel = 'noopener noreferrer'
        li.append(a)
      } else li.append(label)
      li.append(el('span', 'muted', ` ${clock(m.created)}${m.exact ? ', same title and text' : ''}`))
      ul.append(li)
    }
    box.append(ul)
    return box
  }
}

customElements.define('sf-forge-panel', SfForgePanel)

declare global {
  interface HTMLElementTagNameMap {
    'sf-forge-panel': SfForgePanel
  }
}
