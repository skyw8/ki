import { ApiError, type Client } from '../api/client'

export function consumeLauncherToken(location: Pick<Location, 'hash' | 'pathname' | 'search'>, history: Pick<History, 'state' | 'replaceState'>): string | null {
  const fragment = new URLSearchParams(location.hash.slice(1))
  if (!fragment.has('token')) return null
  const token = fragment.get('token')
  fragment.delete('token')
  const rest = fragment.toString()
  history.replaceState(history.state, '', `${location.pathname}${location.search}${rest ? `#${rest}` : ''}`)
  return token || null
}

type AuthClient = Pick<Client, 'authStatus' | 'login'>
export type LauncherAuthResult = 'authenticated' | 'required' | 'serverChanged'

export function createLauncherAuth(token: string | null) {
  const checks = new WeakMap<AuthClient, Promise<LauncherAuthResult>>()
  return {
    check(api: AuthClient): Promise<LauncherAuthResult> {
      const existing = checks.get(api)
      if (existing) return existing
      const credential = token
      token = null
      const result = (async (): Promise<LauncherAuthResult> => {
        try {
          const status = await api.authStatus()
          if (status.authenticated) return 'authenticated'
          if (!credential) return 'required'
          await api.login(credential)
          return 'authenticated'
        } catch (error) {
          return error instanceof ApiError && error.status === 421 ? 'serverChanged' : 'required'
        }
      })()
      checks.set(api, result)
      return result
    },
  }
}

export type LauncherAuth = ReturnType<typeof createLauncherAuth>
