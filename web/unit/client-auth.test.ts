import { expect, test } from 'bun:test'
import { Client } from '../src/api/client'
import type { AuthStatus } from '../src/api/types'

function fixture(run: (requests: Array<{ path: string; headers: Headers; cache?: RequestCache }>, setStatus: (status: AuthStatus) => void) => Promise<void>) {
  const originalFetch = globalThis.fetch
  const originalDocument = Object.getOwnPropertyDescriptor(globalThis, 'document')
  const requests: Array<{ path: string; headers: Headers; cache?: RequestCache }> = []
  let status: AuthStatus = { authenticated: true, serverId: 'one', csrfCookieName: 'ki_csrf_one' }
  // Every localhost port shares this cookie string; even the legacy fixed
  // cookie must not be used in place of a server-advertised name.
  Object.defineProperty(globalThis, 'document', {
    configurable: true,
    value: {
      cookie: 'ki_csrf=legacy; ki_csrf_one_extra=wrong; ki_csrf_two=two%20token;ki_csrf_one=one%20token',
      visibilityState: 'visible',
      addEventListener() {},
      removeEventListener() {},
    },
  })
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    requests.push({ path, headers: new Headers(init?.headers), cache: init?.cache })
    if (path === '/v1/events') return new Response('event: ready\ndata: {"type":"ready","serverId":"one"}\n\n', { headers: { 'Content-Type': 'text/event-stream' } })
    return path === '/v1/auth/status'
      ? Response.json(status)
      : new Response(null, { status: 204 })
  }) as typeof fetch
  return run(requests, next => { status = next }).finally(() => {
    globalThis.fetch = originalFetch
    if (originalDocument) Object.defineProperty(globalThis, 'document', originalDocument)
    else Reflect.deleteProperty(globalThis, 'document')
  })
}

test('client learns server identity and reads only its advertised CSRF cookie', async () => {
  await fixture(async (requests, setStatus) => {
    const first = new Client()
    const second = new Client()
    expect(first.serverId).toBe('')
    await first.logout()
    expect(requests.at(-1)!.headers.has('X-Ki-CSRF')).toBe(false)
    expect(requests.at(-1)!.headers.has('X-Ki-Server-ID')).toBe(false)
    expect(await first.authStatus()).toEqual({ authenticated: true, serverId: 'one', csrfCookieName: 'ki_csrf_one' })
    expect(first.serverId).toBe('one')
    expect(requests.at(-1)!.headers.has('X-Ki-Server-ID')).toBe(false)
    await first.logout()
    expect(requests.at(-1)!.headers.get('X-Ki-CSRF')).toBe('one token')
    expect(requests.at(-1)!.headers.get('X-Ki-Server-ID')).toBe('one')
    await first.login('new-token')
    expect(requests.at(-1)!.headers.get('X-Ki-Server-ID')).toBe('one')

    setStatus({ authenticated: true, serverId: 'two', csrfCookieName: 'ki_csrf_two' })
    await second.authStatus()
    await second.logout()
    expect(requests.at(-1)!.headers.get('X-Ki-CSRF')).toBe('two token')
    expect(requests.at(-1)!.headers.get('X-Ki-Server-ID')).toBe('two')
    await first.logout()
    expect(requests.at(-1)!.headers.get('X-Ki-CSRF')).toBe('one token')
    expect(first.serverId).toBe('one')
    expect(second.serverId).toBe('two')

    await first.authStatus()
    expect(requests.at(-1)!.headers.get('X-Ki-Server-ID')).toBe('one')
    expect(first.serverId).toBe('two')
    await first.logout()
    expect(requests.at(-1)!.headers.get('X-Ki-CSRF')).toBe('two token')
  })
})

test('JSON and SSE identity-dependent reads bypass the browser cache', async () => {
  await fixture(async requests => {
    const client = new Client()
    await client.authStatus()
    expect(requests.at(-1)!.cache).toBe('no-store')
    const events = client.serverEvents()
    expect((await events.next()).value).toEqual({ type: 'ready', serverId: 'one' })
    await events.return()
    expect(requests.at(-1)!.path).toBe('/v1/events')
    expect(requests.at(-1)!.cache).toBe('no-store')
    expect(requests.at(-1)!.headers.get('X-Ki-Server-ID')).toBe('one')
  })
})

test('missing advertised cookie never falls back to another instance or a legacy cookie', async () => {
  await fixture(async (requests, setStatus) => {
    const client = new Client('bearer')
    setStatus({ authenticated: false, serverId: 'missing', csrfCookieName: 'ki_csrf_missing' })
    await client.authStatus()
    await client.logout()
    expect(requests.at(-1)!.headers.get('Authorization')).toBe('Bearer bearer')
    expect(requests.at(-1)!.headers.has('X-Ki-CSRF')).toBe(false)
    expect(requests[0].headers.has('X-Ki-CSRF')).toBe(false)
  })
})
