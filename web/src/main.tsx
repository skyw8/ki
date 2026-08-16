import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import { ErrorBoundary } from './ErrorBoundary'
import { I18nProvider } from './i18n'
import './theme.css'
import './app.css'

const el = document.getElementById('root')
if (!el) throw new Error('missing #root')
createRoot(el).render(
  <StrictMode>
    <I18nProvider>
      <ErrorBoundary>
        <App />
      </ErrorBoundary>
    </I18nProvider>
  </StrictMode>,
)
