import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { Client } from '../../api/client'
import { ICheck, ICopy, IEdit, IRegen } from '../../components/icons'
import { Switch } from '../../components/Switch'
import { copyText } from '../../lib/clipboard'
import { useI18n, type Lang, type MsgKey, type TFn } from '../../i18n/index'
import { clampCompactKeep, MAX_COMPACT_KEEP, type MessageView } from '../../lib/messageView'
import type { NotifyPermission } from '../../lib/notifications'
import { toast } from '../../components/toast'
import { localizedExtensionText } from './ExtensionPanel'
import { ExtensionInstallList } from './ExtensionInstallList'
import { RuntimePanel, type RuntimePanelProps } from '../chat/RuntimePanel'
import type { CatalogContribution, CatalogExtension, CatalogSkill, CatalogTool, ExtensionI18n, MCPServerInfo, SessionCommand, SessionDetail } from '../../api/types'

const SOURCE_KEY: Record<string, MsgKey> = {
  home: 'cfg.src.home',
  global: 'cfg.src.global',
  'user-agents': 'cfg.src.user-agents',
  project: 'cfg.src.project',
  'ancestor-agents': 'cfg.src.ancestor-agents',
  prompt: 'cfg.src.prompt',
  runtime: 'cfg.src.runtime',
  extension: 'cfg.src.sidecar',
  builtin: 'cfg.src.builtin',
  skill: 'cfg.src.skill',
}

function sourceLabel(source: string | undefined, t: TFn): string {
  if (!source) return ''
  if (SOURCE_KEY[source]) return t(SOURCE_KEY[source])
  if (source.startsWith('extension:')) return t('cfg.src.extension', { name: source.slice('extension:'.length) })
  return source
}

const INFO_SECTIONS = [
  { id: 'info-session', label: 'cfg.session', children: [] },
  { id: 'info-agents', label: 'runtime.agents', children: [] },
  { id: 'info-processes', label: 'runtime.processes', children: [] },
  { id: 'info-skills', label: 'cfg.skills', children: [] },
  { id: 'info-mcp', label: 'settings.mcp', children: [] },
  { id: 'info-extensions', label: 'cfg.extensions', children: [] },
  { id: 'info-commands', label: 'cfg.commands', children: [] },
] as const

function extensionDescription(item: { description?: string; i18n?: ExtensionI18n }, lang: Lang): string {
  if (!item.description && !item.i18n?.resources) return ''
  return localizedExtensionText({ key: 'manifest.description', fallback: item.description || '' }, item.i18n, lang)
}

type OutlineItem = {
  id: string
  label: string
  children?: OutlineItem[]
}

