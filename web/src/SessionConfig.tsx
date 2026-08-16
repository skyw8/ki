import { useCallback, useEffect, useState } from 'react'
import type { Client } from './api'
import { useI18n, type MsgKey } from './i18n'
import type { CatalogMcp, CatalogSkill, SessionDetail } from './types'

const SOURCE_KEY: Record<string, MsgKey> = {
  home: 'cfg.src.home',
  'user-agents': 'cfg.src.user-agents',
  project: 'cfg.src.project',
  'ancestor-agents': 'cfg.src.ancestor-agents',
}

export function SessionConfig({
  api,
  sessionId,
  workspaceTitle,
  busy,
  onError,
}: {
  api: Client
  sessionId: string | null
  workspaceTitle?: string
  busy?: boolean
  onError: (msg: string | null) => void
}) {
  const { t } = useI18n()
  const [detail, setDetail] = useState<SessionDetail | null>(null)
  const [skills, setSkills] = useState<CatalogSkill[]>([])
  const [mcps, setMcps] = useState<CatalogMcp[]>([])
  const [loading, setLoading] = useState(false)

  const load = useCallback(async () => {
    if (!sessionId) {
      setDetail(null)
      setSkills([])
      setMcps([])
      return
    }
    setLoading(true)
    try {
      const next = await api.get(sessionId)
      setDetail(next)
      setSkills(next.availableSkills ?? [])
      setMcps(next.availableMcp ?? [])
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [api, onError, sessionId])

  useEffect(() => { void load() }, [load])

  const patchSkills = async (next: CatalogSkill[]) => {
    if (!sessionId) return
    const prev = skills
    setSkills(next)
    try {
      await api.patch(sessionId, { skills: { disabled: next.filter(s => !s.enabled).map(s => s.name) } })
    } catch (e) {
      setSkills(prev)
      onError(e instanceof Error ? e.message : String(e))
      void load()
    }
  }

  const patchMcp = async (next: CatalogMcp[]) => {
    if (!sessionId) return
    const prev = mcps
    setMcps(next)
    try {
      await api.patch(sessionId, { mcp: { disabled: next.filter(s => !s.enabled).map(s => s.name) } })
    } catch (e) {
      setMcps(prev)
      onError(e instanceof Error ? e.message : String(e))
      void load()
    }
  }

  if (!sessionId) {
    return (
      <div className="session-config" data-testid="session-config">
        <p className="cfg-empty">{t('cfg.needSession')}</p>
      </div>
    )
  }

  const model = detail ? [detail.provider, detail.model].filter(Boolean).join('/') : ''

  return (
    <div className="session-config" data-testid="session-config">
      <section className="cfg-block">
        <h3 className="cfg-h">{t('cfg.session')}</h3>
        <dl className="cfg-meta">
          <div>
            <dt>{t('cfg.cwd')}</dt>
            <dd data-testid="cfg-cwd">{detail?.cwd || '—'}</dd>
          </div>
          <div>
            <dt>{t('cfg.workspace')}</dt>
            <dd data-testid="cfg-workspace">{workspaceTitle || '—'}</dd>
          </div>
          <div>
            <dt>{t('cfg.model')}</dt>
            <dd data-testid="cfg-model">{model || '—'}</dd>
          </div>
          <div>
            <dt>{t('cfg.id')}</dt>
            <dd data-testid="cfg-id">{detail?.id || sessionId}</dd>
          </div>
          {detail?.parent ? (
            <div>
              <dt>{t('cfg.parent')}</dt>
              <dd>{detail.parent}</dd>
            </div>
          ) : null}
          {detail?.timestamp ? (
            <div>
              <dt>{t('cfg.created')}</dt>
              <dd>{detail.timestamp}</dd>
            </div>
          ) : null}
        </dl>
      </section>

      <section className="cfg-block">
        <h3 className="cfg-h">{t('cfg.skills')}</h3>
        {skills.length === 0 && !loading ? (
          <p className="cfg-empty">{t('cfg.skillsEmpty')}</p>
        ) : (
          <ul className="cfg-list">
            {skills.map(item => (
              <li key={item.name} className="cfg-row" data-testid="cfg-skill" data-name={item.name}>
                <div className="cfg-copy">
                  <div className="cfg-name">
                    {item.name}
                    {item.source ? <span className="cfg-src">{SOURCE_KEY[item.source] ? t(SOURCE_KEY[item.source]) : item.source}</span> : null}
                  </div>
                  {item.description ? <p className="cfg-desc">{item.description}</p> : null}
                </div>
                <Switch
                  testid={`skill-on-${item.name}`}
                  on={item.enabled}
                  onChange={on => void patchSkills(skills.map(s => s.name === item.name ? { ...s, enabled: on } : s))}
                />
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="cfg-block">
        <h3 className="cfg-h">{t('cfg.mcp')}</h3>
        {mcps.length === 0 && !loading ? (
          <p className="cfg-empty">{t('cfg.mcpEmpty')}</p>
        ) : (
          <ul className="cfg-list">
            {mcps.map(item => (
              <li key={item.name} className="cfg-row" data-testid="cfg-mcp" data-name={item.name}>
                <div className="cfg-copy">
                  <div className="cfg-name">
                    {item.name}
                    {item.source ? <span className="cfg-src">{SOURCE_KEY[item.source] ? t(SOURCE_KEY[item.source]) : item.source}</span> : null}
                  </div>
                  <p className="cfg-desc">{[item.command, ...(item.args ?? [])].filter(Boolean).join(' ')}</p>
                </div>
                <Switch
                  testid={`mcp-on-${item.name}`}
                  on={item.enabled}
                  onChange={on => void patchMcp(mcps.map(s => s.name === item.name ? { ...s, enabled: on } : s))}
                />
              </li>
            ))}
          </ul>
        )}
      </section>

      {busy ? <p className="cfg-hint">{t('cfg.hintBusy')}</p> : <p className="cfg-hint">{t('cfg.hint')}</p>}
    </div>
  )
}

function Switch({ testid, on, onChange }: { testid: string; on: boolean; onChange: (on: boolean) => void }) {
  return (
    <button
      type="button"
      className={`switch${on ? ' on' : ''}`}
      role="switch"
      aria-checked={on}
      data-testid={testid}
      onClick={() => onChange(!on)}
    >
      <span className="switch-knob" />
    </button>
  )
}
