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
            <div style={{ padding: 24, maxWidth: 640 }}>
              <h1 style={{ fontSize: 18 }}>{t('error.title')}</h1>
              <pre style={{ whiteSpace: 'pre-wrap' }}>{this.state.err}</pre>
              <button type="button" onClick={() => { this.setState({ err: null }); window.location.href = '/' }}>{t('error.home')}</button>
            </div>
          )}
        </I18nContext.Consumer>
      )
    }
    return this.props.children
  }
}
