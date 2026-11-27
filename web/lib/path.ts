import { button, el, visible } from './dom.ts'
import type { GatePath, PathAction } from '../types.ts'

function paintBar(bar: HTMLElement, fills: number[]) {
  bar.hidden = !fills.length
  while (bar.children.length > fills.length) bar.lastElementChild!.remove()
  while (bar.children.length < fills.length) bar.append(el('span'))
  fills.forEach((fill, at) => (bar.children[at] as HTMLElement).style.setProperty('--fill', String(fill)))
}

function paintCard(card: HTMLElement, step: string, count: string, detail: string, bar: number[]) {
  card.querySelector('.path-card-step')!.textContent = step
  card.querySelector('.path-card-count')!.textContent = count
  card.querySelector('.path-card-detail')!.textContent = detail
  paintBar(card.querySelector<HTMLElement>('.path-card-bar')!, bar)
}

function paintActions(host: HTMLElement, anchor: HTMLElement, actions: PathAction[]) {
  for (const a of actions) {
    const made = [...host.querySelectorAll<HTMLButtonElement>('button.stage')]
    if (made.some(b => b.dataset.ask === a.ask)) continue
    const b = button(a.label, 'link stage')
    b.dataset.ask = a.ask
    ;(made.at(-1) ?? anchor).after(' ', b)
  }
  for (const b of host.querySelectorAll<HTMLButtonElement>('button.stage')) {
    const action = actions.find(a => a.ask === b.dataset.ask)
    b.hidden = !action
    if (!action) continue
    if (b.textContent !== action.label) b.textContent = action.label
    b.title = action.title
  }
}

function lineOf(root: HTMLElement): HTMLElement {
  const next = root.nextElementSibling
  if (next instanceof HTMLElement && next.classList.contains('path-line')) return next
  const line = el('p', 'path-line small')
  line.hidden = true
  line.append(el('span', 'path-line-text'))
  root.after(line)
  return line
}

function noticeOf(root: HTMLElement): HTMLElement {
  const made = root.parentElement?.querySelector<HTMLElement>(':scope > .path-notice')
  if (made) return made
  const notice = el('div', 'notice alert path-notice')
  notice.hidden = true
  const text = el('p', 'path-notice-text')
  text.setAttribute('role', 'status')
  notice.append(text)
  root.before(notice)
  return notice
}

function paintNotice(root: HTMLElement, said: GatePath['notice']) {
  const notice = noticeOf(root)
  notice.hidden = !said
  notice.classList.toggle('alert', said?.tone !== 'info')
  const text = notice.querySelector<HTMLElement>('.path-notice-text')!
  const shown = visible(said?.text ?? '')
  if (text.textContent !== shown) text.textContent = shown
  paintActions(notice, text, said?.actions ?? [])
}

export function paintPath(root: HTMLElement, path: GatePath | null, card?: HTMLElement, working = false): void {
  const stages = new Map((path?.stages ?? []).map(s => [s.id, s]))
  const here = working && !path?.stages.some(s => s.active)
  for (const li of root.children as HTMLCollectionOf<HTMLElement>) {
    const st = stages.get(li.dataset.stage ?? '')
    if (path) li.hidden = !st
    const name = li.querySelector('strong')!
    if (st && name.textContent !== st.name) name.textContent = st.name
    li.className = st?.state ?? ''
    const workingHere = here && st?.state === 'current'
    const active = !!st?.active || workingHere
    li.classList.toggle('active', active)
    li.querySelector('.stage-detail')!.textContent = visible(st?.detail ?? '')
    let status = li.querySelector<HTMLElement>('.stage-status')
    if (!status) {
      status = el('span', 'stage-status small muted')
      name.after(status)
    }
    const said = workingHere ? 'Working' : visible(st?.status ?? '')
    if (status.textContent !== said) status.textContent = said
    paintActions(li, status, st?.actions ?? [])
  }
  paintNotice(root, path?.notice ?? null)
  root.querySelector('.missing')!.replaceChildren(...(path?.missing ?? []).map(m => el('li', '', visible(m))))
  const free = path?.line ?? null
  const line = lineOf(root)
  root.hidden = !!free
  line.hidden = !free
  const text = line.querySelector<HTMLElement>('.path-line-text')!
  text.textContent = visible(free?.text ?? '')
  paintActions(line, text, free?.actions ?? [])
  if (!card) return
  if (!path) paintCard(card, 'Waiting for the session', '', '', [])
  else paintCard(card, path.card.step, path.card.count, visible(path.card.detail), path.card.bar)
}

export interface StepsEntry { title: string, target: HTMLElement }

function goTo(target: HTMLElement) {
  target.scrollIntoView({ block: 'start' })
  if (!target.hasAttribute('tabindex')) target.tabIndex = -1
  target.focus({ preventScroll: true })
}

export function paintSteps(nav: HTMLElement, root: HTMLElement, extra: StepsEntry[]): void {
  const steps = [...root.children as HTMLCollectionOf<HTMLElement>].filter(li => !li.hidden)
  const rows = steps.map(li => ({
    title: li.querySelector('strong')?.textContent ?? '',
    status: li.querySelector('.stage-status')?.textContent ?? '',
    now: li.classList.contains('current') || li.classList.contains('active'),
    target: li,
  }))
  const shown = extra.filter(x => !x.target.closest('[hidden]'))
  const key = JSON.stringify([rows.map(r => [r.title, r.status, r.now]), shown.map(x => x.title)])
  if (nav.dataset.key === key) return
  nav.dataset.key = key
  if (!rows.length && !shown.length) return nav.replaceChildren()
  const toc = el('ul', 'project-toc')
  for (const r of rows) {
    const b = button(r.title, 'link', () => goTo(r.target))
    if (r.status) b.append(' ', el('span', 'toc-status', r.status))
    if (r.now) b.setAttribute('aria-current', 'step')
    const item = el('li')
    item.append(b)
    toc.append(item)
  }
  for (const x of shown) {
    const item = el('li')
    item.append(button(x.title, 'link', () => goTo(x.target)))
    toc.append(item)
  }
  const box = el('nav', 'project-nav')
  box.setAttribute('aria-label', 'Steps of the execution path')
  box.append(el('p', 'label', 'Contents'), toc)
  nav.replaceChildren(box)
}
