import { Component, type ReactNode } from 'react'

export class ErrorBoundary extends Component<{ children: ReactNode }, { err: string | null }> {
  state = { err: null as string | null }

  static getDerivedStateFromError(err: Error) {
    return { err: err.message || String(err) }
  }

  render() {
    if (this.state.err) {
      return (
        <div style={{ padding: 24, maxWidth: 640 }}>
          <h1 style={{ fontSize: 18 }}>页面出错</h1>
          <pre style={{ whiteSpace: 'pre-wrap' }}>{this.state.err}</pre>
          <button type="button" onClick={() => { this.setState({ err: null }); window.location.href = '/' }}>回到首页</button>
        </div>
      )
    }
    return this.props.children
  }
}
