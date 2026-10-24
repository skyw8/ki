import { expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import type { Client } from '../src/api/client'
import { ensurePushSubscription, urlBase64ToUint8Array } from '../src/lib/push'
import { notifyQuestionRequested } from '../src/lib/notifications'

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

async function withNotificationBrowser(useServiceWorker: boolean, run: (browser: {
  shown: Array<{ title: string; options: NotificationOptions }>
  permission: (value: NotificationPermission) => void
}) => Promise<void>) {
  const keys = ['navigator', 'window', 'Notification'] as const
  const descriptors = keys.map(key => Object.getOwnPropertyDescriptor(globalThis, key))
  const shown: Array<{ title: string; options: NotificationOptions }> = []
  class StubNotification {
    static permission: NotificationPermission = 'granted'
    onclick: (() => void) | null = null
    constructor(title: string, options: NotificationOptions) { shown.push({ title, options }) }
    close() {}
  }
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { isSecureContext: true, focus() {} } })
  Object.defineProperty(globalThis, 'Notification', { configurable: true, value: StubNotification })
  Object.defineProperty(globalThis, 'navigator', {
    configurable: true,
    value: useServiceWorker ? { serviceWorker: { getRegistration: async () => ({ showNotification: async (title: string, options: NotificationOptions) => { shown.push({ title, options }) } }) } } : {},
  })
  try {
    await run({ shown, permission: value => { StubNotification.permission = value } })
  } finally {
    keys.forEach((key, index) => {
      const descriptor = descriptors[index]
      if (descriptor) Object.defineProperty(globalThis, key, descriptor)
      else Reflect.deleteProperty(globalThis, key)
    })
  }
}

for (const useServiceWorker of [true, false]) test(`question notification delivery preserves identity with serviceWorker=${useServiceWorker}`, async () => {
  await withNotificationBrowser(useServiceWorker, async ({ shown, permission }) => {
    const base = { enabled: true, sessionId: 'main', focusedSession: 'other', subagent: false, questionItemId: 'question-1', title: 'Release plan', body: 'Question needs your answer: Deploy now?' }
    expect(await notifyQuestionRequested(base)).toBe(true)
    expect(shown).toHaveLength(1)
    expect(shown[0]).toMatchObject({ title: 'Release plan', options: { body: base.body, tag: 'ki-question-main-question-1', data: { sessionId: 'main', questionItemId: 'question-1' } } })
    for (const patch of [{ enabled: false }, { focusedSession: 'main' }, { subagent: true }]) {
      expect(await notifyQuestionRequested({ ...base, ...patch })).toBe(false)
    }
    for (const value of ['default', 'denied'] as const) {
      permission(value)
      expect(await notifyQuestionRequested(base)).toBe(false)
    }
    expect(shown).toHaveLength(1)
  })
})

function questionPushWorker(language = 'zh-CN') {
  const handlers = new Map<string, (event: unknown) => void>()
  const shown: Array<{ title: string; options: NotificationOptions }> = []
  const clients: Array<{ focused: boolean; visibilityState: string; url: string }> = []
  const self = {
    addEventListener: (type: string, handler: (event: unknown) => void) => { handlers.set(type, handler) },
    skipWaiting() {},
    clients: { claim: async () => {}, matchAll: async () => clients, openWindow: async () => ({ url: '/' }) },
    registration: { scope: 'http://localhost/', showNotification: async (title: string, options: NotificationOptions) => { shown.push({ title, options }) } },
    navigator: { language },
  }
  new Function('self', readFileSync(new URL('../public/sw.js', import.meta.url), 'utf8'))(self)
  return {
    clients, shown,
    async push(payload: unknown, malformed = false) {
      let pending: Promise<unknown> = Promise.resolve()
      handlers.get('push')!({ data: payload === null ? null : { json: () => { if (malformed) throw new SyntaxError('invalid payload'); return payload } }, waitUntil: (value: Promise<unknown>) => { pending = value } })
      await pending
    },
  }
}

for (const language of ['zh-CN', 'en-US']) test(`question push reaches a closed browser in ${language} without changing completions`, async () => {
  const sw = questionPushWorker(language)
  await sw.push({ type: 'question_requested', sessionId: 'main', title: 'Release plan', questionItemId: 'question-1', body: 'Deploy now?' })
  expect(sw.shown).toHaveLength(1)
  expect(sw.shown[0]).toMatchObject({ title: 'Release plan', options: { body: language === 'zh-CN' ? '待回答问题：Deploy now?' : 'Question needs your answer: Deploy now?', tag: 'ki-question-main-question-1', data: { sessionId: 'main', questionItemId: 'question-1' } } })
  await sw.push({ type: 'run_complete', sessionId: 'main', title: 'Release plan', cwd: '/workspace' })
  expect(sw.shown).toHaveLength(2)
  expect(sw.shown[1]).toMatchObject({ title: 'Release plan', options: { body: language === 'zh-CN' ? '/workspace · 会话已完成' : '/workspace · Session finished', tag: 'ki-run-main', data: { sessionId: 'main' } } })
})

test('question push suppresses focused visible clients and ignores incomplete or malformed payloads', async () => {
  const sw = questionPushWorker()
  const payload = { type: 'question_requested', sessionId: 'main', title: 'Release plan', questionItemId: 'question-1', body: 'Deploy now?' }
  sw.clients.push({ focused: true, visibilityState: 'visible', url: 'http://localhost/' })
  await sw.push(payload)
  expect(sw.shown).toHaveLength(0)
  sw.clients[0].visibilityState = 'hidden'
  await sw.push(payload)
  expect(sw.shown).toHaveLength(1)
  sw.clients[0].focused = false
  sw.clients[0].visibilityState = 'visible'
  await sw.push({ ...payload, questionItemId: 'question-2' })
  expect(sw.shown).toHaveLength(2)
  sw.clients.length = 0
  for (const incomplete of [null, {}, { ...payload, type: 'unknown' }, { ...payload, sessionId: '' }, { ...payload, questionItemId: '' }, { ...payload, body: '' }, { ...payload, sessionId: 1 }, { ...payload, questionItemId: 1 }, { ...payload, body: {} }]) await sw.push(incomplete)
  await sw.push(payload, true)
  expect(sw.shown).toHaveLength(2)
})
