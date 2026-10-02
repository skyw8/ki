import { expect, test } from 'bun:test'
import { ApiError } from '../src/api/client'
import { consumeLauncherToken, createLauncherAuth } from '../src/lib/launcher-auth'

test('launcher token is decoded and removed synchronously, preserving path, query and history state', () => {
  const state = { navigation: 1 }
  const calls: unknown[][] = []
  const token = 'bearer+/=& Unicode \u2603'
  expect(consumeLauncherToken(
    { pathname: '/forward/', search: '?view=chat', hash: `#tab=chat&token=${encodeURIComponent(token)}` },
    { state, replaceState: (...args) => { calls.push(args) } },
  )).toBe(token)
  expect(calls).toEqual([[state, '', '/forward/?view=chat#tab=chat']])
})

test('missing fragments are untouched; empty and duplicate token fields are all removed', () => {
  const calls: unknown[][] = []
  const history = { state: null, replaceState: (...args: unknown[]) => { calls.push(args) } }
  const location = { pathname: '/', search: '', hash: '#chat' }
  expect(consumeLauncherToken(location, history)).toBeNull()
  expect(calls).toEqual([])
  expect(consumeLauncherToken({ ...location, hash: '#token=&token=second' }, history)).toBeNull()
  expect(calls).toEqual([[null, '', '/']])
})

test('Go QueryEscape fragments distinguish form-encoded spaces from literal plus signs', () => {
  expect(consumeLauncherToken(
    { pathname: '/', search: '', hash: '#token=bearer%2B%2F%3D+space' },
    { state: null, replaceState: () => {} },
  )).toBe('bearer+/= space')
})

function fixture(authenticated = false, statusError?: Error, loginError?: Error) {
  const calls: string[] = []
  const api = {
    async authStatus() {
      calls.push('status')
      if (statusError) throw statusError
      return { authenticated, serverId: 'server-one', csrfCookieName: 'ki_csrf_one' }
    },
    async login(token: string) {
      calls.push(`login:${token}`)
      if (loginError) throw loginError
    },
  }
  return { api, calls }
}

test('effect replay shares status/login promise; later generations cannot replay credential', async () => {
  const bootstrap = createLauncherAuth('secret')
  const first = fixture()
  const pending = bootstrap.check(first.api)
  expect(bootstrap.check(first.api)).toBe(pending)
  expect(await pending).toBe('authenticated')
  expect(first.calls).toEqual(['status', 'login:secret'])
  expect(await bootstrap.check(first.api)).toBe('authenticated')
  const replacement = fixture()
  expect(await bootstrap.check(replacement.api)).toBe('required')
  expect(replacement.calls).toEqual(['status'])
})

test('valid cookies skip the exchange and discard the launcher token', async () => {
  const bootstrap = createLauncherAuth('secret')
  const first = fixture(true)
  expect(await bootstrap.check(first.api)).toBe('authenticated')
  expect(first.calls).toEqual(['status'])
  const replacement = fixture()
  expect(await bootstrap.check(replacement.api)).toBe('required')
  expect(replacement.calls).toEqual(['status'])
})

test('manual entry checks status without a launcher credential', async () => {
  const first = fixture()
  expect(await createLauncherAuth(null).check(first.api)).toBe('required')
  expect(first.calls).toEqual(['status'])
})

for (const [stage, error, result] of [
  ['status', new TypeError('network failed'), 'required'],
  ['login', new TypeError('network failed'), 'required'],
  ['login', new ApiError(401, 'invalid token'), 'required'],
  ['login', new ApiError(421, 'server changed'), 'serverChanged'],
] as const) {
  test(`${stage} failure falls back without replaying the credential (${error.message})`, async () => {
    const bootstrap = createLauncherAuth('secret')
    const first = fixture(false, stage === 'status' ? error : undefined, stage === 'login' ? error : undefined)
    expect(await bootstrap.check(first.api)).toBe(result)
    expect(first.calls).toEqual(stage === 'status' ? ['status'] : ['status', 'login:secret'])
    const replacement = fixture()
    expect(await bootstrap.check(replacement.api)).toBe('required')
    expect(replacement.calls).toEqual(['status'])
  })
}
