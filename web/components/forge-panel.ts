import { busy, button, el, emit, focusedKey, refocus, shown, unspoofed } from '../lib/dom.ts'
import { field } from '../lib/form.ts'
import { scrolledText } from './cards.ts'
import { saidOf } from '../lib/json.ts'
import type { ForgePanelView, SessionForge } from '../types.ts'

type ForgeAnswer = HTMLElementEventMap['sf-forge-answer']['detail']

function secretKey(f: SessionForge): string {
  return JSON.stringify([f.card, f.secretAt ?? '', f.secretLine ?? ''])
}

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
  #confirmed = new Set<string>()
  #answered = ''
  #observers: ResizeObserver[] = []

  connectedCallback() {
    if (!this.#key) this.hidden = true
  }

  set view(value: ForgePanelView | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    for (const observer of this.#observers) observer.disconnect()
    this.#observers = []
    const list = value?.forge ?? []
    this.hidden = !list.length
    if (!value || !list.length) {
      this.replaceChildren()
      return
    }
    const focused = focusedKey(this)
    for (const card of [...this.#drafts.keys()]) if (!list.some(f => f.card === card && f.state === 'waiting')) this.#drafts.delete(card)
    const flagged = new Set(list.filter(f => f.state === 'waiting' && f.secretAt).map(secretKey))
    for (const key of [...this.#confirmed]) if (!flagged.has(key)) this.#confirmed.delete(key)
    const parts: Node[] = [el('h2', 'label', 'Issues and pull requests')]
    for (const f of [...list].reverse()) parts.push(this.#card(f, value))
    this.replaceChildren(...parts)
    refocus(this, focused)
  }

  #comment(f: SessionForge, value: ForgePanelView): HTMLElement {
    const box = el('div', 'forge-card')
    const { label: word, tone } = saidOf(f.said, f.state)
    const head = el('div', 'row forge-head')
    head.append(el('span', `tag ${tone}`, word), el('span', 'small', `Comment on pull request #${f.number} in ${shown(f.repo)}`), el('span', 'small muted forge-time', clock(f.at || f.asked)))
    box.append(head)
    if (f.note) box.append(el('p', 'small muted', f.note))
    if (f.body) box.append(...this.#body(unspoofed(f.body), `The comment on pull request #${f.number}`))
    if (f.state === 'waiting') box.append(...this.#waiting(f, value, 'Post comment', () => ({ card: f.card, approve: true, confirmed: !!f.secretAt })))
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
    if (f.kind === 'comment') return this.#comment(f, value)
    const box = el('div', 'forge-card')
    const pull = f.kind === 'pull-request'
    const { label: word, tone } = saidOf(f.said, f.state)
    const head = el('div', 'row forge-head')
    head.append(el('span', `tag ${tone}`, word))
    if (pull && f.draft && (f.state === 'opened' || f.state === 'sending')) head.append(el('span', 'tag', 'draft'))
    head.append(el('span', 'small', `${f.kindLabel ?? ''} in ${shown(f.repo)}`), el('span', 'small muted forge-time', clock(f.at || f.asked)))
    box.append(head, el('p', 'forge-title', shown(f.title)))
    if (pull) box.append(el('p', 'small muted forge-refs', `From ${shown(f.head)} into ${shown(f.base)}`))
    if (f.state === 'waiting' || f.state === 'sending') {
      if (f.note) box.append(el('p', 'small muted', f.note))
      box.append(...this.#body(f.body ? unspoofed(f.body) : '(no body)', `Body of the ${pull ? 'pull request' : 'issue'}`))
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
    if (f.reason && f.state === 'waiting' && pull) box.append(el('p', 'small bad', `Asking again: ${shown(f.reason)}.`))
    if (f.reason && (f.state === 'failed' || f.state === 'denied' || f.state === 'opened' || f.state === 'unclear')) box.append(el('p', `small ${f.state === 'failed' ? 'bad' : 'muted'}`, shown(f.reason)))
    if (pull && f.draft && f.state === 'opened') box.append(el('p', 'small muted', 'It opened as a draft: mark it ready for review under Pull request, or on GitHub, when you want reviews.'))
    if (f.state === 'unclear') box.append(this.#matches(f))
    if (f.state === 'waiting') {
      const confirmed = f.secretAt ? { confirmed: true } : {}
      box.append(...this.#waiting(f, value, pull ? (f.reason ? 'Open pull request at the new commit' : 'Open pull request') : 'Open issue', () => pull ? { card: f.card, approve: true, draft: this.#drafts.get(f.card) ?? true, ...confirmed } : { card: f.card, approve: true, ...confirmed }, pull, `The engine opens it on ${shown(f.host)} with your saved token`))
    }
    return box
  }

  #body(text: string, name: string): Node[] {
    const [box, more, observer] = scrolledText(text, name, 'forge-body')
    this.#observers.push(observer)
    return [box, more]
  }

  #waiting(f: SessionForge, value: ForgePanelView, text: string, answer: () => ForgeAnswer, draft = false, title = ''): Node[] {
    const off = value.answering || !!value.disabled
    const key = secretKey(f)
    const parts: Node[] = []
    const actions = el('div', 'actions forge-actions')
    if (draft) actions.append(this.#draftBox(f, off))
    let check: HTMLInputElement | null = null
    let why: HTMLElement | null = null
    const approve = button(text, 'primary small', () => {
      if (off) return
      if (check && !check.checked) {
        if (why) why.hidden = false
        return void check.focus()
      }
      this.#answered = approve.dataset.key ?? ''
      emit(this, 'sf-forge-answer', answer())
    }, 'button', `approve:${f.card}`)
    if (title) approve.title = title
    const deny = button('Deny', 'small', () => {
      if (off) return
      this.#answered = deny.dataset.key ?? ''
      emit(this, 'sf-forge-answer', { card: f.card, approve: false })
    }, 'button', `deny:${f.card}`)
    deny.setAttribute('aria-disabled', String(off))
    const sync = () => {
      const waits = !!check && !check.checked
      approve.setAttribute('aria-disabled', String(off || waits))
      if (why && waits) approve.setAttribute('aria-describedby', why.id)
      else approve.removeAttribute('aria-describedby')
      if (why && !waits) why.hidden = true
    }
    if (f.secretAt) {
      const box = el('div', 'forge-secret')
      box.append(el('p', 'small bad', `This looks like a secret: ${shown(f.secretAt)}.`))
      if (f.secretLine) {
        const line = el('pre', 'forge-body', shown(f.secretLine))
        line.tabIndex = 0
        line.setAttribute('aria-label', `The line that looks like a secret, ${shown(f.secretAt)}`)
        box.append(line)
      }
      const input = el('input')
      input.type = 'checkbox'
      input.checked = this.#confirmed.has(key)
      input.disabled = off
      input.dataset.key = `secret:${f.card}`
      input.addEventListener('change', () => {
        if (input.checked) this.#confirmed.add(key)
        else this.#confirmed.delete(key)
        sync()
      })
      box.append(field('I checked it: this isn\'t a secret, and it can go to the git host', input, { kind: 'check' }))
      check = input
      why = el('p', 'small warn forge-why', 'Tick the box above to continue.')
      why.id = `forge-why-${encodeURIComponent(f.card)}`
      why.hidden = true
      parts.push(box)
    }
    actions.append(approve, deny)
    parts.push(actions)
    if (why) parts.push(why)
    sync()
    if (value.answering && this.#answered === approve.dataset.key) busy(approve, true)
    if (value.answering && this.#answered === deny.dataset.key) busy(deny, true)
    if (value.disabled) parts.push(el('p', 'small muted', shown(value.disabled)))
    return parts
  }

  #draftBox(f: SessionForge, off: boolean): HTMLDivElement {
    const box = el('input')
    box.type = 'checkbox'
    box.checked = this.#drafts.get(f.card) ?? true
    box.disabled = off
    box.dataset.key = `draft:${f.card}`
    box.addEventListener('change', () => this.#drafts.set(f.card, box.checked))
    const draft = field('Open as a draft', box, { kind: 'check' })
    draft.title = 'A draft pull request starts no review requests; you mark it ready on GitHub. If GitHub doesn\'t offer drafts for this repository, it opens ready for review.'
    return draft
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
