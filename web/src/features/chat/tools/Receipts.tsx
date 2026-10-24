import { memo } from 'react'
import { useI18n } from '../../../i18n/index'

type Receipt = { label: string; value: string }

const FIELDS: [string, string][] = [
  ['task_name', 'task'],
  ['status', 'status'],
  ['wake_reason', 'wake'],
  ['timed_out', 'timedOut'],
  ['agent_id', 'agent'],
  ['session_id', 'session'],
  ['message', 'message'],
]

export function receiptOf(details: unknown): Receipt[] | null {
  if (!details || typeof details !== 'object' || Array.isArray(details)) return null
  const raw = details as Record<string, unknown>
  const out: Receipt[] = []
  for (const [key, label] of FIELDS) {
    const value = raw[key]
    if (value === undefined || value === null || value === '') continue
    if (typeof value === 'string') out.push({ label, value })
    else if (typeof value === 'number' || typeof value === 'boolean') out.push({ label, value: String(value) })
  }
  return out.length ? out : null
}

export const AgentReceipt = memo(function AgentReceipt({ details, testId }: { details: unknown; testId?: string }) {
  const { t } = useI18n()
  const receipt = receiptOf(details)
  if (!receipt) return null
  return (
    <div className="receipt" data-testid={testId ?? 'agent-receipt'}>
      {receipt.map(({ label, value }) => (
        <span className={`receipt-item receipt-${label}`} key={label} title={value}>
          <span className="receipt-label">{t(`tool.receipt.${label}` as 'tool.receipt.status')}</span>
          <span className="receipt-value">{value}</span>
        </span>
      ))}
    </div>
  )
})

export function agentListSummary(details: unknown): { total: number; active: number } | null {
  if (!details || typeof details !== 'object') return null
  const raw = details as { agents?: unknown[]; total?: unknown }
  if (!Array.isArray(raw.agents)) return null
  const active = raw.agents.filter(agent => {
    const status = (agent as { status?: string }).status
    return status === 'running' || status === 'pending'
  }).length
  const total = typeof raw.total === 'number' ? raw.total : raw.agents.length
  return { total, active }
}

export const AgentList = memo(function AgentList({ details, testId }: { details: unknown; testId?: string }) {
  const { t } = useI18n()
  const summary = agentListSummary(details)
  if (!summary) return null
  return (
    <div className="receipt" data-testid={testId ?? 'agent-list'}>
      <span className="receipt-item">
        <span className="receipt-label">{t('tool.receipt.status')}</span>
        <span className="receipt-value">{t('tool.agentListMeta', { total: summary.total, active: summary.active })}</span>
      </span>
    </div>
  )
})
