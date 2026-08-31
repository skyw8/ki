import { memo, useEffect, useRef, useState, type ReactNode, type RefObject } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { IChev, IChevDown, ICompact, ICopy, IEdit, IFork, IRegen, ISpark, ITraj, IWrench } from './icons'
import { IFile } from './icons'
import { Composer, type Draft } from './Composer'
import { AttachmentImage } from './AttachmentImage'
import type { Client } from './api'
import { useI18n } from './i18n'
import { Markdown } from './Markdown'
import { reconcileUserNodes } from './model'
import type { ChatNode } from './types'

const VIRTUALIZE_AFTER = 48

function copyText(text: string) {
  void navigator.clipboard?.writeText(text)
}

function fmtUsage(u: { input?: number; output?: number; cacheRead?: number; cacheWrite?: number; cost?: { total: number } }): string {
  let s = `${u.input ?? 0}→${u.output ?? 0}`
  if (u.cacheRead) s += ` cache ${u.cacheRead}`
  if (u.cacheWrite) s += ` +${u.cacheWrite}`
	if (u.cost) s += ` · $${u.cost.total < .01 ? u.cost.total.toFixed(4) : u.cost.total.toFixed(2)}`
  return s
}

function fmtTs(ts?: number): string {
  if (!ts) return ''
  const d = new Date(ts)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

function fmtDuration(ms?: number): string {
  if (ms == null) return ''
  if (ms < 1000) return `${Math.round(ms)} ms`
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
  label, testid, onClick, children,
}: {
  label: string
  testid?: string
  onClick?: () => void
  children: ReactNode
}) {
  return (
    <button
      type="button"
      className="msg-icon"
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

function UserBubble({ api, node, onHydrate }: { api: Client; node: Extract<ChatNode, { kind: 'user' }>; onHydrate?: (id: string) => void }) {
  const { t } = useI18n()
  const ref = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const [overflow, setOverflow] = useState(false)
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
    <div className={`user-message${node.origin ? ' origin-ext' : ''}`} data-testid="user-bubble">
      {node.origin ? <div className="user-origin" data-testid="user-origin">{node.origin}</div> : null}
      {images.length ? <div className={`message-images ${imageLayout}`}>
        {images.map((c, i) => <AttachmentImage api={api} content={c} className="message-image" expandable key={`${c.path || c.name}-${i}`} />)}
      </div> : null}
      {files.length ? <div className="message-files">
		{files.map((c, i) => <span className="message-file" key={`${c.path || c.name}-${i}`} title={c.path}><span className="message-file-icon"><IFile /></span><span className="message-file-copy"><strong>{c.name || c.path}</strong>{c.size != null ? <small>{fmtFileSize(c.size)}</small> : null}</span></span>)}
	  </div> : null}
      {node.text ? <div className="user-text-bubble">
        <div ref={ref} className={`bubble-text${folded ? ' clamped' : ''}`}>{node.text}</div>
        {overflow || open ? (
          <button
            type="button"
            className="bubble-toggle"
            data-testid="user-bubble-toggle"
            aria-label={open ? t('chat.collapse') : t('chat.expand')}
            title={open ? t('chat.collapse') : t('chat.expand')}
            onClick={() => {
              if (!open && node.truncated) onHydrate?.(node.id)
              setOpen(v => !v)
            }}
          >
            <IChevDown className={open ? 'up' : undefined} />
          </button>
        ) : null}
      </div> : null}
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
  onHydrate?: (id: string) => void
}) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const name = node.name
  const cmd = argStr(node.args, 'command')
  const desc = argStr(node.args, 'description')
  const oldS = argStr(node.args, 'old_string')
  const newS = argStr(node.args, 'new_string')
  const content = argStr(node.args, 'content')
  const offset = Number(argStr(node.args, 'offset') || '1') || 1
	const editDiff = node.details && typeof node.details === 'object'
	  ? String((node.details as Record<string, unknown>).diff ?? '')
	  : ''
	const patchDiff = node.name === 'apply_patch' && node.details && typeof node.details === 'object'
	  ? (((node.details as Record<string, unknown>).changes as Array<Record<string, unknown>> | undefined) ?? []).map(c => String(c.unified_diff ?? '')).filter(Boolean).join('\n')
	  : ''
  const state = node.running ? 'running' : node.isError ? 'error' : 'ok'
  const fail = state === 'error' && node.result ? firstLine(node.result) : ''
  const line = fail || summary
  const copyValue = desc || line
  const bodyIn = name === 'Write' ? content : name === 'Bash' ? cmd : prettyArgs(node.args)
  const expandable = !!(node.result || bodyIn || oldS || newS || desc || editDiff || patchDiff)
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
            if (!open && node.truncated) onHydrate?.(node.id)
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
        {copyValue ? (
          <IconBtn label={t('chat.copy')} testid="copy-tool" onClick={() => copyText(copyValue)}><ICopy /></IconBtn>
        ) : null}
      </div>
      {open && expandable ? (
        <div className="tool-row-body">
          {desc ? <div className="tool-desc" data-testid="tool-desc">{desc}</div> : null}
          {name === 'Read' && node.result ? (
            <pre className="tool-read">{node.result.split('\n').map((ln, i) => `${String(offset + i).padStart(4, ' ')}  ${ln}`).join('\n')}</pre>
          ) : name === 'Edit' && editDiff ? (
			<pre className="tool-out term">{editDiff}</pre>
		  ) : name === 'apply_patch' && patchDiff ? (
			<pre className="tool-out term sys-diff">{patchDiff}</pre>
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
  return (
    <div className="compact-row">
      <button type="button" className="compact-btn" onClick={() => setOpen(v => !v)}>
        <ICompact />
        {t('chat.compacted')}
        {node.tokensBefore ? <span>· {t('chat.compactedTokens', { n: node.tokensBefore })}</span> : null}
      </button>
      {open ? <div className="compact-body">{node.summary}</div> : null}
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
	branches?: Record<string, { index: number; total: number }>
	onBranch?: (n: Extract<ChatNode, { kind: 'user' }>, delta: number) => void
	onHydrate?: (id: string) => void
}

const ChatItem = memo(function ChatItem({
	api, node: n, busy, uploading, onSelect, edit, onStartEdit, onEditChange, onCancelEdit, onSendEdit, onAttachEdit, onFilesEdit, onFork, onRegen, branches, onBranch, onHydrate,
}: ChatItemProps) {
  const { t } = useI18n()
  useEffect(() => {
    if (n.truncated && (n.kind === 'assistant' || n.kind === 'system') && !(n.kind === 'assistant' && n.streaming)) {
      onHydrate?.(n.id)
    }
  }, [n, onHydrate])
  if (n.kind === 'user') {
    return (
      <div className="user-row">
        <div className="user-stack">
          {edit?.messageId === n.id ? <Composer api={api} mode="edit" draft={edit.draft} onChange={d => onEditChange?.(d)} onSend={() => onSendEdit?.()} onAttach={() => onAttachEdit?.()} onFiles={onFilesEdit} onCancel={onCancelEdit} busy={busy} uploading={uploading} /> : <UserBubble api={api} node={n} onHydrate={onHydrate} />}
          <div className="msg-foot">
            {n.ts ? <div className="msg-stats">{fmtTs(n.ts)}</div> : null}
            <div className="msg-actions" data-testid="user-actions">
              <IconBtn label={t('chat.copy')} testid="copy-msg" onClick={() => copyText(n.text)}><ICopy /></IconBtn>
              {branches?.[n.id]?.total && branches[n.id].total > 1 ? <span className="branch-nav"><button type="button" onClick={() => onBranch?.(n, -1)}>‹</button>{branches[n.id].index + 1} / {branches[n.id].total}<button type="button" onClick={() => onBranch?.(n, 1)}>›</button></span> : null}
              <IconBtn label={t('chat.edit')} testid="edit-msg" onClick={() => onStartEdit?.(n)}><IEdit /></IconBtn>
            </div>
          </div>
        </div>
      </div>
    )
  }
  if (n.kind === 'system') {
    return (
      <div className="system-prompt-row" data-testid="chat-system-prompt">
        <div className="system-prompt-label"><ISpark /> {t('chat.system')}</div>
        <details>
          <summary>{n.promptChange?.kind === 'initial' ? t('chat.systemInitial') : t('chat.systemChanged')}</summary>
          <pre>{n.text}</pre>
        </details>
      </div>
    )
  }
  if (n.kind === 'assistant') {
    const hasStats = !n.streaming && !!(n.ts || n.latencyMs || n.ttftMs || n.usage)
    return (
      <div className="asst" data-testid="assistant-message">
        <div className="asst-body">
          {n.thinking ? <Think text={n.thinking} streaming={n.streaming} /> : null}
          {n.images?.map((img, i) => (
            <img key={i} className="msg-img" alt="" src={`data:${img.mimeType};base64,${img.data}`} />
          ))}
          {n.text ? <Markdown text={n.text} streaming={n.streaming} /> : n.streaming && !n.thinking ? <span className="status-line">…</span> : null}
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
              </div>
            ) : null}
            <div className="msg-actions" data-testid="asst-actions">
              <IconBtn label={t('chat.copy')} testid="copy-msg" onClick={() => copyText(n.text || n.thinking || '')}><ICopy /></IconBtn>
              {n.stopReason !== 'toolUse' ? <IconBtn label={t('chat.fork')} testid="fork-msg" onClick={() => onFork?.(n)}><IFork /></IconBtn> : null}
              {n.stopReason !== 'toolUse' ? <IconBtn label={t('chat.regen')} testid="regen-msg" onClick={() => onRegen?.(n)}><IRegen /></IconBtn> : null}
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

export function ChatView({ api, nodes: rawNodes, busy, uploading, onSelect, edit, onStartEdit, onEditChange, onCancelEdit, onSendEdit, onAttachEdit, onFilesEdit, onFork, onRegen, branches, onBranch, scrollRef, onHydrate }: Omit<ChatItemProps, 'node'> & {
	nodes: ChatNode[]
	scrollRef?: RefObject<HTMLDivElement | null>
}) {
  const { t } = useI18n()
  const nodes = reconcileUserNodes(rawNodes)
  const virtualize = nodes.length > VIRTUALIZE_AFTER
  const virtualizer = useVirtualizer({
    count: nodes.length,
    getScrollElement: () => scrollRef?.current ?? null,
    estimateSize: () => 96,
    overscan: 10,
    getItemKey: index => nodes[index]?.id ?? index,
    enabled: virtualize,
  })
  const itemProps = { api, busy, uploading, onSelect, edit, onStartEdit, onEditChange, onCancelEdit, onSendEdit, onAttachEdit, onFilesEdit, onFork, onRegen, branches, onBranch, onHydrate }
  const running = busy && !nodes.some(n => (n.kind === 'assistant' && n.streaming) || (n.kind === 'tool' && n.running))
  if (!virtualize) {
    return (
      <div className="chat-col" data-testid="chat">
        {nodes.map(n => <ChatItem key={n.id} node={n} {...itemProps} />)}
        {running ? <div className="status-line">{t('chat.running')}</div> : null}
      </div>
    )
  }
  return (
    <div className="chat-col chat-virtual" data-testid="chat" style={{ height: virtualizer.getTotalSize() }}>
      {virtualizer.getVirtualItems().map(item => {
        const n = nodes[item.index]
        if (!n) return null
        return (
          <div
            key={item.key}
            data-index={item.index}
            ref={virtualizer.measureElement}
            className="chat-virtual-item"
            style={{ transform: `translateY(${item.start}px)` }}
          >
            <ChatItem node={n} {...itemProps} />
          </div>
        )
      })}
      {running ? <div className="status-line chat-virtual-item" style={{ transform: `translateY(${virtualizer.getTotalSize()}px)` }}>{t('chat.running')}</div> : null}
    </div>
  )
}
