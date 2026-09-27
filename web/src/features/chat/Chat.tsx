import { Fragment, memo, useCallback, useEffect, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { useTranscriptScroll, type TranscriptScroll } from './useTranscriptScroll'
import { IChev, IChevDown, IClock, ICompact, ICopy, IEdit, IFork, IRegen, ITraj, IWrench } from '../../components/icons'
import { IFile } from '../../components/icons'
import { Composer, type Draft } from './Composer'
import { AttachmentImage } from '../attachments/AttachmentImage'
import type { Client } from '../../api/client'
import { useI18n } from '../../i18n/index'
import { Markdown } from '../markdown/Markdown'
import { cacheHitRate, cacheMisses, formatCost, formatDuration, formatTokens, formatTokensPerSecond, reconcileUserNodes, requestTitle, turnStats, type CacheMiss, type TurnStats } from '../../lib/model'
import { DEFAULT_COMPACT_KEEP, detailedItems, foldReplies, type ChatRenderItem, type ChatTurn, type MessageViewMode } from '../../lib/messageView'
import { rememberRowHeight, rowHeightEstimate, UNKNOWN_WIDTH } from '../../lib/rowHeight'
import { copyText } from '../../lib/clipboard'
import type { ChatNode } from '../../api/types'

const OVERSCAN = 4

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

function fmtDuration(ms?: number): string {
  if (ms == null) return ''
  if (ms < 1000) return ms === 0 ? '<1 ms' : `${Math.round(ms)} ms`
  return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)} s`
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
  /**
   * Dims the control to signal it is unavailable, but keeps it clickable so the
   * caller can explain why (e.g. regenerate while the session is running).
   * Not `disabled`/`aria-disabled`: those would swallow the click.
   */
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
  useEffect(() => {
    const el = ref.current
    if (!el) return
    if (open) {
      setOverflow(false)
      return
    }
    // The collapsed bubble is line-clamped; if its content is taller than the
    // clamp, the message is genuinely too long and needs the expand toggle.
    // (Measuring keeps short bubbles button-free and long ones toggleable.)
    setOverflow(el.scrollHeight > el.clientHeight + 1)
  }, [node.text, open])
  const folded = !open
  const images = node.content.filter(c => c.type === 'image')
  const files = node.content.filter(c => c.type === 'file' || c.type === 'workspace_file')
  const imageLayout = images.length <= 4 ? `count-${images.length}` : 'count-many'
  return (
    <div className={`user-message${node.origin ? ' origin-external' : ''}${node.origin?.startsWith('agent:') ? ' origin-agent' : ''}`} data-testid="user-bubble">
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
  node, title, summary, onInspect, onHydrate,
}: {
  node: Extract<ChatNode, { kind: 'tool' }>
  title: string
  summary: string
  onInspect?: (n: ChatNode) => void
  onHydrate?: (id: string) => Promise<boolean>
}) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const name = node.name
  const body = useBodyLoad(node.id, onHydrate)
  const cmd = argStr(node.args, 'command')
  const desc = argStr(node.args, 'description')
  const oldS = argStr(node.args, 'old_string')
  const newS = argStr(node.args, 'new_string')
  const content = argStr(node.args, 'content')
  const offset = Number(argStr(node.args, 'offset') || '1') || 1
	const editDiff = node.details && typeof node.details === 'object'
	  ? String((node.details as Record<string, unknown>).diff ?? '')
	  : ''
  const state = node.running ? 'running' : node.isError ? 'error' : 'ok'
  const fail = state === 'error' && node.result ? firstLine(node.result) : ''
  const line = fail || summary
  const bodyIn = name === 'Write' ? content : name === 'Bash' ? cmd : prettyArgs(node.args)
  const expandable = !!(node.truncated || node.result || bodyIn || oldS || newS || desc || editDiff)
  return (
    <div className={`tool-row${node.isError ? ' error' : ''}`} data-testid="tool-card" data-tool={name} data-state={state}>
      <div className="tool-row-h">
        <button
          type="button"
          className="tool-row-toggle"
          aria-expanded={open}
          aria-label={open ? t('chat.collapse') : t('chat.expand')}
          disabled={!expandable}
          onClick={() => {
            if (!expandable) return
            if (!open && node.truncated) void body.load()
            setOpen(v => !v)
          }}
        >
          {expandable ? <IChev open={open} /> : null}
          {state === 'error' ? <span className="state-dot err" /> : node.running ? <span className="spin" /> : <IWrench />}
          <span className="tool-name">{title}</span>
        </button>
        {line ? <span className="tool-sep" aria-hidden /> : null}
        {line ? <span className={`tool-preview${fail ? ' err' : ''}`} data-testid="tool-preview" title={line}>{line}</span> : null}
        {!node.running && node.durationMs != null ? <span className="tool-duration" data-testid="tool-duration">{fmtDuration(node.durationMs)}</span> : null}
      </div>
      {open && expandable ? (
        <div className="tool-row-body">
          {node.truncated ? <button type="button" className="body-load" disabled={body.loading} onClick={() => void body.load()}>{t(body.loading ? 'chat.loadingBody' : body.failed ? 'chat.retryOlder' : 'chat.loadBody')}</button> : null}
          {desc ? <div className="tool-desc" data-testid="tool-desc">{desc}</div> : null}
          {name === 'Read' && node.result ? (
            <pre className="tool-read">{node.result.split('\n').map((ln, i) => `${String(offset + i).padStart(4, ' ')}  ${ln}`).join('\n')}</pre>
          ) : name === 'Edit' && editDiff ? (
			<pre className="tool-out term">{editDiff}</pre>
		  ) : name === 'Edit' && (oldS || newS) ? (
            <div className="diff">
              {oldS ? <pre className="diff-old">{oldS}</pre> : null}
              {newS ? <pre className="diff-new">{newS}</pre> : null}
            </div>
          ) : name === 'Bash' ? (
            <div className="tool-term">
              {cmd ? <div className="tool-term-cmd">{cmd}</div> : null}
              {node.result ? <pre className="tool-out term">{node.result}</pre> : null}
            </div>
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

function Compaction({ node }: { node: Extract<ChatNode, { kind: 'compaction' }> }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  // Why: compaction runs synchronously inside the /compact request, so the row
  // itself is the only progress signal. Keep it colored while it runs and
  // switch to the settled label (with the pre-compaction token count) after.
  const label = node.running
    ? t('chat.compacting')
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
        disabled={node.running}
        onClick={() => setOpen(v => !v)}
      >
        {node.running ? <span className="compact-spin" aria-hidden /> : <ICompact />}
        {label}
        {!node.running && !node.empty && !node.failed && node.tokensBefore ? <span>· {t('chat.compactedTokens', { n: node.tokensBefore })}</span> : null}
      </button>
      {open && node.summary ? <div className="compact-body">{node.summary}</div> : null}
    </div>
  )
}

/**
 * Closes a turn: a hairline rule on both sides of the turn's aggregate stats.
 * The numbers mirror the per-step inspector (elapsed, tokens, TPS, cache) but
 * sum the whole turn, so a long tool-heavy run stays scannable at a glance.
 */
function TurnDivider({ stats }: { stats: TurnStats }) {
  const { t } = useI18n()
  const prompt = stats.input
  const hit = prompt > 0 && stats.cacheRead > 0 ? stats.cacheRead / prompt * 100 : null
  const cells: ReactNode[] = [
    <span className="turn-stat" key="elapsed" data-testid="turn-elapsed"><IClock />{t('turn.elapsed', { duration: formatDuration(stats.elapsedMs) })}</span>,
    <span className="turn-stat" key="steps">{t('turn.steps', { n: stats.steps })}</span>,
  ]
  if (stats.ttftMs > 0) cells.push(<span className="turn-stat" key="ttft">{t('stats.ttft', { duration: formatDuration(stats.ttftMs) })}</span>)
  if (stats.tps != null) cells.push(<span className="turn-stat" key="tps">{t('stats.tps', { tps: formatTokensPerSecond(stats.tps) })}</span>)
  if (stats.input > 0 || stats.output > 0) {
    cells.push(<span className="turn-stat" key="tokens">{t('stats.tokens', { input: formatTokens(stats.input), output: formatTokens(stats.output) })}</span>)
  }
  if (hit != null) cells.push(<span className="turn-stat" key="cache">{t('stats.cacheHit', { percent: hit.toFixed(2) })}</span>)
  if (stats.hasCost) cells.push(<span className="turn-stat" key="cost">{t('stats.cost', { amount: formatCost(stats.cost) })}</span>)
  return (
    <div className="turn-end" data-testid="turn-divider" data-turn={stats.turn}>
      <span className="turn-end-rule" aria-hidden />
      <div className="turn-end-stats">
        <span className="turn-end-label">{t('turn.label', { n: stats.turn })}</span>
        {cells}
      </div>
      <span className="turn-end-rule" aria-hidden />
    </div>
  )
}

/** nodePreview is the one-line hint a fold row shows for what it hides. */
function nodePreview(n: ChatNode): string {
  if (n.kind === 'user' || n.kind === 'assistant') return n.text
  if (n.kind === 'tool') return n.name
  return n.summary
}

/**
 * FoldRow stands in for a turn's folded reply nodes. It sits between the turn's
 * user bubble and the nodes that stayed visible, and opens them in place (the
 * row itself does not move, so expanding never reorders the transcript). It
 * carries the first hidden node id as `data-msg-id` so the request navigator
 * can highlight the row while the target itself is folded away.
 */
function FoldRow({ turn, nodes, expanded, onToggle }: {
  turn: ChatTurn
  nodes: ChatNode[]
  expanded: boolean
  onToggle: () => void
}) {
  const { t } = useI18n()
  const preview = nodes.length ? nodePreview(nodes[nodes.length - 1]) : ''
  return (
    <div className={`fold-row${expanded ? ' expanded' : ''}`} data-testid="fold-row" data-fold={turn.id} data-msg-id={nodes[0]?.id}>
      <button
        type="button"
        className="fold-row-btn"
        data-testid="fold-row-btn"
        aria-expanded={expanded}
        aria-label={expanded ? t('chat.collapse') : t('chat.expand')}
        onClick={onToggle}
      >
        <IChev open={expanded} />
        <span className="fold-row-count">{t('chat.foldCount', { n: nodes.length })}</span>
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
	/** Assistant node id → notable prompt-cache miss for that step. */
	missed?: CacheMiss
}

const ChatItem = memo(function ChatItem({
	api, node: n, busy, uploading, onSelect, edit, onStartEdit, onEditChange, onCancelEdit, onSendEdit, onAttachEdit, onFilesEdit, onFork, onRegen, branchIndex, branchTotal, onBranch, onHydrate, onShowBranches, missed,
}: ChatItemProps) {
  const { t } = useI18n()
  const bodyRef = useRef<HTMLDivElement>(null)
  const [hydrating, setHydrating] = useState(false)
  const [hydrateFailed, setHydrateFailed] = useState(false)
  const hydrate = useCallback(async () => {
    if (!onHydrate) return
    setHydrating(true)
    const ok = await onHydrate(n.id)
    setHydrating(false)
    setHydrateFailed(!ok)
  }, [n.id, onHydrate])
  useEffect(() => {
    const el = bodyRef.current
    if (!el || !n.truncated || n.kind !== 'assistant' || n.streaming || hydrateFailed) return
    let timer = 0
    // Mounted overscan is not reading intent. Wait for actual visibility so a
    // quick fling does not download every large reply it passes.
    const observer = new IntersectionObserver(entries => {
      window.clearTimeout(timer)
      if (entries.some(entry => entry.isIntersecting)) timer = window.setTimeout(() => { observer.disconnect(); void hydrate() }, 100)
    }, { root: el.closest('.scroll') })
    observer.observe(el)
    return () => { observer.disconnect(); window.clearTimeout(timer) }
  }, [n.id, n.truncated, n.kind, n.kind === 'assistant' && n.streaming, hydrate, hydrateFailed])
  if (n.kind === 'user') {
    const editing = edit?.messageId === n.id
    return (
      <div className={`user-row${editing ? ' editing' : ''}`} data-msg-id={n.id}>
        <div className="user-stack">
          {editing ? <Composer api={api} mode="edit" draft={edit!.draft} onChange={d => onEditChange?.(d)} onSend={() => onSendEdit?.()} onAttach={() => onAttachEdit?.()} onFiles={onFilesEdit} onCancel={onCancelEdit} busy={busy} uploading={uploading} /> : <UserBubble api={api} node={n} onHydrate={onHydrate} />}
          <div className="msg-foot">
            {n.ts ? <div className="msg-stats">{fmtTs(n.ts)}</div> : null}
            <div className="msg-actions" data-testid="user-actions">
              <IconBtn label={t('chat.copy')} testid="copy-msg" onClick={() => void copyText(n.text)}><ICopy /></IconBtn>
              {branchTotal == null ? <button type="button" className="branch-load" onClick={onShowBranches}>{t('chat.branches')}</button> : null}
              {branchTotal && branchTotal > 1 ? <span className="branch-nav"><button type="button" onClick={() => onBranch?.(n, -1)}>‹</button>{(branchIndex ?? 0) + 1} / {branchTotal}<button type="button" onClick={() => onBranch?.(n, 1)}>›</button></span> : null}
              <IconBtn label={t('chat.edit')} testid="edit-msg" onClick={() => onStartEdit?.(n)}><IEdit /></IconBtn>
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
    return (
      <div ref={bodyRef} className="asst" data-testid="assistant-message">
        <div className="asst-body">
          {n.thinking ? <Think text={n.thinking} streaming={n.streaming} /> : null}
          {n.images?.map((img, i) => (
            <img key={i} className="msg-img" alt="" src={`data:${img.mimeType};base64,${img.data}`} />
          ))}
          {n.text ? <Markdown text={n.text} streaming={n.streaming} /> : n.streaming && !n.thinking ? <span className="status-line">…</span> : null}
          {n.truncated ? <button type="button" className="body-load" data-testid="load-body" disabled={hydrating} onClick={() => void hydrate()}>{hydrating ? t('chat.loadingBody') : hydrateFailed ? t('chat.retryOlder') : t('chat.loadBody')}</button> : null}
          {n.error ? <div className="notice">{n.error}</div> : null}
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
              <IconBtn label={t('chat.locate')} testid="traj-msg" onClick={() => onSelect?.(n)}><ITraj /></IconBtn>
            </div>
          </div>
        ) : null}
      </div>
    )
  }
  if (n.kind === 'tool') {
    const name = n.name
    const path = argStr(n.args, 'file_path')
    const summary = name === 'Bash'
      ? (argStr(n.args, 'description') || argStr(n.args, 'command'))
      : path || firstLine(n.result || prettyArgs(n.args))
    return (
      <ToolRow
        node={n}
        title={name}
        summary={summary}
        onInspect={onSelect}
        onHydrate={onHydrate}
      />
    )
  }
  return <Compaction node={n} />
})

export function ChatView({ api, nodes: rawNodes, busy, uploading, onSelect, edit, onStartEdit, onEditChange, onCancelEdit, onSendEdit, onAttachEdit, onFilesEdit, onFork, onRegen, branches, onBranch, scrollRef, controlRef, onHydrate, onShowBranches, onVisibleEntries, jumpToId, onJumped, onActiveRequest, onAtBottom, onReadIntent, onLoadOlder, hasMore, olderError, turnBase = 0, mode = 'detailed', keep = DEFAULT_COMPACT_KEEP, loadingOlder }: Omit<ChatItemProps, 'node' | 'missed' | 'branchIndex' | 'branchTotal'> & {
  branches?: Record<string, { index: number; total: number }>
  nodes: ChatNode[]
  turnBase?: number
  mode?: MessageViewMode
  keep?: number
  scrollRef: RefObject<HTMLDivElement | null>
  controlRef?: React.Ref<TranscriptScroll>
  onVisibleEntries?: (ids: string[]) => void
  jumpToId?: string | null
  onJumped?: () => void
  onActiveRequest?: (id: string | null) => void
  onAtBottom?: (bottom: boolean) => void
  onReadIntent?: () => void
  onLoadOlder?: () => Promise<unknown>
  hasMore?: boolean
  loadingOlder?: boolean
  olderError?: boolean
}) {
  const { t } = useI18n()
  const measurementAnchor = useRef<string | null>(null)
  const navigation = useTranscriptScroll({ scrollRef, onAtBottom, onReadIntent: () => { measurementAnchor.current = null; onReadIntent?.() }, onLoadOlder, hasMore, loadingOlder, olderError })
  const nodes = useMemo(() => reconcileUserNodes(rawNodes), [rawNodes])
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
  const [folds, setFolds] = useState<ReadonlySet<string>>(() => new Set())
  const foldAnchor = useRef<{ id: string; offset: number } | null>(null)
  const toggleFold = useCallback((id: string) => {
    navigation.read()
    const scroll = scrollRef.current
    const row = scroll?.querySelector(`[data-testid="fold-row"][data-fold="${CSS.escape(id)}"]`)
    if (scroll && row) foldAnchor.current = { id, offset: row.getBoundingClientRect().top - scroll.getBoundingClientRect().top }
    setFolds(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [scrollRef, navigation.read])
  const items = useMemo(() => mode === 'compact' ? foldReplies(nodes, { keep, expanded: folds }) : detailedItems(nodes), [mode, nodes, keep, folds])
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
  const getItemKey = useCallback((index: number) => itemLookup.current[index].id, [])
  const estimates = useRef(new Map<string, { width: number; chars: number; size: number }>())
  const estimateSize = (index: number) => {
    const key = items[index].id
    const chars = itemChars.get(key) ?? 0
    const cached = estimates.current.get(key)
    if (cached && cached.width === listWidth && cached.chars === chars) return cached.size
    const size = rowHeightEstimate(key, listWidth, chars)
    estimates.current.set(key, { width: listWidth, chars, size })
    return size
  }
  const running = busy && !nodes.some(n => (n.kind === 'assistant' && n.streaming) || (n.kind === 'tool' && n.running))
  const virtualizer = useVirtualizer({
    count: items.length,
    getScrollElement: () => scrollRef.current,
    // A new key callback invalidates every measurement. Likewise, changing
    // unmeasured estimates as the global average learns silently moves rows
    // without a resizeItem delta. Freeze each estimate until its body/width
    // changes; actual measurements remain owned by the virtualizer.
    getItemKey,
    estimateSize,
    overscan: OVERSCAN,
    paddingStart: 60,
    paddingEnd: running ? 40 : 8,
    // The library anchors the CURRENT keyed item when a response commits, not
    // the distance from the tail recorded before a slow network request.
    anchorTo: 'end',
    followOnAppend: navigation.following.current,
    scrollEndThreshold: navigation.following.current ? 80 : -1,
    measureElement: (element, entry) => {
      const size = entry?.borderBoxSize?.[0]?.blockSize ?? element.getBoundingClientRect().height
      const key = (element as HTMLElement).dataset.itemKey
      if (key && !element.querySelector('[data-md-pending]')) rememberRowHeight(key, listWidth, size, itemChars.get(key) ?? 0)
      return size
    },
  })
  navigation.virtual.current = virtualizer
  // A resize above the viewport must not move the visible content. Let the
  // virtualizer apply this adjustment, including its iOS momentum deferral.
  virtualizer.shouldAdjustScrollPositionOnItemSizeChange = (item, delta, instance) => {
    // At the history boundary the reader may be in the 60px padding before
    // the anchor row. After prepend that space contains the preceding row:
    // its resize still belongs ABOVE the content anchor, even if its estimated
    // bottom crosses scrollTop. A pixel-only predicate loses that adjustment.
    if (measurementAnchor.current && anchorIndex >= 0) {
      return delta !== 0 && item.index < anchorIndex
    }
    return delta !== 0 && (instance.itemSizeCache.has(item.key) ? item.end : item.start) <= (instance.scrollOffset ?? 0) + instance.scrollAdjustments
  }
  useImperativeHandle(controlRef, () => ({ read: navigation.read, latest: navigation.latest }), [navigation.read, navigation.latest])

  const opened = useRef(false)
  useLayoutEffect(() => {
    // The parent scroller's ref is attached after the child's first layout
    // pass. Wait for the virtualizer to bind it before the initial end jump.
    if (opened.current || !items.length || !virtualizer.scrollElement) return
    opened.current = true
    navigation.latest()
  })
  // A viewport change (keyboard/rotation) changes the reachable end even if no
  // message changes. Reading remains anchored; following stays at the end.
  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    let previous = el.clientHeight
    const ro = new ResizeObserver(() => {
      if (el.clientHeight === previous) return
      previous = el.clientHeight
      if (navigation.following.current) virtualizer.scrollToEnd()
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [scrollRef, virtualizer])

  const turns = useMemo(() => turnStats(nodes, turnBase), [nodes, turnBase])
  const newestId = nodes.at(-1)?.id
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
  const props = { api, busy, uploading, ...callbacks, onHydrate }
  const visibleItems = virtualizer.getVirtualItems()
  useEffect(() => {
    onVisibleEntries?.(visibleItems.flatMap(item => {
      const it = items[item.index]
      return [it.id]
    }))
  }, [visibleItems, items, onVisibleEntries])
  const renderItem = (it: ChatRenderItem) => {
    if (it.kind === 'fold') return <FoldRow turn={it.turn} nodes={it.nodes} expanded={it.expanded} onToggle={() => toggleFold(it.turn.id)} />
    const foot = turns.get(it.id)
    return <>
      <ChatItem node={it.node} {...props} edit={edit?.messageId === it.id ? edit : null} branchIndex={branches?.[it.id]?.index} branchTotal={branches?.[it.id]?.total} missed={misses.get(it.id)} />
      {foot && !foot.live && foot.steps > 0 && !(busy && newestId === it.id) ? <TurnDivider stats={foot} /> : null}
    </>
  }

  useLayoutEffect(() => {
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
    measurementAnchor.current = null
    navigation.read()
    const index = items.findIndex(it => it.kind === 'node' && it.node.id === jumpToId)
    if (index < 0) {
      const fold = items.find(it => it.kind === 'fold' && it.nodes.some(n => n.id === jumpToId))
      if (fold?.kind === 'fold') toggleFold(fold.turn.id)
      return
    }
    virtualizer.scrollToIndex(index, { align: 'start' })
    onJumped?.()
  }, [jumpToId, items, onJumped, virtualizer, toggleFold, navigation.read])

  const users = useMemo(() => items.flatMap((it, index) => it.kind === 'node' && it.node.kind === 'user' ? [{ id: it.id, index }] : []), [items])
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
        const offset = virtualizer.getOffsetForIndex(users[mid].index, 'start')?.[0] ?? Infinity
        if (offset <= el.scrollTop + 12) lo = mid
        else hi = mid - 1
      }
      const id = users[lo]?.id ?? null
      if (id !== last) { last = id; onActiveRequest?.(id) }
      onAtBottom?.(virtualizer.getDistanceFromEnd() <= 8)
    }
    const schedule = () => { if (!frame) frame = requestAnimationFrame(update) }
    update()
    el.addEventListener('scroll', schedule, { passive: true })
    return () => { cancelAnimationFrame(frame); el.removeEventListener('scroll', schedule) }
  }, [users, scrollRef, virtualizer, onActiveRequest, onAtBottom])

  return (
    <div ref={listRef} className="chat-col chat-virtual" data-testid="chat" data-scroll-intent={navigation.isFollowing ? 'following' : 'reading'} data-anchor-key={measurementAnchor.current ?? undefined} style={{ height: virtualizer.getTotalSize() }}>
      <div className="history-control" aria-live="polite">
        {hasMore ? <button type="button" data-testid="load-older" disabled={loadingOlder} onClick={navigation.loadOlder}>
          {loadingOlder ? <span data-testid="older-loading">{t('chat.loadingOlder')}</span> : olderError ? t('chat.retryOlder') : t('chat.loadOlder')}
        </button> : <span>{t('chat.historyStart')}</span>}
      </div>
      {virtualizer.getVirtualItems().map(item => {
        const it = items[item.index]
        return <div key={item.key} data-index={item.index} data-item-key={it.id} ref={virtualizer.measureElement} className="chat-virtual-item" style={{ transform: `translateY(${item.start}px)` }}>
          {renderItem(it)}
        </div>
      })}
      {running ? <div className="status-line chat-virtual-item" style={{ transform: `translateY(${virtualizer.getTotalSize() - 32}px)` }}>{t('chat.running')}</div> : null}
    </div>
  )
}
