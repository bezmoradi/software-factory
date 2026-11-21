import { el, visible } from './dom.ts'
import type { GatePath } from '../types.ts'

function paintCard(card: HTMLElement, step: string, count: string, detail: string, done: number) {
  card.querySelector('.path-card-step')!.textContent = step
  card.querySelector('.path-card-count')!.textContent = count
  card.querySelector('.path-card-detail')!.textContent = detail
  card.style.setProperty('--done', String(done))
}

export function paintPath(root: HTMLElement, path: GatePath | null, card?: HTMLElement): void {
  const stages = new Map((path?.stages ?? []).map(s => [s.id, s]))
  for (const li of root.children as HTMLCollectionOf<HTMLElement>) {
    const st = stages.get(li.dataset.stage ?? '')
    if (path) li.hidden = !st
    const name = li.querySelector('strong')!
    if (st && name.textContent !== st.name) name.textContent = st.name
    li.className = st?.state ?? ''
    li.classList.toggle('active', !!st?.active)
    li.querySelector('.stage-detail')!.textContent = visible(st?.detail ?? '')
    for (const b of li.querySelectorAll<HTMLButtonElement>('button.stage')) {
      const action = st?.actions.find(a => a.ask === b.dataset.ask)
      b.hidden = !action
      if (!action) continue
      if (b.textContent !== action.label) b.textContent = action.label
      b.title = action.title
    }
  }
  root.querySelector('.missing')!.replaceChildren(...(path?.missing ?? []).map(m => el('li', '', visible(m))))
  if (!card) return
  if (!path) paintCard(card, 'Waiting for the session', '', '', 0)
  else paintCard(card, path.card.step, path.card.count, visible(path.card.detail), path.card.done)
}
