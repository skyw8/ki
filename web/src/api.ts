import type { LoopEvent, SessionDetail, SessionInfo } from './types'

export type Boot = { token: string; cwd: string }

export function boot(): Boot {
  const raw = window.__KI__ ?? {}
  return { token: raw.token ?? '', cwd: raw.cwd ?? '' }
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

export class Client {
  constructor(readonly token: string) {}

  private headers(json = false): HeadersInit {
    const h: Record<string, string> = { Authorization: `Bearer ${this.token}` }
    if (json) h['Content-Type'] = 'application/json'
    return h
  }

  private async json<T>(path: string, init?: RequestInit): Promise<T> {
    const res = await fetch(path, {
      ...init,
      headers: { ...this.headers(init?.body != null), ...(init?.headers ?? {}) },
    })
    if (!res.ok) {
      const text = await res.text()
      throw new ApiError(res.status, text.trim() || res.statusText)
    }
    if (res.status === 204) return undefined as T
    return res.json() as Promise<T>
  }

  list(): Promise<SessionInfo[]> {
    return this.json('/v1/sessions')
  }

  get(id: string): Promise<SessionDetail> {
    return this.json(`/v1/sessions/${id}`)
  }

  create(cwd: string, model?: string): Promise<SessionInfo> {
    return this.json('/v1/sessions', { method: 'POST', body: JSON.stringify({ cwd, model }) })
  }

  prompt(id: string, text: string, model?: string): Promise<void> {
    return this.json(`/v1/sessions/${id}/prompt`, {
      method: 'POST',
      body: JSON.stringify({ text, model }),
    })
  }

  abort(id: string): Promise<void> {
    return this.json(`/v1/sessions/${id}/abort`, { method: 'POST' })
  }

  compact(id: string): Promise<void> {
    return this.json(`/v1/sessions/${id}/compact`, { method: 'POST' })
  }

  fork(id: string): Promise<SessionInfo> {
    return this.json(`/v1/sessions/${id}/fork`, { method: 'POST' })
  }

  async *events(id: string, signal?: AbortSignal): AsyncGenerator<LoopEvent> {
    const res = await fetch(`/v1/sessions/${id}/events`, {
      headers: this.headers(),
      signal,
    })
    if (!res.ok || !res.body) {
      const text = await res.text().catch(() => res.statusText)
      throw new ApiError(res.status, text.trim() || res.statusText)
    }
    const reader = res.body.getReader()
    const dec = new TextDecoder()
    let buf = ''
    let event = ''
    let data: string[] = []
    const flush = (): LoopEvent | null => {
      if (!data.length) return null
      const raw = data.join('\n')
      data = []
      const name = event
      event = ''
      try {
        const ev = JSON.parse(raw) as LoopEvent
        if (!ev.type && name) ev.type = name
        return ev
      } catch {
        return null
      }
    }
    while (true) {
      const { value, done } = await reader.read()
      buf += dec.decode(value || new Uint8Array(), { stream: !done })
      const lines = buf.split('\n')
      buf = done ? '' : (lines.pop() ?? '')
      for (const line of lines) {
        if (line.startsWith('event:')) {
          event = line.slice(6).trim()
        } else if (line.startsWith('data:')) {
          data.push(line.slice(5).trimStart())
        } else if (line === '') {
          const ev = flush()
          if (ev) yield ev
        }
      }
      if (done) {
        const ev = flush()
        if (ev) yield ev
        return
      }
    }
  }
}
