import { useState, type FormEvent } from 'react'
import { ApiError, Client } from './api'
import { useI18n } from './i18n'

export function AuthLoading() {
  const { t } = useI18n()
  return <main className="auth-screen"><div className="auth-card"><div className="auth-mark">ki</div><p>{t('auth.checking')}</p></div></main>
}

export function LoginScreen({ api, onLogin }: { api: Client; onLogin: () => void }) {
  const { t } = useI18n()
  const [token, setToken] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (busy || !token.trim()) return
    setBusy(true)
    setError('')
    try {
      await api.login(token)
      setToken('')
      onLogin()
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) setError(t('auth.invalid'))
      else setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <main className="auth-screen">
      <form className="auth-card" onSubmit={submit} data-testid="auth-login">
        <div className="auth-mark">ki</div>
        <h1>{t('auth.title')}</h1>
        <p>{t('auth.subtitle')}</p>
        <label className="auth-field">
          <span>{t('auth.token')}</span>
          <input
            data-testid="auth-token"
            type="password"
            value={token}
            onChange={event => setToken(event.target.value)}
            placeholder={t('auth.placeholder')}
            autoComplete="current-password"
            autoFocus
          />
        </label>
        {error ? <p className="auth-error" role="alert">{error}</p> : null}
        <button type="submit" className="primary-btn auth-submit" data-testid="auth-submit" disabled={busy || !token.trim()}>
          {busy ? t('auth.loggingIn') : t('auth.submit')}
        </button>
      </form>
    </main>
  )
}
