import { el } from './dom.ts'

const OPEN_DELAY_MS = 300
const CLOSE_GRACE_MS = 120
const SKIP_DELAY_MS = 300
const OFFSET_PX = 6
const EDGE_PX = 8

interface Tip {
  trigger: HTMLElement
  tip: HTMLElement
}

let ids = 0
let current: Tip | null = null
let openTimer = 0
let closeTimer = 0
let closedAt = 0
let listening = false

function noHover(): boolean {
  return matchMedia('(hover: none)').matches
}

function place(t: Tip): void {
  if (!t.trigger.isConnected || !t.tip.isConnected) return hide()
  const r = t.trigger.getBoundingClientRect()
  const width = document.documentElement.clientWidth
  const height = document.documentElement.clientHeight
  const w = t.tip.offsetWidth
  const h = t.tip.offsetHeight
  const above = r.top - OFFSET_PX - h
  const top = above >= EDGE_PX || r.bottom + OFFSET_PX + h > height - EDGE_PX ? Math.max(EDGE_PX, above) : r.bottom + OFFSET_PX
  const left = Math.max(EDGE_PX, Math.min(r.left + r.width / 2 - w / 2, width - w - EDGE_PX))
  t.tip.dataset.side = top < r.top ? 'top' : 'bottom'
  t.tip.style.top = `${Math.round(top)}px`
  t.tip.style.left = `${Math.round(left)}px`
}

function cancelTimers(): void {
  window.clearTimeout(openTimer)
  window.clearTimeout(closeTimer)
  openTimer = 0
  closeTimer = 0
}

function show(t: Tip): void {
  cancelTimers()
  if (current && current !== t) hide()
  if (!t.trigger.isConnected || !t.tip.isConnected) return
  current = t
  listen()
  if (!t.tip.matches(':popover-open')) t.tip.showPopover()
  place(t)
}

function hide(): void {
  cancelTimers()
  const t = current
  current = null
  if (!t) return
  if (t.tip.isConnected && t.tip.matches(':popover-open')) t.tip.hidePopover()
}

function hideLater(t: Tip): void {
  window.clearTimeout(openTimer)
  openTimer = 0
  if (current !== t || closeTimer) return
  closeTimer = window.setTimeout(() => {
    closeTimer = 0
    if (current !== t || t.trigger.matches(':focus-visible')) return
    hide()
    closedAt = Date.now()
  }, CLOSE_GRACE_MS)
}

function listen(): void {
  if (listening) return
  listening = true
  window.addEventListener('keydown', e => {
    if (e.key !== 'Escape' || !current) return
    if (!current.tip.isConnected || !current.tip.matches(':popover-open')) return hide()
    e.preventDefault()
    e.stopImmediatePropagation()
    hide()
  }, true)
  document.addEventListener('pointerdown', e => {
    if (!current) return
    const at = e.target
    if (at instanceof Node && (current.trigger.contains(at) || current.tip.contains(at))) return
    hide()
  }, true)
  document.addEventListener('scroll', () => {
    if (current) place(current)
  }, { capture: true, passive: true })
  window.addEventListener('resize', () => {
    if (current) place(current)
  })
}

export function tooltip(trigger: HTMLElement, text: string): HTMLElement[] {
  const tip = el('span', 'tooltip', text)
  tip.id = `tip-${++ids}`
  tip.setAttribute('role', 'tooltip')
  tip.popover = 'manual'
  trigger.setAttribute('aria-describedby', [trigger.getAttribute('aria-describedby'), tip.id].filter(Boolean).join(' '))
  trigger.removeAttribute('title')
  const t: Tip = { trigger, tip }
  let pointer = ''
  trigger.addEventListener('pointerenter', e => {
    if (e.pointerType !== 'mouse' && e.pointerType !== 'pen') return
    if (noHover()) return
    window.clearTimeout(closeTimer)
    closeTimer = 0
    if (current === t) return
    window.clearTimeout(openTimer)
    openTimer = window.setTimeout(() => show(t), current || Date.now() - closedAt < SKIP_DELAY_MS ? 0 : OPEN_DELAY_MS)
  })
  trigger.addEventListener('pointerleave', e => {
    if (e.pointerType === 'touch') return
    hideLater(t)
  })
  tip.addEventListener('pointerenter', () => {
    window.clearTimeout(closeTimer)
    closeTimer = 0
  })
  tip.addEventListener('pointerleave', e => {
    if (e.pointerType === 'touch') return
    hideLater(t)
  })
  trigger.addEventListener('pointerdown', e => {
    pointer = e.pointerType
  })
  trigger.addEventListener('focus', () => {
    if (trigger.matches(':focus-visible')) show(t)
  })
  trigger.addEventListener('blur', () => {
    if (current === t) hide()
  })
  trigger.addEventListener('click', e => {
    const touched = pointer === 'touch' || noHover()
    const keyed = e.detail === 0 && !pointer
    pointer = ''
    if (!touched && !keyed) return
    if (current === t) hide()
    else show(t)
  })
  return [trigger, tip]
}

export function tipTag(label: string, tone: string, text: string): HTMLElement[] {
  const tag = el('span', `tag ${tone}`.trim(), label)
  if (!text) return [tag]
  const trigger = el('button', 'tip-tag')
  trigger.type = 'button'
  trigger.append(tag)
  return tooltip(trigger, text)
}

export function tipText(text: string, tip: string, cls = ''): HTMLElement[] {
  if (!tip) return [el('span', cls, text)]
  const trigger = el('button', `tip-text ${cls}`.trim(), text)
  trigger.type = 'button'
  return tooltip(trigger, tip)
}
