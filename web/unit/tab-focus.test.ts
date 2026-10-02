import { expect, test } from 'bun:test'
import { clearTabFocus, FOCUS_LEASE_MS, focusedSession, publishTabFocus } from '../src/lib/tab-focus'

test('focus leases isolate backend instances and respect tab ownership', () => {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  const values = new Map<string, string>()
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => { values.set(key, value) },
      removeItem: (key: string) => { values.delete(key) },
    },
  })
  const now = Date.now
  let clock = 1000
  Date.now = () => clock
  try {
    publishTabFocus('a', 'tab1', 'session')
    expect(focusedSession('a')).toBe('session')
    expect(focusedSession('b')).toBeNull()
    publishTabFocus('b', 'tab2', 'session')
    clearTabFocus('a', 'tab2')
    expect(focusedSession('a')).toBe('session')
    clearTabFocus('b', 'tab2')
    expect(focusedSession('b')).toBeNull()
    clock += FOCUS_LEASE_MS
    expect(focusedSession('a')).toBeNull()
    publishTabFocus('a', 'tab1', 'next')
    expect(focusedSession('a')).toBe('next')
    clearTabFocus('a', 'tab1')
    expect(focusedSession('a')).toBeNull()
    for (const raw of ['null', '{}', '[]', '{"tab":"x","session":42,"expires":99999}', '{"tab":"x","session":"s","expires":1e99}']) {
      values.set('ki-focused-session:a', raw)
      expect(focusedSession('a')).toBeNull()
    }
    values.set('ki-focused-session', JSON.stringify({ tab: 'old', session: 'stale' }))
    expect(focusedSession('a')).toBeNull()
    publishTabFocus('', 'tab', 'stale')
    expect(focusedSession('')).toBeNull()
  } finally {
    Date.now = now
    if (descriptor) Object.defineProperty(globalThis, 'localStorage', descriptor)
    else Reflect.deleteProperty(globalThis, 'localStorage')
  }
})
