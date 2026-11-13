import type { Prefs } from '../types.ts'

const root = document.documentElement

function rules(pattern: RegExp): { value: string; style: CSSStyleDeclaration }[] {
  const found: { value: string; style: CSSStyleDeclaration }[] = []
  for (const sheet of document.styleSheets) {
    let list: Iterable<CSSRule> = []
    try {
      list = sheet.cssRules
    } catch {
      continue
    }
    for (const rule of list as Iterable<CSSStyleRule>) {
      const m = pattern.exec(rule.selectorText || '')
      if (m) found.push({ value: m[1]!, style: rule.style })
    }
  }
  return found
}

export function themes(): { value: string; label: string; dark: boolean; default: boolean }[] {
  return rules(/^\[data-theme="([a-z0-9-]+)"\]$/).map(({ value, style }) => ({
    value,
    label: style.getPropertyValue('--label').trim().replace(/^"(.*)"$/, '$1') || value,
    dark: style.getPropertyValue('color-scheme').trim() === 'dark',
    default: style.getPropertyValue('--default').trim() !== '',
  }))
}

export function scales(): string[] {
  return rules(/^:root\[data-scale="(\d+)"\]$/).map(({ value }) => value)
}

function row(theme: { value: string; label: string; default: boolean }) {
  const label = document.createElement('label')
  label.className = 'theme'
  const input = document.createElement('input')
  input.type = 'radio'
  input.name = 'theme'
  input.value = theme.value
  const name = document.createElement('span')
  name.textContent = theme.label
  if (theme.default) {
    const tag = document.createElement('span')
    tag.className = 'label default'
    tag.textContent = '(default)'
    name.append(' ', tag)
  }
  label.append(input, name)
  return label
}

export function initPrefs(save: (prefs: Prefs) => void): void {
  const box = document.getElementById('theme')!
  const down = document.getElementById('scale-down')!
  const up = document.getElementById('scale-up')!
  const shown = document.getElementById('scale-value')!
  const reset = document.getElementById('scale-reset')!
  const sides = document.getElementById('bar-side')!
  const list = themes()
  for (const [heading, dark] of [['Dark', true], ['Light', false]] as const) {
    const group = list.filter(t => t.dark === dark)
    if (!group.length) continue
    const title = document.createElement('p')
    title.className = 'group'
    title.textContent = heading
    box.append(title, ...group.map(row))
  }
  const sizes = scales()
  const paint = () => {
    const meta = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')
    if (meta) meta.content = getComputedStyle(root).getPropertyValue('--paper').trim()
    for (const input of box.querySelectorAll('input')) input.checked = input.value === root.dataset.theme
    const at = sizes.indexOf(root.dataset.scale!)
    shown.textContent = `${root.dataset.scale}%`
    down.setAttribute('aria-disabled', String(at <= 0))
    up.setAttribute('aria-disabled', String(at === -1 || at >= sizes.length - 1))
    reset.hidden = root.dataset.scale === '100'
    for (const input of sides.querySelectorAll('input')) input.checked = input.value === root.dataset.bar
  }
  paint()
  sides.addEventListener('change', e => {
    root.dataset.bar = (e.target as HTMLInputElement).value
    paint()
    save({ bar: (e.target as HTMLInputElement).value })
  })
  box.addEventListener('change', e => {
    root.dataset.theme = (e.target as HTMLInputElement).value
    paint()
    save({ theme: (e.target as HTMLInputElement).value })
  })
  const step = (by: number) => {
    const next = sizes[sizes.indexOf(root.dataset.scale!) + by]
    if (!next) return
    root.dataset.scale = next
    paint()
    save({ scale: Number(next) })
  }
  down.addEventListener('click', () => step(-1))
  up.addEventListener('click', () => step(1))
  reset.addEventListener('click', () => {
    root.dataset.scale = '100'
    paint()
    up.focus()
    save({ scale: 100 })
  })
}
