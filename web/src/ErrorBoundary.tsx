import { Component, type ReactNode } from 'react'
import { I18nContext } from './i18n'

export class ErrorBoundary extends Component<{ children: ReactNode }, { err: string | null }> {
  state = { err: null as string | null }

  static getDerivedStateFromError(err: Error) {
    return { err: err.message || String(err) }
  }

  render() {
    if (this.state.err) {
      return (
        <I18nContext.Consumer>
          {({ t }) => (
            <main className="error-boundary" role="alert">
              <h1>{t('error.title')}</h1>
              <pre>{this.state.err}</pre>
              <button type="button" className="primary-btn" onClick={() => { this.setState({ err: null }); window.location.href = '/' }}>{t('error.home')}</button>
            </main>
          )}
        </I18nContext.Consumer>
      )
    }
    return this.props.children
  }
}
