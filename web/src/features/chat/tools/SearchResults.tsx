import { memo } from 'react'
import { useI18n } from '../../../i18n/index'
import { globDetails, grepDetails } from './parse'
import { formatCount } from './format'

/**
 * Structured renderers for the file/search tools.
 *
 * The model-facing text is a compact listing; every renderer here reads the
 * structured details so paths stay clickable rows instead of a `<pre>` blob.
 * When details are missing (a session from before the field existed), the
 * caller falls back to the text.
 */

export const GrepHits = memo(function GrepHits({ details, onOpenFile }: {
  details: NonNullable<ReturnType<typeof grepDetails>>
  onOpenFile?: (path: string) => void
}) {
  const { t } = useI18n()
  if (details.mode === 'content' && details.hits?.length) {
    return (
      <div className="hits" data-testid="grep-hits">
        {details.hits.map((hit, index) => (
          <button
            type="button"
            key={`${hit.path}:${hit.line}:${index}`}
            className="hit"
            data-testid="grep-hit"
            title={hit.path}
            disabled={!onOpenFile}
            onClick={() => onOpenFile?.(hit.path)}
          >
            <span className="hit-path">{hit.path}</span>
            <span className="hit-line">{hit.line}</span>
            <span className="hit-text">{hit.text}</span>
          </button>
        ))}
        <div className="hits-meta">{t('tool.hitsMeta', { shown: details.hits.length, matches: formatCount(details.matches), files: details.files })}</div>
      </div>
    )
  }
  if (details.mode === 'count' && details.counts?.length) {
    return (
      <div className="hits" data-testid="grep-hits">
        {details.counts.map(row => (
          <button type="button" key={row.path} className="hit" disabled={!onOpenFile} onClick={() => onOpenFile?.(row.path)}>
            <span className="hit-path">{row.path}</span>
            <span className="hit-count">{row.count}</span>
          </button>
        ))}
        <div className="hits-meta">{t('tool.hitsMeta', { shown: details.counts.length, matches: formatCount(details.matches), files: details.files })}</div>
      </div>
    )
  }
  return null
})

export const PathList = memo(function PathList({ paths, meta, testId, onOpenFile }: {
  paths: string[]
  meta: string
  testId: string
  onOpenFile?: (path: string) => void
}) {
  return (
    <div className="hits" data-testid={testId}>
      {paths.map(path => (
        <button type="button" key={path} className="hit" title={path} disabled={!onOpenFile} onClick={() => onOpenFile?.(path)}>
          <span className="hit-path">{path}</span>
        </button>
      ))}
      <div className="hits-meta">{meta}</div>
    </div>
  )
})

export const PathsFromGlob = memo(function PathsFromGlob({ details, onOpenFile }: {
  details: NonNullable<ReturnType<typeof globDetails>>
  onOpenFile?: (path: string) => void
}) {
  const { t } = useI18n()
  if (!details.paths?.length) return null
  const meta = t('tool.globMeta', { shown: details.paths.length, files: details.files, limit: details.limit })
  return <PathList paths={details.paths} meta={meta} testId="glob-paths" onOpenFile={onOpenFile} />
})
