import { button } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import type { SettingsSection } from '../types.ts'

export interface SettingsParts {
  about: HTMLElement
  work: HTMLElementTagNameMap['sf-session-work']
  keep: HTMLElementTagNameMap['sf-keep']
}

export function openSessionSettings(parts: SettingsParts, sid: string, section: SettingsSection | '' = ''): void {
  void parts.work.refresh(sid)
  void parts.keep.refresh(sid)
  const m = modal({ kind: '', title: 'Session settings', docs: 'session-settings', single: 'session-settings', focus: 'heading', body: [parts.about, parts.work, parts.keep], actions: [button('Close', 'ghost small', () => m.close())] })
  if (!section || (section === 'model' && parts.work.focusModel())) return
  const head = m.dialog.querySelector<HTMLElement>(`[data-section="${section}"]`)
  if (!head) return
  head.tabIndex = -1
  head.focus({ preventScroll: true })
  m.dialog.scrollTop += head.getBoundingClientRect().top - m.dialog.getBoundingClientRect().top - head.offsetHeight
}
