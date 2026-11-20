import { api, upload } from './api.ts'
import { current, say, scroller, sessions, state, toast, type Session } from './app-state.ts'
import { button, byId, el } from './lib/dom.ts'
import { complete } from './lib/complete.ts'
import { agentWord } from './lib/format.ts'
import { icon } from './components/icon.ts'

interface ComposerDeps {
  send: (text: string, images: string[], s: Session, pressed: boolean, files: string[]) => Promise<boolean>
}

interface Composer {
  swapDraft: (from: string, to: string) => void
  dropDraft: (key: string) => void
  grow: () => void
  paintJump: () => void
  jump: (to: 'start' | 'latest') => void
  paint: (s: Session | null, blocked: boolean, readOnly?: string) => void
}

const IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp']
const MAX_SIDE = 1568
const MAX_IMAGE = 2.5 * 1024 * 1024
const MAX_DROP = 10 * 1024 * 1024

interface Draft {
  text: string
  images: { blob: Blob; url: string }[]
  files: File[]
}

async function shrink(file: File): Promise<Blob | null> {
  if (file.type === 'image/gif') return file
  const bitmap = await createImageBitmap(file).catch(() => null)
  if (!bitmap) return null
  const scale = Math.min(1, MAX_SIDE / Math.max(bitmap.width, bitmap.height))
  if (scale === 1 && file.size <= MAX_IMAGE) {
    bitmap.close()
    return file
  }
  const canvas = document.createElement('canvas')
  canvas.width = Math.max(1, Math.round(bitmap.width * scale))
  canvas.height = Math.max(1, Math.round(bitmap.height * scale))
  canvas.getContext('2d')!.drawImage(bitmap, 0, 0, canvas.width, canvas.height)
  bitmap.close()
  const encode = (type: string, quality: number) => new Promise<Blob | null>(resolve => canvas.toBlob(resolve, type, quality))
  const blob = await encode(file.type === 'image/jpeg' ? 'image/jpeg' : 'image/png', 0.9)
  return blob && blob.size > MAX_IMAGE ? encode('image/jpeg', 0.85) : blob
}

async function sha256(data: ArrayBuffer): Promise<string> {
  return [...new Uint8Array(await crypto.subtle.digest('SHA-256', data))].map(b => b.toString(16).padStart(2, '0')).join('')
}

