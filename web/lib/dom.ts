export function el<K extends keyof HTMLElementTagNameMap>(tag: K, cls?: string, text?: string): HTMLElementTagNameMap[K]
export function el(tag: string, cls?: string, text?: string): HTMLElement
export function el(tag: string, cls?: string, text?: string): HTMLElement {
  const node = document.createElement(tag)
  if (cls) node.className = cls
  if (text !== undefined) node.textContent = text
  return node
}

const INVISIBLE = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u00ad\u061c\u180e\u200b-\u200f\u2028-\u202e\u2060-\u2069\ufe00-\ufe0f\ufeff\u{e0000}-\u{e007f}￹-￻\p{Cf}\p{Default_Ignorable_Code_Point}]/gu

export function visible(text: string): string {
  return String(text).replace(INVISIBLE, c => `\\u{${c.codePointAt(0)!.toString(16).padStart(4, '0')}}`)
}

const SPOOFING = /[\u061c\u180e\u200b\u200e\u200f\u202a-\u202e\u2060\u2066-\u2069\ufeff]/gu

function codeName(c: string): string {
  return `⟨U+${c.codePointAt(0)!.toString(16).toUpperCase().padStart(4, '0')}⟩`
}

export function unspoofed(text: string): string {
  return String(text).replace(SPOOFING, codeName)
}

export function named(text: string): string {
  return String(text).replace(INVISIBLE, codeName)
}

export function shown(text: string): string {
  return visible(unspoofed(text))
}

export function visibleCommand(text: string): string {
  return visible(text)
    .replace(/(?:\n[ \t]*){4,}/g, run => `\n⏎ ×${run.split('\n').length - 2}\n`)
    .replace(/(\S) {8,}/g, (run, before: string) => `${before} ␠ ×${run.length - before.length} `)
    .replace(/^ {32,}/gm, run => `␠ ×${run.length} `)
}

export function focusedKey(root: HTMLElement): string {
  const a = document.activeElement
  return a instanceof HTMLElement && root.contains(a) ? a.dataset.key || '' : ''
}

export function refocus(root: HTMLElement, key: string, fallback: (key: string) => string[] = () => []): void {
  if (!key) return
  const a = document.activeElement
  if (a instanceof HTMLElement && root.contains(a) && a !== document.body) return
  for (const k of [key, ...fallback(key)]) {
    const node = root.querySelector<HTMLElement>(`[data-key="${CSS.escape(k)}"]`)
    if (node && !(node as HTMLButtonElement).disabled && !node.closest('[hidden]')) return node.focus()
  }
}

export function button(text: string, cls = '', onClick?: () => void, type: 'button' | 'submit' = 'button', key = ''): HTMLButtonElement {
  const b = el('button', cls, text)
  b.type = type
  if (key) b.dataset.key = key
  if (onClick) b.addEventListener('click', onClick)
  return b
}

type SfEvents = { [K in keyof HTMLElementEventMap as K extends `sf-${string}` ? K : never]: HTMLElementEventMap[K] }
type Detail<E> = E extends CustomEvent<infer D> ? [detail: D] : []

export function emit<K extends keyof SfEvents>(target: EventTarget, type: K, ...detail: Detail<SfEvents[K]>): void {
  target.dispatchEvent(detail.length ? new CustomEvent(type, { bubbles: true, detail: detail[0] }) : new Event(type, { bubbles: true }))
}

export function fileLink(path: string, key = ''): HTMLButtonElement {
  const shown = visible(path)
  const link = button(shown, 'link file-link', () => emit(link, 'sf-file', { path }), 'button', key)
  link.title = `Open ${shown} read-only`
  return link
}

export function rovingKeys(e: KeyboardEvent, items: HTMLElement[], at: number, axis: 'horizontal' | 'vertical'): HTMLElement | null {
  const [back, forward] = axis === 'horizontal' ? ['ArrowLeft', 'ArrowRight'] : ['ArrowUp', 'ArrowDown']
  const to = e.key === 'Home' ? 0 : e.key === 'End' ? items.length - 1 : e.key === forward ? at + 1 : e.key === back ? at - 1 : null
  if (to === null || !items.length) return null
  e.preventDefault()
  const next = items[(to + items.length) % items.length]!
  next.focus()
  return next
}

export function remembered(open: Map<string, boolean>, key: string, openByDefault: boolean, summary: HTMLElement, ...content: Node[]): HTMLDetailsElement {
  const d = el('details')
  d.open = open.get(key) ?? openByDefault
  d.addEventListener('toggle', () => open.set(key, d.open))
  d.append(summary, ...content)
  return d
}

export function pageCrumb(text: string): HTMLElement {
  const nav = el('nav', 'crumbs')
  nav.setAttribute('aria-label', 'Breadcrumb')
  const li = el('li')
  li.setAttribute('aria-current', 'page')
  li.append(el('h1', '', text))
  const ol = el('ol')
  ol.append(li)
  nav.append(ol)
  return nav
}
