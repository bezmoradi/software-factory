import { UNREAD, type EngineAnswer } from '../api.ts'
import { actionButton, button, el, statusLine } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { saveFile } from '../lib/download.ts'
import './agent-report.ts'

type Post = (path: string, body: unknown) => Promise<EngineAnswer>

const ROUTE = '/api/diagnostics'

function fileName(): string {
  const d = new Date()
  const pad = (n: number) => String(n).padStart(2, '0')
  return `software-factory-diagnostics-${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}.txt`
}

export function openDiagnostics(post: Post): void {
  let generation = 0
  const say = statusLine()
  const shown = el('pre', 'diagnostics-text')
  shown.tabIndex = 0
  shown.setAttribute('aria-label', 'The whole diagnostics file')
  let text = ''
  const saveButton = button('Save as a file', 'primary small', () => {
    if (!text) return
    saveFile(new Blob([text], { type: 'text/plain;charset=utf-8' }), fileName())
    say.textContent = `Saved ${fileName()}. Nothing was sent anywhere.`
  })
  saveButton.disabled = true
  const load = async () => {
    const mine = ++generation
    saveButton.disabled = true
    say.textContent = 'Building the file…'
    const a = await post(ROUTE, {})
    if (mine !== generation) return
    const got = a.body
    if (!a.ok || typeof got.text !== 'string') {
      say.textContent = a.ok ? UNREAD : a.message
      return
    }
    text = got.text
    shown.textContent = text
    say.textContent = 'This is the whole file. Read it before you save it.'
    saveButton.disabled = false
  }
  const m = modal({
    kind: 'diagnostics',
    title: 'Diagnostics',
    docs: 'maintenance',
    body: [
      el('p', 'small', 'A text file to attach to a bug report: versions, image digests, the operating system and Docker, setting names with their values that aren’t secret, counts, and the last 500 events as time, part, event kind, error kind and request id.'),
      el('p', 'small muted', 'It holds no repository name, host, path, address, token, prompt or log text, and the page never sends it anywhere: you save it and choose where it goes.'),
      document.createElement('sf-agent-report'),
      say, shown,
    ],
    lead: [saveButton, actionButton('Build again', 'ghost small', load)],
    actions: [button('Close', 'ghost small', () => m.close())],
  })
  m.onClose(() => {
    generation++
  })
  void load()
}
