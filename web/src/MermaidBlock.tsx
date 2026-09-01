import { Component, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { useI18n } from './i18n'
import { ICheck, ICopy } from './icons'

type View = 'diagram' | 'source'

function nextRenderId(): string {
  return `kimmd${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`
}

function retargetSvgId(svg: string, renderId: string): string {
  // Why: mermaid.render(id) creates #id / #d{id} and later removeExistingElements
  // looks them up with getElementById. If the SVG we inject keeps that id,
  // the next render yanks it out of our tree and React throws removeChild.
  return svg.replaceAll(renderId, `${renderId}v`)
}

function useDarkTheme(): boolean {
  const [dark, setDark] = useState(() => document.body.dataset.theme === 'dark')
  useEffect(() => {
    const sync = () => setDark(document.body.dataset.theme === 'dark')
    const obs = new MutationObserver(sync)
    obs.observe(document.body, { attributes: true, attributeFilter: ['data-theme'] })
    return () => obs.disconnect()
  }, [])
  return dark
}

class DiagramGuard extends Component<{ fallback: ReactNode; children: ReactNode }, { failed: boolean }> {
  state = { failed: false }
  static getDerivedStateFromError() {
    return { failed: true }
  }
  render() {
    return this.state.failed ? this.props.fallback : this.props.children
  }
}

export default function MermaidBlock({
  code,
  isIncomplete,
}: {
  code: string
  isIncomplete: boolean
}) {
  const { t } = useI18n()
  const dark = useDarkTheme()
  const svgHost = useRef<HTMLDivElement>(null)
  const [view, setView] = useState<View>('diagram')
  const [svg, setSvg] = useState('')
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(0)
  const [copied, setCopied] = useState(false)
  const copiedTimer = useRef(0)
  const source = code.replace(/\n$/, '')
  // Why: mermaid.render is expensive and incomplete fences throw or flicker
  // on every token. Keep the source visible until the fence closes.
  const showDiagram = view === 'diagram' && !isIncomplete
  const showSvg = showDiagram && !error && !!svg
  const showSource = !showDiagram || (!svg && !error)

  useEffect(() => () => window.clearTimeout(copiedTimer.current), [])

  useLayoutEffect(() => {
    const el = svgHost.current
    if (!el) return
    el.innerHTML = svg
  }, [svg])

  useEffect(() => {
    if (!source.trim()) {
      setSvg('')
      setError('')
      return
    }
    if (!showDiagram) return
    let cancelled = false
    const renderId = nextRenderId()
    ;(async () => {
      try {
        const { mermaid } = await import('@streamdown/mermaid')
        if (cancelled) return
        const api = mermaid.getMermaid({
          startOnLoad: false,
          securityLevel: 'strict',
          suppressErrorRendering: true,
          theme: dark ? 'dark' : 'neutral',
          fontFamily: getComputedStyle(document.body).fontFamily,
        })
        const { svg: next } = await api.render(renderId, source)
        if (!cancelled) {
          setSvg(retargetSvgId(next, renderId))
          setError('')
        }
      } catch (err) {
        if (!cancelled) {
          setSvg('')
          setError(err instanceof Error ? err.message : String(err))
        }
      }
    })()
    return () => { cancelled = true }
  }, [showDiagram, source, dark, retry])

  const copy = () => {
    void navigator.clipboard?.writeText(source)
    setCopied(true)
    window.clearTimeout(copiedTimer.current)
    copiedTimer.current = window.setTimeout(() => setCopied(false), 1500)
  }

  const sourceView = (
    <pre data-testid={showDiagram ? 'md-mermaid-pending' : 'md-mermaid-code'}><code>{source}</code></pre>
  )

  return (
    <DiagramGuard fallback={sourceView}>
      <div className="md-mermaid" data-testid="md-mermaid">
        <div className="md-block-bar">
          <div className="md-view-toggle" role="group" aria-label={t('md.view')}>
            <button
              type="button"
              aria-pressed={view === 'diagram'}
              data-testid="md-mermaid-diagram"
              onClick={() => setView('diagram')}
            >
              {t('md.diagram')}
            </button>
            <button
              type="button"
              aria-pressed={view === 'source'}
              data-testid="md-mermaid-source"
              onClick={() => setView('source')}
            >
              {t('md.source')}
            </button>
          </div>
          <button
            type="button"
            className="md-copy"
            data-testid="md-mermaid-copy"
            aria-label={copied ? t('md.copied') : t('chat.copy')}
            onClick={copy}
          >
            {copied ? <ICheck /> : <ICopy />}
          </button>
        </div>
        {showDiagram && error ? (
          <div className="md-mermaid-error" data-testid="md-mermaid-error">
            <p>{t('md.mermaidError')}</p>
            <pre>{error}</pre>
            <button
              type="button"
              className="ui-button small secondary"
              data-testid="md-mermaid-retry"
              onClick={() => setRetry(n => n + 1)}
            >
              {t('md.mermaidRetry')}
            </button>
          </div>
        ) : null}
        <div
          className="md-mermaid-svg"
          data-testid="md-mermaid-svg"
          hidden={!showSvg}
          ref={svgHost}
        />
        {showSource ? sourceView : null}
      </div>
    </DiagramGuard>
  )
}
