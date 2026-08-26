import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { Client } from './api'
import { ICheck, IEdit, IRegen, ITraj, IWrench } from './icons'
import { useI18n, type MsgKey } from './i18n'
import { toast } from './toast'
import type { CatalogExtension, CatalogMcp, CatalogSkill, SessionCommand, SessionDetail } from './types'

const SOURCE_KEY: Record<string, MsgKey> = {
  home: 'cfg.src.home',
  'user-agents': 'cfg.src.user-agents',
  project: 'cfg.src.project',
  'ancestor-agents': 'cfg.src.ancestor-agents',
}

const INFO_SECTIONS = [
  { id: 'info-session', label: 'cfg.session', children: [] },
  { id: 'info-skills', label: 'cfg.skills', children: [] },
  { id: 'info-mcp', label: 'cfg.mcp', children: [] },
  { id: 'info-extensions', label: 'cfg.extensions', children: [] },
  { id: 'info-commands', label: 'cfg.commands', children: [] },
] as const

type OutlineItem = {
  id: string
  label: string
  children?: OutlineItem[]
}

function flattenOutline(items: OutlineItem[]): OutlineItem[] {
  return items.flatMap(item => [item, ...flattenOutline(item.children ?? [])])
}

export function SessionConfig({
  api,
  sessionId,
  workspaceTitle,
  busy,
  onEdit,
  treeAvailable,
  onTreeOpen,
}: {
  api: Client
  sessionId: string | null
  workspaceTitle?: string
  busy?: boolean
  onEdit?: (page: 'skills' | 'mcp' | 'extensions') => void
  treeAvailable?: boolean
  onTreeOpen?: () => void
}) {
  const { t } = useI18n()
  const [detail, setDetail] = useState<SessionDetail | null>(null)
  const [loading, setLoading] = useState(false)
  const [activeSection, setActiveSection] = useState<string>(INFO_SECTIONS[0].id)
  const configRef = useRef<HTMLDivElement>(null)
  const model = detail ? [detail.provider, detail.model].filter(Boolean).join('/') : ''
  const skills = detail?.availableSkills ?? []
  const mcps = detail?.availableMcp ?? []
  const extensions = detail?.availableExtensions ?? []
  const commands = detail?.commands ?? []
  const outlineGroups = useMemo<OutlineItem[]>(() => [
    { id: 'info-session', label: t('cfg.session') },
    {
      id: 'info-skills',
      label: t('cfg.skills'),
      children: skills.map((item, i) => ({ id: `info-skill-${i}`, label: item.name })),
    },
    {
      id: 'info-mcp',
      label: t('cfg.mcp'),
      children: mcps.map((item, i) => ({
        id: `info-mcp-${i}`,
        label: item.name,
        children: (item.tools ?? []).map((tool, j) => ({ id: `info-mcp-${i}-tool-${j}`, label: tool.name })),
      })),
    },
    {
      id: 'info-extensions',
      label: t('cfg.extensions'),
      children: extensions.map((item, i) => ({ id: `info-extension-${i}`, label: item.name })),
    },
    {
      id: 'info-commands',
      label: t('cfg.commands'),
      children: commands.map((item, i) => ({ id: `info-command-${i}`, label: `/${item.name}` })),
    },
  ], [commands, extensions, mcps, skills, t])
  const outlineItems = useMemo(() => flattenOutline(outlineGroups), [outlineGroups])

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

  useEffect(() => {
    const root = configRef.current
    if (!root || !sessionId) return
    const updateActive = () => {
      const marker = root.getBoundingClientRect().top + 72
      let current = outlineItems[0]?.id ?? INFO_SECTIONS[0].id
      for (const section of outlineItems) {
        const el = root.querySelector<HTMLElement>(`#${section.id}`)
        if (el && el.getBoundingClientRect().top <= marker) current = section.id
      }
      setActiveSection(current)
    }
    updateActive()
    root.addEventListener('scroll', updateActive, { passive: true })
    return () => root.removeEventListener('scroll', updateActive)
  }, [detail, loading, outlineItems, sessionId])

  const jumpTo = (id: string) => {
    configRef.current?.querySelector<HTMLElement>(`#${id}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
    setActiveSection(id)
  }

  if (!sessionId) {
    return (
      <div className="session-config" data-testid="session-info" ref={configRef}>
        <p className="cfg-empty">{t('cfg.needSession')}</p>
      </div>
    )
  }

  return (
    <div className="session-config" data-testid="session-info" ref={configRef}>
      <div className="cfg-layout">
        <div className="cfg-main">
          <div className="cfg-actions">
            <ReloadButton testid="info-reload" run={async () => { await api.reload(sessionId); await load() }} />
            <button type="button" className="cfg-btn" data-testid="info-edit" onClick={() => onEdit?.('skills')}>
              <IEdit /> {t('cfg.edit')}
            </button>
            {treeAvailable ? (
              <button type="button" className="cfg-btn" data-testid="info-tree" onClick={onTreeOpen}>
                <ITraj /> {t('tree.open')}
              </button>
            ) : null}
          </div>
          <section className="cfg-block" id="info-session">
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
              {detail?.parentSessionId ? (
                <div>
                  <dt>{t('cfg.parent')}</dt>
                  <dd>{detail.parentSessionId}</dd>
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

          <section className="cfg-block" id="info-skills">
            <h3 className="cfg-h">{t('cfg.skills')}</h3>
            {skills.length === 0 && !loading ? (
              <p className="cfg-empty">{t('cfg.skillsEmpty')}</p>
            ) : (
              <ul className="cfg-list">
                {skills.map((item, i) => (
                  <li key={item.name} id={`info-skill-${i}`} className="cfg-row" data-testid="cfg-skill" data-name={item.name}>
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

          <section className="cfg-block" id="info-mcp">
            <h3 className="cfg-h">{t('cfg.mcp')}</h3>
            {mcps.length === 0 && !loading ? (
              <p className="cfg-empty">{t('cfg.mcpEmpty')}</p>
            ) : (
              <ul className="cfg-list">
                {mcps.map((item, i) => (
                  <li key={item.name} id={`info-mcp-${i}`} className="cfg-row cfg-mcp-row" data-testid="cfg-mcp" data-name={item.name}>
                    <div className="cfg-copy">
                      <div className="cfg-name cfg-mcp-name">
                        <span className="cfg-mcp-mark" aria-hidden><IWrench /></span>
                        <span className="cfg-mcp-title">{item.name}</span>
                        <span className={`cfg-flag${item.enabled ? ' on' : ''}`}>{item.enabled ? t('cfg.enabled') : t('cfg.disabled')}</span>
                      </div>
                      <div className="cfg-endpoint"><code>{[item.command, ...(item.args ?? [])].filter(Boolean).join(' ') || item.url || '—'}</code></div>
                      {item.error ? <p className="cfg-desc settings-error" role="alert">{item.error}</p> : null}
                      {item.tools && item.tools.length > 0 ? (
                        <div className="cfg-tools-wrap">
                          <div className="cfg-tools-head">
                            <span>{t('cfg.tools')}</span>
                            <span className="cfg-tools-count">{t('cfg.toolCount', { n: item.tools.length })}</span>
                          </div>
                          <ul className="cfg-tools">
                            {item.tools.map((tool, j) => (
                              <li key={tool.name} id={`info-mcp-${i}-tool-${j}`} className="cfg-tool" data-testid="cfg-mcp-tool">
                                <code>{tool.name}</code>
                                {tool.description ? <span>{tool.description}</span> : null}
                              </li>
                            ))}
                          </ul>
                        </div>
                      ) : null}
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section className="cfg-block" id="info-extensions">
            <h3 className="cfg-h">{t('cfg.extensions')}</h3>
            {extensions.length === 0 && !loading ? (
              <p className="cfg-empty">{t('cfg.extensionsEmpty')}</p>
            ) : (
              <ul className="cfg-list">
                {extensions.map((item, i) => (
                  <li key={item.name} id={`info-extension-${i}`} className="cfg-row" data-testid="cfg-extension" data-name={item.name}>
                    <div className="cfg-copy">
                      <div className="cfg-name">
                        {item.name}
                        <span className={`cfg-flag${item.enabled ? ' on' : ''}`}>{item.enabled ? t('cfg.enabled') : t('cfg.disabled')}</span>
                      </div>
                      {item.description ? <p className="cfg-desc">{item.description}</p> : null}
                      {item.error ? <p className="cfg-desc settings-error" role="alert">{item.error}</p> : null}
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section className="cfg-block" id="info-commands">
            <h3 className="cfg-h">{t('cfg.commands')}</h3>
            {commands.length === 0 && !loading ? (
              <p className="cfg-empty">{t('cfg.commandsEmpty')}</p>
            ) : (
              <ul className="cfg-list">
                {commands.map((item, i) => (
                  <li key={`${item.source}:${item.name}`} id={`info-command-${i}`} className="cfg-row" data-testid="cfg-command" data-name={item.name}>
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

        <aside className="cfg-outline" data-testid="info-outline">
          <div className="cfg-outline-title">{t('cfg.outline')}</div>
          <nav aria-label={t('cfg.outline')}>
            {outlineGroups.map(group => {
              const renderItem = (item: OutlineItem, level: number): ReactNode => (
                <div key={item.id} className="cfg-outline-branch">
                  <a
                    className={`cfg-outline-link level-${level}${activeSection === item.id ? ' active' : ''}`}
                    href={`#${item.id}`}
                    aria-current={activeSection === item.id ? 'location' : undefined}
                    onClick={event => { event.preventDefault(); jumpTo(item.id) }}
                  >
                    {item.label}
                  </a>
                  {item.children?.map(child => renderItem(child, level + 1))}
                </div>
              )
              return renderItem(group, 0)
            })}
          </nav>
        </aside>
      </div>
    </div>
  )
}

