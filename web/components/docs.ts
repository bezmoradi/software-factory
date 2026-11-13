import { button, el, pageCrumb } from '../lib/dom.ts'
import { headings } from './markdown.ts'
import { icon } from './icon.ts'

const FIRST = '## What you need'
const LAST = '## Development'

type Section = { id: string; title: string; body: string; subs: string[] }
type Entry = { id: string; button: HTMLButtonElement; target: () => HTMLElement | null }

const SESSION = 'session'
const PAGE_LINKS: Record<string, { view: string; text: string }> = {
  'projects-and-new-sessions': { view: 'projects', text: 'Go to Projects' },
  'working-in-a-session': { view: SESSION, text: 'Back to session' },
  'execution-path-with-knackpack': { view: SESSION, text: 'Back to session' },
}

function pageLink(cls: string, view: string, text: string): HTMLAnchorElement {
  const link = el('a', cls)
  link.href = './'
  link.dataset.view = view
  link.append(icon('link'), text)
  return link
}

function addPageLinks(root: HTMLElement, session: string) {
  for (const h of root.querySelectorAll<HTMLElement>(':scope > sf-markdown > :is(h2, h3)')) {
    const had = h.querySelector<HTMLElement>('.docs-page')
    const name = slug(h.dataset.title ?? h.textContent ?? '')
    const to = Object.hasOwn(PAGE_LINKS, name) ? PAGE_LINKS[name] : undefined
    h.dataset.title ??= h.textContent ?? ''
    const view = to?.view === SESSION ? session : to?.view ?? ''
    if (had && had.dataset.view === view) continue
    had?.remove()
    if (to && view) h.append(pageLink('docs-page', view, to.text))
  }
}

function slug(title: string): string {
  return title.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
}

