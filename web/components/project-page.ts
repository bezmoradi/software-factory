import { button, el, replaceKeeping } from '../lib/dom.ts'
import { crumbTrail, emptyState, pageHead, welcomeCrumb } from '../lib/page.ts'
import { factsSource, type FactsSection, type FactsSource } from './project-facts.ts'
import './team-rules.ts'
import './caps.ts'
import './project-commands.ts'
import './metrics.ts'
import './decision-log.ts'
import './review-depth.ts'
import './package-lookups.ts'
import './lessons.ts'
import type { Project } from '../types.ts'

export interface ProjectPageView {
  name: string
  project: Project | null
  known: boolean
}

export type ProjectPart = 'commands' | 'caps' | 'depth' | 'lookups' | FactsSection | 'team' | 'metrics' | 'decisions' | 'lessons'

interface PartSpec {
  id: ProjectPart
  title: string
  make: (p: Project, facts: FactsSource) => HTMLElement
}

function part<K extends keyof HTMLElementTagNameMap>(tag: K, set: (node: HTMLElementTagNameMap[K]) => void): HTMLElementTagNameMap[K] {
  const node = el(tag)
  set(node)
  return node
}

function facts(section: FactsSection) {
  return (_: Project, source: FactsSource) => part('sf-project-facts', n => {
    n.section = section
    n.source = source
  })
}

const PARTS: PartSpec[] = [
  { id: 'commands', title: 'Check and setup commands', make: p => part('sf-project-commands', n => { n.project = p.name }) },
  { id: 'caps', title: 'Caps', make: p => part('sf-project-caps', n => { n.project = p.name }) },
  { id: 'depth', title: 'Review depth', make: p => part('sf-review-depth', n => { n.path = p.path }) },
  { id: 'lookups', title: 'New packages', make: p => part('sf-package-lookups', n => { n.project = p.name }) },
  { id: 'readiness', title: 'Ready for agents', make: facts('readiness') },
  { id: 'workflows', title: 'CI workflows on the agents’ pushes', make: facts('workflows') },
  { id: 'protection', title: 'Protection of main', make: facts('protection') },
  { id: 'team', title: 'Team rules from main', make: p => part('sf-team-rules', n => { n.path = p.path }) },
  { id: 'metrics', title: 'Delivery and reviewer numbers', make: p => part('sf-project-metrics', n => { n.project = p.name }) },
  { id: 'decisions', title: 'Decision log', make: p => part('sf-decision-log', n => { n.project = p.name }) },
  { id: 'lessons', title: 'Lessons from reviews', make: p => part('sf-lessons', n => { n.project = p.name }) },
]

interface Entry {
  id: string
  button: HTMLButtonElement
  target: HTMLElement
}

class SfProjectPage extends HTMLElement {
  #name = ''
  #shape = ''
  #entries: Entry[] = []
  #facts: FactsSource | null = null
  #scroller: HTMLElement | null = null
  #nav: HTMLElement | null = null
  #layout: HTMLElement | null = null
  #slot: HTMLElement | null = null
  #frame = 0
  #pinned = false

