import { request, UNREAD, type EngineAnswer } from '../api.ts'
import { button, statusLine, visible } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { whileOpen } from '../lib/refresh.ts'
import { codeBlock } from './code-block.ts'

export function openCommandOutput(sid: string, kind: string, title: string): void {
  const label = `${title} output`
  const outer = document.activeElement instanceof Element ? document.activeElement.closest('dialog') : null
  const say = statusLine()
  const shown = codeBlock({ text: '', label })
  shown.hidden = true
  const show = (a: EngineAnswer) => {
    const got = a.body
    if (!a.ok || typeof got.text !== 'string') {
      say.textContent = a.ok ? UNREAD : a.message
      return
    }
    const text = visible(got.text)
    shown.show({ text, label, copy: { text, label: 'Copy the output' }, keep: 'end', wrap: true, size: 'small' })
    shown.hidden = false
    say.textContent = typeof got.lead === 'string' ? visible(got.lead) : ''
  }
  const m = modal({
    kind: 'command-output',
    title: label,
    docs: 'check-and-setup-commands',
    body: [say, shown],
    actions: [button('Close', 'ghost small', () => m.close())],
  })
  m.dialog.addEventListener('click', e => {
    if (e.target instanceof Element && e.target.closest('button[data-doc]') && outer?.open) outer.close()
  })
  say.textContent = 'Reading the output from the session\'s container…'
  const read = whileOpen({ root: m.dialog, read: () => request('/api/session-work', { op: 'output', sid, kind }), show })
  m.onClose(() => read.stop())
}