export function startComposer({ send }: ComposerDeps): Composer {
  const drafts = new Map<string, Draft>()
  const sending = new Set<string>()

  function draftOf(key: string): Draft {
    let d = drafts.get(key)
    if (!d) {
      d = { text: '', images: [], files: [] }
      drafts.set(key, d)
    }
    return d
  }

  function dropDraft(key: string) {
    for (const item of drafts.get(key)?.images ?? []) URL.revokeObjectURL(item.url)
    drafts.delete(key)
  }

  function swapDraft(from: string, to: string) {
    if (from.startsWith('s/')) {
      const d = draftOf(from)
      d.text = text.value
      if (!d.text && !d.images.length && !d.files.length) drafts.delete(from)
    }
    text.value = drafts.get(to)?.text ?? ''
    paintAttached()
  }

  function paintAttached() {
    const attached = drafts.get(current.key)?.images ?? []
    const files = drafts.get(current.key)?.files ?? []
    const box = byId('attachments')
    box.replaceChildren()
    for (const file of files) {
      const chip = el('div', 'file-chip')
      chip.title = `${agentWord(current()?.info.agent)} gets a read-only copy of ${file.name}, deleted when the session ends. Changes don't reach your file.`
      const remove = button('×', 'ghost', () => {
        files.splice(files.indexOf(file), 1)
        paintAttached()
        text.focus()
      })
      remove.setAttribute('aria-label', `Remove ${file.name}`)
      chip.append(icon('file'), el('span', 'file-chip-name', file.name), el('span', 'file-chip-note', 'copy'), remove)
      box.append(chip)
    }
    attached.forEach((item, i) => {
      const figure = el('figure', 'thumb')
      const img = el('img')
      img.src = item.url
      img.alt = `Pasted image ${i + 1}`
      const remove = button('×', 'ghost', () => {
        URL.revokeObjectURL(item.url)
        attached.splice(attached.indexOf(item), 1)
        paintAttached()
        text.focus()
      })
      remove.setAttribute('aria-label', `Remove image ${i + 1}`)
      figure.append(img, remove)
      box.append(figure)
    })
    box.hidden = attached.length === 0 && files.length === 0
  }

  function mention(key: string, path: string) {
    const token = `@${path} `
    if (key !== current.key) {
      const d = draftOf(key)
      d.text = `${d.text}${d.text && !/\s$/.test(d.text) ? ' ' : ''}${token}`
      return
    }
    const start = text.selectionStart
    const before = text.value.slice(0, start)
    text.setRangeText(`${before && !/\s$/.test(before) ? ' ' : ''}${token}`, start, text.selectionEnd, 'end')
    grow()
  }

  async function attachFile(file: File, key: string, sid: string) {
    if (file.size > MAX_DROP) return say(`${file.name} is larger than 10 MB. Type its path with @ instead.`)
    const data = await file.arrayBuffer().catch(() => null)
    if (!data) return say(`The page couldn't read ${file.name}. For a folder, type its path with @ instead.`)
    const res = await api('/api/file-match', { sid, name: file.name, size: file.size, sha256: await sha256(data) }).catch(() => null)
    const found: { path?: unknown } | null = res && res.ok ? await res.json().catch(() => null) : null
    if (typeof found?.path === 'string' && found.path) return mention(key, found.path)
    const d = draftOf(key)
    if (d.files.length >= 4) return say('You can attach up to 4 files to a message.')
    d.files.push(file)
    toast({ id: `copy:${file.name}`, title: `${file.name} isn’t in this project`, description: `${agentWord(sessions.get(sid)?.info.agent)} gets a read-only copy, deleted when the session ends. Changes don’t reach your file.`, tone: 'info', timeout: 8000 })
  }

  async function attach(files: File[]) {
    const key = current.key
    const attached = draftOf(key).images
    const sid = current()?.sid
    for (const file of files) {
      if (!IMAGE_TYPES.includes(file.type)) {
        if (sid) await attachFile(file, key, sid)
        continue
      }
      if (attached.length >= 4) {
        say('You can attach up to 4 images to a message.')
        break
      }
      const blob = await shrink(file)
      if (!blob || blob.size > MAX_IMAGE || !IMAGE_TYPES.includes(blob.type)) {
        say('That image is too large to send, or not one the page can read.')
        continue
      }
      if (drafts.get(key)?.images !== attached) break
      attached.push({ blob, url: URL.createObjectURL(blob) })
    }
    if (key === current.key) {
      paintAttached()
      text.focus()
    }
  }

  let jumping: ReturnType<typeof setTimeout> | null = null
  const nearEdge = { start: true, end: true }

  function watchEdge(edge: 'start' | 'end') {
    const mark = el('div', 'scroll-edge')
    mark.setAttribute('aria-hidden', 'true')
    if (edge === 'start') scroller.prepend(mark)
    else scroller.append(mark)
    new IntersectionObserver(entries => {
      const last = entries[entries.length - 1]
      if (!last || nearEdge[edge] === last.isIntersecting) return
      nearEdge[edge] = last.isIntersecting
      paintJump()
    }, { root: scroller, rootMargin: edge === 'start' ? '100% 0px 0px 0px' : '0px 0px 100% 0px' }).observe(mark)
  }
  watchEdge('start')
  watchEdge('end')

  function paintJump() {
    const live = current.key.startsWith('s/') && !jumping
    byId('jump-start').hidden = !live || nearEdge.start
    byId('jump-latest').hidden = !live || nearEdge.end
  }

  function landed() {
    if (jumping) clearTimeout(jumping)
    jumping = null
    paintJump()
  }

  function jump(to: 'start' | 'latest') {
    if (jumping) clearTimeout(jumping)
    jumping = setTimeout(landed, 2000)
    paintJump()
    scroller.scrollTo({ top: to === 'start' ? 0 : scroller.scrollHeight, behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
  }

  scroller.addEventListener('scrollend', landed)
  const strip = byId('jump').parentElement!
  const coverEnd = () => {
    const over = getComputedStyle(strip).position === 'absolute' && strip.offsetHeight ? Math.max(0, Math.ceil(scroller.getBoundingClientRect().bottom - strip.getBoundingClientRect().top)) : 0
    const pad = over ? `${over}px` : ''
    const list = byId('conversations')
    if (list.style.paddingBottom === pad) return
    list.style.paddingBottom = pad
    if (current()?.view.pinned) scroller.scrollTop = scroller.scrollHeight
  }
  const covered = new ResizeObserver(coverEnd)
  covered.observe(strip)
  covered.observe(strip.parentElement!)
  covered.observe(scroller)
  byId('jump-start').addEventListener('click', () => jump('start'))
  byId('jump-latest').addEventListener('click', () => jump('latest'))
  document.addEventListener('keydown', e => {
    if ((e.key !== 'Home' && e.key !== 'End') || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey || !current.key.startsWith('s/')) return
    if (e.defaultPrevented || (e.target instanceof HTMLElement && e.target.closest('input, textarea, select, [contenteditable], [popover], [role="tablist"], dialog'))) return
    if (e.target instanceof HTMLElement && e.target !== document.body && e.target.scrollHeight > e.target.clientHeight + 1) return
    e.preventDefault()
    jump(e.key === 'Home' ? 'start' : 'latest')
  })

  const text = byId<HTMLTextAreaElement>('text')
  byId<HTMLElementTagNameMap['sf-saved-prompts']>('saved-prompts').draft = () => text.value
  byId('saved-prompts').addEventListener('sf-insert-prompt', e => {
    if (text.disabled) return say('Answer the card above first, then insert the prompt.')
    text.setRangeText(e.detail.text, text.selectionStart, text.selectionEnd, 'end')
    text.dispatchEvent(new Event('input', { bubbles: true }))
    text.focus()
  })
  complete(text, byId('complete'), {
    catalog: () => current()?.catalog ?? null,
    files: async q => {
      const s = current()
      if (!s) return { files: [], cut: false }
      const res = await api('/api/files', { sid: s.sid, q })
      const body: { files?: unknown; cut?: unknown } | null = res.ok ? await res.json() : null
      const files = Array.isArray(body?.files) ? (body.files as unknown[]).filter((f): f is string => typeof f === 'string') : []
      return { files, cut: body?.cut === true }
    },
  })
  const grow = () => {
    if (!text.offsetParent) return
    text.style.height = 'auto'
    text.style.height = `${text.scrollHeight + text.offsetHeight - text.clientHeight}px`
  }
  text.addEventListener('input', grow)
  text.addEventListener('paste', e => {
    const files = [...(e.clipboardData?.items || [])].filter(i => i.kind === 'file').map(i => i.getAsFile()).filter((f): f is File => !!f)
    if (!files.length) return
    e.preventDefault()
    void attach(files)
  })
  document.addEventListener('dragover', e => {
    if (![...(e.dataTransfer?.items || [])].some(i => i.kind === 'file')) return
    e.preventDefault()
    if (e.dataTransfer) e.dataTransfer.dropEffect = current() ? 'copy' : 'none'
  })
  document.addEventListener('drop', e => {
    const all = [...(e.dataTransfer?.files || [])]
    if (!all.length) return
    e.preventDefault()
    if (!current() || text.disabled) return
    const items = [...(e.dataTransfer?.items || [])].filter(i => i.kind === 'file')
    const folders = items.filter(i => i.webkitGetAsEntry()?.isDirectory).length
    const files = items.length ? items.filter(i => !i.webkitGetAsEntry()?.isDirectory).map(i => i.getAsFile()).filter((f): f is File => !!f) : all
    if (files.length) void attach(files)
    if (folders) say('A folder can’t be dropped here. Type its path with @ instead.')
  })
  text.addEventListener('keydown', e => {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      e.preventDefault()
      byId<HTMLFormElement>('composer').requestSubmit()
    }
  })
  byId('composer').addEventListener('submit', async e => {
    e.preventDefault()
    const s = current()
    const key = current.key
    const value = text.value.trim()
    const attached = [...(drafts.get(key)?.images ?? [])]
    const files = [...(drafts.get(key)?.files ?? [])]
    if (text.disabled) return
    if (!s || (!value && !attached.length && !files.length) || !state.connected || sending.has(key)) return
    sending.add(key)
    const sent = async (blob: Blob, name = '') => {
      const res = await upload(blob, s.sid, name).catch(() => null)
      const body: { id?: unknown } | null = res && res.ok ? await res.json().catch(() => null) : null
      return body && typeof body.id === 'string' ? body.id : ''
    }
    const ids: string[] = []
    for (const item of attached) {
      const id = await sent(item.blob)
      if (!id) break
      ids.push(id)
    }
    const fids: string[] = []
    if (ids.length === attached.length) {
      for (const file of files) {
        const id = await sent(file, file.name)
        if (!id) break
        fids.push(id)
      }
    }
    if (ids.length !== attached.length) say('An image did not go through. Try again, or check that the session is still running.')
    else if (fids.length !== files.length) say('A file did not go through. Try again, or check that the session is still running.')
    else if (await send(value, ids, s, false, fids)) {
      const d = drafts.get(key)
      if (d) {
        for (const item of attached) {
          URL.revokeObjectURL(item.url)
          if (d.images.includes(item)) d.images.splice(d.images.indexOf(item), 1)
        }
        for (const file of files) if (d.files.includes(file)) d.files.splice(d.files.indexOf(file), 1)
        if (d.text.trim() === value) d.text = ''
        if (!d.text && !d.images.length && !d.files.length) drafts.delete(key)
      }
      if (key === current.key) {
        if (text.value.trim() === value) text.value = ''
        grow()
        paintAttached()
      }
    }
    sending.delete(key)
    if (key === current.key) text.focus()
  })

  function paint(s: Session | null, blocked: boolean, readOnly = '') {
    byId<HTMLElementTagNameMap['sf-saved-prompts']>('saved-prompts').view = s?.info.project && s.info.verified && !readOnly ? { repo: s.info.project, disabled: !state.connected } : null
    const off = blocked || !!readOnly
    if (text.disabled !== off) text.disabled = off
    const placeholder = readOnly || (blocked ? 'Answer the card above to keep going' : 'Type a message and press Enter')
    if (text.placeholder !== placeholder) text.placeholder = placeholder
  }

  return { swapDraft, dropDraft, grow, paintJump, jump, paint }
}
