import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import type { Client } from '../../../api/client'
import { IClose } from '../../../components/icons'
import { useI18n } from '../../../i18n/index'
import { useDialogFocus } from '../../../hooks/useDialogFocus'
import { formatBytes } from './format'

export function OutputPreview({ api, path, onClose }: { api: Client; path: string | null; onClose: () => void }) {
  const { t } = useI18n()
  const [text, setText] = useState('')
  const [error, setError] = useState('')
  const [clipped, setClipped] = useState(false)
  const [loading, setLoading] = useState(false)
  const dialogRef = useDialogFocus<HTMLDivElement>({ open: !!path, onEscape: onClose })

  useEffect(() => {
    setText('')
    setError('')
    setClipped(false)
    if (!path) return
    const ac = new AbortController()
    setLoading(true)
    void api.previewFS(path, ac.signal)
      .then(async blob => {
        const full = await blob.text()
        setClipped(blob.size >= 1 << 20)
        setText(full)
      })
      .catch(e => {
        if ((e as { name?: string }).name !== 'AbortError') setError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => setLoading(false))
    return () => ac.abort()
  }, [api, path])

  if (!path) return null
  return createPortal(
    <div className="modal-mask" onClick={onClose} data-testid="output-preview-mask">
      <div ref={dialogRef} className="output-preview" role="dialog" aria-modal="true" aria-label={t('tool.fullOutputTitle')} tabIndex={-1} onClick={e => e.stopPropagation()}>
        <div className="modal-head">
          <h2 title={path}>{path.split(/[\\/]/).pop()}</h2>
          <button type="button" className="icon-btn" onClick={onClose} aria-label={t('dialog.close')}><IClose /></button>
        </div>
        <div className="output-preview-meta">{path}</div>
        {loading ? <div className="attachment-empty">{t('chat.loadingBody')}</div> : null}
        {error ? <div className="attachment-error">{error}</div> : null}
        {!loading && !error ? (
          <>
            {clipped ? <div className="output-preview-note">{t('tool.outputPreviewClipped')}</div> : null}
            <pre className="output-preview-body" data-testid="output-preview-body">{text}</pre>
            <div className="output-preview-meta">{formatBytes(new Blob([text]).size)}</div>
          </>
        ) : null}
      </div>
    </div>,
    document.body,
  )
}
