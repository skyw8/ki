import { useState, type ReactNode } from 'react'
import { IChev, ICompact, ICopy, IEdit, IFork, IRegen, IWrench } from './icons'
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
  const [open, setOpen] = useState(false)
  const first = text.split('\n').find(l => l.trim()) ?? ''
  return (
    <div className="think">
      <button type="button" className="think-btn" onClick={() => setOpen(v => !v)}>
        <span className="think-head">
          <IChev open={open} />
          <span className="think-label">{streaming ? '思考中…' : '思考'}</span>
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

function ReadCard({ node }: { node: Extract<ChatNode, { kind: 'tool' }> }) {
  const path = argStr(node.args, 'file_path')
  return (
    <div className={`tool-spec read${node.isError ? ' error' : ''}`} data-testid="tool-card" data-tool="Read">
      <div className="tool-spec-h">
        {node.running ? <span className="spin" /> : <IWrench />}
        <strong>Read</strong>
        <span className="tool-preview">{path}</span>
        {node.durationMs != null ? <span className="asst-meta">{(node.durationMs / 1000).toFixed(2)}s</span> : null}
      </div>
      {node.result ? <pre className="tool-out">{node.result}</pre> : null}
    </div>
  )
}

function EditCard({ node }: { node: Extract<ChatNode, { kind: 'tool' }> }) {
  const path = argStr(node.args, 'file_path')
  const oldS = argStr(node.args, 'old_string')
  const newS = argStr(node.args, 'new_string')
  return (
    <div className={`tool-spec edit${node.isError ? ' error' : ''}`} data-testid="tool-card" data-tool="Edit">
      <div className="tool-spec-h">
        {node.running ? <span className="spin" /> : <IWrench />}
        <strong>Edit</strong>
        <span className="tool-preview">{path}</span>
      </div>
      <div className="diff">
        {oldS ? <pre className="diff-old">{oldS}</pre> : null}
        {newS ? <pre className="diff-new">{newS}</pre> : null}
      </div>
      {node.result ? <div className="asst-meta">{node.result}</div> : null}
    </div>
  )
}

function BashCard({ node }: { node: Extract<ChatNode, { kind: 'tool' }> }) {
  const cmd = argStr(node.args, 'command')
  return (
    <div className={`tool-spec bash${node.isError ? ' error' : ''}`} data-testid="tool-card" data-tool="Bash">
      <div className="tool-spec-h">
        {node.running ? <span className="spin" /> : <IWrench />}
        <strong>Bash</strong>
        <span className="tool-preview">{cmd}</span>
      </div>
      {node.result ? <pre className="tool-out term">{node.result}</pre> : null}
    </div>
  )
}

function GenericTool({ node, onOpen }: { node: Extract<ChatNode, { kind: 'tool' }>; onOpen?: (n: ChatNode) => void }) {
  const [open, setOpen] = useState(false)
  const args = node.args == null ? '' : typeof node.args === 'string' ? node.args : JSON.stringify(node.args, null, 2)
  return (
    <div className={`tool${node.isError ? ' error' : ''}`} data-testid="tool-card" data-tool={node.name}>
      <button type="button" className="tool-btn" onClick={() => { setOpen(v => !v); onOpen?.(node) }}>
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

export function ChatView({ nodes, busy, onSelect }: { nodes: ChatNode[]; busy: boolean; onSelect?: (n: ChatNode) => void }) {
  return (
    <div className="chat-col" data-testid="chat">
      {nodes.map(n => {
        if (n.kind === 'user') {
          return (
            <div key={n.id} className="user-row">
              <div className="user-stack">
                <div className="user-bubble" data-testid="user-bubble">{n.text}</div>
                <div className="msg-foot">
                  {n.ts ? <div className="msg-stats">{fmtTs(n.ts)}</div> : null}
                  <div className="msg-actions" data-testid="user-actions">
                    <IconBtn label="复制" testid="copy-msg" onClick={() => copyText(n.text)}><ICopy /></IconBtn>
                    <IconBtn label="编辑" testid="edit-msg"><IEdit /></IconBtn>
                  </div>
                </div>
              </div>
            </div>
          )
        }
        if (n.kind === 'assistant') {
          const hasStats = !n.streaming && !!(n.ts || n.latencyMs || n.ttftMs || n.usage)
          return (
            <div key={n.id} className="asst" data-testid="assistant-message" onClick={() => onSelect?.(n)}>
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
                    <IconBtn label="复制" testid="copy-msg" onClick={() => copyText(n.text || n.thinking || '')}><ICopy /></IconBtn>
                    <IconBtn label="Fork" testid="fork-msg"><IFork /></IconBtn>
                    <IconBtn label="重新生成" testid="regen-msg"><IRegen /></IconBtn>
                  </div>
                </div>
              ) : null}
            </div>
          )
        }
        if (n.kind === 'tool') {
          const name = n.name
          if (name === 'Read') return <div key={n.id} onClick={() => onSelect?.(n)}><ReadCard node={n} /></div>
          if (name === 'Edit') return <div key={n.id} onClick={() => onSelect?.(n)}><EditCard node={n} /></div>
          if (name === 'Bash') return <div key={n.id} onClick={() => onSelect?.(n)}><BashCard node={n} /></div>
          return <GenericTool key={n.id} node={n} onOpen={onSelect} />
        }
        return <Compaction key={n.id} node={n} />
      })}
      {busy && !nodes.some(n => (n.kind === 'assistant' && n.streaming) || (n.kind === 'tool' && n.running)) ? (
        <div className="status-line">正在运行…</div>
      ) : null}
    </div>
  )
}
