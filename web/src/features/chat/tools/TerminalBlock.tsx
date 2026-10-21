import { memo, useEffect, useRef, useState } from 'react'
import Anser from 'anser'
import { useI18n } from '../../../i18n/index'
import { useTranscriptRowMeasurement } from '../TranscriptRow'
import { formatBytes } from './format'
import type { ExecSnapshot } from './parse'

/**
 * A read-only terminal card.
 *
 * Output is kept byte-faithful: `white-space: pre` with horizontal scrolling,
 * because wrapping destroys the column layout of test output, `ps`, and
 * tables. Only when the caller is on a narrow viewport does the component fall
 * back to wrapping, where a horizontal scroll would otherwise be unusable.
 *
 * The window is tail-anchored while a command runs (the newest output is what
 * the reader wants) and head-anchored once it settles or after the reader
 * scrolls up, so a streaming command does not fight someone reading history.
 */

const ANSI = /\x1b\[[0-9;]*[A-Za-z]/
const MAX_RENDERED_LINES = 2000

export type TerminalBlockProps = {
  snapshot: ExecSnapshot
  /** Command text for the prompt row; falls back to the snapshot's own. */
  cmd?: string
  /** TTY input echoed by write_stdin. */
  input?: string
  running?: boolean
  onOpenFile?: (path: string) => void
  testId?: string
}

export const TerminalBlock = memo(function TerminalBlock({
  snapshot, cmd, input, running, onOpenFile, testId,
}: TerminalBlockProps) {
  const { t } = useI18n()
  const output = snapshot.output
  const lines = output ? output.split('\n') : []
  const clipped = lines.length > MAX_RENDERED_LINES
  const shown = clipped ? lines.slice(-MAX_RENDERED_LINES).join('\n') : output
  const [tail, setTail] = useState(true)
  const scroller = useRef<HTMLPreElement | null>(null)
  useTranscriptRowMeasurement([shown, tail, running])

  // Follow the tail only while running and only while the reader has not
  // scrolled away; one upward scroll pins the window until they return.
  useEffect(() => {
    if (!running || !tail) return
    const element = scroller.current
    if (element) element.scrollTop = element.scrollHeight
  }, [shown, running, tail])

  const exit = snapshot.exitCode
  const state = snapshot.status === 'running' ? 'running' : exit !== undefined && exit !== 0 ? 'failed' : 'ok'
  return (
    <div className="term" data-testid={testId ?? 'terminal-block'} data-state={state}>
      <div className="term-bar">
        {input ? <span className="term-prompt" aria-hidden>»</span> : <span className="term-prompt" aria-hidden>$</span>}
        <span className="term-cmd" data-testid="terminal-cmd">{input || cmd || snapshot.cmd}</span>
      </div>
      <div className="term-meta" data-testid="terminal-meta">
        {snapshot.workdir ? <span className="term-meta-item" title={snapshot.workdir}>{snapshot.workdir}</span> : null}
        <span className="term-meta-item">{snapshot.tty ? 'TTY' : 'Pipe'}</span>
        {snapshot.sessionID !== undefined ? <span className="term-meta-item">#{snapshot.sessionID}</span> : null}
        <span className={`term-exit ${state}`} data-testid="terminal-exit">
          {state === 'running' ? t('tool.running') : exit === undefined ? t('tool.noExitCode') : t('tool.exitCode', { code: exit })}
        </span>
        {snapshot.truncated ? <span className="term-meta-item warn">{t('tool.outputTruncated')}</span> : null}
        {clipped ? <span className="term-meta-item warn">{t('tool.showingTail', { n: MAX_RENDERED_LINES })}</span> : null}
      </div>
      {shown ? (
        <pre
          ref={scroller}
          className="term-out"
          data-testid="terminal-output"
          onScroll={event => {
            const element = event.currentTarget
            const atBottom = element.scrollHeight - element.scrollTop - element.clientHeight <= 24
            setTail(atBottom)
          }}
        >
          {ANSI.test(shown) ? <AnsiText text={shown} /> : shown}
        </pre>
      ) : null}
      {snapshot.outputFile && onOpenFile ? (
        <button
          type="button"
          className="term-full"
          data-testid="terminal-full-output"
          onClick={() => onOpenFile(snapshot.outputFile!)}
        >
          {t('tool.fullOutput', { size: formatBytes(snapshot.totalBytes ?? 0) })}
        </button>
      ) : null}
    </div>
  )
})

/**
 * ANSI SGR only. Cursor movement and screen clearing have no meaning in a
 * captured pipe, and interpreting them would corrupt the scrollback that the
 * reader is trying to read.
 */
const AnsiText = memo(function AnsiText({ text }: { text: string }) {
  const spans = Anser.ansiToJson(text, { json: true, remove_empty: true, use_classes: false })
  return (
    <>
      {spans.map((span, index) => {
        const color = span.fg ? `#${span.fg}` : undefined
        const background = span.bg ? `#${span.bg}` : undefined
        const decoration = span.decorations?.length ? span.decorations.join(' ') : undefined
        if (!color && !background && !decoration) return span.content
        return (
          <span
            key={index}
            style={{
              color,
              background,
              textDecoration: decoration?.includes('underline') ? 'underline' : undefined,
              fontWeight: decoration?.includes('bold') ? 600 : undefined,
              fontStyle: decoration?.includes('italic') ? 'italic' : undefined,
              opacity: decoration?.includes('dim') ? 0.7 : undefined,
            }}
          >
            {span.content}
          </span>
        )
      })}
    </>
  )
})