export function SettingsToggles({
  kind,
  api,
  workspaceId,
}: {
  kind: 'skills' | 'mcp' | 'extensions'
  api: Client
  workspaceId?: string | null
}) {
  const { t } = useI18n()
  const [items, setItems] = useState<Array<CatalogSkill | CatalogMcp | CatalogExtension>>([])
  const [loading, setLoading] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const next = kind === 'skills' ? await api.skills(workspaceId) : kind === 'mcp' ? await api.mcpServers() : await api.extensions()
      setItems(next)
    } catch (e) {
      toast.from(e)
    } finally {
      setLoading(false)
    }
  }, [api, kind, workspaceId])

  useEffect(() => { void load() }, [load])

  const patch = async (next: Array<CatalogSkill | CatalogMcp | CatalogExtension>) => {
    const prev = items
    setItems(next)
    try {
      const disabled = next.filter(s => !s.enabled).map(s => s.name)
      if (kind === 'skills') await api.patchSkills(disabled)
      else if (kind === 'mcp') await api.patchMcp(disabled)
      else await api.patchExtensions(disabled)
      const listed = kind === 'skills' ? await api.skills(workspaceId) : kind === 'mcp' ? await api.mcpServers() : await api.extensions()
      setItems(listed)
    } catch (e) {
      setItems(prev)
      toast.from(e)
    }
  }

  const empty = kind === 'skills' ? t('cfg.skillsEmpty') : kind === 'mcp' ? t('cfg.mcpEmpty') : t('cfg.extensionsEmpty')
  const title = kind === 'skills' ? t('settings.skills') : kind === 'mcp' ? t('settings.mcp') : t('settings.extensions')
  const hint = kind === 'skills' ? t('settings.skillsHint') : kind === 'mcp' ? t('settings.mcpHint') : t('settings.extensionsHint')
  return (
    <div className="preference-page" data-testid={`${kind}-settings`}>
      <header className="settings-page-title">
        <div>
          <h3>{title}</h3>
          <p>{hint}</p>
        </div>
        <ReloadButton testid={`${kind}-reload`} run={async () => { await api.reload(); await load() }} />
      </header>
      {items.length === 0 && !loading ? <p className="cfg-empty">{empty}</p> : (
        <ul className="cfg-list">
          {items.map(item => (
            <li key={item.name} className="cfg-row" data-testid={`cfg-${kind === 'skills' ? 'skill' : kind === 'mcp' ? 'mcp' : 'extension'}`} data-name={item.name}>
              <div className="cfg-copy">
                <div className="cfg-name">
                  {item.name}
                  {kind === 'skills' && item.source ? <span className="cfg-src">{SOURCE_KEY[item.source] ? t(SOURCE_KEY[item.source]) : item.source}</span> : null}
                </div>
                {'description' in item && item.description ? <p className="cfg-desc">{item.description}</p> : null}
                {'command' in item ? <p className="cfg-desc">{[item.command, ...(item.args ?? [])].filter(Boolean).join(' ')}</p> : null}
                {'error' in item && item.error ? <p className="cfg-desc settings-error-inline" data-testid={`${kind}-error-${item.name}`} role="alert">{item.error}</p> : null}
              </div>
              <Switch
                testid={`${kind === 'skills' ? 'skill' : kind === 'mcp' ? 'mcp' : 'extension'}-on-${item.name}`}
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

export function MessageSettings({ api }: { api: Client }) {
  const { t } = useI18n()
  const [busy, setBusy] = useState<'steer' | 'queue'>('steer')
  useEffect(() => {
    void api.message().then(got => setBusy(got.busy)).catch(e => toast.from(e))
  }, [api])
  const save = async (next: 'steer' | 'queue') => {
    const prev = busy
    setBusy(next)
    try { await api.patchMessage(next) }
    catch (e) { setBusy(prev); toast.from(e) }
  }
  return (
    <div className="preference-page" data-testid="message-settings">
      <header className="settings-page-title">
        <div>
          <h3>{t('settings.message')}</h3>
          <p>{t('settings.messageHint')}</p>
        </div>
      </header>
      <section className="preference-section">
        <div className="theme-picks" data-testid="settings-busy" role="radiogroup" aria-label={t('settings.busyDelivery')}>
          <button type="button" role="radio" aria-checked={busy === 'steer'} className={`theme-pick${busy === 'steer' ? ' on' : ''}`} data-testid="busy-steer" onClick={() => void save('steer')}>
            <span>{t('settings.busySteer')}</span>
          </button>
          <button type="button" role="radio" aria-checked={busy === 'queue'} className={`theme-pick${busy === 'queue' ? ' on' : ''}`} data-testid="busy-queue" onClick={() => void save('queue')}>
            <span>{t('settings.busyQueue')}</span>
          </button>
        </div>
      </section>
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
