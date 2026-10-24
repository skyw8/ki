import { useState } from 'react'
import type { ModelInfo } from '../../../api/types'
import { Select } from '../../../components/Select'
import { IChevDown } from '../../../components/icons'
import { clampThinkingEffort } from '../../../lib/model'
import { ModelPickerDialog } from '../ModelPickerDialog'

export type ModelFieldProps = {
  fieldLabel: string
  hint: string
  emptyLabel: string
  emptyDetail: string
  customDetail: string
  dialogTitle: string
  testid: string
  dialogTestid: string
  value: string
  pickerValue?: string
  models: ModelInfo[]
  onSelect: (spec: string) => void
  onClear?: () => void
  clearLabel?: string
  canClear?: boolean
  thinkingLabel?: string
  thinkingEffort?: string
  onThinking?: (effort: string) => void
}

export function ModelField({
  fieldLabel,
  hint,
  emptyLabel,
  emptyDetail,
  customDetail,
  dialogTitle,
  testid,
  dialogTestid,
  value,
  pickerValue = value,
  models,
  onSelect,
  onClear,
  clearLabel,
  canClear = !!value,
  thinkingLabel,
  thinkingEffort = '',
  onThinking,
}: ModelFieldProps) {
  const [open, setOpen] = useState(false)
  const selected = models.find(item => item.spec === pickerValue)
  const modelLabel = selected?.name || value || emptyLabel
  const modelDetail = selected
    ? (selected.name && selected.name !== selected.id ? `${selected.provider} / ${selected.id}` : selected.provider)
    : value ? customDetail : emptyDetail
  const levels = onThinking ? selected?.thinkingLevels ?? [] : []
  const effort = clampThinkingEffort(thinkingEffort, selected) || selected?.defaultThinking || levels[0] || ''

  return (
    <div className="form-control full">
      <div className="extension-model-row">
        <div className="extension-model-col">
          <span>{fieldLabel}</span>
          <button type="button" className="extension-model-trigger" data-testid={testid} aria-haspopup="dialog" disabled={!models.length} onClick={() => setOpen(true)}>
            <span className="extension-model-value"><strong>{modelLabel}</strong><small>{modelDetail}</small></span>
            <IChevDown />
          </button>
        </div>
        {levels.length > 1 ? (
          <div className="extension-model-col extension-thinking-col">
            <span>{thinkingLabel}</span>
            <Select
              className="extension-thinking-select"
              testid={`${testid}-thinking`}
              ariaLabel={thinkingLabel || 'Thinking effort'}
              value={effort}
              options={levels.map(level => ({ value: level, label: level }))}
              onChange={next => onThinking?.(next)}
            />
          </div>
        ) : null}
      </div>
      <div className="extension-model-meta">
        <small className="form-hint">{hint}</small>
        {onClear && canClear ? <button type="button" className="cfg-btn" onClick={onClear}>{clearLabel}</button> : null}
      </div>
      <ModelPickerDialog open={open} models={models} value={pickerValue} onSelect={onSelect} onClose={() => setOpen(false)} testid={dialogTestid} title={dialogTitle} />
    </div>
  )
}
