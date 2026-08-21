import { useCallback, useEffect, useState } from 'react'
import type { Client } from './api'
import { ICheck, IEdit, IRegen } from './icons'
import { useI18n, type MsgKey } from './i18n'
import { toast } from './toast'
import type { CatalogMcp, CatalogSkill, SessionCommand, SessionDetail } from './types'

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
  onEdit,
}: {
  api: Client
  sessionId: string | null
  workspaceTitle?: string
  busy?: boolean
  onEdit?: (page: 'skills' | 'mcp') => void
}) {
  const { t } = useI18n()
  const [detail, setDetail] = useState<SessionDetail | null>(null)
  const [loading, setLoading] = useState(false)

  const load = useCallback(async () => {
    if (!sessionId) {
      setDetail(null)
      return
    }
    setLoading(true)
    try {
      setDetail(await api.get(sessionId))
    } catch (e) {
      toast.from(e)
    } finally {
      setLoading(false)
    }
  }, [api, sessionId])

  useEffect(() => { void load() }, [load])

  if (!sessionId) {
    return (
      <div className="session-config" data-testid="session-info">
        <p className="cfg-empty">{t('cfg.needSession')}</p>
      </div>
    )
  }

  const model = detail ? [detail.provider, detail.model].filter(Boolean).join('/') : ''
  const skills = detail?.availableSkills ?? []
  const mcps = detail?.availableMcp ?? []
  const commands = detail?.commands ?? []

  return (
    <div className="session-config" data-testid="session-info">
      <div className="cfg-actions">
        <ReloadButton testid="info-reload" run={async () => { await api.reload(); await load() }} />
        <button type="button" className="cfg-btn" data-testid="info-edit" onClick={() => onEdit?.('skills')}>
          <IEdit /> {t('cfg.edit')}
        </button>
      </div>
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
                    <span className={`cfg-flag${item.enabled ? ' on' : ''}`}>{item.enabled ? t('cfg.enabled') : t('cfg.disabled')}</span>
                  </div>
                  {item.description ? <p className="cfg-desc">{item.description}</p> : null}
                </div>
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
                    <span className={`cfg-flag${item.enabled ? ' on' : ''}`}>{item.enabled ? t('cfg.enabled') : t('cfg.disabled')}</span>
                  </div>
                  <p className="cfg-desc">{[item.command, ...(item.args ?? [])].filter(Boolean).join(' ') || item.url}</p>
                  {item.tools && item.tools.length > 0 ? (
                    <ul className="cfg-tools">
                      {item.tools.map(tool => (
                        <li key={tool.name}><code>{tool.name}</code>{tool.description ? ` — ${tool.description}` : ''}</li>
                      ))}
                    </ul>
                  ) : null}
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="cfg-block">
        <h3 className="cfg-h">{t('cfg.commands')}</h3>
        {commands.length === 0 && !loading ? (
          <p className="cfg-empty">{t('cfg.commandsEmpty')}</p>
        ) : (
          <ul className="cfg-list">
            {commands.map(item => (
              <li key={`${item.source}:${item.name}`} className="cfg-row" data-testid="cfg-command" data-name={item.name}>
                <div className="cfg-copy">
                  <div className="cfg-name">
                    /{item.name}
                    <span className="cfg-src">{item.source}</span>
                  </div>
                  {item.description ? <p className="cfg-desc">{item.description}</p> : null}
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
      {busy ? <p className="cfg-hint">{t('cfg.hintBusy')}</p> : null}
    </div>
  )
}

export function SettingsToggles({
  kind,
  api,
  workspaceId,
}: {
  kind: 'skills' | 'mcp'
  api: Client
  workspaceId?: string | null
}) {
  const { t } = useI18n()
  const [items, setItems] = useState<Array<CatalogSkill | CatalogMcp>>([])
  const [loading, setLoading] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const next = kind === 'skills' ? await api.skills(workspaceId) : await api.mcpServers(workspaceId)
      setItems(next)
    } catch (e) {
      toast.from(e)
    } finally {
      setLoading(false)
    }
  }, [api, kind, workspaceId])

  useEffect(() => { void load() }, [load])

  const patch = async (next: Array<CatalogSkill | CatalogMcp>) => {
    const prev = items
    setItems(next)
    try {
      const disabled = next.filter(s => !s.enabled).map(s => s.name)
      if (kind === 'skills') await api.patchSkills(disabled)
      else await api.patchMcp(disabled)
      const listed = kind === 'skills' ? await api.skills(workspaceId) : await api.mcpServers(workspaceId)
      setItems(listed)
    } catch (e) {
      setItems(prev)
      toast.from(e)
    }
  }

  const empty = kind === 'skills' ? t('cfg.skillsEmpty') : t('cfg.mcpEmpty')
  return (
    <div className="preference-page" data-testid={`${kind}-settings`}>
      <header className="settings-page-title">
        <div>
          <h3>{kind === 'skills' ? t('settings.skills') : t('settings.mcp')}</h3>
          <p>{kind === 'skills' ? t('settings.skillsHint') : t('settings.mcpHint')}</p>
        </div>
        <ReloadButton testid={`${kind}-reload`} run={async () => { await api.reload(); await load() }} />
      </header>
      {items.length === 0 && !loading ? <p className="cfg-empty">{empty}</p> : (
        <ul className="cfg-list">
          {items.map(item => (
            <li key={item.name} className="cfg-row" data-testid={`cfg-${kind === 'skills' ? 'skill' : 'mcp'}`} data-name={item.name}>
              <div className="cfg-copy">
                <div className="cfg-name">
                  {item.name}
                  {item.source ? <span className="cfg-src">{SOURCE_KEY[item.source] ? t(SOURCE_KEY[item.source]) : item.source}</span> : null}
                </div>
                {'description' in item && item.description ? <p className="cfg-desc">{item.description}</p> : null}
                {'command' in item ? <p className="cfg-desc">{[item.command, ...(item.args ?? [])].filter(Boolean).join(' ')}</p> : null}
              </div>
              <Switch
                testid={`${kind === 'skills' ? 'skill' : 'mcp'}-on-${item.name}`}
                on={item.enabled}
                onChange={on => void patch(items.map(s => s.name === item.name ? { ...s, enabled: on } : s))}
              />
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function ReloadButton({ testid, run }: { testid: string; run: () => Promise<void> }) {
  const { t } = useI18n()
  const [phase, setPhase] = useState<'idle' | 'busy' | 'done'>('idle')
  useEffect(() => {
    if (phase !== 'done') return
    const timer = window.setTimeout(() => setPhase('idle'), 1800)
    return () => window.clearTimeout(timer)
  }, [phase])
  return (
    <button
      type="button"
      className={`cfg-btn${phase === 'busy' ? ' busy' : ''}${phase === 'done' ? ' done' : ''}`}
      data-testid={testid}
      disabled={phase === 'busy'}
      aria-busy={phase === 'busy'}
      onClick={() => {
        if (phase === 'busy') return
        setPhase('busy')
        void run().then(
          () => setPhase('done'),
          e => {
            setPhase('idle')
            toast.from(e)
          },
        )
      }}
    >
      {phase === 'busy' ? <span className="spin" data-testid="reload-spin" /> : phase === 'done' ? <ICheck /> : <IRegen />}
      {phase === 'busy' ? t('cfg.reloading') : phase === 'done' ? t('cfg.reloaded') : t('cfg.reload')}
    </button>
  )
}

function Switch({ on, onChange, testid }: { on: boolean; onChange: (on: boolean) => void; testid?: string }) {
  return (
    <button type="button" role="switch" aria-checked={on} className={`switch${on ? ' on' : ''}`} data-testid={testid} onClick={() => onChange(!on)}>
      <span className="switch-knob" aria-hidden />
    </button>
  )
}

export type { SessionCommand }
