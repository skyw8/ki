import { useEffect, useRef, type CSSProperties } from 'react'
import { IAttach, IClose, IFile, ISend, IStop } from './icons'
import type { Client } from './api'
import { AttachmentImage } from './AttachmentImage'
import { Select } from './Select'
import { useI18n } from './i18n'
import type { Content } from './types'

export type Draft = { text: string; attachments: Content[] }

function basename(p: string): string {
  const s = p.replace(/[\\/]+$/, '')
  const i = Math.max(s.lastIndexOf('/'), s.lastIndexOf('\\'))
  return i >= 0 ? s.slice(i + 1) : s
}

function fileKind(content: Content): string {
  const name = content.name || basename(content.path || '')
  const dot = name.lastIndexOf('.')
  if (dot <= 0 || dot === name.length - 1) return 'FILE'
  return name.slice(dot + 1).toLocaleUpperCase().slice(0, 5)
}

export function Composer({ api, draft, onChange, onSend, onStop, onAttach, onFiles, onCancel, busy, uploading, disabled, hero, cwd, model, err, onPickModel, thinkingLevels, thinkingEffort, onThinking, contextUsage, mode = 'new' }: {
  api: Client
  draft: Draft
  onChange: (draft: Draft) => void
  onSend: () => void
  onStop?: () => void
  onAttach: () => void
  onFiles?: (files: File[]) => void
  onCancel?: () => void
  busy: boolean
  uploading?: boolean
  disabled?: boolean
  hero?: boolean
  cwd?: string
  model?: string
  err?: string | null
  onPickModel?: () => void
  thinkingLevels?: string[]
  thinkingEffort?: string
  onThinking?: (effort: string) => void
  contextUsage?: { usedTokens: number; contextWindow: number; estimated: boolean }
  mode?: 'new' | 'edit'
}) {
  const { t } = useI18n()
  const ref = useRef<HTMLTextAreaElement>(null)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 180)}px`
  }, [draft.text])
  useEffect(() => { if (mode === 'edit') ref.current?.focus({ preventScroll: true }) }, [mode])
  const canSend = !uploading && (!!draft.text.trim() || draft.attachments.length > 0)
  return (
    <div className={`composer-wrap${hero ? ' hero-pos' : ''}${mode === 'edit' ? ' edit-pos' : ''}`}>
      {err ? <div className="notice" data-testid="notice">{err}</div> : null}
      <div className="composer">
        {draft.attachments.length ? <div className="attachment-strip">
          {draft.attachments.map((a, i) => a.type === 'image' ? (
            <span className="attachment-draft attachment-draft-image" key={`${a.path || a.name}-${i}`} title={a.name || basename(a.path || '')}>
              <AttachmentImage api={api} content={a} className="composer-image" expandable />
              <button type="button" className="attachment-remove" aria-label={t('composer.removeAttachment')} onClick={() => onChange({ ...draft, attachments: draft.attachments.filter((_, j) => i !== j) })}><IClose /></button>
            </span>
          ) : (
            <span className="attachment-draft attachment-draft-file" key={`${a.path || a.name}-${i}`} title={a.name || basename(a.path || '')}>
              <span className="attachment-file-tile"><IFile /><small>{fileKind(a)}</small></span>
              <button type="button" className="attachment-remove" aria-label={t('composer.removeAttachment')} onClick={() => onChange({ ...draft, attachments: draft.attachments.filter((_, j) => i !== j) })}><IClose /></button>
            </span>
          ))}
        </div> : null}
        <textarea
          ref={ref}
          data-testid={mode === 'edit' ? 'edit-input' : 'composer-input'}
          rows={1}
          placeholder={disabled ? t('composer.placeholderDisabled') : t('composer.placeholder')}
          value={draft.text}
          disabled={disabled}
          onChange={e => onChange({ ...draft, text: e.target.value })}
		  onPaste={e => {
			const files = Array.from(e.clipboardData.files)
			if (files.length) onFiles?.(files)
		  }}
          onKeyDown={e => {
            if (e.key === 'Escape' && mode === 'edit') { e.preventDefault(); onCancel?.(); return }
            if ((mode === 'new' && e.key === 'Enter' && !e.shiftKey) || (mode === 'edit' && e.key === 'Enter' && (e.ctrlKey || e.metaKey))) {
              e.preventDefault()
              if (busy) onStop?.()
              else if (canSend) onSend()
            }
          }}
        />
        <div className="composer-row">
          <button type="button" className="attach-btn" onClick={onAttach} disabled={disabled || busy} aria-label={t('composer.attach')} title={t('composer.attach')}><IAttach /></button>
          {mode === 'new' && cwd ? <span className="cwd-chip" title={cwd}>{basename(cwd)}</span> : null}
          {mode === 'new' ? <button type="button" className="model-chip" data-testid="open-model" onClick={onPickModel}>{model || t('composer.pickModel')}</button> : null}
          {mode === 'new' && thinkingLevels && thinkingLevels.length > 1 ? <Select className="thinking-select" ariaLabel="Thinking effort" value={thinkingEffort || thinkingLevels[0]} options={thinkingLevels.map(level => ({ value: level, label: level }))} onChange={value => onThinking?.(value)} /> : null}
          <span className="grow" />
		  {uploading ? <span className="uploading-label">{t('composer.uploading')}</span> : null}
          {mode === 'edit' ? <button type="button" className="composer-cancel" onClick={onCancel}>{t('composer.cancelEdit')}</button> : null}
          {mode === 'new' && contextUsage?.contextWindow ? <span className="context-meter" title={`${contextUsage.estimated ? '~' : ''}${contextUsage.usedTokens.toLocaleString()} / ${contextUsage.contextWindow.toLocaleString()} tokens`} style={{ '--context-pct': `${Math.min(100, contextUsage.usedTokens / contextUsage.contextWindow * 100)}%` } as CSSProperties}>{contextUsage.estimated ? '~' : ''}{Math.round(contextUsage.usedTokens / contextUsage.contextWindow * 100)}%</span> : null}
          {busy && mode === 'new' ? <button type="button" className="send stop" data-testid="composer-stop" onClick={onStop} aria-label={t('composer.stop')}><IStop /></button> : <button type="button" className="send" data-testid={mode === 'edit' ? 'edit-send' : 'composer-send'} disabled={disabled || !canSend || busy} onClick={onSend} aria-label={mode === 'edit' ? t('composer.sendEdit') : t('composer.send')}><ISend /></button>}
        </div>
      </div>
    </div>
  )
}
