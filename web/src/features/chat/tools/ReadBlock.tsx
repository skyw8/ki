import { memo } from 'react'
import { useI18n } from '../../../i18n/index'
import type { ReadDetails } from './parse'

/**
 * `read` output with line numbers and a truncation footer.
 *
 * `read` returns the raw file text plus an inline `[Showing lines X-Y of Z]`
 * note. The note is replaced by real chrome: the reader can see how much of
 * the file they got and what offset continues it, without reading prose.
 */
export const ReadBlock = memo(function ReadBlock({ text, details, offset }: {
  text: string
  details: NonNullable<ReadDetails['truncation']>
  offset: number
}) {
  const { t } = useI18n()
  const lines = text ? text.replace(/\n$/, '').split('\n') : []
  const from = offset
  const to = offset + Math.max(0, lines.length - 1)
  return (
    <div className="read-block" data-testid="read-block">
      <pre className="tool-read">{lines.map((line, i) => `${String(offset + i).padStart(4, ' ')}  ${line}`).join('\n')}</pre>
      <div className="read-meta" data-testid="read-meta">
        <span>{t('tool.readMeta', { lines: details.output_lines, from, to, total: details.total_lines })}</span>
        {details.next_offset ? <span className="read-next">{t('tool.readMore', { offset: details.next_offset })}</span> : null}
      </div>
    </div>
  )
})
