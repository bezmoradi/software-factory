import { busy, button, el, say, visible } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { codeBlock } from './code-block.ts'

export interface TerminalCommands {
  logs?: string
  attach?: string
}

const ROWS = [['logs', 'Watch what it does'], ['attach', 'Take over in your terminal']] as const

function handOver(readOnly: () => Promise<string>): { line: HTMLElement; make: HTMLButtonElement } {
  const line = el('p', 'small', 'To answer its questions in your terminal instead of on this page, make it read-only here first.')
  const make = button('Make it read-only', 'ghost small', async () => {
    busy(make, true)
    const refused = await readOnly()
    busy(make, false)
    if (refused) return say(line, refused, 'bad')
    make.remove()
    say(line, 'Read-only on this page. Let this page control it, above the message box, switches back.', 'muted')
  })
  return { line, make }
}

export function openTerminal(commands: TerminalCommands, readOnly?: () => Promise<string>): void {
  const rows = ROWS.flatMap(([kind, what]) => {
    const command = commands[kind]
    return command ? [el('p', 'small', what), codeBlock({ text: visible(command), label: `The command to ${what.toLowerCase()}`, copy: { text: command, label: `Copy the command to ${what.toLowerCase()}` }, look: 'terminal', wrap: true })] : []
  })
  if (!rows.length) return
  const hand = readOnly && commands.attach ? handOver(readOnly) : null
  const m = modal({
    kind: 'terminal',
    title: 'Open in terminal',
    docs: 'open-in-terminal',
    single: 'terminal',
    focus: 'heading',
    body: [el('p', 'small', 'Run these in a terminal on this computer.'), ...rows, ...(hand ? [hand.line] : [])],
    lead: hand ? [hand.make] : [],
    actions: [button('Close', 'ghost small', () => m.close())],
  })
}
