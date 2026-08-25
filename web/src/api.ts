import type { FsListing, LoopEvent, Meta, ProviderCatalog, SearchHit, SessionDetail, SessionInfo, WorkspaceInfo } from './types'

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

  create(opts?: { cwd?: string; workspaceId?: string; model?: string; thinkingEffort?: string }): Promise<SessionInfo> {
    return this.json('/v1/sessions', { method: 'POST', body: JSON.stringify(opts ?? {}) })
  }

  meta(): Promise<Meta> {
    return this.json('/v1/meta')
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

  listFS(path?: string, signal?: AbortSignal, files = false): Promise<FsListing> {
	const p = new URLSearchParams()
	if (path) p.set('path', path)
	if (files) p.set('files', '1')
	const q = p.size ? `?${p}` : ''
    return this.json(`/v1/fs${q}`, { signal })
  }

  async previewFS(path: string, signal?: AbortSignal): Promise<Blob> {
	const p = new URLSearchParams({ path, preview: '1' })
	const res = await fetch(`/v1/fs?${p}`, { headers: this.headers(), signal })
	if (!res.ok) throw new ApiError(res.status, (await res.text()).trim() || res.statusText)
	return res.blob()
  }

  createFS(path: string, name: string): Promise<{ path: string }> {
    return this.json('/v1/fs', { method: 'POST', body: JSON.stringify({ path, name }) })
  }

  search(q: string, signal?: AbortSignal): Promise<{ items: SearchHit[]; hasMore: boolean }> {
    return this.json(`/v1/sessions/search?q=${encodeURIComponent(q)}`, { signal })
  }

  prompt(id: string, content: import('./types').Content[], model?: string, parentId?: string, delivery?: 'steer' | 'queue', queueId?: string): Promise<{ handled?: boolean; notice?: string; error?: boolean; accepted?: boolean | string }> {
    return this.json(`/v1/sessions/${id}/prompt`, {
      method: 'POST',
	  body: JSON.stringify({
		...(queueId ? { queueId } : { content }),
		model,
		...(parentId !== undefined ? { parentId } : {}),
		...(delivery ? { delivery } : {}),
	  }),
    })
  }

  message(): Promise<{ busy: 'steer' | 'queue' }> {
    return this.json('/v1/message')
  }

  patchMessage(busy: 'steer' | 'queue'): Promise<{ busy: 'steer' | 'queue' }> {
    return this.json('/v1/message', { method: 'PATCH', body: JSON.stringify({ busy }) })
  }

  reload(sessionId?: string): Promise<{ ok: boolean; queued?: boolean }> {
	return this.json('/v1/reload', { method: 'POST', body: sessionId ? JSON.stringify({ sessionId }) : undefined })
  }

  async skills(workspaceId?: string | null): Promise<import('./types').CatalogSkill[]> {
    const q = workspaceId ? `?workspaceId=${encodeURIComponent(workspaceId)}` : ''
    const got = await this.json<{ items: import('./types').CatalogSkill[] }>(`/v1/skills${q}`)
    return got.items ?? []
  }

  async mcpServers(workspaceId?: string | null): Promise<import('./types').CatalogMcp[]> {
    const q = workspaceId ? `?workspaceId=${encodeURIComponent(workspaceId)}` : ''
    const got = await this.json<{ items: import('./types').CatalogMcp[] }>(`/v1/mcp${q}`)
    return got.items ?? []
  }

  patchSkills(disabled: string[]): Promise<{ items: import('./types').CatalogSkill[] }> {
    return this.json('/v1/skills', { method: 'PATCH', body: JSON.stringify({ disabled }) })
  }

  patchMcp(disabled: string[]): Promise<{ items: import('./types').CatalogMcp[] }> {
    return this.json('/v1/mcp', { method: 'PATCH', body: JSON.stringify({ disabled }) })
  }

  async extensions(workspaceId?: string | null): Promise<import('./types').CatalogExtension[]> {
    const q = workspaceId ? `?workspaceId=${encodeURIComponent(workspaceId)}` : ''
    const got = await this.json<{ items: import('./types').CatalogExtension[] }>(`/v1/extensions${q}`)
    return got.items ?? []
  }

  patchExtensions(disabled: string[]): Promise<{ items: import('./types').CatalogExtension[] }> {
    return this.json('/v1/extensions', { method: 'PATCH', body: JSON.stringify({ disabled }) })
  }

  abort(id: string): Promise<void> {
    return this.json(`/v1/sessions/${id}/abort`, { method: 'POST' })
  }

  extensionUI(id: string, body: { kind: string; extension: string; ok?: boolean; value?: string; fields?: Record<string, unknown> }): Promise<{ ok: boolean }> {
    return this.json(`/v1/sessions/${id}/extension-ui`, { method: 'POST', body: JSON.stringify(body) })
  }

  compact(id: string): Promise<void> {
    return this.json(`/v1/sessions/${id}/compact`, { method: 'POST' })
  }

  fork(id: string, entryId: string, forkMode: 'flat' | 'tree' = 'flat'): Promise<SessionInfo> {
	return this.json(`/v1/sessions/${id}/fork`, { method: 'POST', body: JSON.stringify({ entryId, forkMode }) })
  }

  async uploadAttachment(id: string, file: File): Promise<import('./types').Content> {
	const body = new FormData()
	body.append('file', file, file.name)
	const res = await fetch(`/v1/sessions/${id}/attachments`, { method: 'POST', headers: this.headers(), body })
	if (!res.ok) throw new ApiError(res.status, (await res.text()).trim() || res.statusText)
	return res.json() as Promise<import('./types').Content>
  }

  models(): Promise<import('./types').ModelInfo[]> {
    return this.json('/v1/models')
  }

  patch(id: string, body: { model?: string; thinkingEffort?: string; title?: string; pinned?: boolean; leafId?: string; queued?: string[] }): Promise<SessionDetail> {
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

  async *events(id: string, signal?: AbortSignal, notifications = false): AsyncGenerator<LoopEvent> {
    const res = await fetch(`/v1/sessions/${id}/events${notifications ? '?notifications=1' : ''}`, {
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