function sections(text: string): Section[] {
  const start = text.indexOf(FIRST)
  if (start < 0) return []
  const end = text.indexOf(LAST, start)
  const parts = text.slice(start, end < 0 ? undefined : end).split(/^(?=## )/m)
  return parts.map(part => {
    const newline = part.indexOf('\n')
    const title = part.slice(3, newline < 0 ? undefined : newline).trim()
    const body = part
    return { id: slug(title), title, body, subs: headings(body, 3) }
  })
}

class SfDocs extends HTMLElement {
  #text = ''
  #want = ''
  #session = ''
  #back: { view: string; label: string } | null = null
  #nav: HTMLElement | null = null
  #slot: HTMLElement | null = null
  #toc: HTMLElement | null = null
  #entries: Entry[] = []
  #current = -1
  #pinned = false
  #scroller: HTMLElement | null = null
  #frame = 0

  #onScroll = () => {
    if (!this.#frame) this.#frame = requestAnimationFrame(() => {
      this.#frame = 0
      this.#spy()
    })
  }

  #unpin = () => {
    this.#pinned = false
  }

  get loaded(): boolean {
    return !!this.#text
  }

  set text(value: string) {
    if (value && value === this.#text) return
    this.#text = value
    this.#render()
  }

  set navSlot(value: HTMLElement | null) {
    if (value === this.#slot) return
    this.#slot = value
    this.#placeNav()
  }

  connectedCallback() {
    this.#scroller = this.closest<HTMLElement>('.scroller')
    this.#scroller?.addEventListener('scroll', this.#onScroll, { passive: true })
    for (const type of ['wheel', 'pointerdown', 'keydown', 'touchstart']) addEventListener(type, this.#unpin, { passive: true, capture: true })
  }

  disconnectedCallback() {
    this.#scroller?.removeEventListener('scroll', this.#onScroll)
    for (const type of ['wheel', 'pointerdown', 'keydown', 'touchstart']) removeEventListener(type, this.#unpin, { capture: true })
  }

  set session(view: string) {
    if (view === this.#session) return
    this.#session = view
    for (const part of this.querySelectorAll<HTMLElement>('.docs-section')) addPageLinks(part, view)
  }

  show(id: string, back: { view: string; label: string } | null = this.#want === id ? this.#back : null) {
    this.#want = id
    this.#back = back
    const index = id ? this.#entries.findIndex(e => e.id === id) : -1
    const target = index >= 0 ? this.#entries[index]!.target() : null
    this.querySelector('.docs-back')?.remove()
    const heading = !target ? null : target.matches('h2, h3') ? target : target.querySelector<HTMLElement>('h2, h3')
    if (heading && back && !heading.querySelector(`.docs-page[data-view="${CSS.escape(back.view)}"]`)) {
      const link = pageLink('docs-back', back.view, `Back to ${back.label}`)
      link.addEventListener('click', () => {
        link.remove()
        this.#back = null
      })
      heading.append(link)
    }
    if (target) {
      target.scrollIntoView({ block: 'start' })
      this.#jumped(index)
    } else if (!id) this.closest<HTMLElement>('.scroller')?.scrollTo({ top: 0 })
  }

  #jumped(index: number) {
    this.#pinned = true
    this.#mark(index)
  }

  #placeNav() {
    if (!this.#nav) return
    if (this.#slot) this.#slot.replaceChildren(this.#nav)
    else this.prepend(this.#nav)
    requestAnimationFrame(() => this.#mark(this.#current))
  }

  #spy() {
    const s = this.#scroller
    if (this.hidden || !s || this.#pinned || !this.#entries.length) return
    let index = 0
    if (s.scrollTop < 2) index = 0
    else if (s.scrollHeight - s.scrollTop - s.clientHeight < 2) index = this.#entries.length - 1
    else {
      const line = s.getBoundingClientRect().top + 3 * (parseFloat(getComputedStyle(document.documentElement).fontSize) || 16)
      this.#entries.forEach((e, i) => {
        const t = e.target()
        if (t && t.getBoundingClientRect().top <= line) index = i
      })
    }
    this.#mark(index)
  }

  #mark(index: number) {
    const toc = this.#toc
    if (!toc) return
    this.#current = index
    this.#entries.forEach((e, i) => {
      if (i === index) e.button.setAttribute('aria-current', 'location')
      else e.button.removeAttribute('aria-current')
    })
    const b = this.#entries[index]?.button
    if (!b || !b.offsetParent) {
      delete toc.dataset.marked
      return
    }
    const style = getComputedStyle(b)
    const top = b.getBoundingClientRect().top - toc.getBoundingClientRect().top + parseFloat(style.paddingTop)
    const height = b.getBoundingClientRect().height - parseFloat(style.paddingTop) - parseFloat(style.paddingBottom)
    toc.style.setProperty('--mark-top', `${top}px`)
    toc.style.setProperty('--mark-height', `${height}px`)
    toc.dataset.marked = ''
  }

  #render() {
    const list = sections(this.#text)
    this.#entries = []
    this.#current = -1
    if (!list.length) {
      this.#nav = null
      this.#toc = null
      this.#slot?.replaceChildren()
      this.replaceChildren(el('p', 'muted', "The docs couldn't be read. They're also in the README on GitHub."))
      return
    }
    const nav = el('nav', 'docs-nav')
    nav.setAttribute('aria-label', 'Docs sections')
    const toc = el('ul', 'docs-toc')
    const body = el('div', 'docs-body')
    body.append(pageCrumb('Docs'))
    for (const s of list) {
      const part = el('section', 'docs-section')
      part.dataset.doc = s.id
      const md = el('sf-markdown')
      md.addEventListener('sf-paint', () => addPageLinks(part, this.#session))
      md.text = s.body
      part.append(md)
      body.append(part)
      const li = el('li')
      const top = this.#entry(s.id, s.title, () => part)
      li.append(top)
      if (s.subs.length) {
        const ul = el('ul')
        s.subs.forEach((title, k) => {
          const sub = el('li')
          sub.append(this.#entry(slug(title), title, () => part.querySelectorAll<HTMLElement>(':scope > sf-markdown > h3')[k] ?? null))
          ul.append(sub)
        })
        li.append(ul)
      }
      toc.append(li)
    }
    nav.append(el('p', 'label docs-label', 'Contents'), toc)
    this.#nav = nav
    this.#toc = toc
    new ResizeObserver(() => {
      if (this.#toc === toc) this.#mark(this.#current)
    }).observe(toc)
    this.replaceChildren(body)
    this.#placeNav()
    requestAnimationFrame(() => {
      if (this.#want) this.show(this.#want)
      else this.#spy()
    })
  }

  #entry(id: string, title: string, target: () => HTMLElement | null): HTMLButtonElement {
    const index = this.#entries.length
    const b = button(title, 'link', () => {
      const t = target()
      if (!t) return
      t.scrollIntoView({ block: 'start' })
      this.#jumped(index)
    })
    this.#entries.push({ id, button: b, target })
    return b
  }
}

customElements.define('sf-docs', SfDocs)

declare global {
  interface HTMLElementTagNameMap {
    'sf-docs': SfDocs
  }
}
