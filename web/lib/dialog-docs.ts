import { docsId } from './page.ts'

export const GATEWAY_HEADING = 'Company LLM gateway'

export const DIALOGS = {
  egress: { title: 'Network access', inside: [GATEWAY_HEADING] },
  credentials: { title: 'Credentials', inside: [] },
  workspace: { title: 'Workspace', inside: [] },
  exceptions: { title: 'Register of exceptions', inside: [] },
  backup: { title: 'Export and import', inside: [] },
  diagnostics: { title: 'Diagnostics', inside: [] },
  identity: { title: 'Git name and email', inside: [] },
  signIn: { title: 'Sign Claude Code in for the reviewers', inside: [] },
  shortcuts: { title: 'Keyboard shortcuts', inside: [] },
} as const satisfies Record<string, { title: string; inside: readonly string[] }>

export type DialogName = keyof typeof DIALOGS

const NAMES = Object.keys(DIALOGS) as DialogName[]

export function isDialog(name: string): name is DialogName {
  return Object.hasOwn(DIALOGS, name)
}

export function dialogDocs(name: DialogName): { title: string; docs: string } {
  const title = DIALOGS[name].title
  return { title, docs: docsId(title) }
}

export function dialogOpenedFrom(section: string): DialogName | undefined {
  return NAMES.find(name => [DIALOGS[name].title, ...DIALOGS[name].inside].some(title => docsId(title) === section))
}
