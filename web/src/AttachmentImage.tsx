import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import type { Client } from './api'
import { IClose, IImage } from './icons'
import { useI18n } from './i18n'
import type { Content } from './types'

export function AttachmentImage({ api, content, className = '', expandable = false }: {
  api: Client
  content: Content
  className?: string
  expandable?: boolean
}) {
  const { t } = useI18n()
  const inline = content.data ? `data:${content.mimeType || 'image/png'};base64,${content.data}` : ''
  const [url, setURL] = useState(inline)
  const [failed, setFailed] = useState(false)
  const [open, setOpen] = useState(false)

  useEffect(() => {
    setFailed(false)
    if (inline) {
      setURL(inline)
      return
    }
    setURL('')
    if (!content.path) {
      setFailed(true)
      return
    }
    const ac = new AbortController()
    let objectURL = ''
    void api.previewFS(content.path, ac.signal).then(blob => {
      objectURL = URL.createObjectURL(blob)
      setURL(objectURL)
    }).catch(e => {
      if ((e as { name?: string }).name !== 'AbortError') setFailed(true)
    })
    return () => {
      ac.abort()
      if (objectURL) URL.revokeObjectURL(objectURL)
    }
  }, [api, content.path, inline])

  useEffect(() => {
    if (!open) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [open])

  const name = content.name || content.path || 'image'
  const preview = expandable && url && !failed ? (
    <button type="button" className={`attachment-image expandable ${className}`} title={name} aria-label={t('image.openPreview')} onClick={() => setOpen(true)}>
      <img src={url} alt={name} onError={() => setFailed(true)} />
    </button>
  ) : (
    <span className={`attachment-image ${className}${failed ? ' failed' : ''}`} title={name}>
      {url && !failed ? <img src={url} alt={name} onError={() => setFailed(true)} /> : <IImage />}
    </span>
  )
  return <>
    {preview}
    {open && url ? createPortal(
      <div className="image-lightbox" role="dialog" aria-modal="true" aria-label={t('image.preview')} onClick={() => setOpen(false)}>
        <button type="button" className="image-lightbox-close" aria-label={t('image.closePreview')} onClick={() => setOpen(false)}><IClose /></button>
        <div className="image-lightbox-stage" onClick={event => event.stopPropagation()}>
          <img src={url} alt={name} />
        </div>
      </div>,
      document.body,
    ) : null}
  </>
}
