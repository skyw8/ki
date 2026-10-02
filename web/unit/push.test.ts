import { expect, test } from 'bun:test'
import type { Client } from '../src/api/client'
import { ensurePushSubscription, urlBase64ToUint8Array } from '../src/lib/push'

const publicKey = 'AQID'
const currentKey = new Uint8Array([1, 2, 3])

function fixture(options: {
  previousKey?: number[] | null
  noSubscription?: boolean
  unsubscribe?: boolean
  deleteFails?: boolean
  subscribeFails?: boolean
  enabled?: boolean
} = {}) {
  const calls: string[] = []
  const posted: unknown[] = []
  const subscribed: PushSubscriptionOptionsInit[] = []
  const previous = {
    endpoint: 'https://push.example/previous',
    options: { applicationServerKey: options.previousKey === null ? null : new Uint8Array(options.previousKey ?? currentKey).buffer },
    unsubscribe: async () => { calls.push('unsubscribe'); return options.unsubscribe ?? true },
    toJSON: () => ({ endpoint: 'https://push.example/previous', keys: { p256dh: 'old-p256dh', auth: 'old-auth' } }),
  }
  const replacement = {
    toJSON: () => ({ endpoint: 'https://push.example/current', keys: { p256dh: 'new-p256dh', auth: 'new-auth' } }),
  }
  const registration = {
    pushManager: {
      getSubscription: async () => { calls.push('get'); return options.noSubscription ? null : previous },
      subscribe: async (request: PushSubscriptionOptionsInit) => {
        calls.push('subscribe')
        subscribed.push(request)
        if (options.subscribeFails) throw new Error('push service refused')
        return replacement
      },
    },
  }
  const api = {
    pushConfig: async () => { calls.push('config'); return { enabled: options.enabled ?? true, publicKey } },
    deletePushSubscription: async (endpoint: string) => {
      calls.push(`delete:${endpoint}`)
      if (options.deleteFails) throw new Error('server offline')
    },
    putPushSubscription: async (value: unknown) => { calls.push('put'); posted.push(value) },
  } as unknown as Client
  return {
    api, calls, posted, subscribed,
    navigator: {
      serviceWorker: {
        register: async (url: string) => { calls.push(`register:${url}`); return registration },
        ready: Promise.resolve(registration),
      },
    },
  }
}

async function withPushBrowser(f: ReturnType<typeof fixture>, run: () => Promise<void>) {
  const keys = ['navigator', 'window'] as const
  const descriptors = keys.map(key => Object.getOwnPropertyDescriptor(globalThis, key))
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: f.navigator })
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { PushManager: {} } })
  try {
    await run()
  } finally {
    keys.forEach((key, index) => {
      const descriptor = descriptors[index]
      if (descriptor) Object.defineProperty(globalThis, key, descriptor)
      else Reflect.deleteProperty(globalThis, key)
    })
  }
}

test('VAPID base64url conversion preserves key bytes', () => {
  expect(urlBase64ToUint8Array(publicKey)).toEqual(currentKey)
  expect(urlBase64ToUint8Array('-_8')).toEqual(new Uint8Array([251, 255]))
})

test('matching VAPID key reuses and re-POSTs the existing subscription', async () => {
  const f = fixture()
  await withPushBrowser(f, async () => {
    expect(await ensurePushSubscription(f.api)).toBe(true)
    expect(f.calls).toEqual(['config', 'register:/sw.js', 'get', 'put'])
    expect(f.posted).toEqual([{ endpoint: 'https://push.example/previous', keys: { p256dh: 'old-p256dh', auth: 'old-auth' } }])
    expect(f.subscribed).toHaveLength(0)
  })
})

test('changed, missing, or differently sized VAPID key renews before posting', async () => {
  for (const previousKey of [[9, 2, 3], [1, 2, 9], [1, 2], [1, 2, 3, 4], null]) {
    const f = fixture({ previousKey })
    await withPushBrowser(f, async () => {
      expect(await ensurePushSubscription(f.api)).toBe(true)
      expect(f.calls).toEqual(['config', 'register:/sw.js', 'get', 'unsubscribe', 'delete:https://push.example/previous', 'subscribe', 'put'])
      expect(f.subscribed).toEqual([{ userVisibleOnly: true, applicationServerKey: currentKey }])
      expect(f.posted).toEqual([{ endpoint: 'https://push.example/current', keys: { p256dh: 'new-p256dh', auth: 'new-auth' } }])
    })
  }
})

test('no existing subscription subscribes using the current server key', async () => {
  const f = fixture({ noSubscription: true })
  await withPushBrowser(f, async () => {
    expect(await ensurePushSubscription(f.api)).toBe(true)
    expect(f.calls).toEqual(['config', 'register:/sw.js', 'get', 'subscribe', 'put'])
    expect(f.subscribed).toEqual([{ userVisibleOnly: true, applicationServerKey: currentKey }])
    expect(f.posted).toEqual([{ endpoint: 'https://push.example/current', keys: { p256dh: 'new-p256dh', auth: 'new-auth' } }])
  })
})

test('unsuccessful unsubscribe never reports the old key as ready', async () => {
  const f = fixture({ previousKey: [4, 5, 6], unsubscribe: false })
  await withPushBrowser(f, async () => {
    expect(await ensurePushSubscription(f.api)).toBe(false)
    expect(f.calls).toEqual(['config', 'register:/sw.js', 'get', 'unsubscribe'])
    expect(f.posted).toHaveLength(0)
    expect(f.subscribed).toHaveLength(0)
  })
})

test('old subscription server cleanup is best-effort after browser unsubscribe', async () => {
  const f = fixture({ previousKey: [4, 5, 6], deleteFails: true })
  await withPushBrowser(f, async () => {
    expect(await ensurePushSubscription(f.api)).toBe(true)
    expect(f.calls).toEqual(['config', 'register:/sw.js', 'get', 'unsubscribe', 'delete:https://push.example/previous', 'subscribe', 'put'])
    expect(f.posted).toEqual([{ endpoint: 'https://push.example/current', keys: { p256dh: 'new-p256dh', auth: 'new-auth' } }])
  })
})

test('replacement subscription failure never uploads stale subscription', async () => {
  const f = fixture({ previousKey: [4, 5, 6], subscribeFails: true })
  await withPushBrowser(f, async () => {
    expect(await ensurePushSubscription(f.api)).toBe(false)
    expect(f.calls).toEqual(['config', 'register:/sw.js', 'get', 'unsubscribe', 'delete:https://push.example/previous', 'subscribe'])
    expect(f.posted).toHaveLength(0)
  })
})

test('disabled push leaves an existing browser subscription untouched', async () => {
  const f = fixture({ enabled: false, previousKey: [4, 5, 6] })
  await withPushBrowser(f, async () => {
    expect(await ensurePushSubscription(f.api)).toBe(false)
    expect(f.calls).toEqual(['config'])
    expect(f.posted).toHaveLength(0)
  })
})
