import { current, scroller, state, waitingOf, type Session } from './app-state.ts'
import { byId } from './lib/dom.ts'
import { agentWord } from './lib/format.ts'

const topBar = document.querySelector<HTMLElement>('body > .bar')
const BAR_STEPS = ['fit-brand', 'fit-compact', 'fit-tight']
let barFitQueued = false

export function barStatus(detail: string) {
  const stateEl = byId('state')
  const title = byId('title')
  stateEl.textContent = state.connected ? '' : 'Disconnected'
  stateEl.hidden = state.connected
  title.textContent = detail
  title.hidden = !detail
}

function sideNeed(side: Element, rem: number): number {
  let need = 0
  let shown = 0
  for (const c of side.children) {
    if (!c.getClientRects().length) continue
    shown++
    if (c.id === 'state') need += (document.body.classList.contains('live') ? 8 : 6) * rem
    else if (c.id !== 'title') need += c.getBoundingClientRect().width
  }
  return need + (parseFloat(getComputedStyle(side).columnGap) || 0) * Math.max(0, shown - 1)
}

function barFits(bar: HTMLElement): boolean {
  const style = getComputedStyle(bar)
  const rem = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16
  const inner = bar.clientWidth - (parseFloat(style.paddingLeft) || 0) - (parseFloat(style.paddingRight) || 0)
  const box = byId('palette-open').getBoundingClientRect().width
  const room = (inner - box) / 2 - (parseFloat(style.columnGap) || 0)
  return [...bar.querySelectorAll(':scope > .bar-side')].every(side => sideNeed(side, rem) <= room)
}

function fitBar() {
  barFitQueued = false
  if (!topBar) return
  topBar.classList.remove(...BAR_STEPS)
  for (const step of BAR_STEPS) {
    if (barFits(topBar)) return
    topBar.classList.add(step)
  }
}

export function queueBarFit() {
  if (barFitQueued) return
  barFitQueued = true
  requestAnimationFrame(fitBar)
}

const WIDE_REM = 52
let wide: boolean | null = null
let sideAt = ''
let sideFull = false
let sideScroll = 0
const rootRem = () => parseFloat(getComputedStyle(document.documentElement).fontSize) || 16
let frameRem = byId('frame').clientWidth / rootRem()

function placeSide() {
  const now = frameRem >= WIDE_REM
  const at = document.body.classList.contains('side-full') ? 'full' : now ? 'frame' : 'narrow'
  if (at !== sideAt) {
    sideAt = at
    const active = document.activeElement
    if (at === 'frame') byId('frame').append(byId('composer'), byId('side'))
    else {
      byId('frame').after(byId('composer'))
      if (at === 'full') byId('crumbs').parentElement!.after(byId('side'))
      else byId('side-toggle').after(byId('side'))
    }
    if (active instanceof HTMLElement && active !== document.activeElement) active.focus()
  }
  if (now === wide) return
  wide = now
  document.body.classList.toggle('wide', now)
  byId<HTMLElementTagNameMap['sf-docs']>('docs').navSlot = now ? byId('docs-side') : null
  byId<HTMLElementTagNameMap['sf-projects']>('projects').sideSlot = now ? byId('projects-side') : null
  byId<HTMLElementTagNameMap['sf-project-page']>('project-page').navSlot = now ? byId('project-page-side') : null
}

export function paintSide(s: Session | null) {
  const full = !!s && sideFull && !byId('path-section').hidden
  const was = document.body.classList.contains('side-full')
  if (full && !was) sideScroll = scroller.scrollTop
  document.body.classList.toggle('side-full', full)
  placeSide()
  if (!full && was) scroller.scrollTop = sideScroll
  const needs = !!(full && s && waitingOf(s).length)
  byId('side-needs').hidden = !needs
  const said = needs && s ? `${agentWord(s.info.agent)} needs you in the conversation.` : ''
  if (byId('side-needs-said').textContent !== said) byId('side-needs-said').textContent = said
  byId('session-docs').dataset.doc = full ? 'execution-path-with-knackpack' : 'working-in-a-session'
}

export function setSide(full: boolean) {
  sideFull = full
  paintSide(current())
  if (full) scroller.scrollTop = 0
}

const measureFrame = () => {
  frameRem = byId('frame').clientWidth / rootRem()
  placeSide()
}

export function startLayout({ jumpLatest }: { jumpLatest: () => void }) {
  byId('side-open').addEventListener('click', () => {
    setSide(true)
    byId('side-expand').focus()
  })
  byId('side-expand').addEventListener('click', () => {
    setSide(false)
    byId('side-open').focus()
  })
  byId('side-back').addEventListener('click', () => {
    setSide(false)
    jumpLatest()
  })
  document.addEventListener('keydown', e => {
    if (e.key !== 'Escape' || e.defaultPrevented || !document.body.classList.contains('side-full')) return
    if (e.target instanceof HTMLElement && e.target.closest('[popover], select, dialog')) return
    setSide(false)
    byId('side-open').focus()
  })
  byId('side-toggle').addEventListener('click', () => {
    const toggle = byId('side-toggle')
    const open = toggle.getAttribute('aria-expanded') !== 'true'
    toggle.setAttribute('aria-expanded', String(open))
    byId('side').classList.toggle('open', open)
  })
  new ResizeObserver(measureFrame).observe(byId('frame'))
  new MutationObserver(measureFrame).observe(document.documentElement, { attributes: true, attributeFilter: ['data-scale'] })
  placeSide()
  byId('frame').addEventListener('wheel', e => {
    const target = e.target instanceof Element ? e.target : null
    const side = target?.closest<HTMLElement>('.page-side') ?? null
    const still = !!target && !!side && !scrollsWithin(target, side)
    if (e.target !== e.currentTarget && !still) return
    scroller.scrollTop += e.deltaMode === 1 ? e.deltaY * 16 : e.deltaMode === 2 ? e.deltaY * scroller.clientHeight : e.deltaY
  }, { passive: true })
}

function scrollsWithin(node: Element, root: Element): boolean {
  for (let n: Element | null = node; n; n = n === root ? null : n.parentElement) {
    if (n.scrollHeight > n.clientHeight && /auto|scroll/.test(getComputedStyle(n).overflowY)) return true
  }
  return false
}

export function startTopBar() {
  if (topBar) {
    const watch = new ResizeObserver(queueBarFit)
    for (const node of [topBar, byId('halt'), byId('update'), byId('palette-open')]) watch.observe(node)
    fitBar()
  }
}
