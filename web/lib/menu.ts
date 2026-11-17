export function placeMenu(anchor: HTMLElement, menu: HTMLElement) {
  const r = anchor.getBoundingClientRect()
  const w = menu.offsetWidth
  const h = menu.offsetHeight
  const top = r.bottom + 4 + h > innerHeight - 8 ? Math.max(8, r.top - 4 - h) : r.bottom + 4
  menu.style.top = `${Math.round(top)}px`
  menu.style.left = `${Math.round(Math.max(8, Math.min(r.right - w, innerWidth - w - 8)))}px`
}
