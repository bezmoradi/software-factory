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

export function docsId(title: string): string {
  return title.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
}

export interface DialogLinkOptions {
  title: string
  dialog: string
}

export function dialogLink(o: DialogLinkOptions): HTMLButtonElement {
  const link = button(`Open ${o.title}`, 'link small docs-open')
  link.dataset.dialog = o.dialog
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

export interface UsageMeterOptions {
  label: string
  percent: number
  detail: string
  warn?: boolean
}

export function usageMeter(o: UsageMeterOptions): HTMLElement {
  const percent = Math.min(100, Math.max(0, Math.round(o.percent)))
  const card = el('div', ['usage-meter', o.warn ? 'warn' : ''].filter(Boolean).join(' '))
  const head = el('div', 'usage-meter-head')
  head.append(el('span', 'usage-meter-label', o.label), el('span', 'usage-meter-value', `${percent}% used`))
  const bar = el('div', 'usage-meter-bar')
  bar.setAttribute('role', 'meter')
  bar.setAttribute('aria-label', o.label)
  bar.setAttribute('aria-valuemin', '0')
  bar.setAttribute('aria-valuemax', '100')
  bar.setAttribute('aria-valuenow', String(percent))
  bar.setAttribute('aria-valuetext', `${percent}% used, ${o.detail}`)
  bar.style.setProperty('--used', `${percent}%`)
  bar.append(el('span', 'usage-meter-fill'))
  const detail = el('p', 'usage-meter-detail', o.detail)
  head.setAttribute('aria-hidden', 'true')
  detail.setAttribute('aria-hidden', 'true')
  card.append(head, bar, detail)
  return card
}

export interface EmptyStateOptions {
  title?: string
  icon?: string
  action?: Node
  tag?: 'div' | 'li'
}

export function emptyState(text: string, o: EmptyStateOptions = {}): HTMLElement {
  if (!o.title && !o.icon && !o.action) {
    const line = el('p', 'small muted empty-state-line', text)
    if (o.tag !== 'li') return line
    const item = el('li', 'empty-state-item')
    item.append(line)
    return item
  }
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
