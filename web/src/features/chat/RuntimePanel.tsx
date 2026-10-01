import type { AgentSnapshot, ProcessSnapshot } from '../../api/types'
import { useI18n, type MsgKey } from '../../i18n'

const phaseKeys: Record<string, MsgKey> = {
 starting: 'runtime.phase.starting', executing: 'runtime.phase.executing', waiting_message: 'runtime.phase.waiting_message',
 waiting_resource: 'runtime.phase.waiting_resource', settled: 'runtime.phase.settled', interrupted: 'runtime.phase.interrupted',
 running: 'runtime.phase.running', completed: 'runtime.phase.completed', failed: 'runtime.phase.failed', killed: 'runtime.phase.killed', pending: 'runtime.phase.waiting_resource',
}

export function RuntimePanel({ processes, agents, onStop, onInterrupt }: {
 processes: ProcessSnapshot[]; agents: AgentSnapshot[]
 onStop: (id: number) => Promise<void>; onInterrupt: (sessionID: string) => Promise<void>
}) {
 const { t } = useI18n()
 const active = processes.filter(p=>p.status==='running')
 const children = agents.filter(a=>a.task_name!=='/root')
 if (!active.length && !children.length) return null
 return <details className="runtime-panel" data-testid="runtime-panel">
  <summary>{t('runtime.summary', { processes: active.length, agents: children.length })}</summary>
  <div className="runtime-items">
   {active.map(p=><article key={p.session_id} className="runtime-item" data-testid="runtime-process">
    <div><strong>{p.cmd}</strong><small>{p.tty ? 'TTY' : 'Pipe'} · {p.session_id}</small></div>
    <button type="button" onClick={()=>void onStop(p.session_id)}>{t('runtime.stopProcess')}</button>
    {p.output ? <pre>{p.output}</pre> : null}
   </article>)}
   {children.map(a=><article key={a.task_name} className="runtime-item" data-testid="runtime-agent">
    <div><strong>{a.task_name}</strong><small>{t(phaseKeys[a.phase || a.status] ?? 'runtime.phase.executing')} · {t('runtime.generation', { generation: a.generation ?? 0 })}</small>
     {a.current_tools?.length ? <small>{a.current_tools.map(tool => tool.name).join(', ')}</small> : null}
     {a.run_stats ? <small>{t('runtime.stats', { tools: a.run_stats.tools, tokens: a.run_stats.total_tokens })}</small> : null}</div>
    {a.status==='running' || a.status==='pending' ? <button type="button" onClick={()=>void onInterrupt(a.session_id)}>{t('runtime.interruptAgent')}</button> : null}
   </article>)}
  </div>
 </details>
}
