import { memo } from 'react'
import type { Content } from '../../../api/types'
import { useI18n } from '../../../i18n/index'
import { codeModeSnapshot, codeModeStatusKey, nestedCalls } from './parse'

export { nestedCalls }

export type CodeModeResultProps = {
  result: string
  blocks?: Content[]
  details?: unknown
  source?: string
  cellId?: string
  running?: boolean
  testId?: string
}

export const CodeModeResult = memo(function CodeModeResult({
  result, blocks, details, source, cellId, running, testId,
}: CodeModeResultProps) {
  const { t } = useI18n()
  const snapshot = codeModeSnapshot(result, blocks, details)
  const id = snapshot.cellId || cellId || ''
  const statusKey = codeModeStatusKey(snapshot.status)
  const status = statusKey ? t(statusKey) : snapshot.status
  const showOutput = !snapshot.calls && (!!snapshot.body || !running)
  const showMeta = !!status || !!id || snapshot.truncated
  return (
    <div className="cell-result" data-testid={testId ?? 'cell-result'} data-state={snapshot.status || undefined}>
      {showMeta ? (
        <div className="cell-meta">
          {status ? <span className={`cell-status ${snapshot.status}`} data-testid="cell-status">{status}</span> : null}
          {id ? <span className="cell-id" title={id}>#{id}</span> : null}
          {snapshot.truncated ? <span className="cell-warn">{t('tool.outputTruncated')}</span> : null}
        </div>
      ) : null}
      {source ? (
        <div className="cell-sec" data-testid="cell-code-sec">
          <div className="cell-sec-head">{t('tool.code')}</div>
          <pre className="cell-code" data-testid="cell-code">{source}</pre>
        </div>
      ) : null}
      {showOutput ? (
        <div className="cell-sec" data-testid="cell-output-sec">
          <div className="cell-sec-head">{t('tool.output')}</div>
          <pre className="cell-output" data-testid="cell-output">{snapshot.body || t('tool.emptyResult')}</pre>
        </div>
      ) : null}
      {snapshot.calls ? (
        <div className="cell-calls">
          {snapshot.calls.map((call, index) => (
            <div className={`cell-call${call.isError ? ' err' : ''}`} key={index} data-testid="cell-call">
              <div className="cell-call-head">
                <span className="cell-call-index">#{index + 1}</span>
                <span className={call.isError ? 'cell-call-state err' : 'cell-call-state'}>{t(call.isError ? 'tool.callFailed' : 'tool.callOk')}</span>
              </div>
              <pre className="cell-call-body">{call.text || t('tool.emptyResult')}</pre>
            </div>
          ))}
        </div>
      ) : null}
      {snapshot.images.length ? (
        <div className="cell-images">
          {snapshot.images.map((image, index) => (
            <img
              className="cell-image"
              key={index}
              alt=""
              src={`data:${image.mimeType};base64,${image.data}`}
            />
          ))}
        </div>
      ) : null}
      {snapshot.error ? <pre className="cell-error" data-testid="cell-error">{snapshot.error}</pre> : null}
    </div>
  )
})
