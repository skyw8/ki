import { Component, useEffect, useLayoutEffect, useRef, useState, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useI18n } from '../../i18n/index'
import { ICheck, IClose, ICopy, IDownloadPng, IDownloadSvg, IZoomFit, IZoomIn, IZoomOut } from '../../components/icons'
import { plantumlUrl } from './plantuml'
import { useDialogFocus } from '../../hooks/useDialogFocus'
import { copyText } from '../../lib/clipboard'

export type DiagramKind = 'mermaid' | 'plantuml'

type View = 'diagram' | 'source'
type Format = 'png' | 'svg'

function nextRenderId(): string {
  return `kimmd${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`
}

function retargetSvgId(svg: string, renderId: string): string {
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

function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  window.setTimeout(() => URL.revokeObjectURL(url), 0)
}

async function fetchBlob(url: string): Promise<Blob> {
  const res = await fetch(url)
  if (!res.ok) throw new Error(`HTTP ${res.status}`)
  return res.blob()
}

function svgDimensions(svg: string): { width: number; height: number } | null {
  const vb = /viewBox="([^"]+)"/.exec(svg)
  if (vb) {
    const parts = vb[1].trim().split(/[\s,]+/).map(Number)
    if (parts.length === 4 && parts[2] > 0 && parts[3] > 0) return { width: parts[2], height: parts[3] }
  }
  const width = Number(/\bwidth="([\d.]+)/.exec(svg)?.[1])
  const height = Number(/\bheight="([\d.]+)/.exec(svg)?.[1])
  if (width > 0 && height > 0) return { width, height }
  return null
}

function pinSvgSize(svg: string, width: number, height: number): string {
  return svg.replace(/<svg\b[^>]*>/, tag =>
    tag
      .replace(/\swidth="[^"]*"/, '')
      .replace(/\sheight="[^"]*"/, '')
      .replace(/<svg/, `<svg width="${width}" height="${height}"`),
  )
}

async function svgToPngBlob(svg: string): Promise<Blob> {
  const dims = svgDimensions(svg)
  const markup = dims ? pinSvgSize(svg, dims.width, dims.height) : svg
  const url = URL.createObjectURL(new Blob([markup], { type: 'image/svg+xml;charset=utf-8' }))
  try {
    const img = new Image()
    await new Promise<void>((resolve, reject) => {
      img.onload = () => resolve()
      img.onerror = () => reject(new Error('image decode failed'))
      img.src = url
    })
    const width = dims?.width ?? img.naturalWidth ?? 800
    const height = dims?.height ?? img.naturalHeight ?? 600
    const scale = 2
    const canvas = document.createElement('canvas')
    canvas.width = width * scale
    canvas.height = height * scale
    const ctx = canvas.getContext('2d')
    if (!ctx) throw new Error('canvas unavailable')
    ctx.scale(scale, scale)
    ctx.drawImage(img, 0, 0, width, height)
    return await new Promise<Blob>((resolve, reject) => {
      canvas.toBlob(blob => (blob ? resolve(blob) : reject(new Error('png encode failed'))), 'image/png')
    })
  } finally {
    URL.revokeObjectURL(url)
  }
}

const MIN_SCALE = 0.1
const MAX_SCALE = 16

function clampScale(scale: number): number {
  return Math.min(MAX_SCALE, Math.max(MIN_SCALE, scale))
}

function DiagramZoom({
  kind,
  svg,
  imgUrl,
  testid,
  onClose,
}: {
  kind: DiagramKind
  svg: string
  imgUrl: string
  testid: string
  onClose: () => void
}) {
  const { t } = useI18n()
  const dialogRef = useDialogFocus<HTMLDivElement>({ open: true, onEscape: onClose })
  const stageRef = useRef<HTMLDivElement>(null)
  const drag = useRef<{ x: number; y: number; left: number; top: number } | null>(null)
  const [pinned, setPinned] = useState('')
  const [dims, setDims] = useState<{ width: number; height: number } | null>(null)
  const [scale, setScale] = useState(1)
  const [fitTick, setFitTick] = useState(0)

  useEffect(() => {
    if (kind !== 'mermaid' || !svg) {
      setPinned('')
      return
    }
    const size = svgDimensions(svg)
    const markup = size ? pinSvgSize(svg, size.width, size.height) : svg
    const url = URL.createObjectURL(new Blob([markup], { type: 'image/svg+xml;charset=utf-8' }))
    setPinned(url)
    return () => URL.revokeObjectURL(url)
  }, [kind, svg])

  useEffect(() => {
    const stage = stageRef.current
    if (!dims || !stage) return
    const fit = Math.min((stage.clientWidth - 32) / dims.width, (stage.clientHeight - 32) / dims.height)
    setScale(Number.isFinite(fit) && fit > 0 ? fit : 1)
  }, [dims, fitTick])

  useEffect(() => {
    const stage = stageRef.current
    if (!stage) return
    const onWheel = (event: WheelEvent) => {
      event.preventDefault()
      setScale(current => clampScale(current * (event.deltaY < 0 ? 1.1 : 1 / 1.1)))
    }
    stage.addEventListener('wheel', onWheel, { passive: false })
    return () => stage.removeEventListener('wheel', onWheel)
  }, [])

  const onPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return
    const stage = stageRef.current
    if (!stage) return
    drag.current = { x: event.clientX, y: event.clientY, left: stage.scrollLeft, top: stage.scrollTop }
    stage.setPointerCapture(event.pointerId)
  }
  const onPointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const start = drag.current
    const stage = stageRef.current
    if (!start || !stage) return
    stage.scrollLeft = start.left - (event.clientX - start.x)
    stage.scrollTop = start.top - (event.clientY - start.y)
  }
  const endDrag = () => { drag.current = null }

  const src = kind === 'mermaid' ? pinned : imgUrl
  const known = kind === 'mermaid' ? svgDimensions(svg) : null

  return createPortal(
    <div ref={dialogRef} className="diagram-zoom" data-testid={testid} role="dialog" aria-modal="true" aria-label={t('md.preview')} tabIndex={-1}>
      <div className="diagram-zoom-bar">
        <button type="button" data-testid={`${testid}-out`} aria-label={t('md.zoomOut')} title={t('md.zoomOut')} onClick={() => setScale(s => clampScale(s * 0.8))}><IZoomOut /></button>
        <span className="diagram-zoom-pct" data-testid={`${testid}-level`}>{Math.round(scale * 100)}%</span>
        <button type="button" data-testid={`${testid}-in`} aria-label={t('md.zoomIn')} title={t('md.zoomIn')} onClick={() => setScale(s => clampScale(s * 1.25))}><IZoomIn /></button>
        <button type="button" data-testid={`${testid}-fit`} aria-label={t('md.zoomFit')} title={t('md.zoomFit')} onClick={() => setFitTick(n => n + 1)}><IZoomFit /></button>
        <button type="button" data-testid={`${testid}-close`} aria-label={t('close')} title={t('close')} onClick={onClose}><IClose /></button>
      </div>
      <div
        ref={stageRef}
        className="diagram-zoom-stage"
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
      >
        <div
          className="diagram-zoom-canvas"
          style={dims ? { width: dims.width * scale, height: dims.height * scale } : undefined}
        >
          {src ? (
            <img
              src={src}
              alt=""
              draggable={false}
              onLoad={event => {
                const img = event.currentTarget
                setDims({
                  width: img.naturalWidth || known?.width || 800,
                  height: img.naturalHeight || known?.height || 600,
                })
              }}
            />
          ) : null}
        </div>
      </div>
    </div>,
    document.body,
  )
}

