import { useId, useMemo, useState } from 'react'
import type { AgentSnapshot, ProcessSnapshot } from '../../api/types'
import { IChev, IChevDown } from '../../components/icons'
import { useI18n, type MsgKey } from '../../i18n'
import { agentTree, type AgentBranch } from '../../lib/runtimeTree'

const phaseKeys: Record<string, MsgKey> = {
  starting: 'runtime.phase.starting', executing: 'runtime.phase.executing', waiting_message: 'runtime.phase.waiting_message',
  waiting_resource: 'runtime.phase.waiting_resource', settled: 'runtime.phase.settled', interrupted: 'runtime.phase.interrupted',
  running: 'runtime.phase.running', completed: 'runtime.phase.completed', failed: 'runtime.phase.failed', killed: 'runtime.phase.killed', pending: 'runtime.phase.waiting_resource',
}

export type RuntimePanelProps = {
  processes: ProcessSnapshot[]
  agents: AgentSnapshot[]
  sessionId: string
  onOpenSession?: (id: string) => void
  onStop?: (id: number) => Promise<void>
  onInterrupt?: (sessionID: string) => Promise<void>
}

function AgentItem({ branch: { agent: a, children }, sessionId, onOpenSession, onInterrupt }: Pick<RuntimePanelProps, 'sessionId' | 'onOpenSession' | 'onInterrupt'> & { branch: AgentBranch }) {
  const { t } = useI18n()
  const [expanded, setExpanded] = useState(true)
  const childrenID = useId()
  const name = a.task_name.split('/').pop() || a.task_name
  const live = a.status === 'running' || a.status === 'pending'
  // Failed/killed agents also settle; their outcome must remain readable,
  // including for screen readers that cannot see the decorative status dot.
  const phase = ['failed', 'killed', 'interrupted'].includes(a.status) ? a.status : a.phase || a.status
  const row = <article className="runtime-item runtime-agent-item" data-testid="runtime-agent" data-task={a.task_name}>
    <div className="runtime-copy">
      <div className="runtime-title">
        <span className={`runtime-dot${live ? ' live' : ''}${a.status === 'failed' ? ' failed' : ''}`} aria-hidden />
        <button type="button" className="runtime-link" title={a.task_name} onClick={() => onOpenSession?.(a.session_id)}>
          <span>{name}</span><IChev />
        </button>
        <span className="runtime-phase">{t(phaseKeys[phase] ?? 'runtime.phase.executing')}</span>
      </div>
      {a.description && a.description !== name && a.description !== a.task_name ? <p className="runtime-description">{a.description}</p> : null}
      <div className="runtime-meta">
        <span>{t('runtime.generation', { generation: a.generation ?? 0 })}</span>
        {a.current_tools?.length ? <span className="runtime-tools">{a.current_tools.map(tool => tool.name).join(', ')}</span> : null}
        {a.run_stats ? <span>{t('runtime.stats', { tools: a.run_stats.tools, tokens: a.run_stats.total_tokens })}</span> : null}
      </div>
    </div>
    {live && a.session_id !== sessionId && onInterrupt ? <button type="button" className="runtime-control" onClick={() => void onInterrupt(a.session_id)}>{t('runtime.interruptAgent')}</button> : null}
  </article>
  return <li className="runtime-agent-node">
    <div className="runtime-branch">
      {/* Branch toggling must be separate from navigation/stop controls: a
          summary's center can otherwise activate a nested session link. */}
      <div className="runtime-agent-row">
        {/* Every node reserves the same disclosure column. Without a leaf
            spacer, children render to the left of their parent's name. */}
        {children.length ? <button type="button" className="runtime-toggle" aria-expanded={expanded} aria-controls={childrenID} aria-label={t(expanded ? 'runtime.collapse' : 'runtime.expand', { name: a.task_name })} onClick={() => setExpanded(value => !value)}><IChevDown /></button> : <span className="runtime-toggle-space" aria-hidden />}
        {row}
      </div>
      {children.length ? <ul id={childrenID} className="runtime-tree runtime-agent-tree" hidden={!expanded}>{children.map(branch => <AgentItem key={branch.agent.task_name} branch={branch} sessionId={sessionId} onOpenSession={onOpenSession} onInterrupt={onInterrupt} />)}</ul> : null}
    </div>
  </li>
}

export function RuntimePanel({ processes, agents, sessionId, onOpenSession, onStop, onInterrupt }: RuntimePanelProps) {
  const { t } = useI18n()
  const tree = useMemo(() => agentTree(agents), [agents])
  const active = processes.filter(p => p.status === 'running')
  const owners = [...new Set(active.map(p => p.owner_session_id || sessionId))]
  return <div className="runtime-info" data-testid="runtime-panel">
    <section className="cfg-block" id="info-agents">
      <h2 className="cfg-h">{t('runtime.agents')}<span className="runtime-count">{agents.length}</span></h2>
      {tree.length ? <ul className="runtime-tree runtime-agent-tree runtime-tree-root">{tree.map(branch => <AgentItem key={branch.agent.task_name} branch={branch} sessionId={sessionId} onOpenSession={onOpenSession} onInterrupt={onInterrupt} />)}</ul> : <p className="cfg-empty">{t('runtime.agentsEmpty')}</p>}
    </section>
    <section className="cfg-block" id="info-processes">
      <h2 className="cfg-h">{t('runtime.processes')}<span className="runtime-count">{active.length}</span></h2>
      {active.length ? <ul className="runtime-tree runtime-tree-root">
        {owners.map(owner => <li key={owner}>
          <div className="runtime-owner">
            <button type="button" className="runtime-link" onClick={() => onOpenSession?.(owner)}>
              <span>{agents.find(a => a.session_id === owner)?.task_name || owner}</span><IChev />
            </button>
          </div>
          <ul className="runtime-tree">
            {active.filter(p => (p.owner_session_id || sessionId) === owner).map(p => <li key={p.session_id}>
              <article className="runtime-item" data-testid="runtime-process">
                <div className="runtime-copy">
                  <div className="runtime-title"><span className="runtime-dot live" aria-hidden /><button type="button" className="runtime-link runtime-command" onClick={() => onOpenSession?.(owner)}><span>{p.cmd || `#${p.session_id}`}</span><IChev /></button></div>
                  <div className="runtime-meta"><span>{p.tty ? 'TTY' : 'Pipe'} · #{p.session_id}</span>{p.workdir ? <span>{p.workdir}</span> : null}</div>
                </div>
                {onStop ? <button type="button" className="runtime-control" onClick={() => void onStop(p.session_id)}>{t('runtime.stopProcess')}</button> : null}
                {p.output ? <details className="runtime-output"><summary>{t('runtime.output')}</summary><pre>{p.output}</pre></details> : null}
              </article>
            </li>)}
          </ul>
        </li>)}
      </ul> : <p className="cfg-empty">{t('runtime.processesEmpty')}</p>}
    </section>
  </div>
}
