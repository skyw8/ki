import type { FsListing, LoopEvent, ProviderCatalog, SearchHit, SessionDetail, SessionInfo, WorkspaceInfo } from './types'

export type Boot = { token: string }

export function boot(): Boot {
  const raw = window.__KI__ ?? {}
  return { token: raw.token ?? '' }
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
    const res = await fetch(path, { // same-origin relative; works behind a port-forward
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

  create(opts?: { cwd?: string; workspaceId?: string; model?: string }): Promise<SessionInfo> {
    return this.json('/v1/sessions', { method: 'POST', body: JSON.stringify(opts ?? {}) })
  }

  deleteSession(id: string): Promise<void> {
    return this.json(`/v1/sessions/${id}`, { method: 'DELETE' })
  }

  workspaces(): Promise<WorkspaceInfo[]> {
    return this.json('/v1/workspaces')
  }

  createWorkspace(path: string, title?: string): Promise<WorkspaceInfo> {
    return this.json('/v1/workspaces', { method: 'POST', body: JSON.stringify({ path, title }) })
  }

  patchWorkspace(id: string, body: { title: string }): Promise<WorkspaceInfo> {
    return this.json(`/v1/workspaces/${id}`, { method: 'PATCH', body: JSON.stringify(body) })
  }

  deleteWorkspace(id: string): Promise<void> {
    return this.json(`/v1/workspaces/${id}`, { method: 'DELETE' })
  }

  moveWorkspace(id: string, beforeId?: string | null): Promise<WorkspaceInfo[]> {
    return this.json(`/v1/workspaces/${id}/move`, { method: 'POST', body: JSON.stringify({ beforeId: beforeId ?? null }) })
  }

  moveSession(wsId: string, sessionId: string, beforeId?: string | null): Promise<WorkspaceInfo> {
    return this.json(`/v1/workspaces/${wsId}/sessions/move`, {
      method: 'POST',
      body: JSON.stringify({ sessionId, beforeId: beforeId ?? null }),
    })
  }

  listFS(path?: string, signal?: AbortSignal): Promise<FsListing> {
    const q = path ? `?path=${encodeURIComponent(path)}` : ''
    return this.json(`/v1/fs${q}`, { signal })
  }

  createFS(path: string, name: string): Promise<{ path: string }> {
    return this.json('/v1/fs', { method: 'POST', body: JSON.stringify({ path, name }) })
  }

  search(q: string, signal?: AbortSignal): Promise<{ items: SearchHit[]; hasMore: boolean }> {
    return this.json(`/v1/sessions/search?q=${encodeURIComponent(q)}`, { signal })
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

  models(): Promise<import('./types').ModelInfo[]> {
    return this.json('/v1/models')
  }

  patch(id: string, body: { model?: string; thinkingEffort?: string; title?: string; pinned?: boolean; skills?: import('./types').Toggle; mcp?: import('./types').Toggle }): Promise<SessionDetail> {
    return this.json(`/v1/sessions/${id}`, { method: 'PATCH', body: JSON.stringify(body) })
  }

  providers(): Promise<ProviderCatalog> { return this.json('/v1/providers') }
  createProvider(body: Record<string, unknown>): Promise<ProviderCatalog> {
	return this.json('/v1/providers', { method: 'POST', body: JSON.stringify(body) })
  }
  patchProvider(id: string, body: Record<string, unknown>): Promise<ProviderCatalog> {
	return this.json(`/v1/providers/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(body) })
  }
  deleteProvider(id: string): Promise<void> {
	return this.json(`/v1/providers/${encodeURIComponent(id)}`, { method: 'DELETE' })
  }
  setCredential(id: string, apiKey: string | null): Promise<ProviderCatalog> {
	return this.json(`/v1/providers/${encodeURIComponent(id)}/credential`, { method: 'PUT', body: JSON.stringify({ apiKey }) })
  }
  createModel(id: string, body: Record<string, unknown>): Promise<ProviderCatalog> {
	return this.json(`/v1/providers/${encodeURIComponent(id)}/models`, { method: 'POST', body: JSON.stringify(body) })
  }
  patchModel(id: string, body: Record<string, unknown>): Promise<ProviderCatalog> {
	return this.json(`/v1/providers/${encodeURIComponent(id)}/models`, { method: 'PATCH', body: JSON.stringify(body) })
  }
  deleteModel(id: string, model: string): Promise<void> {
	return this.json(`/v1/providers/${encodeURIComponent(id)}/models?model=${encodeURIComponent(model)}`, { method: 'DELETE' })
  }
  setDefault(provider: string, model: string): Promise<ProviderCatalog> {
	return this.json('/v1/default-model', { method: 'PUT', body: JSON.stringify({ provider, model }) })
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
