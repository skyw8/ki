import { canonicalToolName } from '../../lib/toolname'
import { TerminalBlock } from './tools/TerminalBlock'
import { DiffView } from './tools/DiffView'
import { ReadBlock } from './tools/ReadBlock'
import { GrepHits, PathsFromGlob } from './tools/SearchResults'
import { AgentList, AgentReceipt } from './tools/Receipts'
import { CodeModeResult } from './tools/CodeModeResult'
import { diffFiles, execSnapshotFromProcess, execSnapshotOf, globDetails, grepDetails, readDetails, syntheticEdit, writeDetails, writeDiff } from './tools/parse'
import { Fragment, memo, useCallback, useEffect, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from 'react'
import { flushSync } from 'react-dom'
import { observeElementOffset, useVirtualizer } from '@tanstack/react-virtual'
import { useTranscriptScroll, type TranscriptScroll } from './useTranscriptScroll'
import { useNow } from '../../hooks/useNow'
import { Duration } from './Duration'
import { TranscriptRow, useTranscriptRowMeasurement } from './TranscriptRow'
import { IChev, IChevDown, IClock, ICompact, ICopy, IEdit, IFork, IRegen, IContext, IWrench, IQuestion } from '../../components/icons'
import { IFile } from '../../components/icons'
import { Composer, type Draft } from './Composer'
import { AttachmentImage } from '../attachments/AttachmentImage'
import type { Client } from '../../api/client'
import { useI18n } from '../../i18n/index'
import { Markdown } from '../markdown/Markdown'
import { cacheHitRate, cacheMisses, formatDuration, formatTokens, formatTokensPerSecond, isHumanPrompt, nodeLive, projectTurnStats, reconcileUserNodes, requestTitle, type TurnStats } from '../../lib/model'
import { DEFAULT_COMPACT_KEEP, detailedItems, foldReplies, groupTurns, type ChatRenderItem, type ChatTurn, type MessageViewMode } from '../../lib/messageView'
import { COLLAPSED_TOOL_HEIGHT, rememberRowHeight, rowBodyToken, rowHeightEstimate, sameRowRevision, UNKNOWN_WIDTH, type RowHeightRevision } from '../../lib/rowHeight'
import { copyText } from '../../lib/clipboard'
import type { CacheMiss, ChatNode, CompactTurn, ProcessSnapshot } from '../../api/types'

const OVERSCAN = 4

const RECEIPT_TOOLS = new Set(['spawn_agent', 'send_message', 'followup_task', 'wait_agent', 'interrupt_agent', 'list_agents'])

function fmtUsage(u: { input?: number; output?: number; cacheRead?: number; cacheWrite?: number }): string {
  let s = `${u.input ?? 0}→${u.output ?? 0}`
  if (u.cacheRead) s += ` cache ${u.cacheRead}`
  if (u.cacheWrite) s += ` +${u.cacheWrite}`
  return s
}

function fmtCost(total: number): string {
  return `$${total < .01 ? total.toFixed(4) : total.toFixed(2)}`
}

function fmtTs(ts?: number): string {
  if (!ts) return ''
  const d = new Date(ts)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

function fmtFileSize(size?: number): string {
  if (size == null) return ''
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${Math.ceil(size / 1024)} KB`
  return `${(size / 1024 / 1024).toFixed(1)} MB`
}

function Think({ text, streaming }: { text: string; streaming?: boolean }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  useTranscriptRowMeasurement([text, open, streaming])
  const first = text.split('\n').find(l => l.trim()) ?? ''
  return (
    <div className="think">
      <button type="button" className="think-btn" onClick={() => setOpen(v => !v)}>
        <span className="think-head">
          <IChev open={open} />
          <span className="think-label">{streaming ? t('chat.thinkingNow') : t('chat.thinking')}</span>
        </span>
        {!open && first ? <span className="think-preview">{first}</span> : null}
      </button>
      {open ? <div className="think-body">{text}</div> : null}
    </div>
  )
}

function IconBtn({
  label, testid, onClick, disabled, children,
}: {
  label: string
  testid?: string
  onClick?: () => void
  disabled?: boolean
  children: ReactNode
}) {
  return (
    <button
      type="button"
      className={`msg-icon${disabled ? ' disabled' : ''}`}
      data-disabled={disabled || undefined}
      aria-label={label}
      data-testid={testid}
      onClick={e => { e.stopPropagation(); onClick?.() }}
    >
      {children}
    </button>
  )
}

function argStr(args: unknown, key: string): string {
  if (!args || typeof args !== 'object') return ''
  const v = (args as Record<string, unknown>)[key]
  return v == null ? '' : String(v)
}

function firstLine(s: string): string {
  return s.split('\n').find(l => l.trim()) ?? s
}

function prettyArgs(args: unknown): string {
  if (args == null) return ''
  if (typeof args === 'string') return args
  return JSON.stringify(args, null, 2)
}

function useBodyLoad(id: string, onHydrate?: (id: string) => Promise<boolean>) {
  const [loading, setLoading] = useState(false)
  const [failed, setFailed] = useState(false)
  const load = async () => {
    if (!onHydrate || loading) return
    setLoading(true)
    setFailed(false)
    const ok = await onHydrate(id)
    setLoading(false)
    setFailed(!ok)
  }
  return { loading, failed, load }
}

function UserBubble({ api, node, onHydrate }: { api: Client; node: Extract<ChatNode, { kind: 'user' }>; onHydrate?: (id: string) => Promise<boolean> }) {
  const { t } = useI18n()
  const ref = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const [overflow, setOverflow] = useState(false)
  const body = useBodyLoad(node.id, onHydrate)
  useTranscriptRowMeasurement([node, open, overflow, body.loading, body.failed])
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    if (open) {
      setOverflow(false)
      return
    }
    const measure = () => setOverflow(el.scrollHeight > el.clientHeight + 1)
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    return () => observer.disconnect()
  }, [node.text, open])
  const runtime = !isHumanPrompt(node.origin)
  const folded = !open
  const images = node.content.filter(c => c.type === 'image')
  const files = node.content.filter(c => c.type === 'file' || c.type === 'workspace_file')
  const imageLayout = images.length <= 4 ? `count-${images.length}` : 'count-many'
  return (
    <div className={`user-message${runtime ? ' runtime-notification' : ''}${node.origin ? ' origin-external' : ''}${node.origin?.startsWith('agent:') ? ' origin-agent' : ''}`} data-testid="user-bubble" role={runtime ? 'note' : undefined} aria-label={runtime ? t('chat.runtimeNotification') : undefined}>
      {node.questionReply ? <div className="question-reply-label"><IQuestion /><strong>{node.questionReply.title}</strong></div> : null}
      {runtime ? <div className="runtime-notification-label">{t('chat.runtimeNotification')}</div> : null}
      {node.origin ? <div className="user-origin" data-testid="user-origin">{node.origin}</div> : null}
      {images.length ? <div className={`message-images ${imageLayout}`}>
        {images.map((c, i) => <AttachmentImage api={api} content={c} className="message-image" expandable key={`${c.path || c.name}-${i}`} />)}
      </div> : null}
      {files.length ? <div className="message-files">
		{files.map((c, i) => <span className="message-file" key={`${c.path || c.name}-${i}`} title={c.path}><span className="message-file-icon"><IFile /></span><span className="message-file-copy"><strong>{c.name || c.path}</strong>{c.size != null ? <small>{fmtFileSize(c.size)}</small> : null}</span></span>)}
	  </div> : null}
      {node.text ? <div className="user-text-bubble">
        <div ref={ref} className={`bubble-text${folded ? ' clamped' : ''}`}>{node.text}</div>
        {overflow || open || node.truncated ? (
          <button
            type="button"
            className="bubble-toggle"
            data-testid="user-bubble-toggle"
            aria-label={open ? t('chat.collapse') : t('chat.expand')}
            title={open ? t('chat.collapse') : t('chat.expand')}
            onClick={() => {
              if (!open && node.truncated) void body.load()
              setOpen(v => !v)
            }}
          >
            <IChevDown className={open ? 'up' : undefined} />
          </button>
        ) : null}
      </div> : null}
      {node.truncated && (open || !node.text) ? <button type="button" className="body-load" disabled={body.loading} onClick={() => void body.load()}>{t(body.loading ? 'chat.loadingBody' : body.failed ? 'chat.retryOlder' : 'chat.loadBody')}</button> : null}
    </div>
  )
}

function ToolRow({
  node, title, summary, processes, open, onToggle, onInspect, onHydrate, onOpenFile,
}: {
  node: Extract<ChatNode, { kind: 'tool' }>
  title: string
  summary: string
  processes?: ProcessSnapshot[]
  open: boolean
  onToggle: () => void
  onInspect?: (n: ChatNode) => void
  onHydrate?: (id: string) => Promise<boolean>
  onOpenFile?: (path: string) => void
}) {
  const { t } = useI18n()
  const name = canonicalToolName(node.name)
  const body = useBodyLoad(node.id, onHydrate)
  useTranscriptRowMeasurement([node, open, body.loading, body.failed])
  const cmd = argStr(node.args, 'cmd')
  const desc = argStr(node.args, 'description')
  const oldS = argStr(node.args, 'old_string')
  const newS = argStr(node.args, 'new_string')
  const content = argStr(node.args, 'content')
  const offset = Number(argStr(node.args, 'offset') || '1') || 1
  const live = name === 'exec_command' || name === 'write_stdin'
    ? processes?.find(p => p.tool_call_id === node.id) ?? undefined
    : undefined
  const terminal = live ? execSnapshotFromProcess(live) : execSnapshotOf(node.details, node.result ?? '')
  const patchDiffs = diffFiles(node.details)
  const written = name === 'write' ? writeDetails(node.details) : null
  const grep = name === 'grep' ? grepDetails(node.details) : null
  const globbed = name === 'glob' ? globDetails(node.details) : null
  const read = name === 'read' ? readDetails(node.details) : null
  const receipt = RECEIPT_TOOLS.has(name)
  const CellResult = name === 'exec' || name === 'wait' ? CodeModeResult : null
  const editDiff = patchDiffs.length > 0 ? patchDiffs[0].diff : ''
  const patchDiff = name === 'apply_patch' && patchDiffs.length > 0 ? patchDiffs.map(file => file.diff).join('\n') : ''
  const state = node.running ? 'running' : node.isError ? 'error' : 'ok'
  const now = useNow(!!node.running)
  const fail = state === 'error' && node.result && !terminal ? firstLine(node.result) : ''
  const line = fail || summary
  const bodyIn = name === 'write' ? content : name === 'exec_command' ? cmd : prettyArgs(node.args)
  const structured = !!terminal || patchDiffs.length > 0 || !!grep || !!globbed || receipt || !!CellResult
  const expandable = !!(node.truncated || node.result || bodyIn || oldS || newS || desc || editDiff || patchDiff || structured)
  return (
    <div className={`tool-row${node.isError ? ' error' : ''}`} data-testid="tool-card" data-tool={name} data-state={state}>
      <div className="tool-row-h">
        <button
          type="button"
          className="tool-row-toggle"
          aria-expanded={open}
          aria-label={t(open ? 'chat.collapseTool' : 'chat.expandTool', { name: title })}
          title={title}
          disabled={!expandable}
          onClick={() => {
            if (!expandable) return
            if (!open && node.truncated) void body.load()
            onToggle()
          }}
        >
          {expandable ? <IChev open={open} /> : null}
          {state === 'error' ? <span className="state-dot err" /> : node.running ? <span className="spin" /> : <IWrench />}
          <span className="tool-name" title={title}>{title}</span>
        </button>
        {line ? <span className="tool-sep" aria-hidden /> : null}
        {line ? <span className={`tool-preview${fail ? ' err' : ''}`} data-testid="tool-preview" title={line}>{line}</span> : null}
        {terminal && terminal.status !== 'running' && terminal.exitCode !== undefined ? (
          <span className={`tool-exit${terminal.exitCode === 0 ? '' : ' err'}`} data-testid="tool-exit">{terminal.exitCode}</span>
        ) : null}
        {node.running && node.startedAt ? <span className="tool-duration live" data-testid="tool-duration"><Duration ms={Math.max(0, now - node.startedAt)} /></span> : null}
        {!node.running && node.durationMs != null ? <span className="tool-duration" data-testid="tool-duration"><Duration ms={node.durationMs} /></span> : null}
      </div>
      {open && expandable ? (
        <div className="tool-row-body">
          {node.truncated ? <button type="button" className="body-load" disabled={body.loading} onClick={() => void body.load()}>{t(body.loading ? 'chat.loadingBody' : body.failed ? 'chat.retryOlder' : 'chat.loadBody')}</button> : null}
          {desc ? <div className="tool-desc" data-testid="tool-desc">{desc}</div> : null}
          {terminal ? (
            <TerminalBlock
              snapshot={terminal}
              cmd={cmd || desc}
              input={name === 'write_stdin' ? argStr(node.args, 'chars') : undefined}
              running={node.running}
              onOpenFile={onOpenFile}
            />
          ) : name === 'list_agents' ? (
            <AgentList details={node.details} />
          ) : receipt ? (
            <AgentReceipt details={node.details} />
          ) : CellResult ? (
            <CellResult result={node.result ?? ''} />
          ) : name === 'read' && read?.truncation ? (
            <ReadBlock text={node.result ?? ''} details={read.truncation} offset={offset} />
          ) : name === 'read' && node.result ? (
            <pre className="tool-read">{node.result.split('\n').map((ln, i) => `${String(offset + i).padStart(4, ' ')}  ${ln}`).join('\n')}</pre>
          ) : grep ? (
            <GrepHits details={grep} onOpenFile={onOpenFile} />
          ) : globbed ? (
            <PathsFromGlob details={globbed} onOpenFile={onOpenFile} />
          ) : name === 'write' && content && written ? (
            <DiffView files={[writeDiff(written, content)]} onOpenFile={onOpenFile} />
          ) : patchDiffs.length ? (
            <DiffView files={patchDiffs} onOpenFile={onOpenFile} />
          ) : name === 'edit' && (oldS || newS) ? (
            <DiffView files={[syntheticEdit(node.args, oldS, newS)]} onOpenFile={onOpenFile} />
          ) : (
            <div className="io-card">
              {bodyIn ? <div className="io-sec"><span className="io-lab">IN</span><span className="io-txt">{bodyIn}</span></div> : null}
              {bodyIn && node.result ? <span className="io-div" /> : null}
              {node.result ? <div className="io-sec"><span className="io-lab">OUT</span><span className={`io-txt${node.isError ? ' err' : ''}`}>{node.result}</span></div> : null}
            </div>
          )}
          {onInspect ? (
            <button type="button" className="insp-pill" data-testid="tool-inspect" onClick={() => onInspect(node)}>{t('chat.inspect')}</button>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}

function Compaction({ node, onHydrate, hydrating, hydrateFailed }: {
  node: Extract<ChatNode, { kind: 'compaction' }>
  onHydrate: () => Promise<boolean | undefined>
  hydrating: boolean
  hydrateFailed: boolean
}) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  useTranscriptRowMeasurement([node, open])
  const label = hydrating ? t('chat.loadingBody') : hydrateFailed ? t('chat.retryOlder') : node.running
    ? t('chat.compacting')
    : node.unknown
      ? t('chat.compactionUnknown')
      : node.empty
        ? t('chat.nothingToCompact')
        : node.failed
          ? t('chat.compactFailed')
          : t('chat.compacted')
  const state = node.running ? ' running' : node.empty ? ' empty' : node.failed ? ' failed' : ''
  return (
    <div className="compact-row" data-testid="compact-row">
      <button
        type="button"
        className={`compact-btn${state}`}
        data-testid="compact-btn"
        aria-live={node.running ? 'polite' : undefined}
        disabled={node.running || hydrating}
        onClick={async () => {
          if (node.truncated && !await onHydrate()) return
          setOpen(v => !v)
        }}
      >
        {node.running ? <span className="compact-spin" aria-hidden /> : <ICompact />}
        {label}
        {!node.running && !node.empty && !node.failed && node.tokensBefore ? <span>· {t('chat.compactedTokens', { n: node.tokensBefore })}</span> : null}
      </button>
      {open && node.summary ? <div className="compact-body">{node.summary}</div> : null}
    </div>
  )
}

function Cancellation({ node }: { node: Extract<ChatNode, { kind: 'cancellation' }> }) {
  const { t } = useI18n()
  const label = node.reason === 'user_request' ? t('chat.cancelled.user')
    : node.reason === 'extension_request' ? t('chat.cancelled.extension')
    : node.reason === 'session_delete' ? t('chat.cancelled.sessionDelete')
    : node.reason === 'server_shutdown' ? t('chat.cancelled.serverShutdown')
    : t('chat.cancelled.unknown')
  return (
    <div className="notice" data-testid="cancel-row" data-cancel-id={node.id} data-run-id={node.runId} role="status" aria-live="polite">
      {label}
    </div>
  )
}

function TurnDivider({ stats }: { stats: TurnStats }) {
  const { t } = useI18n()
  const live = stats.live
  const now = useNow(live)
  const elapsedMs = live && stats.startedAt != null && stats.startedAt > 0 ? Math.max(0, now - stats.startedAt) : stats.elapsedMs
  const prompt = stats.input
  const hit = prompt > 0 && stats.cacheRead > 0 ? stats.cacheRead / prompt * 100 : null
  const ttftMs = stats.ttftMs
  const tps = stats.tps
  const cells: ReactNode[] = [
    <span className={`turn-stat${live ? ' live' : ''}`} key="elapsed" data-testid="turn-elapsed"><IClock /><Duration ms={elapsedMs} label="elapsed" /></span>,
  ]
  if (stats.steps > 0) cells.push(<span className="turn-stat" key="steps">{t('turn.steps', { n: stats.steps })}</span>)
  if (stats.tools > 0) {
    cells.push(
      <span
        className={`turn-stat${stats.toolFailures > 0 ? ' turn-stat-err' : ''}`}
        key="tools"
        data-testid="turn-tools"
        title={t('turn.toolsTitle', { failed: stats.toolFailures, total: stats.tools })}
      >
        <IWrench />
        {t('turn.tools', { failed: stats.toolFailures, total: stats.tools })}
      </span>,
    )
  }
  if (ttftMs > 0) cells.push(<span className="turn-stat" key="ttft">{t('stats.ttft', { duration: formatDuration(ttftMs) })}</span>)
  if (tps != null) cells.push(<span className="turn-stat" key="tps">{t('stats.tps', { tps: formatTokensPerSecond(tps) })}</span>)
  if (hit != null) cells.push(<span className="turn-stat" key="cache">{t('stats.cacheHit', { percent: hit.toFixed(2) })}</span>)
  if (stats.steps > 0) {
    cells.push(
      <span
        className={`turn-stat${stats.cacheMisses > 0 ? ' turn-stat-warn' : ''}`}
        key="miss"
        data-testid="turn-cache-miss"
        title={t('turn.cacheMissTitle', { n: stats.cacheMisses })}
      >
        {t('turn.cacheMiss', { n: stats.cacheMisses })}
      </span>,
    )
  }
  return (
    <div className={`turn-end${live ? ' live' : ''}`} data-testid="turn-divider" data-turn={stats.turn} data-turn-id={stats.turnId} data-live={live || undefined}>
      <div className="turn-end-row">
        <span className="turn-end-rule" aria-hidden />
        <span className="turn-end-label">
          {live ? <span className="turn-live-dot" aria-hidden /> : null}
          {t('turn.label', { n: stats.turn })}
        </span>
        {cells}
        <span className="turn-end-rule" aria-hidden />
      </div>
    </div>
  )
}

function nodePreview(n: ChatNode): string {
  if (n.kind === 'user' || n.kind === 'assistant') return n.text
  if (n.kind === 'tool') return n.name
  if (n.kind === 'compaction') return n.summary
  return ''
}

function FoldRow({ turn, nodes, expanded, onToggle, count, preview: summaryPreview, firstHiddenId, loading, failed }: {
  turn: ChatTurn
  nodes: ChatNode[]
  expanded: boolean
  onToggle: () => void
  count: number
  preview?: string
  firstHiddenId?: string
  loading?: boolean
  failed?: boolean
}) {
  const { t } = useI18n()
  const preview = nodes.length ? nodePreview(nodes[nodes.length - 1]) : summaryPreview ?? ''
  return (
    <div className={`fold-row${expanded ? ' expanded' : ''}`} data-testid="fold-row" data-fold={turn.id} data-msg-id={firstHiddenId ?? nodes[0]?.id}>
      <button
        type="button"
        className="fold-row-btn"
        data-testid="fold-row-btn"
        aria-expanded={expanded}
        disabled={loading}
        aria-label={expanded ? t('chat.collapse') : t('chat.expand')}
        onClick={onToggle}
      >
        <IChev open={expanded} />
        <span className="fold-row-count">{loading ? t('chat.loadingBody') : failed ? t('chat.retryOlder') : t('chat.foldCount', { n: count })}</span>
        {!expanded && preview ? <span className="fold-row-preview">{preview}</span> : null}
      </button>
    </div>
  )
}

type ChatItemProps = {
	api: Client
	node: ChatNode
	busy: boolean
	uploading?: boolean
	onSelect?: (n: ChatNode) => void
	edit?: { messageId: string; draft: Draft } | null
	onStartEdit?: (n: Extract<ChatNode, { kind: 'user' }>) => void
	onEditChange?: (draft: Draft) => void
	onCancelEdit?: () => void
	onSendEdit?: () => void
	onAttachEdit?: () => void
	onFilesEdit?: (files: File[]) => void
	onFork?: (n: Extract<ChatNode, { kind: 'assistant' }>) => void
	onRegen?: (n: Extract<ChatNode, { kind: 'assistant' }>) => void
	branchIndex?: number
	branchTotal?: number
	onBranch?: (n: Extract<ChatNode, { kind: 'user' }>, delta: number) => void
	onShowBranches?: () => void
	onHydrate?: (id: string) => Promise<boolean>
	processes?: ProcessSnapshot[]
	openTools: ReadonlySet<string>
	onToggleTool: (id: string) => void
	onOpenFile?: (path: string) => void
	missed?: CacheMiss
}

const ChatItem = memo(function ChatItem({
	api, node: n, busy, uploading, onSelect, edit, onStartEdit, onEditChange, onCancelEdit, onSendEdit, onAttachEdit, onFilesEdit, onFork, onRegen, branchIndex, branchTotal, onBranch, onHydrate, processes, openTools, onToggleTool, onOpenFile, onShowBranches, missed,
}: ChatItemProps) {
  const { t } = useI18n()
  const bodyRef = useRef<HTMLDivElement>(null)
  const [hydrating, setHydrating] = useState(false)
  const [hydrateFailed, setHydrateFailed] = useState(false)
  useTranscriptRowMeasurement([n, hydrating, hydrateFailed, edit])
  const hydrate = useCallback(async () => {
    if (!onHydrate) return
    setHydrating(true)
    const ok = await onHydrate(n.id)
    setHydrating(false)
    setHydrateFailed(!ok)
    return ok
  }, [n.id, onHydrate])
  useEffect(() => {
    const el = bodyRef.current
    if (!el || !n.truncated || n.kind !== 'assistant' || n.streaming || hydrateFailed) return
    let timer = 0
    const observer = new IntersectionObserver(entries => {
      window.clearTimeout(timer)
      if (entries.some(entry => entry.isIntersecting)) timer = window.setTimeout(() => { observer.disconnect(); void hydrate() }, 100)
    }, { root: el.closest('.scroll') })
    observer.observe(el)
    return () => { observer.disconnect(); window.clearTimeout(timer) }
  }, [n.id, n.truncated, n.kind, n.kind === 'assistant' && n.streaming, hydrate, hydrateFailed])
  if (n.kind === 'user') {
    const editing = edit?.messageId === n.id
    const human = isHumanPrompt(n.origin)
    return (
      <div className={`user-row${human ? '' : ' runtime-row'}${editing ? ' editing' : ''}`} data-msg-id={n.id}>
        <div className="user-stack">
          {editing ? <Composer api={api} mode="edit" draft={edit!.draft} onChange={d => onEditChange?.(d)} onSend={() => onSendEdit?.()} onAttach={() => onAttachEdit?.()} onFiles={onFilesEdit} onCancel={onCancelEdit} busy={busy} uploading={uploading} /> : <UserBubble api={api} node={n} onHydrate={onHydrate} />}
          <div className="msg-foot">
            {n.ts ? <div className="msg-stats">{fmtTs(n.ts)}</div> : null}
            <div className="msg-actions" data-testid="user-actions">
              <IconBtn label={t('chat.copy')} testid="copy-msg" onClick={() => void copyText(n.text)}><ICopy /></IconBtn>
              {human && branchTotal == null ? <button type="button" className="branch-load" onClick={onShowBranches}>{t('chat.branches')}</button> : null}
              {human && branchTotal && branchTotal > 1 ? <span className="branch-nav"><button type="button" onClick={() => onBranch?.(n, -1)}>‹</button>{(branchIndex ?? 0) + 1} / {branchTotal}<button type="button" onClick={() => onBranch?.(n, 1)}>›</button></span> : null}
              {human ? <IconBtn label={t('chat.edit')} testid="edit-msg" onClick={() => onStartEdit?.(n)}><IEdit /></IconBtn> : null}
            </div>
          </div>
        </div>
      </div>
    )
  }
  if (n.kind === 'assistant') {
    const hasStats = !n.streaming && !!(n.ts || n.latencyMs || n.ttftMs || n.usage)
    const hitRate = cacheHitRate(n.usage)
    const cost = n.usage?.cost
    const errorText = n.stopReason !== 'aborted' ? n.error : undefined
    return (
      <div ref={bodyRef} className="asst" data-testid="assistant-message">
        <div className="asst-body">
          {n.thinking ? <Think text={n.thinking} streaming={n.streaming} /> : null}
          {n.images?.map((img, i) => (
            <img key={i} className="msg-img" alt="" src={`data:${img.mimeType};base64,${img.data}`} />
          ))}
          {n.text ? <Markdown text={n.text} streaming={n.streaming} revision={n.display} /> : n.streaming && !n.thinking ? <span className="status-line">…</span> : null}
          {n.truncated ? <button type="button" className="body-load" data-testid="load-body" disabled={hydrating} onClick={() => void hydrate()}>{hydrating ? t('chat.loadingBody') : hydrateFailed ? t('chat.retryOlder') : t('chat.loadBody')}</button> : null}
          {errorText ? <div className="notice" data-testid="cancel-notice">{errorText}</div> : null}
        </div>
        {!n.streaming ? (
          <div className="msg-foot">
            {hasStats ? (
              <div className="msg-stats">
                {n.ts ? <span>{fmtTs(n.ts)}</span> : null}
                {n.latencyMs != null ? <span>Ran {(n.latencyMs / 1000).toFixed(2)}s</span> : null}
                {n.ttftMs != null ? <span>TTFT {(n.ttftMs / 1000).toFixed(2)}s</span> : null}
                {n.usage ? <span>{fmtUsage(n.usage)}</span> : null}
                {hitRate != null ? <span data-testid="cache-hit-rate" title={t('stats.cacheHitRate', { percent: hitRate.toFixed(2) })}>{hitRate.toFixed(2)}%</span> : null}
                {cost ? <span>{fmtCost(cost.total)}</span> : null}
                {missed != null ? (
                  <span className="cache-miss" data-testid="cache-miss" title={t('stats.cacheMissTitle', { tokens: formatTokens(missed.missedTokens), percent: Math.round(missed.missRatio * 100) })}>
                    {t('stats.cacheMiss')}
                  </span>
                ) : null}
              </div>
            ) : null}
            <div className="msg-actions" data-testid="asst-actions">
              <IconBtn label={t('chat.copy')} testid="copy-msg" onClick={() => void copyText(n.text || n.thinking || '')}><ICopy /></IconBtn>
              {n.stopReason !== 'toolUse' ? <IconBtn label={t('chat.fork')} testid="fork-msg" onClick={() => onFork?.(n)}><IFork /></IconBtn> : null}
              {n.stopReason !== 'toolUse' ? <IconBtn label={t('chat.regen')} testid="regen-msg" disabled={busy} onClick={() => onRegen?.(n)}><IRegen /></IconBtn> : null}
               <IconBtn label={t('chat.context')} testid="context-msg" onClick={() => onSelect?.(n)}><IContext /></IconBtn>
            </div>
          </div>
        ) : null}
      </div>
    )
  }
  if (n.kind === 'tool') {
    const name = canonicalToolName(n.name)
    const path = argStr(n.args, 'file_path')
    const terminal = name === 'exec_command' || name === 'write_stdin'
      ? execSnapshotOf(n.details, n.result ?? '')
      : null
    const summary = name === 'exec_command'
      ? (argStr(n.args, 'description') || argStr(n.args, 'cmd'))
      : path || firstLine(n.result || prettyArgs(n.args))
    const preview = terminal && n.isError
      ? (terminal.cmd ? `exit ${terminal.exitCode ?? '?'}: ${terminal.cmd}` : firstLine(terminal.output))
      : summary
    return (
      <ToolRow
        node={n}
        title={name}
        summary={preview}
        processes={processes}
        open={openTools.has(n.id)}
        onToggle={() => onToggleTool(n.id)}
        onInspect={onSelect}
        onHydrate={onHydrate}
        onOpenFile={onOpenFile}
      />
    )
  }
  if (n.kind === 'compaction') return <Compaction node={n} onHydrate={hydrate} hydrating={hydrating} hydrateFailed={hydrateFailed} />
  return <Cancellation node={n} />
})

export function ChatView({ api, nodes: rawNodes, busy, uploading, onSelect, edit, onStartEdit, onEditChange, onCancelEdit, onSendEdit, onAttachEdit, onFilesEdit, onFork, onRegen, branches, onBranch, scrollRef, controlRef, onHydrate, processes, onOpenFile, onShowBranches, onVisibleEntries, jumpToId, navigationPending, onJumped, onActiveRequest, onAtBottom, onReadIntent, onLoadOlder, hasMore, olderError, turnBase = 0, mode = 'detailed', keep = DEFAULT_COMPACT_KEEP, loadingOlder, compactTurns, loadedTurnIds, onLoadTurn }: Omit<ChatItemProps, 'node' | 'missed' | 'branchIndex' | 'branchTotal' | 'openTools' | 'onToggleTool'> & {
  branches?: Record<string, { index: number; total: number }>
  nodes: ChatNode[]
  turnBase?: number
  mode?: MessageViewMode
  keep?: number
  scrollRef: RefObject<HTMLDivElement | null>
  controlRef?: React.Ref<TranscriptScroll>
  onVisibleEntries?: (ids: string[]) => void
  jumpToId?: string | null
  navigationPending?: boolean
  onJumped?: () => void
  onActiveRequest?: (id: string | null) => void
  onAtBottom?: (bottom: boolean) => void
  onReadIntent?: () => void
  onLoadOlder?: () => Promise<unknown>
  hasMore?: boolean
  loadingOlder?: boolean
  olderError?: boolean
  compactTurns?: CompactTurn[]
  loadedTurnIds?: string[]
  onLoadTurn?: (id: string) => Promise<boolean>
}) {
  const { t, lang } = useI18n()
  const measurementAnchor = useRef<string | null>(null)
  const selectedRequest = useRef<string | null>(null)
  const nodes = useMemo(() => reconcileUserNodes(rawNodes), [rawNodes])
  const running = busy && !nodes.some(nodeLive)
  const endPadding = running ? 40 : 8
  const navigation = useTranscriptScroll({ scrollRef, initialIntent: navigationPending || jumpToId ? 'reading' : 'following', onAtBottom, onReadIntent: () => { measurementAnchor.current = null; selectedRequest.current = null; onReadIntent?.() }, onLoadOlder, hasMore, loadingOlder, olderError, pageBudget: mode === 'compact' ? 1 : 2, endPadding })
  const misses = useMemo(() => cacheMisses(nodes), [nodes])
  const listRef = useRef<HTMLDivElement>(null)
  const [listWidth, setListWidth] = useState(UNKNOWN_WIDTH)
  useLayoutEffect(() => {
    const el = listRef.current
    if (!el) return
    const ro = new ResizeObserver(() => setListWidth(el.clientWidth))
    setListWidth(el.clientWidth)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  const [openTools, setOpenTools] = useState<ReadonlySet<string>>(() => new Set())
  const toggleTool = useCallback((id: string) => {
    setOpenTools(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])
  const [folds, setFolds] = useState<ReadonlySet<string>>(() => new Set())
  const [loadingFolds, setLoadingFolds] = useState<ReadonlySet<string>>(() => new Set())
  const [failedFolds, setFailedFolds] = useState<ReadonlySet<string>>(() => new Set())
  const foldAnchor = useRef<{ id: string; offset: number } | null>(null)
  const foldFollow = useRef(false)
  const toggleFold = useCallback(async (id: string, preserveJump = false) => {
    selectedRequest.current = null
    const tailTurnId = groupTurns(nodes).at(-1)?.id
    const keepFollowing = navigation.following.current && id === tailTurnId
    if (!preserveJump) onReadIntent?.()
    if (keepFollowing) {
      navigation.latest()
    } else {
      navigation.read()
    }
    if (!folds.has(id) && compactTurns?.some(t => t.id === id && t.hiddenCount > 0) && !loadedTurnIds?.includes(id)) {
      if (loadingFolds.has(id) || !onLoadTurn) return
      setLoadingFolds(prev => new Set([...prev, id]))
      const ok = await onLoadTurn(id)
      setLoadingFolds(prev => new Set([...prev].filter(key => key !== id)))
      setFailedFolds(prev => new Set(ok ? [...prev].filter(key => key !== id) : [...prev, id]))
      if (!ok) return
    }
    const scroll = scrollRef.current
    const row = scroll?.querySelector(`[data-testid="fold-row"][data-fold="${CSS.escape(id)}"]`)
    if (!keepFollowing && scroll && row) foldAnchor.current = { id, offset: row.getBoundingClientRect().top - scroll.getBoundingClientRect().top }
    foldFollow.current = keepFollowing && navigation.following.current
    setFolds(prev => {
      const next = new Set(prev)
      if (preserveJump) next.add(id)
      else if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [scrollRef, navigation.read, navigation.latest, onReadIntent, folds, compactTurns, loadedTurnIds, loadingFolds, onLoadTurn, nodes])
  const items = useMemo(() => mode === 'compact' ? foldReplies(nodes, { keep, expanded: folds, summaries: compactTurns, loadedTurnIds }) : detailedItems(nodes), [mode, nodes, keep, folds, compactTurns, loadedTurnIds])
  const turns = useMemo(() => {
    const stats = projectTurnStats(nodes, turnBase, compactTurns)
    const owner = new Map<string, string>()
    for (const turn of groupTurns(nodes)) for (const node of turn.nodes) owner.set(node.id, turn.id)
    const lastItem = new Map<string, string>()
    for (const item of items) {
      const turn = item.kind === 'fold' ? item.turn.id : owner.get(item.id)
      if (turn) lastItem.set(turn, item.id)
    }
    const out = new Map<string, TurnStats>()
    for (const value of stats.values()) {
      const key = lastItem.get(value.turnId)
      if (key) out.set(key, value)
    }
    return out
  }, [nodes, turnBase, compactTurns, items])
  const revisions = useMemo(() => new Map(items.map(it => {
    const stats = turns.get(it.id)
    const footer = stats && [stats.turn, stats.live, stats.steps, stats.tools, stats.toolFailures, stats.cacheMisses, stats.input, stats.cacheRead, stats.ttftMs, stats.tps]
    return [it.id, {
      body: rowBodyToken(it.kind === 'node' ? it.node : it.turn.user),
      decoration: JSON.stringify([lang, it.kind === 'fold' ? [it.count, it.expanded, !!it.preview] : null, footer, edit?.messageId === it.id, branches?.[it.id]]),
    }]
  })), [items, lang, turns, edit?.messageId, branches])
  const previousItems = useRef(items)
  const previousVirtual = navigation.virtual.current
  if (items !== previousItems.current) {
    if (!navigation.following.current && previousVirtual && items[0]?.id !== previousItems.current[0]?.id) {
      measurementAnchor.current = String(previousVirtual.getVirtualItemForOffset(previousVirtual.scrollOffset ?? 0)?.key ?? '') || null
    }
    previousItems.current = items
  }
  const anchorIndex = measurementAnchor.current ? items.findIndex(item => item.id === measurementAnchor.current) : -1
  const itemChars = useMemo(() => new Map(items.map(it => {
    const n = it.kind === 'node' ? it.node : null
    return [it.id, n?.kind === 'user' ? n.text.length : n?.kind === 'assistant' ? n.text.length + (n.thinking?.length ?? 0) : 0]
  })), [items])
  const itemLookup = useRef(items)
  itemLookup.current = items
  const getItemKey = useCallback((index: number) => {
    const item = itemLookup.current[index]
    return item.kind === 'node' && item.node.kind === 'assistant' ? item.node.renderKey ?? item.id : item.id
  }, [])
  const estimates = useRef(new Map<string, { width: number; revision: RowHeightRevision; size: number }>())
  useLayoutEffect(() => {
    for (const key of estimates.current.keys()) if (!revisions.has(key)) estimates.current.delete(key)
  }, [items.length, items[0]?.id, items.at(-1)?.id])
  const estimateSize = (index: number) => {
    const item = items[index]
    const key = item.id
    const chars = itemChars.get(key) ?? 0
    const revision = revisions.get(key)!
    const cached = estimates.current.get(key)
    if (cached && cached.width === listWidth && sameRowRevision(cached.revision, revision)) return cached.size
    const footer = turns.get(key)
    const collapsedTool = item.kind === 'node' && item.node.kind === 'tool' && !(footer && (footer.steps > 0 || footer.live))
    const size = rowHeightEstimate(key, listWidth, chars, revision, collapsedTool ? COLLAPSED_TOOL_HEIGHT : undefined)
    estimates.current.set(key, { width: listWidth, revision, size })
    return size
  }
  const virtualizer = useVirtualizer({
    count: items.length,
    getScrollElement: () => scrollRef.current,
    scrollToFn: navigation.scrollToFn,
    observeElementOffset: (instance, cb) => observeElementOffset(instance, (offset, scrolling) => {
      const logicalOffset = navigation.observeOffset(offset)
      if (scrolling && !navigation.userScrolling.current) {
        flushSync(() => cb(logicalOffset, false))
      } else cb(logicalOffset, scrolling)
    }),
    getItemKey,
    estimateSize,
    overscan: OVERSCAN,
    paddingStart: 60,
    paddingEnd: endPadding,
    anchorTo: 'end',
    followOnAppend: navigation.following.current,
    scrollEndThreshold: navigation.following.current ? 80 : -1,
    measureElement: (element, entry) => {
      const size = entry?.borderBoxSize?.[0]?.blockSize ?? element.getBoundingClientRect().height
      const key = (element as HTMLElement).dataset.itemKey
      if (key && revisions.has(key) && !element.querySelector('[data-md-pending], .tool-row-body, .think-body, .compact-body, .bubble-text:not(.clamped), .editing')) {
        rememberRowHeight(key, listWidth, size, itemChars.get(key) ?? 0, revisions.get(key)!)
      }
      return size
    },
  })
  navigation.virtual.current = virtualizer
  const measureMountedRow = useCallback((element: HTMLDivElement | null) => {
    virtualizer.measureElement(element)
    if (element && virtualizer.isScrolling) {
      virtualizer.resizeItem(Number(element.dataset.index), virtualizer.options.measureElement(element, undefined, virtualizer))
    }
  }, [virtualizer])
  useLayoutEffect(() => {
    const el = scrollRef.current
    if (!el) return
    for (const node of el.querySelectorAll<HTMLDivElement>('[data-index]')) measureMountedRow(node)
  }, [items, listWidth, lang, measureMountedRow, scrollRef])
  virtualizer.shouldAdjustScrollPositionOnItemSizeChange = (item, delta, instance) => {
    const above = measurementAnchor.current && anchorIndex >= 0
      ? item.index < anchorIndex
      : (instance.itemSizeCache.has(item.key) ? item.end : item.start) <= (instance.scrollOffset ?? 0) + instance.scrollAdjustments
    return delta !== 0 && above && !navigation.deferResize(delta, instance)
  }
  useImperativeHandle(controlRef, () => ({ read: navigation.read, latest: () => {
    selectedRequest.current = null
    measurementAnchor.current = null
    navigation.latest()
  }, preparePrepend: navigation.preparePrepend }), [navigation.read, navigation.latest, navigation.preparePrepend])

  const opened = useRef(false)
  useLayoutEffect(() => {
    if (opened.current || !items.length || !virtualizer.scrollElement) return
    opened.current = true
    if (navigation.following.current) navigation.latest()
  })
  const actions = useRef({ onSelect, onStartEdit, onEditChange, onCancelEdit, onSendEdit, onAttachEdit, onFilesEdit, onFork, onRegen, onBranch, onShowBranches })
  actions.current = { onSelect, onStartEdit, onEditChange, onCancelEdit, onSendEdit, onAttachEdit, onFilesEdit, onFork, onRegen, onBranch, onShowBranches }
  const callbacks = useMemo(() => ({
    onSelect: (...args: Parameters<NonNullable<ChatItemProps['onSelect']>>) => actions.current.onSelect?.(...args),
    onStartEdit: (...args: Parameters<NonNullable<ChatItemProps['onStartEdit']>>) => actions.current.onStartEdit?.(...args),
    onEditChange: (...args: Parameters<NonNullable<ChatItemProps['onEditChange']>>) => actions.current.onEditChange?.(...args),
    onShowBranches: () => actions.current.onShowBranches?.(),
    onCancelEdit: () => actions.current.onCancelEdit?.(),
    onSendEdit: () => actions.current.onSendEdit?.(),
    onAttachEdit: () => actions.current.onAttachEdit?.(),
    onFilesEdit: (...args: Parameters<NonNullable<ChatItemProps['onFilesEdit']>>) => actions.current.onFilesEdit?.(...args),
    onFork: (...args: Parameters<NonNullable<ChatItemProps['onFork']>>) => actions.current.onFork?.(...args),
    onRegen: (...args: Parameters<NonNullable<ChatItemProps['onRegen']>>) => actions.current.onRegen?.(...args),
    onBranch: (...args: Parameters<NonNullable<ChatItemProps['onBranch']>>) => actions.current.onBranch?.(...args),
  }), [])
  const props = { api, busy, uploading, ...callbacks, onHydrate, processes, openTools, onToggleTool: toggleTool, onOpenFile }
  const visibleItems = virtualizer.getVirtualItems()
  useEffect(() => {
    onVisibleEntries?.(visibleItems.flatMap(item => {
      const it = items[item.index]
      return [it.id]
    }))
  }, [visibleItems, items, onVisibleEntries])
  const renderItem = (it: ChatRenderItem) => {
    const foot = turns.get(it.id)
    return <>
      {it.kind === 'fold'
        ? <FoldRow {...it} loading={loadingFolds.has(it.turn.id) || (loadingOlder && !compactTurns?.length)} failed={failedFolds.has(it.turn.id)} onToggle={() => void toggleFold(it.turn.id)} />
        : <ChatItem node={it.node} {...props} edit={edit?.messageId === it.id ? edit : null} branchIndex={branches?.[it.id]?.index} branchTotal={branches?.[it.id]?.total} missed={misses.get(it.id)} />}

      {foot && (foot.steps > 0 || foot.live) ? <TurnDivider stats={foot} /> : null}
    </>
  }

  useLayoutEffect(() => {
    if (foldFollow.current) {
      foldFollow.current = false
      if (navigation.following.current) {
        virtualizer.scrollToEnd()
        return
      }
    }
    const anchor = foldAnchor.current
    if (!anchor) return
    foldAnchor.current = null
    const scroll = scrollRef.current
    const row = scroll?.querySelector(`[data-testid="fold-row"][data-fold="${CSS.escape(anchor.id)}"]`)
    if (scroll && row) {
      const delta = row.getBoundingClientRect().top - scroll.getBoundingClientRect().top - anchor.offset
      if (Math.abs(delta) > 1) virtualizer.scrollBy(delta)
    }
  }, [items, scrollRef, virtualizer])

  useLayoutEffect(() => {
    if (!jumpToId) return
    const index = items.findIndex(it => it.kind === 'node' && it.node.id === jumpToId)
    if (index < 0) {
      navigation.read()
      const fold = items.find(it => it.kind === 'fold' && it.nodes.some(n => n.id === jumpToId))
      if (fold?.kind === 'fold') void toggleFold(fold.turn.id, true)
      return
    }
    measurementAnchor.current = items[index].id
    selectedRequest.current = jumpToId
    return navigation.seek(jumpToId, index, onJumped)
  }, [jumpToId, items, onJumped, virtualizer, toggleFold, navigation.read, navigation.seek, scrollRef])

  const users = useMemo(() => items.flatMap((it, index) => it.kind === 'node' && it.node.kind === 'user' && isHumanPrompt(it.node.origin) ? [{ id: it.id, index }] : []), [items])
  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    let frame = 0
    let last: string | null | undefined
    const update = () => {
      frame = 0
      let lo = 0, hi = users.length - 1
      while (lo < hi) {
        const mid = Math.ceil((lo + hi) / 2)
        const offset = virtualizer.measurementsCache[users[mid].index]?.start ?? Infinity
        if (offset <= (virtualizer.scrollOffset ?? el.scrollTop) + 12) lo = mid
        else hi = mid - 1
      }
      const id = selectedRequest.current ?? (navigation.following.current ? users.at(-1)?.id : users[lo]?.id) ?? null
      if (id !== last) { last = id; onActiveRequest?.(id) }
      onAtBottom?.(Math.max(0, el.scrollHeight - el.clientHeight - el.scrollTop) <= 8)
    }
    const schedule = () => { if (!frame) frame = requestAnimationFrame(update) }
    update()
    el.addEventListener('scroll', schedule, { passive: true })
    return () => { cancelAnimationFrame(frame); el.removeEventListener('scroll', schedule) }
  }, [users, scrollRef, virtualizer, onActiveRequest, onAtBottom, navigation.following, navigation.isFollowing, jumpToId])

  return (
    <div ref={listRef} className="chat-col chat-virtual" data-testid="chat" data-scroll-intent={navigation.scrollIntent} data-anchor-key={measurementAnchor.current ?? undefined} data-scroll-offset={virtualizer.scrollOffset} data-deferred-offset={navigation.geometryOffset || undefined} data-library-scrolling={String(virtualizer.isScrolling)} style={{ height: Math.max(0, virtualizer.getTotalSize() - navigation.geometryOffset) }}>
      <div className="history-control" aria-live="polite" style={navigation.geometryOffset ? { transform: `translateY(${-navigation.geometryOffset}px)` } : undefined}>
        {hasMore || loadingOlder || olderError ? <button type="button" data-testid="load-older" disabled={loadingOlder} onClick={navigation.loadOlder}>
          {loadingOlder ? <span data-testid="older-loading">{t('chat.loadingOlder')}</span> : olderError ? t('chat.retryOlder') : t('chat.loadOlder')}
        </button> : <span>{t('chat.historyStart')}</span>}
      </div>
      {virtualizer.getVirtualItems().map(item => {
        const it = items[item.index]
        return <TranscriptRow key={item.key} id={it.id} index={item.index} start={item.start - navigation.geometryOffset} measure={measureMountedRow}>
          {renderItem(it)}
        </TranscriptRow>
      })}
      {running ? <div className="status-line chat-virtual-item" style={{ transform: `translateY(${virtualizer.getTotalSize() - navigation.geometryOffset - 32}px)` }}>{t('chat.running')}</div> : null}
    </div>
  )
}
