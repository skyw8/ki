import { lazy, Suspense, useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import type { Client } from './api'
import { IChevRight, IClose, IFile, IFolder, IImage } from './icons'
import { useI18n } from './i18n'
import type { Content, FsEntry, FsListing } from './types'

const imageExt = /\.(avif|bmp|gif|jpe?g|png|webp)$/i
const PDFPreview = lazy(() => import('./PDFPreview').then(module => ({ default: module.PDFPreview })))
type Preview = { kind: 'image' | 'pdf'; url: string } | { kind: 'text'; text: string }

function formatSize(size?: number) {
  if (size == null) return ''
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${Math.ceil(size / 1024)} KB`
  return `${(size / 1024 / 1024).toFixed(size < 10 * 1024 * 1024 ? 1 : 0)} MB`
}

export function contentForFile(file: FsEntry): Content {
  return {
    type: imageExt.test(file.name) ? 'image' : 'workspace_file',
    path: file.path,
    name: file.name,
    size: file.size,
  }
}

export function AttachmentBrowser({ api, open, startPath, onPick, onClose }: {
  api: Client
  open: boolean
  startPath?: string
  onPick: (content: Content) => void
  onClose: () => void
}) {
  const { t } = useI18n()
  const [listing, setListing] = useState<FsListing | null>(null)
  const [selected, setSelected] = useState<FsEntry | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [preview, setPreview] = useState<Preview | null>(null)
  const [previewFailed, setPreviewFailed] = useState(false)
  const [previewLoading, setPreviewLoading] = useState(false)

  useEffect(() => {
    if (!open) return
    const ac = new AbortController()
    setSelected(null)
    setError('')
    setLoading(true)
    void api.listFS(startPath, ac.signal, true).then(setListing).catch(e => {
      if ((e as { name?: string }).name !== 'AbortError') setError(e instanceof Error ? e.message : String(e))
    }).finally(() => setLoading(false))
    return () => ac.abort()
  }, [api, open, startPath])

  useEffect(() => {
    setPreview(null)
    setPreviewFailed(false)
    if (!open || !selected) return
    const ac = new AbortController()
    let active = true
    let url = ''
    setPreviewLoading(true)
    void api.previewFS(selected.path, ac.signal).then(async blob => {
      if (blob.type.startsWith('image/')) {
        url = URL.createObjectURL(blob)
        if (active) setPreview({ kind: 'image', url })
      } else if (blob.type === 'application/pdf') {
        url = URL.createObjectURL(blob)
        if (active) setPreview({ kind: 'pdf', url })
      } else if (blob.type.startsWith('text/')) {
        const text = await blob.text()
        if (active) setPreview({ kind: 'text', text })
      } else if (active) setPreviewFailed(true)
    }).catch(e => {
      if (active && (e as { name?: string }).name !== 'AbortError') setPreviewFailed(true)
    }).finally(() => { if (active) setPreviewLoading(false) })
    return () => {
      active = false
      ac.abort()
      if (url) URL.revokeObjectURL(url)
    }
  }, [api, open, selected])

  if (!open) return null
  const navigate = (path: string) => {
    setSelected(null)
    setError('')
    setLoading(true)
    void api.listFS(path, undefined, true).then(setListing).catch(e => setError(e instanceof Error ? e.message : String(e))).finally(() => setLoading(false))
  }
  const pick = (entry: FsEntry | null) => {
    if (!entry || entry.directory) return
    onPick(contentForFile(entry))
    onClose()
  }
  return createPortal(
    <div className="modal-mask" onClick={onClose} data-testid="attachment-browser-mask">
      <div className="attachment-browser" role="dialog" aria-label={t('file.title')} onClick={e => e.stopPropagation()}>
        <div className="modal-head">
          <h2>{t('file.title')}</h2>
          <button type="button" className="icon-btn" onClick={onClose} aria-label={t('dialog.close')}><IClose /></button>
        </div>
        <div className="attachment-crumbs">
          {(listing?.crumbs ?? []).map((c, i) => (
            <span key={c.path} className="attachment-crumb">
              {i ? <IChevRight /> : null}
              <button type="button" onClick={() => navigate(c.path)}>{c.name}</button>
            </span>
          ))}
        </div>
        <div className="attachment-browser-body">
          <div className="attachment-list" aria-label={t('file.title')}>
            {loading ? <div className="attachment-empty">{t('file.loading')}</div> : null}
            {!loading && (listing?.entries ?? []).map(entry => (
              <button
                type="button"
                key={entry.path}
                aria-pressed={!entry.directory ? selected?.path === entry.path : undefined}
                className={`attachment-row${selected?.path === entry.path ? ' on' : ''}`}
                onDoubleClick={() => pick(entry)}
                onClick={() => { if (entry.directory) navigate(entry.path); else setSelected(entry) }}
              >
                <span className={`attachment-kind${imageExt.test(entry.name) ? ' image' : ''}`}>{entry.directory ? <IFolder /> : imageExt.test(entry.name) ? <IImage /> : <IFile />}</span>
                <span className="attachment-row-name">{entry.name}</span>
                {!entry.directory && entry.size != null ? <small>{formatSize(entry.size)}</small> : null}
                {entry.directory ? <IChevRight /> : null}
              </button>
            ))}
            {!loading && listing && (listing.entries?.length ?? 0) === 0 ? <div className="attachment-empty">{t('file.empty')}</div> : null}
            {listing?.truncated ? <div className="attachment-limit">{t('file.truncated')}</div> : null}
          </div>
          <aside className="attachment-preview" aria-label={t('file.preview')}>
            {selected ? <>
              <div className="attachment-preview-stage">
                {previewLoading ? <span>{t('file.loading')}</span> : null}
                {!previewLoading && preview?.kind === 'image' ? <img src={preview.url} alt={selected.name} /> : null}
                {!previewLoading && preview?.kind === 'pdf' ? <Suspense fallback={<span>{t('file.loading')}</span>}><PDFPreview url={preview.url} /></Suspense> : null}
                {!previewLoading && preview?.kind === 'text' ? <div className="attachment-text-preview"><pre>{preview.text}</pre>{(selected.size ?? 0) > 1024 * 1024 ? <small>{t('file.textTruncated')}</small> : null}</div> : null}
                {!previewLoading && (!preview || previewFailed) ? <div className="attachment-preview-placeholder"><IFile /><span>{t('file.noPreview')}</span></div> : null}
              </div>
              <div className="attachment-preview-info">
                <strong title={selected.name}>{selected.name}</strong>
                <span>{formatSize(selected.size)}</span>
              </div>
            </> : <div className="attachment-preview-placeholder"><IImage /><span>{t('file.previewHint')}</span></div>}
          </aside>
        </div>
        {error ? <div className="attachment-error">{error}</div> : null}
        <div className="modal-actions">
          <button type="button" onClick={onClose}>{t('dir.cancel')}</button>
          <button type="button" className="primary-btn" disabled={!selected} onClick={() => {
            pick(selected)
          }}>{t('file.add')}</button>
        </div>
      </div>
    </div>,
    document.body,
  )
}
