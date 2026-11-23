import { button, el } from './dom.ts'
import { tipText, tooltip } from './tooltip.ts'

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

export function welcomeCrumb(): HTMLElement {
  const li = el('li')
  const a = el('a', '', 'Welcome')
  a.href = './'
  a.dataset.view = 'overview'
  li.append(a)
  return li
}

function crumb(text: string): HTMLElement {
  const li = el('li')
  li.setAttribute('aria-current', 'page')
  li.append(el('h1', '', text))
  return crumbTrail([welcomeCrumb(), li])
}

export interface PageHeadOptions {
  title: string | Node
  crumb?: boolean
  level?: 'h1' | 'h2' | 'h3' | 'h4'
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

export type CheckState = 'found' | 'missing' | 'unknown'

export interface CheckRowOptions {
  state: CheckState
  word?: string
  label: string
  value?: string
  tip?: string
  detail?: string | Node
  action?: Node
  below?: Node[]
}

const CHECK_MARK: Record<CheckState, [string, string]> = { found: ['check', 'ok'], missing: ['warning', 'warn'], unknown: ['info', 'muted'] }

export function checkRow(o: CheckRowOptions): HTMLLIElement {
  const li = el('li', 'row text-row check-row')
  const [name, tone] = CHECK_MARK[o.state]
  const mark = el('span', `check-mark ${tone}`)
  mark.append(glyph(name))
  const text = el('p', 'check-text')
  if (o.word) text.append(el('span', 'sr-only', `${o.word}: `))
  text.append(o.label)
  if (o.value) text.append(el('span', 'muted', ' — '), ...tipText(o.value, o.tip ?? '', 'muted'))
  if (o.detail) {
    text.append(el('span', 'muted', ' · '))
    text.append(typeof o.detail === 'string' ? el('span', 'muted', o.detail) : o.detail)
  }
  li.append(mark, text)
  if (o.action) li.append(el('span', 'spacer'), o.action)
  if (o.below) li.append(...o.below)
  return li
}

export interface Fact {
  label: string
  value: string
  tip?: string
  tone?: string
}

export function factGrid(facts: Fact[]): HTMLDListElement {
  const dl = el('dl', 'small fact-grid')
  for (const f of facts) {
    const item = el('div', 'fact')
    const dd = el('dd', f.tone ?? '')
    dd.append(...tipText(f.value, f.tip ?? ''))
    item.append(el('dt', 'muted', f.label), dd)
    dl.append(item)
  }
  return dl
}

export interface PickRowOptions {
  text: string
  tip: string
  name: string
  on: boolean
  key: string
  onPick: (b: HTMLButtonElement) => void
}

export function pickRow(o: PickRowOptions): HTMLLIElement {
  const li = el('li', 'row pick-row')
  const text = el('p', 'pick-text')
  text.append(...tipText(o.text, o.tip))
  const b = button('', 'small pick', () => o.onPick(b), 'button', o.key)
  b.setAttribute('aria-pressed', String(o.on))
  const word = el('span', 'pick-word', 'Select')
  if (!o.on) {
    b.append(word, el('span', 'sr-only', ` ${o.name}`))
    li.append(b, text)
    return li
  }
  word.setAttribute('aria-hidden', 'true')
  const mark = glyph('check')
  mark.classList.add('pick-mark')
  b.append(word, mark, el('span', 'sr-only', `Selected ${o.name}`))
  li.append(...tooltip(b, 'Clear the choice'), text)
  return li
}

function glyph(name: string): HTMLElement {
  const mark = el('sf-icon', 'icon')
  mark.setAttribute('name', name)
  return mark
}
