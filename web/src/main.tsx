import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import { ErrorBoundary } from './components/ErrorBoundary'
import { I18nProvider } from './i18n/index'
import { Toaster } from './components/toast'
import { consumeLauncherToken, createLauncherAuth } from './lib/launcher-auth'
import './theme.css'
import './app.css'

const launcherAuth = createLauncherAuth(consumeLauncherToken(window.location, window.history))
const el = document.getElementById('root')
if (!el) throw new Error('missing #root')
createRoot(el).render(
  <StrictMode>
    <I18nProvider>
      <ErrorBoundary>
        <App launcherAuth={launcherAuth} />
        <Toaster />
      </ErrorBoundary>
    </I18nProvider>
  </StrictMode>,
)
