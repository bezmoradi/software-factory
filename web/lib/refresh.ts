import { focusedKey, refocus } from './dom.ts'

export const CHEAP_EVERY_MS = 5000
export const WALK_EVERY_MS = 30000
const SHOWN_AGAIN_MS = 1000

export interface WhileOpenOptions<T> {
  root: HTMLElement
  read: () => Promise<T>
  show: (answer: T) => void
  everyMs?: number
  open?: () => boolean
  hold?: () => boolean
  same?: (answer: T) => string
  near?: (key: string) => string[]
}

export interface WhileOpen {
  now(): Promise<void>
  stop(): void
  readonly live: boolean
}

function scrolled(root: HTMLElement): [Element, number][] {
  const out: [Element, number][] = []
  for (let node: Element | null = root; node; node = node.parentElement) if (node.scrollTop) out.push([node, node.scrollTop])
  return out
}

export function whileOpen<T>(o: WhileOpenOptions<T>): WhileOpen {
  let stopped = false
  let timer = 0
  let running: Promise<void> | null = null
  let again = false
  let forced = false
  let shown: string | undefined
  const fingerprint = o.same ?? ((a: T) => JSON.stringify(a, (k, v: unknown) => (k === 'now' ? undefined : v)))
  const open = () => (o.open ? o.open() : o.root.checkVisibility())

  const later = (ms: number) => {
    window.clearTimeout(timer)
    timer = stopped ? 0 : window.setTimeout(tick, ms)
  }

  const draw = (answer: T) => {
    const key = focusedKey(o.root)
    const scroll = scrolled(o.root)
    o.show(answer)
    refocus(o.root, key, o.near)
    for (const [node, top] of scroll) if (node.scrollTop !== top) node.scrollTop = top
  }

  const pass = (force: boolean): Promise<void> => {
    forced ||= force
    if (running) {
      again = true
      return running
    }
    window.clearTimeout(timer)
    timer = 0
    running = (async () => {
      do {
        again = false
        const redraw = forced
        forced = false
        const answer = await o.read()
        if (stopped) return
        if (!o.root.isConnected) return stop()
        if (o.hold?.()) {
          shown = undefined
          continue
        }
        const key = fingerprint(answer)
        if (!redraw && key === shown) continue
        shown = key
        draw(answer)
      } while (again && !stopped)
    })()
    return running.finally(() => {
      running = null
      if (!stopped && o.everyMs) later(o.everyMs)
    })
  }

  const due = { first: true }
  function tick() {
    timer = 0
    if (stopped) return
    if (!o.root.isConnected) return stop()
    if (document.hidden) return
    if (!open()) return later(SHOWN_AGAIN_MS)
    if (!o.everyMs && !due.first) return
    due.first = false
    void pass(false)
  }

  const visibility = () => {
    if (document.hidden) {
      window.clearTimeout(timer)
      timer = 0
    } else if (!running && (o.everyMs || due.first)) tick()
  }

  function stop() {
    stopped = true
    window.clearTimeout(timer)
    timer = 0
    document.removeEventListener('visibilitychange', visibility)
  }

  document.addEventListener('visibilitychange', visibility)
  tick()

  return {
    now: () => (stopped ? Promise.resolve() : pass(true)),
    stop,
    get live() {
      return !stopped
    },
  }
}
