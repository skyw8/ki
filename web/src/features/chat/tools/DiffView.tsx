import { memo, useMemo, useState } from 'react'
import { parseDiff, Diff, Hunk, type FileData } from 'react-diff-view'
import { useI18n } from '../../../i18n/index'
import { useTranscriptRowMeasurement } from '../TranscriptRow'
import { copyText } from '../../../lib/clipboard'
import { ICopy } from '../../../components/icons'
import { diffStat, type DiffFile } from './parse'
import 'react-diff-view/style/index.css'

const MAX_HUNKS = 40
const CONTEXT = 3

export type DiffViewProps = {
  files: DiffFile[]
  onOpenFile?: (path: string) => void
  testId?: string
}

export const DiffView = memo(function DiffView({ files, onOpenFile, testId }: DiffViewProps) {
  const { t } = useI18n()
  const stat = useMemo(() => diffStat(files), [files])
  return (
    <div className="diffs" data-testid={testId ?? 'diff-view'}>
      {files.map((file, index) => <FileDiff key={`${file.path}-${index}`} file={file} onOpenFile={onOpenFile} />)}
      {files.length > 1 ? (
        <div className="diff-total" data-testid="diff-total">
          {t('tool.filesChanged', { n: files.length })} · <span className="diff-add">+{stat.added}</span> <span className="diff-del">−{stat.removed}</span>
        </div>
      ) : null}
    </div>
  )
})

const FileDiff = memo(function FileDiff({ file, onOpenFile }: { file: DiffFile; onOpenFile?: (path: string) => void }) {
  const { t } = useI18n()
  const [expanded, setExpanded] = useState(false)
  const parsed = useMemo(() => parse(file.diff), [file.diff])
  const stat = useMemo(() => diffStat([file]), [file])
  useTranscriptRowMeasurement([expanded, file.diff])

  const hidden = parsed.omitted ? parsed.hunks.slice(expanded ? 0 : 0) : parsed.hunks
  const shown = expanded ? hidden : capHunks(hidden)
  const folded = parsed.omitted || shown.length < hidden.length
  const name = file.movePath ? `${file.path} → ${file.movePath}` : file.path
  return (
    <div className="diff-file" data-testid="diff-file" data-path={file.path}>
      <div className="diff-head">
        <button
          type="button"
          className="diff-path"
          title={onOpenFile ? t('tool.openFile') : name}
          onClick={() => onOpenFile?.(file.path)}
          disabled={!onOpenFile}
        >
          {name}
        </button>
        <span className={`diff-kind ${file.kind}`} data-testid="diff-kind">{t(`tool.kind.${file.kind}` as 'tool.kind.update')}</span>
        <span className="diff-stat">
          <span className="diff-add">+{stat.added}</span>
          <span className="diff-del">−{stat.removed}</span>
        </span>
        <button type="button" className="diff-copy" aria-label={t('tool.copyDiff')} title={t('tool.copyDiff')} onClick={() => void copyText(file.diff)}>
          <ICopy />
        </button>
      </div>
      {shown.length ? (
        <Diff
          viewType="unified"
          diffType={parsed.type}
          hunks={shown}
          gutterType="default"
          className="diff-body"
        >
          {hunks => hunks.map(hunk => <Hunk key={hunk.content} hunk={hunk} />)}
        </Diff>
      ) : (
        <div className="diff-empty">{t('tool.noDiffHunks')}</div>
      )}
      {folded ? (
        <button type="button" className="diff-more" data-testid="diff-more" onClick={() => setExpanded(true)}>
          {t('tool.showWholeDiff', { n: parsed.totalLines })}
        </button>
      ) : null}
    </div>
  )
})

type Parsed = {
  type: 'add' | 'delete' | 'modify' | 'rename' | 'copy'
  hunks: FileData['hunks']
  omitted: boolean
  totalLines: number
}

const cache = new Map<string, Parsed>()
const CACHE_LIMIT = 64

function parse(diff: string): Parsed {
  const hit = cache.get(diff)
  if (hit) return hit
  let parsed: Parsed
  try {
    const files = parseDiff(diff)
    const file = files[0]
    parsed = {
      type: file?.type ?? 'modify',
      hunks: file?.hunks ?? [],
      omitted: false,
      totalLines: diff.split('\n').length,
    }
  } catch {
    parsed = { type: 'modify', hunks: [], omitted: false, totalLines: diff.split('\n').length }
  }
  if (cache.size >= CACHE_LIMIT) cache.delete(cache.keys().next().value as string)
  cache.set(diff, parsed)
  return parsed
}

function capHunks(hunks: FileData['hunks']): FileData['hunks'] {
  if (hunks.length <= MAX_HUNKS) return hunks
  const head = hunks.slice(0, MAX_HUNKS - 4)
  return [...head, ...hunks.slice(-4)]
}

export function contextRunLengths(diff: string): number[] {
  const runs: number[] = []
  let run = 0
  for (const line of diff.split('\n').slice(4)) {
    if (line.startsWith(' ') || line === '') {
      run++
      continue
    }
    if (run > CONTEXT * 2) runs.push(run)
    run = 0
  }
  return runs
}
