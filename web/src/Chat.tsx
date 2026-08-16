import { useState } from 'react'
import { IChev, ICompact, IWrench } from './icons'
import { renderMarkdown } from './markdown'
import type { ChatNode } from './types'

function Markdown({ text }: { text: string }) {
  return <div className="md" dangerouslySetInnerHTML={{ __html: renderMarkdown(text) }} />
}

function Think({ text, streaming }: { text: string; streaming?: boolean }) {
  const [open, setOpen] = useState(false)
  const first = text.split('\n').find(l => l.trim()) ?? ''
  return (
    <div className="think">
      <button type="button" className="think-btn" onClick={() => setOpen(v => !v)}>
        <IChev open={open} />
        {streaming ? '思考中…' : '思考'}
        {!open && first ? <span style={{ color: 'var(--dsw-alias-label-caption)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{first}</span> : null}
      </button>
      {open ? <div className="think-body">{text}</div> : null}
    </div>
  )
}

function Tool({ node }: { node: Extract<ChatNode, { kind: 'tool' }> }) {
  const [open, setOpen] = useState(false)
  const args = node.args == null ? '' : typeof node.args === 'string' ? node.args : JSON.stringify(node.args, null, 2)
  return (
    <div className={`tool${node.isError ? ' error' : ''}`} data-testid="tool-card" data-tool={node.name}>
      <button type="button" className="tool-btn" onClick={() => setOpen(v => !v)}>
        {node.running ? <span className="spin" /> : <IWrench />}
        <span className="tool-name">{node.name}</span>
        <span className="tool-preview">{node.result || args}</span>
        {node.durationMs != null ? <span className="asst-meta">{(node.durationMs / 1000).toFixed(2)}s</span> : null}
      </button>
      {open ? (
        <div className="tool-body">
          {args ? <pre>{args}</pre> : null}
          {node.result ? <pre>{node.result}</pre> : null}
        </div>
      ) : null}
    </div>
  )
}

function Compaction({ node }: { node: Extract<ChatNode, { kind: 'compaction' }> }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="compact-row">
      <button type="button" className="compact-btn" onClick={() => setOpen(v => !v)}>
        <ICompact />
        上下文已压缩
        {node.tokensBefore ? <span>· 约 {node.tokensBefore} tokens</span> : null}
      </button>
      {open ? <div className="compact-body">{node.summary}</div> : null}
    </div>
  )
}

export function ChatView({ nodes, busy }: { nodes: ChatNode[]; busy: boolean }) {
  return (
    <div className="chat-col" data-testid="chat">
      {nodes.map(n => {
        if (n.kind === 'user') {
          return (
            <div key={n.id} className="user-row">
              <div className="user-bubble" data-testid="user-bubble">{n.text}</div>
            </div>
          )
        }
        if (n.kind === 'assistant') {
          return (
            <div key={n.id} className="asst" data-testid="assistant-message">
              {n.thinking ? <Think text={n.thinking} streaming={n.streaming} /> : null}
              {n.text ? <Markdown text={n.text} /> : n.streaming && !n.thinking ? <span className="status-line">…</span> : null}
              {n.error ? <div className="notice">{n.error}</div> : null}
              {!n.streaming && (n.latencyMs || n.ttftMs) ? (
                <div className="asst-meta">
                  {n.latencyMs != null ? `Ran ${(n.latencyMs / 1000).toFixed(2)}s` : ''}
                  {n.ttftMs != null ? ` · TTFT ${(n.ttftMs / 1000).toFixed(2)}s` : ''}
                  {n.usage ? ` · ${n.usage.input ?? 0}→${n.usage.output ?? 0}` : ''}
                </div>
              ) : null}
            </div>
          )
        }
        if (n.kind === 'tool') return <Tool key={n.id} node={n} />
        return <Compaction key={n.id} node={n} />
      })}
      {busy && !nodes.some(n => (n.kind === 'assistant' && n.streaming) || (n.kind === 'tool' && n.running)) ? (
        <div className="status-line">正在运行…</div>
      ) : null}
    </div>
  )
}