function extensionOutlineChildren(item: CatalogExtension, index: number): OutlineItem[] {
  return [
    ...(item.skills ?? []).map((sk, j) => ({ id: `info-extension-${index}-skill-${j}`, label: sk.name })),
    ...(item.tools ?? []).map((tool, j) => ({ id: `info-extension-${index}-tool-${j}`, label: tool.name })),
    ...(item.commands ?? []).map((cmd, j) => ({ id: `info-extension-${index}-command-${j}`, label: `/${cmd.name}` })),
    ...(item.promptAppend ?? []).map((file, j) => ({ id: `info-extension-${index}-prompt-${j}`, label: file })),
    ...(item.pathDirs ?? []).map((dir, j) => ({ id: `info-extension-${index}-path-${j}`, label: dir.path })),
    ...(item.providers ?? []).map((p, j) => ({ id: `info-extension-${index}-provider-${j}`, label: p.name || p.id })),
  ]
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
  onOpenSession,
  runtimeReady,
  runtime,
}: {
  api: Client
  sessionId: string | null
  workspaceTitle?: string
  busy?: boolean
  onEdit?: (page: 'skills' | 'extensions') => void
  onOpenSession?: (id: string) => void
  runtimeReady?: boolean
  runtime?: Omit<RuntimePanelProps, 'sessionId' | 'onOpenSession'>
}) {
  const { t, lang } = useI18n()
  const [detail, setDetail] = useState<SessionDetail | null>(null)
  const [loading, setLoading] = useState(false)
  const [activeSection, setActiveSection] = useState<string>(INFO_SECTIONS[0].id)
  const configRef = useRef<HTMLDivElement>(null)
  const model = detail ? [detail.provider, detail.model].filter(Boolean).join('/') : ''
  const skills = detail?.availableSkills ?? []
  const mcp = detail?.availableMCP ?? []
  const extensions = detail?.availableExtensions ?? []
  const commands = detail?.commands ?? []
  const outlineGroups = useMemo<OutlineItem[]>(() => [
    { id: 'info-session', label: t('cfg.session') },
    { id: 'info-agents', label: t('runtime.agents') },
    { id: 'info-processes', label: t('runtime.processes') },
    {
      id: 'info-skills',
      label: t('cfg.skills'),
      children: skills.map((item, i) => ({ id: `info-skill-${i}`, label: item.name })),
    },
    {
      id: 'info-mcp',
      label: t('settings.mcp'),
      children: mcp.map((item, i) => ({ id: `info-mcp-${i}`, label: item.name })),
    },
    {
      id: 'info-extensions',
      label: t('cfg.extensions'),
      children: extensions.map((item, i) => ({
        id: `info-extension-${i}`,
        label: item.name,
        children: extensionOutlineChildren(item, i),
      })),
    },
    {
      id: 'info-commands',
      label: t('cfg.commands'),
      children: commands.map((item, i) => ({ id: `info-command-${i}`, label: `/${item.name}` })),
    },
    { id: 'info-system', label: t('cfg.systemPrompt') },
  ], [commands, extensions, mcp, skills, t])
  const outlineItems = useMemo(() => flattenOutline(outlineGroups), [outlineGroups])

  const load = useCallback(async () => {
    if (!sessionId) {
      setDetail(null)
      return
    }
    setLoading(true)
    try {
      setDetail(await api.get(sessionId, { fields: 'runtime,system' }))
    } catch (e) {
      toast.from(e)
    } finally {
      setLoading(false)
    }
  }, [api, sessionId])

  useEffect(() => { void load() }, [load, runtimeReady])

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
          </div>
          <section className="cfg-block" id="info-session">
            <h2 className="cfg-h">{t('cfg.session')}</h2>
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
                  <dd>
                    <button
                      type="button"
                      className="cfg-link"
                      data-testid="cfg-parent"
                      onClick={() => onOpenSession?.(detail.parentSessionId!)}
                    >
                      {detail.parentSessionId}
                    </button>
                  </dd>
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

          <RuntimePanel
            sessionId={sessionId}
            onOpenSession={onOpenSession}
            processes={runtime?.processes ?? detail?.processes ?? []}
            agents={runtime?.agents ?? detail?.agents ?? []}
            onStop={runtime?.onStop}
            onInterrupt={runtime?.onInterrupt}
          />

          <section className="cfg-block" id="info-skills">
            <h2 className="cfg-h">{t('cfg.skills')}</h2>
            {skills.length === 0 && !loading ? (
              <p className="cfg-empty">{t('cfg.skillsEmpty')}</p>
            ) : (
              <ul className="cfg-list">
                {skills.map((item, i) => (
                  <li key={item.name} id={`info-skill-${i}`} className="cfg-row" data-testid="cfg-skill" data-name={item.name}>
                    <div className="cfg-copy">
                      <div className="cfg-name">
                        {item.name}
                        {item.source ? <span className="cfg-src">{sourceLabel(item.source, t)}</span> : null}
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
            <h2 className="cfg-h">{t('settings.mcp')}</h2>
            <p className="cfg-hint">{t('cfg.mcpSnapshotHint')}</p>
            {mcp.length === 0 && !loading ? (
              <p className="cfg-empty">{t('cfg.mcpEmpty')}</p>
            ) : (
              <ul className="cfg-list">
                {mcp.map((item, i) => (
                  <li key={item.name} id={`info-mcp-${i}`} className="cfg-row" data-testid="cfg-mcp-info" data-name={item.name}>
                    <div className="cfg-copy">
                      <div className="cfg-name">
                        {item.name}
                        <span className="cfg-src">{sourceLabel(item.source, t)}</span>
                        <span className={`cfg-flag${item.enabled ? ' on' : ''}`}>{item.enabled ? t('cfg.enabled') : t('cfg.disabled')}</span>
                      </div>
                      <MCPMetadata item={item} t={t} />
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section className="cfg-block" id="info-extensions">
            <h2 className="cfg-h">{t('cfg.extensions')}</h2>
            {extensions.length === 0 && !loading ? (
              <p className="cfg-empty">{t('cfg.extensionsEmpty')}</p>
            ) : (
              <ul className="cfg-list cfg-extension-list">
                {extensions.map((item, i) => (
                  <ExtensionInfoCard key={item.name} item={item} index={i} lang={lang} t={t} />
                ))}
              </ul>
            )}
          </section>

          <section className="cfg-block" id="info-commands">
            <h2 className="cfg-h">{t('cfg.commands')}</h2>
            {commands.length === 0 && !loading ? (
              <p className="cfg-empty">{t('cfg.commandsEmpty')}</p>
            ) : (
              <ul className="cfg-list">
                {commands.map((item, i) => (
                  <li key={`${item.source}:${item.name}`} id={`info-command-${i}`} className="cfg-row" data-testid="cfg-command" data-name={item.name}>
                    <div className="cfg-copy">
                      <div className="cfg-name">
                        /{item.name}
                        <span className="cfg-src">{item.extension ? sourceLabel(`extension:${item.extension}`, t) : sourceLabel(item.source, t)}</span>
                      </div>
                      {item.description ? <p className="cfg-desc">{item.description}</p> : null}
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </section>
          <SystemPromptBlock text={detail?.systemPrompt ?? ''} loading={loading} />
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

/**
 * The complete system prompt the model last received, shown raw. It is the
 * rendered prompt from the newest request_header, not a re-render, so the block
 * only frames and copies it instead of interpreting the text.
 */
function SystemPromptBlock({ text, loading }: { text: string; loading: boolean }) {
  const { t } = useI18n()
  const [copied, setCopied] = useState(false)
  const timer = useRef(0)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  const stats = useMemo(() => ({ lines: text ? text.split('\n').length : 0, chars: text.length }), [text])
  return (
    <section className="cfg-block" id="info-system">
      <h2 className="cfg-h">{t('cfg.systemPrompt')}</h2>
      {text ? (
        <div className="cfg-prompt" data-testid="cfg-system-prompt">
          <header className="cfg-prompt-bar">
            <span className="cfg-prompt-stats" data-testid="cfg-system-prompt-stats">
              {t('cfg.systemPromptStats', { lines: String(stats.lines), chars: String(stats.chars) })}
            </span>
            <button
              type="button"
              className={`cfg-btn cfg-prompt-copy${copied ? ' done' : ''}`}
              data-testid="cfg-system-prompt-copy"
              onClick={() => {
                void copyText(text).then(ok => {
                  if (!ok) return
                  setCopied(true)
                  window.clearTimeout(timer.current)
                  timer.current = window.setTimeout(() => setCopied(false), 1500)
                })
              }}
            >
              {copied ? <ICheck /> : <ICopy />}
              <span>{copied ? t('md.copied') : t('chat.copy')}</span>
            </button>
          </header>
          <pre className="cfg-prompt-text" data-testid="cfg-system-prompt-text">{text}</pre>
        </div>
      ) : (
        <p className="cfg-empty" data-testid="cfg-system-prompt-empty">{loading ? t('file.loading') : t('cfg.systemPromptEmpty')}</p>
      )}
    </section>
  )
}

function ExtensionInfoCard({ item, index, lang, t }: { item: CatalogExtension; index: number; lang: Lang; t: TFn }) {
  const desc = extensionDescription(item, lang)
  const skills = item.skills ?? []
  const tools = item.tools ?? []
  const commands = item.commands ?? []
  const prompts = item.promptAppend ?? []
  const pathDirs = item.pathDirs ?? []
  const providers = item.providers ?? []
  const loaded = skills.length + tools.length + commands.length + prompts.length + pathDirs.length + providers.length
  const runtimePending = item.enabled && (item.capabilities ?? []).some(cap => cap === 'tool' || cap === 'command') && item.runtime?.state !== 'ready' && item.runtime?.state !== 'failed'
  return (
    <li id={`info-extension-${index}`} className="cfg-extension" data-testid="cfg-extension" data-name={item.name}>
      <header className="cfg-extension-head">
        <h3 className="cfg-h2">{item.name}</h3>
        {item.version ? <span className="cfg-src">{t('cfg.version')} {item.version}</span> : null}
        <span className={`cfg-flag${item.enabled ? ' on' : ''}`}>{item.enabled ? t('cfg.enabled') : t('cfg.disabled')}</span>
      </header>
      {desc ? <p className="cfg-desc">{desc}</p> : null}
      <dl className="cfg-meta cfg-extension-meta">
        {item.runtime ? (
          <div>
            <dt>{t('cfg.runtime')}</dt>
            <dd>{item.runtime.state}{item.runtime.error ? ` · ${item.runtime.error}` : ''}</dd>
          </div>
        ) : null}
        {item.capabilities?.length ? (
          <div>
            <dt>{t('cfg.capabilities')}</dt>
            <dd className="cfg-caps" data-testid="cfg-extension-capabilities">
              {item.capabilities.map(cap => <span key={cap} className="cfg-src">{cap}</span>)}
            </dd>
          </div>
        ) : null}
      </dl>
      {item.error ? <p className="cfg-desc settings-error" role="alert">{item.error}</p> : null}
      {loaded === 0 && !runtimePending ? (
        <p className="cfg-empty" data-testid="cfg-extension-empty">{t('cfg.contribEmpty')}</p>
      ) : null}
      <ContribGroup
        title={t('cfg.skills')}
        items={skills}
        idPrefix={`info-extension-${index}-skill`}
        testid="cfg-extension-skill"
        sourceOf={item => item.source}
        t={t}
      />
      <ContribGroup
        title={t('cfg.tools')}
        items={tools}
        idPrefix={`info-extension-${index}-tool`}
        testid="cfg-extension-tool"
        t={t}
      />
      <ContribGroup
        title={t('cfg.commands')}
        items={commands}
        idPrefix={`info-extension-${index}-command`}
        testid="cfg-extension-command"
        nameOf={item => `/${item.name}`}
        sourceOf={item => item.source}
        t={t}
      />
      {prompts.length > 0 ? (
        <div className="cfg-contrib">
          <h4 className="cfg-h3">{t('cfg.promptAppend')}</h4>
          <ul className="cfg-contrib-list">
            {prompts.map((file, j) => (
              <li key={file} id={`info-extension-${index}-prompt-${j}`} className="cfg-contrib-item" data-testid="cfg-extension-prompt" data-name={file}>
                <div className="cfg-name">{file}</div>
                <p className="cfg-desc">{t('cfg.promptLayer')}</p>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {pathDirs.length > 0 ? (
        <div className="cfg-contrib">
          <h4 className="cfg-h3">{t('cfg.pathDirs')}</h4>
          <ul className="cfg-contrib-list">
            {pathDirs.map((dir, j) => (
              <li key={dir.path} id={`info-extension-${index}-path-${j}`} className="cfg-contrib-item" data-testid="cfg-extension-path" data-name={dir.path} data-exists={dir.exists ? 'true' : 'false'}>
                <div className="cfg-name">
                  {dir.path}
                  <span className={`cfg-flag${dir.exists ? ' on' : ''}`}>{dir.exists ? t('cfg.pathDirPresent') : t('cfg.pathDirMissing')}</span>
                </div>
                <p className="cfg-desc">{t('cfg.pathDirHint')}</p>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {providers.length > 0 ? (
        <div className="cfg-contrib">
          <h4 className="cfg-h3">{t('cfg.providers')}</h4>
          <ul className="cfg-contrib-list">
            {providers.map((provider, j) => (
              <li key={provider.id} id={`info-extension-${index}-provider-${j}`} className="cfg-contrib-item" data-testid="cfg-extension-provider" data-name={provider.id}>
                <div className="cfg-name">
                  {provider.name || provider.id}
                  {provider.api ? <span className="cfg-src">{provider.api}</span> : null}
                </div>
                {provider.name && provider.name !== provider.id ? <p className="cfg-desc">{provider.id}</p> : null}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </li>
  )
}

function ContribGroup({
  title,
  items,
  idPrefix,
  testid,
  nameOf,
  sourceOf,
  t,
}: {
  title: string
  items: CatalogContribution[]
  idPrefix: string
  testid: string
  nameOf?: (item: CatalogContribution) => string
  sourceOf?: (item: CatalogContribution) => string | undefined
  t: TFn
}) {
  if (items.length === 0) return null
  return (
    <div className="cfg-contrib">
      <h4 className="cfg-h3">{title}</h4>
      <ul className="cfg-contrib-list">
        {items.map((item, j) => {
          const source = sourceOf?.(item)
          return (
            <li key={`${item.name}:${j}`} id={`${idPrefix}-${j}`} className="cfg-contrib-item" data-testid={testid} data-name={item.name}>
              <div className="cfg-name">
                {nameOf ? nameOf(item) : item.name}
                {source ? <span className="cfg-src">{sourceLabel(source, t)}</span> : null}
              </div>
              {item.description ? <p className="cfg-desc">{item.description}</p> : null}
            </li>
          )
        })}
      </ul>
    </div>
  )
}

function MCPMetadata({ item, t }: { item: MCPServerInfo; t: TFn }) {
  return (
    <p className="cfg-desc mcp-metadata">
      <span>{item.transport === 'stdio' ? t('cfg.mcpStdio') : t('cfg.mcpHTTP')}</span>
      <span>{item.tools === undefined ? t('cfg.mcpToolsUnknown') : t('cfg.mcpTools', { count: String(item.tools) })}</span>
      {!item.configuredEnabled ? <span>{t('cfg.mcpConfigDisabled')}</span> : null}
    </p>
  )
}

type ToggleItem = CatalogSkill | CatalogTool | CatalogExtension | MCPServerInfo

export function SettingsToggles({
  kind,
  api,
  sessionId,
  workspaceId,
  onConfigure,
  onChanged,
  refreshKey = 0,
}: {
  kind: 'skills' | 'tools' | 'extensions' | 'mcp'
  api: Client
  sessionId?: string | null
  workspaceId?: string | null
  onConfigure?: (name: string) => void
  onChanged?: () => void | Promise<void>
  /** Bumped whenever the global extension catalog is refetched. */
  refreshKey?: number
}) {
  const { t, lang } = useI18n()
  const [items, setItems] = useState<ToggleItem[]>([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const pending = useRef(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      if (kind === 'tools' || kind === 'mcp') {
        const next = await api.toolsSettings(sessionId, workspaceId)
        setItems(kind === 'mcp' ? next.mcp ?? [] : next.items)
      } else {
        setItems(kind === 'skills' ? await api.skills(workspaceId) : await api.extensions())
      }
    } catch (e) {
      setError(String(e instanceof Error ? e.message : e))
      toast.from(e)
    } finally {
      setLoading(false)
    }
  }, [api, kind, sessionId, workspaceId])

  useEffect(() => { void load() }, [load, refreshKey])

  const patch = async (next: ToggleItem[]) => {
    if (pending.current || loading) return
    pending.current = true
    setSaving(true)
    setError('')
    const prev = items
    let committed = false
    setItems(next)
    try {
      const disabled = next.filter(s => !s.enabled).map(s => s.name)
      if (kind === 'skills') await api.patchSkills(disabled)
      else if (kind === 'mcp') {
        // File-disabled servers cannot be enabled here or become redundant global overrides.
        const mcpDisabled = next.filter(s => !s.enabled && 'configuredEnabled' in s && s.configuredEnabled).map(s => s.name)
        const updated = await api.patchMCP(mcpDisabled, sessionId, workspaceId)
        setItems(updated.mcp ?? [])
      }
      else if (kind === 'tools') {
        const updated = await api.patchTools(disabled, sessionId, workspaceId)
        setItems(updated.items)
      }
      else await api.patchExtensions(disabled)
      committed = true
      if (kind !== 'tools' && kind !== 'mcp') setItems(kind === 'skills' ? await api.skills(workspaceId) : await api.extensions())
      await onChanged?.()
    } catch (e) {
      // A failed catalog refresh must not undo a mutation already saved by the server.
      if (!committed) setItems(prev)
      setError(String(e instanceof Error ? e.message : e))
      toast.from(e)
    } finally {
      pending.current = false
      setSaving(false)
    }
  }

  const empty = kind === 'skills' ? t('cfg.skillsEmpty') : kind === 'tools' ? t('cfg.toolsEmpty') : kind === 'mcp' ? t('cfg.mcpEmpty') : t('cfg.extensionsEmpty')
  const title = kind === 'skills' ? t('settings.skills') : kind === 'tools' ? t('settings.tools') : kind === 'mcp' ? t('settings.mcp') : t('settings.extensions')
  const hint = kind === 'skills' ? t('settings.skillsHint') : kind === 'tools' ? t('settings.toolsHint') : kind === 'mcp' ? t('settings.mcpHint') : t('settings.extensionsHint')
  const itemKind = kind === 'skills' ? 'skill' : kind === 'tools' ? 'tool' : kind === 'mcp' ? 'mcp' : 'extension'
  return (
    <div className={`preference-page${kind === 'mcp' ? ' mcp-settings' : ''}`} data-testid={`${kind}-settings`}>
      <header className="settings-page-title">
        <div>
          <h3>{title}</h3>
          <p>{hint}</p>
        </div>
        <ReloadButton testid={`${kind}-reload`} disabled={loading || saving} run={async () => {
          if (pending.current) return
          pending.current = true
          setSaving(true)
          try { await api.reload(); await load(); await onChanged?.() } finally {
            pending.current = false
            setSaving(false)
          }
        }} />
      </header>
      {error ? <p className="settings-error-inline" role="alert" data-testid={`${kind}-settings-error`}>{error}</p> : null}
      {items.length === 0 && !loading ? <p className="cfg-empty">{empty}</p> : (
        <ul className="cfg-list">
          {items.map(item => (
            <li key={item.name} className="cfg-row" data-testid={`cfg-${itemKind}`} data-name={item.name}>
              <div className="cfg-copy">
                <div className="cfg-name">
                  {item.name}
                  {kind !== 'extensions' && 'source' in item && item.source ? <span className="cfg-src">{sourceLabel(item.source, t)}</span> : null}
                  {kind === 'tools' && 'available' in item && item.available === false ? <span className="cfg-src">{t('cfg.toolUnavailable')}</span> : null}
                </div>
                {kind === 'mcp' && 'transport' in item ? <MCPMetadata item={item} t={t} /> : null}
	              {'description' in item && kind === 'extensions' && extensionDescription(item, lang) ? <p className="cfg-desc">{extensionDescription(item, lang)}</p> : null}
	              {'description' in item && kind !== 'extensions' && item.description ? <p className="cfg-desc">{item.description}</p> : null}
	              {'error' in item && item.error ? <p className="cfg-desc settings-error-inline" data-testid={`${kind}-error-${item.name}`} role="alert">{item.error}</p> : null}
	              </div>
	              {/* Why: the inspector catalog is enabled extensions only.
	                  Disabled packages have no live sidecar view, so Configure
	                  would open an empty or unrelated inspector. Goal and other
	                  UI-only packages have no config schema, but they still
	                  use the same inspector as configurable extensions. */}
	              {kind === 'extensions' && item.enabled ? <button type="button" className="cfg-btn" data-testid={`cfg-configure-${item.name}`} onClick={() => onConfigure?.(item.name)}>{t('cfg.configure')}</button> : null}
              <Switch
                testid={`${itemKind}-on-${item.name}`}
                ariaLabel={`${t('cfg.enabled')} ${item.name}`}
                on={item.enabled}
                disabled={loading || saving || ('configuredEnabled' in item && !item.configuredEnabled)}
                onChange={on => void patch(items.map(s => s.name === item.name ? { ...s, enabled: on } : s))}
              />
            </li>
          ))}
        </ul>
      )}
      {/* Installing lives next to the toggle list: an installed package moves
          from here into the list above on the next catalog refresh. */}
      {kind === 'extensions' ? (
        <ExtensionInstallList
          api={api}
          refreshKey={refreshKey}
          onInstalled={async () => { await load(); await onChanged?.() }}
        />
      ) : null}
    </div>
  )
}

export { SchemaConfigForm as ExtensionConfigEditor } from './schema/SchemaConfigForm'

// Mirrors maxMessageContextWindow in internal/server/slash.go.
const MAX_CONTEXT_WINDOW = 100_000_000

export function MessageSettings({ api, view, onView }: { api: Client; view: MessageView; onView: (view: MessageView) => void }) {
  const { t } = useI18n()
  const [busy, setBusy] = useState<'steer' | 'queue'>('steer')
  const [contextWindow, setContextWindow] = useState<number | null>(null)
  const [draft, setDraft] = useState('')
  const [savingWindow, setSavingWindow] = useState(false)
  useEffect(() => {
    void api.message()
      .then(got => { setBusy(got.busy); setContextWindow(got.contextWindow); setDraft(String(got.contextWindow)) })
      .catch(e => toast.from(e))
  }, [api])
  const save = async (next: 'steer' | 'queue') => {
    const prev = busy
    setBusy(next)
    try { await api.patchMessage({ busy: next }) }
    catch (e) { setBusy(prev); toast.from(e) }
  }
  const parsed = Number(draft)
  const windowValid = Number.isInteger(parsed) && parsed >= 1 && parsed <= MAX_CONTEXT_WINDOW
  const windowDirty = contextWindow !== null && parsed !== contextWindow
  const saveWindow = async () => {
    if (!windowValid || !windowDirty || savingWindow) return
    setSavingWindow(true)
    try {
      const got = await api.patchMessage({ contextWindow: parsed })
      setContextWindow(got.contextWindow)
      setDraft(String(got.contextWindow))
      toast.info(t('settings.contextWindowSaved'))
    } catch (e) {
      toast.from(e)
    } finally {
      setSavingWindow(false)
    }
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
        <div className="preference-copy">
          <h4>{t('settings.viewMode')}</h4>
          <p>{t('settings.viewModeHint')}</p>
        </div>
        <div className="theme-picks" data-testid="settings-view" role="radiogroup" aria-label={t('settings.viewMode')}>
          <button type="button" role="radio" aria-checked={view.mode === 'detailed'} className={`theme-pick${view.mode === 'detailed' ? ' on' : ''}`} data-testid="view-detailed" onClick={() => onView({ ...view, mode: 'detailed' })}>
            <span>{t('settings.viewDetailed')}</span>
          </button>
          <button type="button" role="radio" aria-checked={view.mode === 'compact'} className={`theme-pick${view.mode === 'compact' ? ' on' : ''}`} data-testid="view-compact" onClick={() => onView({ ...view, mode: 'compact' })}>
            <span>{t('settings.viewCompact')}</span>
          </button>
        </div>
        {view.mode === 'compact' ? (
          <div className="view-keep">
            <label htmlFor="message-view-keep">{t('settings.viewKeep')}</label>
            <input
              id="message-view-keep"
              data-testid="view-keep"
              type="number"
              min={0}
              max={MAX_COMPACT_KEEP}
              inputMode="numeric"
              value={view.keep}
              onChange={e => onView({ ...view, keep: clampCompactKeep(Number(e.target.value)) })}
            />
            <p className="cfg-hint">{t('settings.viewKeepHint', { max: MAX_COMPACT_KEEP })}</p>
          </div>
        ) : null}
      </section>
      <section className="preference-section">
        <div className="preference-copy">
          <h4>{t('settings.contextWindow')}</h4>
          <p>{t('settings.contextWindowHint')}</p>
        </div>
        <div className="view-keep">
          <label htmlFor="message-context-window">{t('settings.contextWindowLabel')}</label>
          <input
            id="message-context-window"
            data-testid="message-context-window"
            type="number"
            min={1}
            max={MAX_CONTEXT_WINDOW}
            step={1000}
            inputMode="numeric"
            value={draft}
            aria-invalid={!windowValid}
            onChange={e => setDraft(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') void saveWindow() }}
          />
          <button
            type="button"
            className="cfg-btn"
            data-testid="message-context-save"
            disabled={!windowValid || !windowDirty || savingWindow}
            onClick={() => void saveWindow()}
          >
            {savingWindow ? t('settings.contextWindowSaving') : t('settings.contextWindowSave')}
          </button>
          <p className="cfg-hint" data-testid="message-context-hint">
            {windowValid ? t('settings.contextWindowRange', { max: MAX_CONTEXT_WINDOW.toLocaleString() }) : t('settings.contextWindowInvalid', { max: MAX_CONTEXT_WINDOW.toLocaleString() })}
          </p>
        </div>
      </section>
      <section className="preference-section">
        <div className="preference-copy">
          <h4>{t('settings.busyDelivery')}</h4>
          <p>{t('settings.busyDeliveryHint')}</p>
        </div>
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

export function NotificationSettings({ enabled, permission, pushReady, onToggle, onTest }: {
  enabled: boolean
  permission: NotifyPermission
  pushReady: boolean
  onToggle: (on: boolean) => void
  onTest: () => void | Promise<void>
}) {
  const { t } = useI18n()
  const permissionKey: MsgKey = permission === 'granted'
    ? 'settings.notifyStateGranted'
    : permission === 'denied'
      ? 'settings.notifyStateDenied'
      : permission === 'insecure'
        ? 'settings.notifyStateInsecure'
        : permission === 'unsupported' ? 'settings.notifyUnsupported' : 'settings.notifyStateDefault'
  return (
    <div className="preference-page" data-testid="notifications-settings">
      <header className="settings-page-title">
        <div>
          <h3>{t('settings.notifications')}</h3>
          <p>{t('settings.notificationsHint')}</p>
        </div>
      </header>
      <section className="preference-section inline">
        <div className="preference-copy">
          <h4>{t('settings.notifyEnable')}</h4>
          <p>{t('settings.notifyEnableHint')}</p>
        </div>
        <div className="notify-actions">
          <Switch on={enabled} onChange={onToggle} testid="notify-toggle" ariaLabel={t('settings.notifyEnable')} />
        </div>
      </section>
      <section className="preference-section inline">
        <div className="preference-copy">
          <h4>{t('settings.notifyTest')}</h4>
          <p data-testid="notify-permission">{t(permissionKey)}</p>
        </div>
        <div className="notify-actions">
          <button type="button" className="primary-btn notify-test-btn" data-testid="notify-test" disabled={permission !== 'granted'} onClick={() => void onTest()}>
            {t('settings.notifyTest')}
          </button>
        </div>
      </section>
      {permission === 'denied' ? <p className="preference-footnote">{t('settings.notifyDenied')}</p> : null}
      {permission === 'insecure' ? <p className="preference-footnote" data-testid="notify-insecure">{t('settings.notifyInsecure')}</p> : null}
      {enabled && permission === 'granted'
        ? <p className="preference-footnote" data-testid="notify-push" data-push={pushReady ? 'on' : 'off'}>{t(pushReady ? 'settings.notifyPushReady' : 'settings.notifyPushUnavailable')}</p>
        : null}
      <p className="preference-footnote">{t('settings.hint')}</p>
    </div>
  )
}

function ReloadButton({ testid, run, disabled }: { testid: string; run: () => Promise<void>; disabled?: boolean }) {
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
      disabled={disabled || phase === 'busy'}
      aria-busy={phase === 'busy'}
      onClick={() => {
        if (disabled || phase === 'busy') return
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

export type { SessionCommand }
