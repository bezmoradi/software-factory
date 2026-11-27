import { UNREAD, type EngineAnswer } from '../api.ts'
import { button, el, say, statusLine } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { dropdown, option } from '../lib/controls.ts'
import { field, form } from '../lib/form.ts'

type Post = (path: string, body: unknown) => Promise<EngineAnswer>

const ROUTE = '/api/preview'

export function openPreview(post: Post, sid: string): void {
  const line = statusLine()
  const pick = dropdown({ name: 'port' })
  const picker = form({ inline: true, onSubmit: () => void show(Number(pick.value)), children: [field('Port', pick, { kind: 'inline' })] })
  picker.hidden = true
  const frame = el('iframe', 'preview-frame')
  frame.title = "The session's app"
  frame.setAttribute('sandbox', 'allow-scripts allow-same-origin allow-forms allow-popups')
  frame.referrerPolicy = 'no-referrer'
  frame.hidden = true
  const tab = el('a', 'small', 'Open in a new tab')
  tab.target = '_blank'
  tab.rel = 'noopener noreferrer'
  tab.hidden = true
  let asked = 0
  const show = async (port?: number) => {
    const n = ++asked
    say(line, 'Opening the preview…')
    const a = await post(ROUTE, port ? { sid, port } : { sid })
    if (n !== asked) return
    const got = a.body
    if (!a.ok || typeof got.url !== 'string' || typeof got.tab !== 'string' || typeof got.port !== 'number' || !Array.isArray(got.ports)) {
      say(line, a.ok ? UNREAD : a.message, 'bad')
      frame.hidden = true
      tab.hidden = true
      return
    }
    pick.replaceChildren(...got.ports.filter((p): p is number => typeof p === 'number').map(p => option(String(p), String(p), { selected: p === got.port })))
    picker.hidden = false
    frame.src = got.url
    frame.hidden = false
    tab.href = got.tab
    tab.hidden = false
    say(line, '')
  }
  pick.addEventListener('change', () => void show(Number(pick.value)))
  const m = modal({
    kind: 'preview',
    title: 'Preview',
    docs: 'preview',
    expand: true,
    body: [
      el('p', 'small', 'The app this session runs, written by the agent. It updates as the agent saves.'),
      picker, line, frame, tab,
    ],
    actions: [button('Close', 'ghost small', () => m.close())],
  })
  m.onClose(() => {
    asked++
    frame.removeAttribute('src')
  })
  void show()
}
