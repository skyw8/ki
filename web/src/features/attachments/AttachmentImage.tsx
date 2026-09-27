import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import type { Client } from '../../api/client'
import { IClose, IImage } from '../../components/icons'
import { useI18n } from '../../i18n/index'
import type { Content } from '../../api/types'
import { useDialogFocus } from '../../hooks/useDialogFocus'

const ImagePreview = createContext<((url: string, name: string, blob?: Blob) => void) | null>(null)

/** The lightbox must outlive a virtual row leaving the viewport or an
 * optimistic row being replaced by its persisted entry during a live run. */
export function ImagePreviewProvider({ children }: { children: ReactNode }) {
  const { t } = useI18n()
  const [preview, setPreview] = useState<{ url: string; name: string; owned: boolean } | null>(null)
  const close = useCallback(() => setPreview(null), [])
  const root = useDialogFocus<HTMLDivElement>({ open: !!preview, onEscape: close })
  const show = useCallback((url: string, name: string, blob?: Blob) => {
    setPreview({ url: blob ? URL.createObjectURL(blob) : url, name, owned: !!blob })
  }, [])
  useEffect(() => () => { if (preview?.owned) URL.revokeObjectURL(preview.url) }, [preview])
  return <ImagePreview.Provider value={show}>
    {children}
    {preview ? createPortal(
      <div ref={root} className="image-lightbox" role="dialog" aria-modal="true" aria-label={t('image.preview')} tabIndex={-1} onClick={close}>
        <button type="button" className="image-lightbox-close" aria-label={t('image.closePreview')} onClick={close}><IClose /></button>
        <div className="image-lightbox-stage" onClick={event => event.stopPropagation()}><img src={preview.url} alt={preview.name} /></div>
      </div>, document.body,
    ) : null}
  </ImagePreview.Provider>
}

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
  const showPreview = useContext(ImagePreview)
  const blobRef = useRef<Blob>()
  const previewRef = useRef<HTMLButtonElement & HTMLSpanElement>(null)

  useEffect(() => {
    setFailed(false)
    blobRef.current = undefined
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
    const load = () => {
      void api.previewFS(content.path!, ac.signal).then(blob => {
        if (ac.signal.aborted) return
        blobRef.current = blob
        objectURL = URL.createObjectURL(blob)
        setURL(objectURL)
      }).catch(e => {
        if ((e as { name?: string }).name !== 'AbortError') setFailed(true)
      })
    }
    const observer = new IntersectionObserver(entries => {
      if (!entries.some(entry => entry.isIntersecting)) return
      observer.disconnect()
      load()
    }, { root: previewRef.current?.closest('.scroll'), rootMargin: '200px' })
    if (previewRef.current) observer.observe(previewRef.current)
    return () => {
      observer.disconnect()
      ac.abort()
      if (objectURL) URL.revokeObjectURL(objectURL)
    }
  }, [api, content.path, inline])

  const name = content.name || content.path || 'image'
  const preview = expandable && url && !failed ? (
    <button ref={previewRef} type="button" className={`attachment-image expandable ${className}`} title={name} aria-label={t('image.openPreview')} onClick={() => showPreview?.(url, name, blobRef.current)}>
      <img src={url} alt={name} decoding="async" onError={() => setFailed(true)} />
    </button>
  ) : (
    <span ref={previewRef} className={`attachment-image ${className}${failed ? ' failed' : ''}`} title={name}>
      {url && !failed ? <img src={url} alt={name} decoding="async" onError={() => setFailed(true)} /> : <IImage />}
    </span>
  )
  return preview
}
