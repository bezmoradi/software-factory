import { button, el } from './dom.ts'

export interface DocsLinkOptions {
  doc?: string
  key?: string
  id?: string
  open?: () => void
}

export function docsLink(o: DocsLinkOptions): HTMLButtonElement {
  const link = button('View in docs', 'link small docs-link', o.open, 'button', o.key ?? '')
  if (o.doc) link.dataset.doc = o.doc
  if (o.id) link.id = o.id
  link.prepend(glyph('link'))
  return link
}

export function crumbTrail(items: Node[] = []): HTMLElement {
  const nav = el('nav', 'crumbs')
  nav.setAttribute('aria-label', 'Breadcrumb')
  const ol = el('ol')
  ol.append(...items)
  nav.append(ol)
  return nav
}

function crumb(text: string): HTMLElement {
  const li = el('li')
  li.setAttribute('aria-current', 'page')
  li.append(el('h1', '', text))
  return crumbTrail([li])
}

export interface PageHeadOptions {
  title: string | Node
  crumb?: boolean
  level?: 'h1' | 'h2' | 'h3'
  headingClass?: string
  intro?: string
  docs?: DocsLinkOptions
  action?: Node
}

export function pageHead(o: PageHeadOptions): HTMLElement[] {
  const row = el('div', 'page-head')
  const title = typeof o.title !== 'string' ? o.title : o.crumb ? crumb(o.title) : el(o.level ?? 'h2', o.headingClass ?? '', o.title)
  row.append(title)
  if (o.docs) row.append(docsLink(o.docs))
  if (o.action) row.append(o.action)
  return o.intro ? [row, el('p', 'small muted page-intro', o.intro)] : [row]
}

export interface EmptyStateOptions {
  title?: string
  icon?: string
  action?: Node
  tag?: 'div' | 'li'
}

export function emptyState(text: string, o: EmptyStateOptions = {}): HTMLElement {
  const box = el('div', 'empty-state')
  if (o.icon) {
    const media = el('span', 'empty-state-icon')
    media.append(glyph(o.icon))
    box.append(media)
  }
  if (o.title) box.append(el('h3', 'empty-state-title', o.title))
  box.append(el('p', 'small muted', text))
  if (o.action) box.append(o.action)
  if (o.tag !== 'li') return box
  const item = el('li')
  item.append(box)
  return item
}

function glyph(name: string): HTMLElement {
  const mark = el('sf-icon', 'icon')
  mark.setAttribute('name', name)
  return mark
}
