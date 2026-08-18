import { useEffect, useRef, useState } from 'react'
import { GlobalWorkerOptions, getDocument, type PDFDocumentProxy, type RenderTask } from 'pdfjs-dist'
import workerURL from 'pdfjs-dist/build/pdf.worker.min.mjs?url'
import { useI18n } from './i18n'

GlobalWorkerOptions.workerSrc = workerURL

export function PDFPreview({ url }: { url: string }) {
  const { t } = useI18n()
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const [doc, setDoc] = useState<PDFDocumentProxy | null>(null)
  const [pageNumber, setPageNumber] = useState(1)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    // Pass an explicit URL object: PDF.js 6 no longer classifies blob: strings
    // passed as the shorthand source form.
    const task = getDocument({ url })
    let active = true
    setDoc(null)
    setPageNumber(1)
    setError('')
    setLoading(true)
    void task.promise.then(pdf => {
      if (active) setDoc(pdf)
      else void pdf.destroy()
    }).catch(err => {
      if (active) setError(err instanceof Error ? err.message : String(err))
    }).finally(() => { if (active) setLoading(false) })
    return () => {
      active = false
      void task.destroy()
    }
  }, [url])

  useEffect(() => {
    if (!doc) return
    let active = true
    let renderTask: RenderTask | null = null
    setLoading(true)
    void doc.getPage(pageNumber).then(page => {
      if (!active) return
      const canvas = canvasRef.current
      if (!canvas) return
      const viewport = page.getViewport({ scale: 1.45 })
      const context = canvas.getContext('2d')
      if (!context) return
      canvas.width = Math.ceil(viewport.width)
      canvas.height = Math.ceil(viewport.height)
      renderTask = page.render({ canvas, canvasContext: context, viewport })
      return renderTask.promise
    }).catch(err => {
      if (active && (err as { name?: string }).name !== 'RenderingCancelledException') setError(err instanceof Error ? err.message : String(err))
    }).finally(() => { if (active) setLoading(false) })
    return () => {
      active = false
      renderTask?.cancel()
    }
  }, [doc, pageNumber])

  return (
    <div className="pdf-preview">
      <div className="pdf-preview-page">
        {loading ? <span className="pdf-preview-loading">{t('file.loading')}</span> : null}
        {error ? <span className="pdf-preview-error" data-error={error} title={error}>{t('file.noPreview')}</span> : null}
        <canvas ref={canvasRef} hidden={!doc || !!error} />
      </div>
      {doc ? <div className="pdf-preview-toolbar">
        <button type="button" disabled={pageNumber <= 1} aria-label={t('pdf.previous')} onClick={() => setPageNumber(n => Math.max(1, n - 1))}>‹</button>
        <span>{t('pdf.page', { page: pageNumber, total: doc.numPages })}</span>
        <button type="button" disabled={pageNumber >= doc.numPages} aria-label={t('pdf.next')} onClick={() => setPageNumber(n => Math.min(doc.numPages, n + 1))}>›</button>
      </div> : null}
    </div>
  )
}
