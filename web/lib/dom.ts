import { INVISIBLE, visible } from '../types.ts'

export { visible }

export function el<K extends keyof HTMLElementTagNameMap>(tag: K, cls?: string, text?: string): HTMLElementTagNameMap[K]
export function el(tag: string, cls?: string, text?: string): HTMLElement
export function el(tag: string, cls?: string, text?: string): HTMLElement {
  const node = document.createElement(tag)
  if (cls) node.className = cls
  if (text !== undefined) node.textContent = text
  return node
}

export const byId = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T

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

export function around<T>(list: readonly T[], item: T): T[] {
  const at = list.indexOf(item)
  return at < 0 ? [] : [...list.slice(at + 1), ...list.slice(0, at).reverse()]
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

let ids = 0

export function ensureId(node: HTMLElement): string {
  if (!node.id) node.id = `field-${++ids}`
  return node.id
}

const TONES = ['bad', 'warn', 'ok', 'muted'] as const
export type Tone = typeof TONES[number]

function sideBySideRow(label: HTMLLabelElement): HTMLElement | null {
  const holder = label.closest('.field')?.parentElement?.closest<HTMLElement>('.form, .form-row')
  return holder?.matches('.form-inline, .form-row') ? holder : null
}

function labelName(label: HTMLLabelElement): string {
  const name = label.cloneNode(true) as HTMLLabelElement
  for (const part of name.querySelectorAll('.label-error, .muted, input, select, textarea, button')) part.remove()
  return (name.textContent ?? '').trim()
}

export function labelError(input: HTMLElement, message: string): void {
  const id = `${ensureId(input)}-error`
  const root = input.getRootNode()
  const scope = root instanceof Document || root instanceof DocumentFragment || root instanceof Element ? root : null
  const label = scope?.querySelector<HTMLLabelElement>(`label[for="${CSS.escape(input.id)}"]`) ?? null
  label?.querySelector('.label-error')?.remove()
  const row = label ? sideBySideRow(label) : null
  const lineId = `${id}-row`
  for (const place of [row, scope]) place?.querySelector(`[id="${CSS.escape(lineId)}"]`)?.remove()
  const described = (input.getAttribute('aria-describedby') ?? '').split(' ').filter(d => d && d !== id)
  if (!message || !label) {
    input.removeAttribute('aria-invalid')
    if (described.length) input.setAttribute('aria-describedby', described.join(' '))
    else input.removeAttribute('aria-describedby')
    return
  }
  const note = el('span', 'label-error', `(${message.trim().replace(/\.$/, '')})`)
  note.id = id
  note.setAttribute('aria-hidden', 'true')
  if (row) {
    const line = el('p', 'small row-error')
    line.id = lineId
    line.setAttribute('aria-hidden', 'true')
    line.append(labelName(label), ' ', note)
    row.append(line)
  } else label.append(note)
  input.setAttribute('aria-invalid', 'true')
  input.setAttribute('aria-describedby', [...described, id].join(' '))
}

export function statusLine(cls?: string): HTMLParagraphElement
export function statusLine(cls: string, tag: 'span'): HTMLSpanElement
export function statusLine(cls = 'small', tag: 'p' | 'span' = 'p'): HTMLParagraphElement | HTMLSpanElement {
  const line = el(tag, cls)
  line.setAttribute('role', 'status')
  return line
}

const pending = new WeakMap<HTMLElement, { text: string; tone: Tone | undefined }>()

function toned(line: HTMLElement, tone: Tone | undefined): void {
  pending.delete(line)
  for (const t of TONES) line.classList.toggle(t, t === tone)
}

export function say(line: HTMLElement, text: string, tone?: Tone): void {
  toned(line, tone)
  line.textContent = text
}

export function sayIfChanged(line: HTMLElement, text: string, tone?: Tone): void {
  toned(line, tone)
  if (line.textContent !== text) line.textContent = text
}

function onScreen(line: HTMLElement): boolean {
  if (!line.isConnected || line.closest('[hidden]')) return false
  for (let box = line.closest('details'); box; box = box.parentElement?.closest('details') ?? null) {
    if (!box.open && !box.querySelector(':scope > summary')?.contains(line)) return false
  }
  return true
}

export function sayWhenShown(line: HTMLElement, text: string, tone?: Tone): void {
  const waiting = pending.get(line)
  if (waiting && waiting.text === text) {
    waiting.tone = tone
    return
  }
  if (!text || onScreen(line)) return sayIfChanged(line, text, tone)
  say(line, '', tone)
  const next = { text, tone }
  pending.set(line, next)
  requestAnimationFrame(() => requestAnimationFrame(() => {
    if (pending.get(line) === next) say(line, next.text, next.tone)
  }))
}

export function replaceKeeping(parent: Element, parts: Node[]): void {
  const kept = new Set(parts)
  for (const child of [...parent.childNodes]) if (!kept.has(child)) child.remove()
  let at = parent.firstChild
  for (const part of parts) {
    if (part === at) at = at.nextSibling
    else parent.insertBefore(part, at)
  }
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

const resting = new WeakMap<HTMLButtonElement, string | null>()
let lastPressed: HTMLButtonElement | null = null

export function pressed(): HTMLButtonElement | null {
  return lastPressed
}

function refuseBusy(e: Event): void {
  const t = e.target
  if (!(t instanceof Element)) return
  if (e.type === 'click') {
    lastPressed = t.closest('button')
    setTimeout(() => {
      lastPressed = null
    })
  }
  if (busyWorks.size) checkBusy()
  const held = e.type === 'submit' ? t.querySelector('button[type="submit"][aria-busy="true"]') : t.closest('button[aria-busy="true"]')
  if (!held) return
  e.preventDefault()
  e.stopImmediatePropagation()
}

if (typeof document !== 'undefined') {
  document.addEventListener('click', refuseBusy, true)
  document.addEventListener('submit', refuseBusy, true)
}

const BUSY_SAID = 'Working…'
const BUSY_SHOWN_AFTER_MS = 300
const BUSY_CHECKED_EVERY_MS = 500
const busyLines = new WeakMap<Element, HTMLSpanElement>()
const busySince = new WeakMap<HTMLButtonElement, number>()

type BusyWork = {
  layer: Element
  key: string | HTMLButtonElement
  line: HTMLSpanElement
  since: number
  shown: boolean
  wait: ReturnType<typeof setTimeout>
}

const busyWorks = new Set<BusyWork>()
let busyChecks: ReturnType<typeof setInterval> | undefined

function busyLayer(b: Element): Element {
  return b.closest('dialog[open]') ?? document.body
}

function busyLine(layer: Element): HTMLSpanElement {
  let line = busyLines.get(layer)
  if (!line?.isConnected) {
    line = statusLine('sr-only', 'span')
    layer.prepend(line)
    busyLines.set(layer, line)
  }
  return line
}

function busyKey(b: HTMLButtonElement): string | HTMLButtonElement {
  if (b.dataset.key) return `button[data-key="${CSS.escape(b.dataset.key)}"]`
  if (b.id) return `button[id="${CSS.escape(b.id)}"]`
  return b
}

function busyHolders(work: BusyWork): HTMLButtonElement[] {
  const found = typeof work.key === 'string' ? work.layer.querySelectorAll<HTMLButtonElement>(work.key) : [work.key]
  return [...found].filter(b => resting.has(b) && b.isConnected && busyLayer(b) === work.layer)
}

function sayBusy(lines: Iterable<HTMLSpanElement>): void {
  const speaking = new Set<HTMLSpanElement>()
  for (const work of busyWorks) if (work.shown) speaking.add(work.line)
  for (const line of lines) sayIfChanged(line, speaking.has(line) ? BUSY_SAID : '')
}

function checkBusy(): void {
  const ended = new Set<HTMLSpanElement>()
  for (const work of busyWorks) {
    if (busyHolders(work).length) continue
    busyWorks.delete(work)
    clearTimeout(work.wait)
    if (work.shown) ended.add(work.line)
  }
  if (!busyWorks.size) {
    clearInterval(busyChecks)
    busyChecks = undefined
  }
  sayBusy(ended)
}

function busyStarted(b: HTMLButtonElement): void {
  if (!b.isConnected || !resting.has(b)) return
  const layer = busyLayer(b)
  const key = busyKey(b)
  const going = [...busyWorks].find(work => work.layer === layer && work.key === key)
  if (going) {
    if (going.shown) b.dataset.busyShown = ''
    return
  }
  const work: BusyWork = {
    layer,
    key,
    line: busyLine(layer),
    since: busySince.get(b) ?? performance.now(),
    shown: false,
    wait: setTimeout(() => {
      if (!busyWorks.has(work)) return
      const held = busyHolders(work)
      if (!held.length) return checkBusy()
      work.shown = true
      for (const h of held) if ((busySince.get(h) ?? work.since) > work.since) h.dataset.busyShown = ''
      sayBusy([work.line])
    }, BUSY_SHOWN_AFTER_MS),
  }
  busyWorks.add(work)
  busyChecks ??= setInterval(checkBusy, BUSY_CHECKED_EVERY_MS)
}

const unplaced = new Set<WeakRef<HTMLButtonElement>>()
let placing: MutationObserver | undefined

function placeBusy(): void {
  for (const ref of unplaced) {
    const b = ref.deref()
    if (!b || !resting.has(b)) unplaced.delete(ref)
    else if (b.isConnected) {
      unplaced.delete(ref)
      busyStarted(b)
    }
  }
  if (unplaced.size) return
  placing?.disconnect()
  placing = undefined
}

function awaitPlace(b: HTMLButtonElement): void {
  for (const ref of unplaced) if (ref.deref() === b) return
  unplaced.add(new WeakRef(b))
  if (placing || typeof MutationObserver === 'undefined') return
  placing = new MutationObserver(placeBusy)
  placing.observe(document, { childList: true, subtree: true })
}

if (typeof document !== 'undefined' && document.body) busyLines.set(document.body, document.body.appendChild(statusLine('sr-only', 'span')))

export function busy(b: HTMLButtonElement, on: boolean): void {
  if (on) {
    if (!resting.has(b)) {
      resting.set(b, b.getAttribute('aria-disabled'))
      busySince.set(b, performance.now())
    }
    b.setAttribute('aria-busy', 'true')
    b.setAttribute('aria-disabled', 'true')
    if (b.isConnected) busyStarted(b)
    else awaitPlace(b)
    return
  }
  if (!resting.has(b)) return
  const before = resting.get(b) ?? null
  resting.delete(b)
  if (unplaced.size) placeBusy()
  busySince.delete(b)
  delete b.dataset.busyShown
  b.removeAttribute('aria-busy')
  if (before === null) b.removeAttribute('aria-disabled')
  else b.setAttribute('aria-disabled', before)
  if (busyWorks.size) checkBusy()
}

export async function whileBusy<T>(b: HTMLButtonElement, work: () => Promise<T>): Promise<T> {
  busy(b, true)
  try {
    return await work()
  } finally {
    busy(b, false)
  }
}

export function actionButton(text: string, cls: string, run: () => Promise<unknown>, type: 'button' | 'submit' = 'button', key = ''): HTMLButtonElement {
  const b = button(text, cls, undefined, type, key)
  b.addEventListener('click', () => void whileBusy(b, run))
  return b
}

type SfEvents = { [K in keyof HTMLElementEventMap as K extends `sf-${string}` ? K : never]: HTMLElementEventMap[K] }
type Detail<E> = E extends CustomEvent<infer D> ? [detail: D] : []

export function emit<K extends keyof SfEvents>(target: EventTarget, type: K, ...detail: Detail<SfEvents[K]>): void {
  target.dispatchEvent(detail.length ? new CustomEvent(type, { bubbles: true, detail: detail[0] }) : new Event(type, { bubbles: true }))
}

const REPO_PATH_UNSAFE = /[\\:\u0000-\u001f\u007f]/

export function repoPath(path: string): boolean {
  return path.length > 0 && path.length <= 300 && !path.startsWith('/') && !path.startsWith('~') && !REPO_PATH_UNSAFE.test(path) && path === visible(path) && path.split('/').every(part => part !== '' && part !== '.' && part !== '..')
}

export function fileLink(path: string, key = '', at?: { commit: string; line?: number }): HTMLButtonElement {
  const shown = visible(path)
  const where = at ? `${shown}${at.line ? `:${at.line}` : ''}` : shown
  const link = button(where, 'link file-link', () => emit(link, 'sf-file', at ? { path, commit: at.commit, ...(at.line ? { line: at.line } : {}) } : { path }), 'button', key)
  link.title = at ? `Open ${shown}${at.line ? ` at line ${at.line}` : ''}, as commit ${at.commit.slice(0, 7)} has it, read-only` : `Open ${shown} read-only`
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

export function focusView(): void {
  const shownNode = (h: HTMLElement) => h.offsetParent !== null
  const target = [...document.querySelectorAll<HTMLElement>('#crumbs h1, main h1, .view h1, main h2')].find(shownNode) ?? [...document.querySelectorAll<HTMLElement>('main')].find(shownNode)
  if (!target) return
  if (!target.hasAttribute('tabindex')) target.tabIndex = -1
  target.focus({ preventScroll: true })
}

function lost(): boolean {
  const a = document.activeElement
  return !(a instanceof HTMLElement) || a === document.body || a.offsetParent === null && a !== document.documentElement && getComputedStyle(a).position !== 'fixed'
}

export function settleFocus(): void {
  const check = () => {
    if (!document.querySelector('dialog[open]') && lost()) focusView()
  }
  requestAnimationFrame(check)
  setTimeout(check, 250)
  setTimeout(check, 1000)
}

export function opener(): () => void {
  const back = document.activeElement instanceof HTMLElement && document.activeElement !== document.body ? document.activeElement : null
  return () => {
    if (back?.isConnected && !back.closest('[hidden], dialog:not([open])')) back.focus({ preventScroll: true })
    else focusView()
  }
}