  #onScroll = () => {
    if (!this.#frame) this.#frame = requestAnimationFrame(() => {
      this.#frame = 0
      this.#spy()
    })
  }

  #unpin = () => {
    this.#pinned = false
  }

  #jumped = (e: HTMLElementEventMap['sf-project-jump']) => {
    const index = this.#entries.findIndex(x => x.id === e.detail.part)
    if (index < 0) return
    e.stopPropagation()
    this.#go(index, true)
  }

  connectedCallback() {
    this.addEventListener('sf-project-jump', this.#jumped)
    this.#scroller = this.closest<HTMLElement>('.scroller')
    this.#scroller?.addEventListener('scroll', this.#onScroll, { passive: true })
    for (const type of ['wheel', 'pointerdown', 'keydown', 'touchstart']) addEventListener(type, this.#unpin, { passive: true, capture: true })
  }

  disconnectedCallback() {
    this.removeEventListener('sf-project-jump', this.#jumped)
    this.#scroller?.removeEventListener('scroll', this.#onScroll)
    for (const type of ['wheel', 'pointerdown', 'keydown', 'touchstart']) removeEventListener(type, this.#unpin, { capture: true })
    cancelAnimationFrame(this.#frame)
    this.#frame = 0
  }

  set navSlot(value: HTMLElement | null) {
    if (value === this.#slot) return
    this.#slot?.replaceChildren()
    this.#slot = value
    this.#placeNav()
  }

  get name(): string {
    return this.#name
  }

  set view(v: ProjectPageView | null) {
    if (!v) {
      this.#unmount()
      return
    }
    const shape = v.project ? `in:${v.project.path}` : v.known ? 'gone' : 'reading'
    if (v.name === this.#name && shape === this.#shape) return
    const titled = document.activeElement instanceof HTMLHeadingElement && this.contains(document.activeElement) && document.activeElement.tagName === 'H1'
    const opening = v.name !== this.#name
    this.#name = v.name
    this.#shape = shape
    this.#render(v)
    if (opening || titled) this.querySelector<HTMLElement>('h1')?.focus({ preventScroll: true })
  }

  jump(id: ProjectPart | string) {
    const index = this.#entries.findIndex(e => e.id === id)
    if (index < 0) return
    this.#go(index, false)
  }

  verifiedOnMain(repo: string) {
    if (this.#facts?.repo !== repo) return
    this.#facts.again()
    for (const node of this.querySelectorAll('sf-project-facts')) node.refresh()
  }

  #unmount() {
    if (!this.#name && !this.childElementCount) return
    this.#name = ''
    this.#shape = ''
    this.#entries = []
    this.#facts = null
    this.#nav = null
    this.#layout = null
    this.#slot?.replaceChildren()
    this.replaceChildren()
  }

  #placeNav() {
    if (!this.#nav || !this.#layout) return
    if (this.#slot) this.#slot.replaceChildren(this.#nav)
    else this.#layout.prepend(this.#nav)
  }

  #head(v: ProjectPageView): HTMLElement[] {
    const crumb = (text: string, view: string, reveal = '') => {
      const li = el('li')
      const a = el('a', '', text)
      a.href = './'
      a.dataset.view = view
      if (reveal) a.dataset.reveal = reveal
      li.append(a)
      return li
    }
    const here = el('li')
    here.setAttribute('aria-current', 'page')
    const title = el('h1', '', 'Settings')
    title.tabIndex = -1
    here.append(title)
    const trail = crumbTrail([welcomeCrumb(), crumb('Projects', 'projects'), v.project ? crumb(v.project.name, 'projects', v.project.path) : crumb(v.name, 'projects'), here])
    return pageHead({ title: trail })
  }

  #render(v: ProjectPageView) {
    this.#entries = []
    this.#facts = null
    this.#nav = null
    this.#layout = null
    this.#slot?.replaceChildren()
    const head = this.#head(v)
    if (!v.project) {
      if (!v.known) return this.replaceChildren(...head)
      const back = el('a', '', 'Go to Projects')
      back.href = './'
      back.dataset.view = 'projects'
      const gone = emptyState(`There is no project named ${v.name} on the Projects page. It may have been removed.`)
      gone.append(' ', back)
      return this.replaceChildren(...head, gone)
    }
    const p = v.project
    const nav = el('nav', 'project-nav')
    nav.setAttribute('aria-label', `Sections of ${p.name}'s settings`)
    const toc = el('ul', 'project-toc')
    const parts = el('div', 'project-parts')
    const source = factsSource(p.name)
    this.#facts = source
    for (const s of PARTS) {
      const node = s.make(p, source)
      parts.append(node)
      const item = el('li')
      item.append(this.#entry(s.id, s.title, node))
      toc.append(item)
    }
    nav.append(el('p', 'label', 'Contents'), toc)
    const layout = el('div', 'project-layout')
    layout.append(parts)
    this.#nav = nav
    this.#layout = layout
    this.#placeNav()
    replaceKeeping(this, [...head, layout])
    this.#mark(0)
  }

  #entry(id: string, title: string, target: HTMLElement): HTMLButtonElement {
    const index = this.#entries.length
    const b = button(title, 'link', () => this.#go(index, true), 'button', `nav:${id}`)
    this.#entries.push({ id, button: b, target })
    return b
  }

  #go(index: number, focus: boolean) {
    const e = this.#entries[index]
    if (!e) return
    e.target.scrollIntoView({ block: 'start' })
    this.#pinned = true
    this.#mark(index)
    if (!focus) return
    const heading = e.target.querySelector<HTMLElement>('h2')
    if (!heading) return
    if (!heading.hasAttribute('tabindex')) heading.tabIndex = -1
    heading.focus({ preventScroll: true })
  }

  #spy() {
    const s = this.#scroller
    if (this.hidden || !s || this.#pinned || !this.#entries.length) return
    let index = 0
    if (s.scrollHeight - s.scrollTop - s.clientHeight < 2 && s.scrollTop > 0) index = this.#entries.length - 1
    else {
      const line = s.getBoundingClientRect().top + 3 * (parseFloat(getComputedStyle(document.documentElement).fontSize) || 16)
      this.#entries.forEach((e, i) => {
        if (e.target.getBoundingClientRect().top <= line) index = i
      })
    }
    this.#mark(index)
  }

  #mark(index: number) {
    this.#entries.forEach((e, i) => {
      if (i === index) e.button.setAttribute('aria-current', 'location')
      else e.button.removeAttribute('aria-current')
    })
  }
}

customElements.define('sf-project-page', SfProjectPage)

declare global {
  interface HTMLElementTagNameMap {
    'sf-project-page': SfProjectPage
  }
}
