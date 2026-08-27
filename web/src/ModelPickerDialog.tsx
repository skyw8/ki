import { createPortal } from 'react-dom'
import { useEffect, useMemo, useRef, useState } from 'react'
import { ICheck, IClose, ISearch } from './icons'
import { useI18n } from './i18n'
import type { ModelInfo } from './types'

function fuzzyTextMatch(value: string, query: string): boolean {
  const haystack = value.normalize('NFKC').toLocaleLowerCase()
  const needle = query.normalize('NFKC').toLocaleLowerCase()
  if (haystack.includes(needle)) return true
  let cursor = 0
  for (const char of haystack) {
    if (char === needle[cursor]) cursor++
    if (cursor === needle.length) return true
  }
  return needle.length === 0
}

function modelMatches(model: ModelInfo, query: string): boolean {
  const fields = [model.provider, model.id, model.name, model.spec, `${model.provider}/${model.id}`]
  return query.trim().split(/\s+/).every(token => fields.some(field => fuzzyTextMatch(field || '', token)))
}

export function ModelPickerDialog({
  open,
  models,
  value,
  onSelect,
  onClose,
  testid = 'model-dialog',
}: {
  open: boolean
  models: ModelInfo[]
  value: string
  onSelect: (spec: string) => void
  onClose: () => void
  testid?: string
}) {
  const { t } = useI18n()
  const [query, setQuery] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)
  const closeRef = useRef(onClose)
  closeRef.current = onClose

  useEffect(() => {
    if (!open) {
      setQuery('')
      return
    }
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    const focus = window.requestAnimationFrame(() => inputRef.current?.focus())
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      event.stopImmediatePropagation()
      closeRef.current()
    }
    window.addEventListener('keydown', onKey)
    return () => {
      window.cancelAnimationFrame(focus)
      window.removeEventListener('keydown', onKey)
      document.body.style.overflow = previousOverflow
    }
  }, [open])

  const filteredModels = useMemo(
    () => query.trim() ? models.filter(model => modelMatches(model, query)) : models,
    [models, query],
  )

  if (!open) return null
  return createPortal(
    <div className="modal-mask" onClick={onClose} data-testid={`${testid}-mask`}>
      <div className="modal model-picker-modal" data-testid={testid} onClick={event => event.stopPropagation()} role="dialog" aria-label={t('model.title')}>
        <div className="modal-head">
          <h2>{t('model.title')}</h2>
          <button type="button" className="icon-btn" onClick={onClose} aria-label={t('dialog.close')}><IClose /></button>
        </div>
        <div className="modal-body">
          <div className="model-search-wrap">
            <ISearch />
            <input
              ref={inputRef}
              type="search"
              data-testid="model-search"
              aria-label={t('model.search')}
              placeholder={t('model.searchPlaceholder')}
              value={query}
              onChange={event => setQuery(event.target.value)}
            />
            {query ? <button type="button" aria-label={t('model.clearSearch')} onClick={() => { setQuery(''); inputRef.current?.focus() }}><IClose /></button> : null}
          </div>
          <ul className="model-list">
            {filteredModels.map(model => {
              const selected = value === model.spec
              return (
                <li key={model.spec}>
                  <button
                    type="button"
                    className={`model-opt${selected ? ' on' : ''}`}
                    data-testid="model-option"
                    data-spec={model.spec}
                    onClick={() => {
                      onSelect(model.spec)
                      onClose()
                    }}
                  >
                    <span className="model-opt-copy"><strong>{model.name || model.id}</strong><small>{model.name && model.name !== model.id ? `${model.provider} / ${model.id}` : model.provider}</small></span>
                    {selected ? <ICheck /> : null}
                  </button>
                </li>
              )
            })}
          </ul>
          {!filteredModels.length ? <div className="model-search-empty" data-testid="model-search-empty"><ISearch /><span>{t('model.noResults')}</span></div> : null}
        </div>
      </div>
    </div>,
    document.body,
  )
}
