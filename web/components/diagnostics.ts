import { button, el, opener } from '../lib/dom.ts'

type Post = (path: string, body: unknown) => Promise<Response>

const ROUTE = '/api/diagnostics'

function fileName(): string {
  const d = new Date()
  const pad = (n: number) => String(n).padStart(2, '0')
  return `software-factory-diagnostics-${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}.txt`
}

function save(text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }))
  const a = el('a')
  a.href = url
  a.download = fileName()
  document.body.append(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 60_000)
}

export function openDiagnostics(post: Post): void {
  const back = opener()
  const d = el('dialog', 'consent diagnostics surface')
  d.setAttribute('aria-label', 'Diagnostics')
  const close = () => {
    d.close()
    back()
    d.remove()
  }
  const say = el('p', 'small')
  say.setAttribute('role', 'status')
  const shown = el('pre', 'diagnostics-text')
  shown.tabIndex = 0
  shown.setAttribute('aria-label', 'The whole diagnostics file')
  let text = ''
  const saveButton = button('Save as a file', 'primary small', () => {
    if (!text) return
    save(text)
    say.textContent = `Saved ${fileName()}. Nothing was sent anywhere.`
  })
  saveButton.disabled = true
  const load = async () => {
    saveButton.disabled = true
    say.textContent = 'Building the file…'
    const res = await post(ROUTE, {}).catch(() => null)
    const parsed: unknown = res ? await res.json().catch(() => null) : null
    const got = parsed !== null && typeof parsed === 'object' ? parsed as Record<string, unknown> : {}
    if (!res || !res.ok || typeof got.text !== 'string') {
      say.textContent = typeof got.message === 'string' && got.message ? got.message : 'The engine didn’t build the file.'
      return
    }
    text = got.text
    shown.textContent = text
    say.textContent = 'This is the whole file. Read it before you save it.'
    saveButton.disabled = false
  }
  const actions = el('div', 'row consent-actions')
  actions.append(saveButton, button('Build again', 'ghost small', () => void load()), el('span', 'spacer'), button('Close', 'ghost small', close))
  d.append(
    el('h2', 'label', 'Diagnostics'),
    el('p', 'small', 'A text file to attach to a bug report: versions, image digests, the operating system and Docker, setting names with their values that aren’t secret, counts, and the last 500 events as time, part, event kind, error kind and request id.'),
    el('p', 'small muted', 'It holds no repository name, host, path, address, token, prompt or log text, and the page never sends it anywhere: you save it and choose where it goes.'),
    say, shown, actions,
  )
  d.addEventListener('cancel', e => {
    e.preventDefault()
    close()
  })
  document.body.append(d)
  d.showModal()
  void load()
}
