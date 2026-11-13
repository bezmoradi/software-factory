declare global {
  interface AbortSignal {
    readonly aborted: boolean
  }

  class TextEncoder {
    encode(input?: string): Uint8Array
  }

  const crypto: {
    getRandomValues<T extends Uint8Array>(array: T): T
  }
}

export {}
