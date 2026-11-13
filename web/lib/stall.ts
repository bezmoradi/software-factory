export const STALL_MS = 10 * 60 * 1000

export interface StallFacts {
  inTurn: boolean
  waiting: boolean
  subagents: boolean
  lastAt: number
  dismissedAt: number
}

export function stalled(f: StallFacts, now: number): boolean {
  return f.inTurn && !f.waiting && !f.subagents && f.lastAt > 0 && now - f.lastAt >= STALL_MS && f.dismissedAt !== f.lastAt
}
