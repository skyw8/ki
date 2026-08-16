import { useEffect, useRef, useState, type ReactNode } from 'react'
import { IChev, IChevDown, ICompact, ICopy, IEdit, IFork, IRegen, ITraj, IWrench } from './icons'
import { useI18n } from './i18n'
import { renderMarkdown } from './markdown'
import type { ChatNode } from './types'

function Markdown({ text }: { text: string }) {
  return <div className="md" dangerouslySetInnerHTML={{ __html: renderMarkdown(text) }} />
}

function copyText(text: string) {
  void navigator.clipboard?.writeText(text)
}

function fmtUsage(u: { input?: number; output?: number; cacheRead?: number; cacheWrite?: number }): string {
  let s = `${u.input ?? 0}→${u.output ?? 0}`
  if (u.cacheRead) s += ` cache ${u.cacheRead}`
  if (u.cacheWrite) s += ` +${u.cacheWrite}`
  return s
}

function fmtTs(ts?: number): string {
  if (!ts) return ''
  const d = new Date(ts)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
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

function UserBubble({ text }: { text: string }) {
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
  }, [text, open])
  const folded = !open
  return (
    <div className="user-bubble" data-testid="user-bubble">
      <div ref={ref} className={`bubble-text${folded ? ' clamped' : ''}`}>{text}</div>
      {overflow || open ? (
        <button
          type="button"
          className="bubble-toggle"
          data-testid="user-bubble-toggle"
          aria-label={open ? t('chat.collapse') : t('chat.expand')}
          title={open ? t('chat.collapse') : t('chat.expand')}
          onClick={() => setOpen(v => !v)}
        >
          <IChevDown className={open ? 'up' : undefined} />
        </button>
      ) : null}
    </div>
  )
}

function ToolRow({
  node, title, summary, onInspect,
}: {
  node: Extract<ChatNode, { kind: 'tool' }>
  title: string
  summary: string
  onInspect?: (n: ChatNode) => void
}) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const name = node.name
  const cmd = argStr(node.args, 'command')
  const oldS = argStr(node.args, 'old_string')
  const newS = argStr(node.args, 'new_string')
  const content = argStr(node.args, 'content')
  const offset = Number(argStr(node.args, 'offset') || '1') || 1
  const state = node.running ? 'running' : node.isError ? 'error' : 'ok'
  const fail = state === 'error' && node.result ? firstLine(node.result) : ''
  const line = fail || summary
  const bodyIn = name === 'Write' ? content : name === 'Bash' ? cmd : prettyArgs(node.args)
  const expandable = !!(node.result || bodyIn || oldS || newS)
  return (
    <div className={`tool-row${node.isError ? ' error' : ''}`} data-testid="tool-card" data-tool={name} data-state={state}>
      <button type="button" className="tool-row-h" onClick={() => expandable && setOpen(v => !v)}>
        {state === 'error' ? <span className="state-dot err" /> : node.running ? <span className="spin" /> : <IWrench />}
        <span className="tool-name">{title}</span>
        {line ? <span className="tool-sep" aria-hidden /> : null}
        <span className={`tool-preview${fail ? ' err' : ''}`}>{line}</span>
      </button>
      {open && expandable ? (
        <div className="tool-row-body">
          {name === 'Read' && node.result ? (
            <pre className="tool-read">{node.result.split('\n').map((ln, i) => `${String(offset + i).padStart(4, ' ')}  ${ln}`).join('\n')}</pre>
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

export function ChatView({ nodes, busy, onSelect }: { nodes: ChatNode[]; busy: boolean; onSelect?: (n: ChatNode) => void }) {
  const { t } = useI18n()
  return (
    <div className="chat-col" data-testid="chat">
      {nodes.map(n => {
        if (n.kind === 'user') {
          return (
            <div key={n.id} className="user-row">
              <div className="user-stack">
                <UserBubble text={n.text} />
                <div className="msg-foot">
                  {n.ts ? <div className="msg-stats">{fmtTs(n.ts)}</div> : null}
                  <div className="msg-actions" data-testid="user-actions">
                    <IconBtn label={t('chat.copy')} testid="copy-msg" onClick={() => copyText(n.text)}><ICopy /></IconBtn>
                    <IconBtn label={t('chat.edit')} testid="edit-msg"><IEdit /></IconBtn>
                  </div>
                </div>
              </div>
            </div>
          )
        }
        if (n.kind === 'assistant') {
          const hasStats = !n.streaming && !!(n.ts || n.latencyMs || n.ttftMs || n.usage)
          return (
            <div key={n.id} className="asst" data-testid="assistant-message">
              <div className="asst-body">
                {n.thinking ? <Think text={n.thinking} streaming={n.streaming} /> : null}
                {n.images?.map((img, i) => (
                  <img key={i} className="msg-img" alt="" src={`data:${img.mimeType};base64,${img.data}`} />
                ))}
                {n.text ? <Markdown text={n.text} /> : n.streaming && !n.thinking ? <span className="status-line">…</span> : null}
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
                    <IconBtn label={t('chat.fork')} testid="fork-msg"><IFork /></IconBtn>
                    <IconBtn label={t('chat.regen')} testid="regen-msg"><IRegen /></IconBtn>
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
              key={n.id}
              node={n}
              title={name === 'Read' || name === 'Edit' || name === 'Write' || name === 'Bash' ? name : name}
              summary={summary}
              onInspect={onSelect}
            />
          )
        }
        return <Compaction key={n.id} node={n} />
      })}
      {busy && !nodes.some(n => (n.kind === 'assistant' && n.streaming) || (n.kind === 'tool' && n.running)) ? (
        <div className="status-line">{t('chat.running')}</div>
      ) : null}
    </div>
  )
}