export default function DiagramBlock({
  kind,
  code,
  isIncomplete,
}: {
  kind: DiagramKind
  code: string
  isIncomplete: boolean
}) {
  const { t } = useI18n()
  const dark = useDarkTheme()
  const svgHost = useRef<HTMLDivElement>(null)
  const [view, setView] = useState<View>('diagram')
  const [svg, setSvg] = useState('')
  const [imgUrl, setImgUrl] = useState('')
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(0)
  const [copied, setCopied] = useState(false)
  const [busy, setBusy] = useState<Format | null>(null)
  const [zoomed, setZoomed] = useState(false)
  const copiedTimer = useRef(0)
  const prefix = kind === 'mermaid' ? 'md-mermaid' : 'md-plantuml'
  const source = code.replace(/\n$/, '')
  const showDiagram = view === 'diagram' && !isIncomplete
  const ready = kind === 'mermaid' ? !!svg : !!imgUrl
  const showSvg = showDiagram && kind === 'mermaid' && !error && !!svg
  const showImg = showDiagram && kind === 'plantuml' && !error && !!imgUrl
  const showSource = !showDiagram || (!ready && !error)

  useEffect(() => () => window.clearTimeout(copiedTimer.current), [])

  useLayoutEffect(() => {
    const el = svgHost.current
    if (!el) return
    el.innerHTML = svg
  }, [svg])

  useEffect(() => {
    if (kind !== 'mermaid') return
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
          htmlLabels: false,
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
  }, [kind, showDiagram, source, dark, retry])

  useEffect(() => {
    if (kind !== 'plantuml') return
    if (!source.trim()) {
      setImgUrl('')
      setError('')
      return
    }
    if (!showDiagram) return
    let cancelled = false
    ;(async () => {
      try {
        const url = await plantumlUrl(source, 'svg')
        if (!cancelled) {
          setImgUrl(url)
          setError('')
        }
      } catch (err) {
        if (!cancelled) {
          setImgUrl('')
          setError(err instanceof Error ? err.message : String(err))
        }
      }
    })()
    return () => { cancelled = true }
  }, [kind, showDiagram, source, retry])

  const copy = () => {
    void copyText(source).then(ok => {
      if (!ok) return
      setCopied(true)
      window.clearTimeout(copiedTimer.current)
      copiedTimer.current = window.setTimeout(() => setCopied(false), 1500)
    })
  }

  const download = async (format: Format) => {
    if (busy) return
    setBusy(format)
    try {
      let blob: Blob
      if (kind === 'mermaid') {
        blob = format === 'svg'
          ? new Blob([svg], { type: 'image/svg+xml;charset=utf-8' })
          : await svgToPngBlob(svg)
      } else {
        blob = await fetchBlob(await plantumlUrl(source, format))
      }
      saveBlob(blob, `${kind}.${format}`)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  const sourceView = (
    <pre data-testid={showDiagram ? `${prefix}-pending` : `${prefix}-code`}><code>{source}</code></pre>
  )

  return (
    <DiagramGuard fallback={sourceView}>
      <div className={`md-diagram ${prefix}`} data-testid={prefix}>
        <div className="md-block-bar">
          <div className="md-view-toggle" role="group" aria-label={t('md.view')}>
            <button
              type="button"
              aria-pressed={view === 'diagram'}
              data-testid={`${prefix}-diagram`}
              onClick={() => setView('diagram')}
            >
              {t('md.diagram')}
            </button>
            <button
              type="button"
              aria-pressed={view === 'source'}
              data-testid={`${prefix}-source`}
              onClick={() => setView('source')}
            >
              {t('md.source')}
            </button>
          </div>
          <div className="md-block-actions">
            <button
              type="button"
              className="md-copy"
              data-testid={`${prefix}-download-png`}
              aria-label={t('md.downloadPng')}
              title={t('md.downloadPng')}
              disabled={!ready || busy !== null}
              onClick={() => void download('png')}
            >
              <IDownloadPng />
            </button>
            <button
              type="button"
              className="md-copy"
              data-testid={`${prefix}-download-svg`}
              aria-label={t('md.downloadSvg')}
              title={t('md.downloadSvg')}
              disabled={!ready || busy !== null}
              onClick={() => void download('svg')}
            >
              <IDownloadSvg />
            </button>
            <button
              type="button"
              className="md-copy"
              data-testid={`${prefix}-copy`}
              aria-label={copied ? t('md.copied') : t('chat.copy')}
              title={copied ? t('md.copied') : t('chat.copy')}
              onClick={copy}
            >
              {copied ? <ICheck /> : <ICopy />}
            </button>
          </div>
        </div>
        {showDiagram && error ? (
          <div className="md-diagram-error" data-testid={`${prefix}-error`}>
            <p>{t('md.diagramError')}</p>
            <pre>{error}</pre>
            <button
              type="button"
              className="ui-button small secondary"
              data-testid={`${prefix}-retry`}
              onClick={() => {
                setError('')
                setImgUrl('')
                setRetry(n => n + 1)
              }}
            >
              {t('md.diagramRetry')}
            </button>
          </div>
        ) : null}
        {kind === 'mermaid' ? (
          <div
            className="md-diagram-svg"
            data-testid={`${prefix}-svg`}
            hidden={!showSvg}
            ref={svgHost}
            role="button"
            tabIndex={0}
            aria-label={t('md.openDiagram')}
            title={t('md.openDiagram')}
            onClick={() => setZoomed(true)}
            onKeyDown={event => {
              if (event.key !== 'Enter' && event.key !== ' ') return
              event.preventDefault()
              setZoomed(true)
            }}
          />
        ) : (
          <div
            className="md-diagram-svg"
            data-testid={`${prefix}-svg`}
            hidden={!showImg}
            role="button"
            tabIndex={0}
            aria-label={t('md.openDiagram')}
            title={t('md.openDiagram')}
            onClick={() => setZoomed(true)}
            onKeyDown={event => {
              if (event.key !== 'Enter' && event.key !== ' ') return
              event.preventDefault()
              setZoomed(true)
            }}
          >
            {imgUrl ? (
              <img
                key={retry}
                src={imgUrl}
                alt=""
                data-testid={`${prefix}-image`}
                onError={() => setError(t('md.diagramError'))}
              />
            ) : null}
          </div>
        )}
        {showSource ? sourceView : null}
      </div>
      {zoomed && ready ? (
        <DiagramZoom
          kind={kind}
          svg={svg}
          imgUrl={imgUrl}
          testid={`${prefix}-zoom`}
          onClose={() => setZoomed(false)}
        />
      ) : null}
    </DiagramGuard>
  )
}
