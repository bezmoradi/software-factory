export function isRecord(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v)
}

export interface Said {
  label: string
  tone: string
}

export function saidOf(v: unknown, fallback = ''): Said {
  return isRecord(v) && typeof v.label === 'string' ? { label: v.label, tone: typeof v.tone === 'string' ? v.tone : '' } : { label: fallback, tone: '' }
}

export function tagClass(s: Said): string {
  return s.tone ? `tag ${s.tone}` : 'tag'
}
