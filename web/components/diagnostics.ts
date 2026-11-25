import { UNREAD, type EngineAnswer } from '../api.ts'
import { button, el, statusLine } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { dialogDocs } from '../lib/dialog-docs.ts'
import { saveFile } from '../lib/download.ts'
import { whileOpen } from '../lib/refresh.ts'
import './agent-report.ts'
import { codeBlock } from './code-block.ts'

type Post = (path: string, body: unknown) => Promise<EngineAnswer>

const ROUTE = '/api/diagnostics'

function fileName(): string {
  const d = new Date()
  const pad = (n: number) => String(n).padStart(2, '0')
  return `software-factory-diagnostics-${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}.txt`
}

export function openDiagnostics(post: Post): void {
  const say = statusLine()
  const shown = codeBlock({ text: '', label: 'The whole diagnostics file' })
  shown.hidden = true
  let text = ''
  const saveButton = button('Save as a file', 'primary small', () => {
    if (!text) return
    saveFile(new Blob([text], { type: 'text/plain;charset=utf-8' }), fileName())
    say.textContent = `Saved ${fileName()}. Nothing was sent anywhere.`
  })
  saveButton.disabled = true
  const show = (a: EngineAnswer) => {
    const got = a.body
    if (!a.ok || typeof got.text !== 'string') {
      say.textContent = a.ok ? UNREAD : a.message
      return
    }
    text = got.text
    shown.show({ text, label: 'The whole diagnostics file', copy: { text, label: 'Copy the diagnostics file' }, wrap: true })
    shown.hidden = false
    say.textContent = 'This is the whole file. Read it before you save it.'
    saveButton.disabled = false
  }
  const fileRow = el('div', 'row text-row')
  fileRow.append(say, el('span', 'spacer'), saveButton)
  const m = modal({
    kind: 'diagnostics',
    ...dialogDocs('diagnostics'),
    body: [
      el('p', 'small', 'A text file to attach to a bug report. It holds no names, hosts, paths, tokens or prompts, and nothing is sent until you save it and share it yourself.'),
      document.createElement('sf-agent-report'),
      el('h3', 'label', 'Diagnostics file'),
      fileRow, shown,
    ],
    actions: [button('Close', 'ghost small', () => m.close())],
  })
  say.textContent = 'Building the file…'
  const build = whileOpen({ root: m.dialog, read: () => post(ROUTE, {}), show })
  m.onClose(() => build.stop())
}
