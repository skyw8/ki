import { useCallback, useEffect, useMemo, useRef, useState, type ChangeEvent, type FormEvent, type ReactNode } from 'react'
import type { Client } from './api'
import { ICheck, IChevDown, IEdit, IRegen, ITraj } from './icons'
import { useI18n, type Lang, type MsgKey } from './i18n'
import { ModelPickerDialog } from './ModelPickerDialog'
import { toast } from './toast'
import { localizedExtensionText } from './ExtensionPanel'
import type { CatalogExtension, CatalogSkill, ExtensionConfig, ExtensionI18n, ModelInfo, SessionCommand, SessionDetail } from './types'

const SOURCE_KEY: Record<string, MsgKey> = {
  home: 'cfg.src.home',
  'user-agents': 'cfg.src.user-agents',
  project: 'cfg.src.project',
  'ancestor-agents': 'cfg.src.ancestor-agents',
}

const INFO_SECTIONS = [
  { id: 'info-session', label: 'cfg.session', children: [] },
  { id: 'info-skills', label: 'cfg.skills', children: [] },
  { id: 'info-extensions', label: 'cfg.extensions', children: [] },
  { id: 'info-commands', label: 'cfg.commands', children: [] },
] as const

function extensionCopy(i18n: ExtensionI18n | undefined, lang: Lang, key: string): string {
  return localizedExtensionText({ key }, i18n, lang)
}

function extensionDescription(item: { description?: string; i18n?: ExtensionI18n }, lang: Lang): string {
  if (!item.description && !item.i18n?.resources) return ''
  return localizedExtensionText({ key: 'manifest.description', fallback: item.description || '' }, item.i18n, lang)
}

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
  onEdit?: (page: 'skills' | 'extensions') => void
  treeAvailable?: boolean
  onTreeOpen?: () => void
}) {
  const { t, lang } = useI18n()
  const [detail, setDetail] = useState<SessionDetail | null>(null)
  const [loading, setLoading] = useState(false)
  const [activeSection, setActiveSection] = useState<string>(INFO_SECTIONS[0].id)
  const configRef = useRef<HTMLDivElement>(null)
  const model = detail ? [detail.provider, detail.model].filter(Boolean).join('/') : ''
  const skills = detail?.availableSkills ?? []
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
      id: 'info-extensions',
      label: t('cfg.extensions'),
      children: extensions.map((item, i) => ({ id: `info-extension-${i}`, label: item.name })),
    },
    {
      id: 'info-commands',
      label: t('cfg.commands'),
      children: commands.map((item, i) => ({ id: `info-command-${i}`, label: `/${item.name}` })),
    },
  ], [commands, extensions, skills, t])
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
                      {extensionDescription(item, lang) ? <p className="cfg-desc">{extensionDescription(item, lang)}</p> : null}
                      {item.runtime ? <p className="cfg-desc">{t('cfg.runtime')}: {item.runtime.state}</p> : null}
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
  onConfigure,
  onChanged,
}: {
  kind: 'skills' | 'extensions'
  api: Client
  workspaceId?: string | null
  onConfigure?: (name: string) => void
  onChanged?: () => void | Promise<void>
}) {
  const { t, lang } = useI18n()
  const [items, setItems] = useState<Array<CatalogSkill | CatalogExtension>>([])
  const [loading, setLoading] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const next = kind === 'skills' ? await api.skills(workspaceId) : await api.extensions()
      setItems(next)
    } catch (e) {
      toast.from(e)
    } finally {
      setLoading(false)
    }
  }, [api, kind, workspaceId])

  useEffect(() => { void load() }, [load])

  const patch = async (next: Array<CatalogSkill | CatalogExtension>) => {
    const prev = items
    setItems(next)
    try {
      const disabled = next.filter(s => !s.enabled).map(s => s.name)
      if (kind === 'skills') await api.patchSkills(disabled)
      else await api.patchExtensions(disabled)
      const listed = kind === 'skills' ? await api.skills(workspaceId) : await api.extensions()
      setItems(listed)
      await onChanged?.()
    } catch (e) {
      setItems(prev)
      toast.from(e)
    }
  }

  const empty = kind === 'skills' ? t('cfg.skillsEmpty') : t('cfg.extensionsEmpty')
  const title = kind === 'skills' ? t('settings.skills') : t('settings.extensions')
  const hint = kind === 'skills' ? t('settings.skillsHint') : t('settings.extensionsHint')
  return (
    <div className="preference-page" data-testid={`${kind}-settings`}>
      <header className="settings-page-title">
        <div>
          <h3>{title}</h3>
          <p>{hint}</p>
        </div>
        <ReloadButton testid={`${kind}-reload`} run={async () => { await api.reload(); await load(); await onChanged?.() }} />
      </header>
      {items.length === 0 && !loading ? <p className="cfg-empty">{empty}</p> : (
        <ul className="cfg-list">
          {items.map(item => (
            <li key={item.name} className="cfg-row" data-testid={`cfg-${kind === 'skills' ? 'skill' : 'extension'}`} data-name={item.name}>
              <div className="cfg-copy">
                <div className="cfg-name">
                  {item.name}
                  {kind === 'skills' && 'source' in item && item.source ? <span className="cfg-src">{SOURCE_KEY[item.source] ? t(SOURCE_KEY[item.source]) : item.source}</span> : null}
                </div>
              {'description' in item && kind === 'extensions' && extensionDescription(item, lang) ? <p className="cfg-desc">{extensionDescription(item, lang)}</p> : null}
              {'description' in item && kind !== 'extensions' && item.description ? <p className="cfg-desc">{item.description}</p> : null}
	              {'error' in item && item.error ? <p className="cfg-desc settings-error-inline" data-testid={`${kind}-error-${item.name}`} role="alert">{item.error}</p> : null}
	              </div>
	              {kind === 'extensions' && 'configurable' in item && item.configurable ? <button type="button" className="cfg-btn" data-testid={`cfg-configure-${item.name}`} onClick={() => onConfigure?.(item.name)}>{t('cfg.configure')}</button> : null}
              <Switch
                testid={`${kind === 'skills' ? 'skill' : 'extension'}-on-${item.name}`}
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

type ExtensionConfigEditorProps = {
	api: Client
	name: string
	onClose: () => void
	embedded?: boolean
	models?: ModelInfo[]
	defaultModel?: string
}

type ExtensionModelPickerProps = {
	fieldLabel: string
	hint: string
	emptyLabel: string
	emptyDetail: string
	customDetail: string
	dialogTitle: string
	testid: string
	dialogTestid: string
	value: string
	pickerValue?: string
	models: ModelInfo[]
	onSelect: (spec: string) => void
	onClear?: () => void
	clearLabel?: string
}

function ExtensionModelPicker({
	fieldLabel,
	hint,
	emptyLabel,
	emptyDetail,
	customDetail,
	dialogTitle,
	testid,
	dialogTestid,
	value,
	pickerValue = value,
	models,
	onSelect,
	onClear,
	clearLabel,
}: ExtensionModelPickerProps) {
	const [open, setOpen] = useState(false)
	const selected = models.find(item => item.spec === pickerValue)
	const modelLabel = selected?.name || value || emptyLabel
	const modelDetail = selected
		? (selected.name && selected.name !== selected.id ? `${selected.provider} / ${selected.id}` : selected.provider)
		: value ? customDetail : emptyDetail

	return (
		<div className="form-control full">
			<span>{fieldLabel}</span>
			<button type="button" className="extension-model-trigger" data-testid={testid} aria-haspopup="dialog" disabled={!models.length} onClick={() => setOpen(true)}>
				<span className="extension-model-value"><strong>{modelLabel}</strong><small>{modelDetail}</small></span>
				<IChevDown />
			</button>
			<div className="extension-model-meta">
				<small className="form-hint">{hint}</small>
				{onClear && value ? <button type="button" className="cfg-btn" onClick={onClear}>{clearLabel}</button> : null}
			</div>
			<ModelPickerDialog open={open} models={models} value={pickerValue} onSelect={onSelect} onClose={() => setOpen(false)} testid={dialogTestid} title={dialogTitle} />
		</div>
	)
}

export function ExtensionConfigEditor(props: ExtensionConfigEditorProps) {
	if (props.name === 'telegram-bot') return <TelegramConfigForm {...props} />
	if (props.name === 'deep-web-search') return <DeepWebSearchConfigForm {...props} />
	if (props.name === 'freerouter') return <FreeRouterConfigForm {...props} />
	return <JsonExtensionConfigEditor {...props} />
}

function JsonExtensionConfigEditor({ api, name, onClose, embedded = false }: ExtensionConfigEditorProps) {
	const { t } = useI18n()
	const [config, setConfig] = useState<ExtensionConfig | null>(null)
	const [text, setText] = useState('')
	const [loading, setLoading] = useState(true)
	const [saving, setSaving] = useState(false)
	useEffect(() => {
		let alive = true
		setLoading(true)
		void api.extensionConfig(name).then(next => {
			if (!alive) return
			setConfig(next)
			setText(JSON.stringify(next.config ?? {}, null, 2))
		}).catch(e => toast.from(e)).finally(() => { if (alive) setLoading(false) })
		return () => { alive = false }
	}, [api, name])
	const save = async () => {
		let values: Record<string, unknown>
		try {
			const parsed = JSON.parse(text) as unknown
			if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error(t('cfg.configObject'))
			values = parsed as Record<string, unknown>
		} catch (e) {
			toast.error(e instanceof Error ? e.message : String(e))
			return
		}
		setSaving(true)
		try {
			const next = await api.patchExtensionConfig(name, values)
			setConfig(next)
			setText(JSON.stringify(next.config ?? {}, null, 2))
			toast.info(t('cfg.configSaved'))
		} catch (e) { toast.from(e) } finally { setSaving(false) }
	}
	return (
		<section className={`extension-config-editor${embedded ? ' embedded' : ''}`} data-testid={`extension-config-${name}`}>
			{embedded ? <div className="cfg-name"><span>{t('cfg.config')}</span></div> : <div className="cfg-name"><span>{t('cfg.config')} · {name}</span><button type="button" className="cfg-btn" onClick={onClose}>{t('cfg.configClose')}</button></div>}
			{loading ? <p className="cfg-empty">{t('file.loading')}</p> : (
				<>
					<p className="cfg-desc">{t('cfg.configHint')}</p>
					<textarea className="extension-config-textarea" data-testid="extension-config-input" value={text} onChange={event => setText(event.target.value)} spellCheck={false} />
					<div className="cfg-actions"><button type="button" className="primary-btn" data-testid="extension-config-save" disabled={saving || !config} onClick={() => void save()}>{saving ? t('cfg.configSaving') : t('cfg.configSave')}</button></div>
				</>
			)}
		</section>
	)
}

function TelegramConfigForm({ api, name, onClose, embedded = false, models = [], defaultModel = '' }: ExtensionConfigEditorProps) {
  const { t, lang } = useI18n()
	const [config, setConfig] = useState<ExtensionConfig | null>(null)
	const [loading, setLoading] = useState(true)
	const [saving, setSaving] = useState(false)
	const [botId, setBotId] = useState('')
	const [token, setToken] = useState('')
	const [tokenConfigured, setTokenConfigured] = useState(false)
	const [model, setModel] = useState('')
	const [modelOpen, setModelOpen] = useState(false)
	const copy = (key: string) => extensionCopy(config?.i18n, lang, key)

	useEffect(() => {
		let alive = true
		setLoading(true)
		void api.extensionConfig(name).then(next => {
			if (!alive) return
			const rawToken = typeof next.config.token === 'string' ? next.config.token : ''
			setConfig(next)
			setBotId(typeof next.config.botId === 'string' ? next.config.botId : String(next.config.botId ?? ''))
			setToken(rawToken === '<configured>' ? '' : rawToken)
			setTokenConfigured(rawToken === '<configured>')
			setModel(typeof next.config.model === 'string' ? next.config.model : '')
		}).catch(e => toast.from(e)).finally(() => { if (alive) setLoading(false) })
		return () => { alive = false }
	}, [api, name])

	const save = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault()
		const nextBotID = botId.trim()
		if (!nextBotID) {
			toast.error(copy('config.botIdRequired'))
			return
		}
		const nextToken = token.trim() || (tokenConfigured ? '<configured>' : '')
		if (!nextToken) {
			toast.error(copy('config.tokenRequired'))
			return
		}
		setSaving(true)
		try {
			const next = await api.patchExtensionConfig(name, {
				botId: nextBotID,
				token: nextToken,
				model: model.trim(),
			})
			setConfig(next)
			setBotId(typeof next.config.botId === 'string' ? next.config.botId : String(next.config.botId ?? nextBotID))
			setToken('')
			setTokenConfigured(next.config.token === '<configured>')
			setModel(typeof next.config.model === 'string' ? next.config.model : model.trim())
			toast.info(t('cfg.configSaved'))
		} catch (e) {
			toast.from(e)
		} finally {
			setSaving(false)
		}
	}

	const effectiveModel = model.trim() || defaultModel.trim()
	const selectedModel = models.find(item => item.spec === effectiveModel)
	const modelLabel = selectedModel?.name || selectedModel?.id || effectiveModel || copy('config.modelDefault')
	const modelDetail = selectedModel
		? (selectedModel.name && selectedModel.name !== selectedModel.id ? `${selectedModel.provider} / ${selectedModel.id}` : selectedModel.provider)
		: effectiveModel ? copy('config.modelCustom') : copy('config.modelDefaultHint')

	return (
		<section className={`extension-config-editor telegram-config-editor${embedded ? ' embedded' : ''}`} data-testid={`extension-config-${name}`}>
			{embedded ? <div className="cfg-name"><span>{t('cfg.config')}</span></div> : <div className="cfg-name"><span>{t('cfg.config')} · {name}</span><button type="button" className="cfg-btn" onClick={onClose}>{t('cfg.configClose')}</button></div>}
			{loading ? <p className="cfg-empty">{t('file.loading')}</p> : (
				<form className="telegram-config-form" onSubmit={event => void save(event)}>
					<p className="cfg-desc">{copy('config.hint')}</p>
					<div className="form-grid two">
						<label className="form-control">
							<span>{copy('config.botId')}</span>
							<input data-testid="telegram-bot-id" value={botId} onChange={event => setBotId(event.target.value)} placeholder={copy('config.botIdPlaceholder')} inputMode="numeric" autoComplete="off" />
						</label>
						<label className="form-control">
							<span>{copy('config.token')}</span>
							<input data-testid="telegram-bot-token" type="password" value={token} onChange={event => { setToken(event.target.value); setTokenConfigured(false) }} placeholder={tokenConfigured ? copy('config.tokenConfigured') : copy('config.tokenPlaceholder')} autoComplete="new-password" />
							<small className="form-hint">{copy('config.tokenHint')}</small>
						</label>
							<div className="form-control full">
								<span>{copy('config.model')}</span>
								<button type="button" className="extension-model-trigger" data-testid="telegram-model-picker" disabled={!models.length} onClick={() => setModelOpen(true)}>
									<span className="extension-model-value"><strong>{modelLabel}</strong><small>{modelDetail}</small></span>
									<IChevDown />
								</button>
								<div className="extension-model-meta">
									<small className="form-hint">{copy('config.modelHint')}</small>
									{model ? <button type="button" className="cfg-btn" onClick={() => setModel('')}>{copy('config.modelReset')}</button> : null}
								</div>
							</div>
					</div>
					<div className="cfg-actions telegram-config-actions"><button type="submit" className="primary-btn" data-testid="telegram-config-save" disabled={saving || !config}>{saving ? t('cfg.configSaving') : t('cfg.configSave')}</button></div>
				</form>
			)}
			<ModelPickerDialog open={modelOpen} models={models} value={effectiveModel} onSelect={setModel} onClose={() => setModelOpen(false)} testid="telegram-model-dialog" />
		</section>
	)
}

function DeepWebSearchConfigForm({ api, name, onClose, embedded = false, models = [] }: ExtensionConfigEditorProps) {
  const { t, lang } = useI18n()
	const [config, setConfig] = useState<ExtensionConfig | null>(null)
	const [loading, setLoading] = useState(true)
	const [saving, setSaving] = useState(false)
	const [exaKey, setExaKey] = useState('')
	const [exaConfigured, setExaConfigured] = useState(false)
	const [exaMode, setExaMode] = useState('auto')
	const [tinyfishKey, setTinyfishKey] = useState('')
	const [tinyfishConfigured, setTinyfishConfigured] = useState(false)
	const [codexModel, setCodexModel] = useState('gpt-5.5')
	const [provider, setProvider] = useState('all')
	const [toggles, setToggles] = useState({ codex: true, exa: true, tinyfish: true, duckduckgo: true })
	const [maxResults, setMaxResults] = useState(5)
	const [fetchContent, setFetchContent] = useState(false)
	const [summaryModel, setSummaryModel] = useState('openai-codex/gpt-5.5')
	const [workflow, setWorkflow] = useState('none')
	const copy = (key: string) => extensionCopy(config?.i18n, lang, key)
	const codexModels = useMemo(() => models.filter(item => item.provider === 'openai-codex'), [models])
	const codexModelSpec = codexModels.some(item => item.id === codexModel) ? `openai-codex/${codexModel}` : codexModel
	// Keep older bare summary model values selectable after the picker switches
	// to the canonical provider/model specs returned by /v1/models.
	const summaryModelSpec = summaryModel && !summaryModel.includes('/') ? `openai-codex/${summaryModel}` : summaryModel

	const apply = (next: Record<string, unknown>, metadata?: ExtensionConfig) => {
		const rawExa = typeof next.exaApiKey === 'string' ? next.exaApiKey : ''
		const rawTinyfish = typeof next.tinyfishApiKey === 'string' ? next.tinyfishApiKey : ''
		setConfig(metadata ?? { ...(config ?? { name, schema: {}, config: {} }), config: next })
		setExaKey(rawExa === '<configured>' ? '' : rawExa)
		setExaConfigured(rawExa === '<configured>')
		setExaMode(typeof next.exaMode === 'string' ? next.exaMode : 'auto')
		setTinyfishKey(rawTinyfish === '<configured>' ? '' : rawTinyfish)
		setTinyfishConfigured(rawTinyfish === '<configured>')
		const rawCodexModel = typeof next.codexModel === 'string' ? next.codexModel : 'gpt-5.5'
		setCodexModel(rawCodexModel.startsWith('openai-codex/') ? rawCodexModel.slice('openai-codex/'.length) : rawCodexModel)
		setProvider(typeof next.provider === 'string' ? next.provider : 'all')
		const rawToggles = next.providerToggles && typeof next.providerToggles === 'object' ? next.providerToggles as Record<string, unknown> : {}
		setToggles({ codex: rawToggles.codex !== false, exa: rawToggles.exa !== false, tinyfish: rawToggles.tinyfish !== false, duckduckgo: rawToggles.duckduckgo !== false })
		setMaxResults(typeof next.maxResults === 'number' ? next.maxResults : 5)
		setFetchContent(next.fetchContent === true)
		setSummaryModel(typeof next.summaryModel === 'string' ? next.summaryModel : 'openai-codex/gpt-5.5')
		setWorkflow(next.workflow === 'auto-summary' ? 'auto-summary' : 'none')
	}

	useEffect(() => {
		let alive = true
		setLoading(true)
		void api.extensionConfig(name).then(next => {
			if (alive) apply(next.config, next)
		}).catch(e => toast.from(e)).finally(() => { if (alive) setLoading(false) })
		return () => { alive = false }
		// apply only initializes state from the response; including it would make
		// every field update restart the request.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [api, name])

	const save = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault()
		setSaving(true)
		try {
			const next = await api.patchExtensionConfig(name, {
				exaApiKey: exaKey.trim() || (exaConfigured ? '<configured>' : ''),
				exaMode,
				tinyfishApiKey: tinyfishKey.trim() || (tinyfishConfigured ? '<configured>' : ''),
				codexModel: codexModel.trim(),
				provider,
				providerToggles: toggles,
				maxResults: Math.max(1, Math.min(20, Math.floor(maxResults || 5))),
				fetchContent,
				summaryModel: summaryModel.trim(),
				workflow,
			})
			apply(next.config, next)
			toast.info(t('cfg.configSaved'))
		} catch (e) {
			toast.from(e)
		} finally {
			setSaving(false)
		}
	}
	const providerCopy = {
		codex: { name: copy('provider.codex'), description: copy('provider.codexDescription') },
		exa: { name: copy('provider.exa'), description: copy('provider.exaDescription') },
		tinyfish: { name: copy('provider.tinyfish'), description: copy('provider.tinyfishDescription') },
		duckduckgo: { name: copy('provider.duckduckgo'), description: copy('provider.duckduckgoDescription') },
	} as const

	return (
		<section className={`extension-config-editor deep-web-search-config-editor${embedded ? ' embedded' : ''}`} data-testid={`extension-config-${name}`}>
			{embedded ? <div className="cfg-name"><span>{t('cfg.config')}</span></div> : <div className="cfg-name"><span>{t('cfg.config')} · {name}</span><button type="button" className="cfg-btn" onClick={onClose}>{t('cfg.configClose')}</button></div>}
			{loading ? <p className="cfg-empty">{t('file.loading')}</p> : (
				<form className="deep-web-search-config-form" onSubmit={event => void save(event)}>
					<div className="deep-web-search-config-scroll">
						<p className="cfg-desc">{copy('config.hint')}</p>
						<section className="deep-web-search-settings-section" aria-labelledby="deep-web-search-global-title">
						<div className="deep-web-search-section-head">
							<div><h4 id="deep-web-search-global-title">{copy('config.globalTitle')}</h4><p>{copy('config.globalHint')}</p></div>
						</div>
						<div className="deep-web-search-provider-toggles" data-testid="deep-web-search-provider-toggles">
							<div className="deep-web-search-block-head"><strong>{copy('config.toggles')}</strong><small>{copy('config.toggleHint')}</small></div>
							{(['codex', 'exa', 'tinyfish', 'duckduckgo'] as const).map(key => (
								<label className="deep-web-search-provider-toggle" key={key}>
									<span className="deep-web-search-toggle-copy"><strong>{providerCopy[key].name}</strong><small>{toggles[key] ? copy('status.enabled') : copy('status.disabled')}</small></span>
									<Switch testid={`deep-web-search-toggle-${key}`} on={toggles[key]} onChange={on => setToggles(value => ({ ...value, [key]: on }))} />
								</label>
							))}
						</div>
						<div className="form-grid two deep-web-search-global-fields">
							<label className="form-control"><span>{copy('config.provider')}</span><select data-testid="deep-web-search-provider" value={provider} onChange={event => setProvider(event.target.value)}><option value="all">{copy('option.provider.all')}</option><option value="auto">{copy('option.provider.auto')}</option><option value="codex">{copy('option.provider.codex')}</option><option value="exa">{copy('option.provider.exa')}</option><option value="tinyfish">{copy('option.provider.tinyfish')}</option><option value="duckduckgo">{copy('option.provider.duckduckgo')}</option></select></label>
							<label className="form-control"><span>{copy('config.workflow')}</span><select data-testid="deep-web-search-workflow" value={workflow} onChange={event => setWorkflow(event.target.value === 'auto-summary' ? 'auto-summary' : 'none')}><option value="none">{copy('option.workflow.none')}</option><option value="auto-summary">{copy('option.workflow.auto-summary')}</option></select></label>
							<label className="form-control"><span>{copy('config.maxResults')}</span><input data-testid="deep-web-search-max-results" type="number" min={1} max={20} value={maxResults} onChange={event => setMaxResults(Number(event.target.value))} /></label>
							<ExtensionModelPicker
								fieldLabel={copy('config.summaryModel')}
								hint={copy('config.summaryModelHint')}
								emptyLabel={copy('config.summaryDisabled')}
								emptyDetail={copy('config.summaryDisabled')}
								customDetail={copy('config.modelCustom')}
								dialogTitle={copy('config.summaryModelPickerTitle')}
								testid="deep-web-search-summary-model-picker"
								dialogTestid="deep-web-search-summary-model-dialog"
								value={summaryModel}
								pickerValue={summaryModelSpec}
								models={models}
								onSelect={setSummaryModel}
								onClear={() => setSummaryModel('')}
								clearLabel={copy('config.summaryDisable')}
							/>
						</div>
						<div className="deep-web-search-options">
							<label><input data-testid="deep-web-search-fetch-content" type="checkbox" checked={fetchContent} onChange={event => setFetchContent(event.target.checked)} /> {copy('config.fetchContent')}</label>
						</div>
						</section>
						<section className="deep-web-search-settings-section" aria-labelledby="deep-web-search-providers-title">
						<div className="deep-web-search-section-head">
							<div><h4 id="deep-web-search-providers-title">{copy('config.providersTitle')}</h4><p>{copy('config.providersHint')}</p></div>
						</div>
						<div className="deep-web-search-provider-list">
							<article className="deep-web-search-provider-row">
								<div className="deep-web-search-provider-copy"><div className="deep-web-search-provider-title"><span className="deep-web-search-provider-mark codex" aria-hidden /> <strong>{providerCopy.codex.name}</strong><span className="deep-web-search-provider-status">{copy('provider.codexStatus')}</span></div><p>{providerCopy.codex.description}</p></div>
								<div className="deep-web-search-provider-controls deep-web-search-provider-controls-codex">
									<ExtensionModelPicker
										fieldLabel={copy('config.codexModel')}
										hint={copy('config.codexModelHint')}
										emptyLabel={copy('config.modelUnavailable')}
										emptyDetail={copy('config.modelUnavailable')}
										customDetail={copy('config.modelCustom')}
										dialogTitle={copy('config.codexModelPickerTitle')}
										testid="deep-web-search-codex-model-picker"
										dialogTestid="deep-web-search-codex-model-dialog"
										value={codexModel}
										pickerValue={codexModelSpec}
										models={codexModels}
										onSelect={spec => setCodexModel(spec.slice('openai-codex/'.length))}
									/>
									<div className="deep-web-search-provider-value"><span className="deep-web-search-provider-badge">{copy('status.connected')}</span><code>codex-oauth</code></div>
								</div>
							</article>
							<article className="deep-web-search-provider-row">
								<div className="deep-web-search-provider-copy"><div className="deep-web-search-provider-title"><span className="deep-web-search-provider-mark exa" aria-hidden /> <strong>{providerCopy.exa.name}</strong></div><p>{providerCopy.exa.description}</p></div>
								<div className="deep-web-search-provider-controls">
									<label className="form-control"><span>{copy('config.exaKey')}</span><input data-testid="deep-web-search-exa-key" type="password" value={exaKey} onChange={event => { setExaKey(event.target.value); setExaConfigured(false) }} placeholder={exaConfigured ? copy('config.configured') : copy('config.keyPlaceholder')} autoComplete="new-password" /><small className="form-hint">{copy('config.exaKeyHint')}</small></label>
									<label className="form-control"><span>{copy('config.exaMode')}</span><select data-testid="deep-web-search-exa-mode" value={exaMode} onChange={event => setExaMode(event.target.value)}><option value="auto">{copy('option.exaMode.auto')}</option><option value="api">{copy('option.exaMode.api')}</option><option value="mcp">{copy('option.exaMode.mcp')}</option></select></label>
								</div>
							</article>
							<article className="deep-web-search-provider-row">
								<div className="deep-web-search-provider-copy"><div className="deep-web-search-provider-title"><span className="deep-web-search-provider-mark tinyfish" aria-hidden /> <strong>{providerCopy.tinyfish.name}</strong></div><p>{providerCopy.tinyfish.description}</p></div>
								<div className="deep-web-search-provider-controls single"><label className="form-control"><span>{copy('config.tinyfishKey')}</span><input data-testid="deep-web-search-tinyfish-key" type="password" value={tinyfishKey} onChange={event => { setTinyfishKey(event.target.value); setTinyfishConfigured(false) }} placeholder={tinyfishConfigured ? copy('config.configured') : copy('config.keyPlaceholder')} autoComplete="new-password" /><small className="form-hint">{copy('config.tinyfishKeyHint')}</small></label></div>
							</article>
							<article className="deep-web-search-provider-row">
								<div className="deep-web-search-provider-copy"><div className="deep-web-search-provider-title"><span className="deep-web-search-provider-mark duckduckgo" aria-hidden /> <strong>{providerCopy.duckduckgo.name}</strong></div><p>{providerCopy.duckduckgo.description}</p></div>
								<div className="deep-web-search-provider-value"><span className="deep-web-search-provider-badge free">{copy('status.noAuth')}</span></div>
							</article>
						</div>
						</section>
					</div>
					<div className="cfg-actions deep-web-search-config-actions"><button type="submit" className="primary-btn" data-testid="deep-web-search-config-save" disabled={saving || !config}>{saving ? t('cfg.configSaving') : t('cfg.configSave')}</button></div>
				</form>
			)}
		</section>
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

// Numeric field editing: keep an empty input as NaN until the next keystroke
// so clearing a field doesn't snap to 0 mid-edit.
function numberField(value: number, set: (v: number) => void) {
  return {
    type: 'number' as const,
    value: Number.isFinite(value) ? String(value) : '',
    onChange: (event: ChangeEvent<HTMLInputElement>) => {
      const next = Number(event.target.value)
      set(Number.isFinite(next) ? next : NaN)
    },
  }
}

function FreeRouterConfigForm({ api, name, onClose, embedded = false }: ExtensionConfigEditorProps) {
  const { t, lang } = useI18n()
	const [config, setConfig] = useState<ExtensionConfig | null>(null)
	const [loading, setLoading] = useState(true)
	const [saving, setSaving] = useState(false)
	const [apiKey, setApiKey] = useState('')
	const [apiKeyConfigured, setApiKeyConfigured] = useState(false)
	const [baseUrl, setBaseUrl] = useState('')
	const [raceWidth, setRaceWidth] = useState(2)
	const [maxBatches, setMaxBatches] = useState(3)
	const [exhaustedTtlSec, setExhaustedTtlSec] = useState(90)
	const [slowTtlSec, setSlowTtlSec] = useState(15)
	const [firstTokenSec, setFirstTokenSec] = useState(10)
	const [idleSec, setIdleSec] = useState(30)
	const [refreshMin, setRefreshMin] = useState(60)
	const [listen, setListen] = useState('127.0.0.1:18427')
	const copy = (key: string) => extensionCopy(config?.i18n, lang, key)

	const apply = (next: Record<string, unknown>, metadata?: ExtensionConfig) => {
		const rawKey = typeof next.apiKey === 'string' ? next.apiKey : ''
		setConfig(metadata ?? { ...(config ?? { name, schema: {}, config: {} }), config: next })
		setApiKey(rawKey === '<configured>' ? '' : rawKey)
		setApiKeyConfigured(rawKey === '<configured>')
		setBaseUrl(typeof next.baseUrl === 'string' ? next.baseUrl : '')
		setListen(typeof next.listen === 'string' && next.listen.trim() ? next.listen : '127.0.0.1:18427')
		const num = (v: unknown, fallback: number) => (typeof v === 'number' && Number.isFinite(v) ? v : fallback)
		setRaceWidth(num(next.raceWidth, 2))
		setMaxBatches(num(next.maxBatches, 3))
		setExhaustedTtlSec(Math.round(num(next.exhaustedTtlMs, 90000) / 1000))
		setSlowTtlSec(Math.round(num(next.slowTtlMs, 15000) / 1000))
		setFirstTokenSec(Math.round(num(next.firstTokenTimeoutMs, 10000) / 1000))
		setIdleSec(Math.round(num(next.idleTimeoutMs, 30000) / 1000))
		setRefreshMin(Math.round(num(next.refreshIntervalMs, 3600000) / 60000))
	}

	useEffect(() => {
		let alive = true
		setLoading(true)
		void api.extensionConfig(name).then(next => {
			if (alive) apply(next.config, next)
		}).catch(e => toast.from(e)).finally(() => { if (alive) setLoading(false) })
		return () => { alive = false }
		// apply only initializes state from the response; including it would make
		// every field update restart the request.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [api, name])

	const save = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault()
		const seconds = { exhaustedTtlSec, slowTtlSec, firstTokenSec, idleSec, refreshMin }
		if (Object.values(seconds).some(v => !Number.isFinite(v) || v <= 0) || raceWidth < 1 || maxBatches < 1) {
			toast.error(copy('config.invalidNumber'))
			return
		}
		setSaving(true)
		try {
			// The host only streams models whose provider credential is configured,
			// so a fresh key must land there first; the config apiKey is only a
			// sidecar-side fallback. If the provider credential write fails (e.g.
			// fixture environments without the provider), keep going via config.
			let configKey = ''
			const key = apiKey.trim()
			if (key && !apiKeyConfigured) {
				try {
					await api.setCredential('free-router', key)
				} catch {
					configKey = key
					toast.error(copy('config.keySaveFailed'))
				}
			}
			const next = await api.patchExtensionConfig(name, {
				apiKey: configKey || (apiKeyConfigured ? '<configured>' : ''),
				baseUrl: baseUrl.trim(),
				listen: listen.trim() || '127.0.0.1:18427',
				raceWidth: Math.max(1, Math.min(8, Math.round(raceWidth))),
				maxBatches: Math.max(1, Math.min(6, Math.round(maxBatches))),
				exhaustedTtlMs: Math.round(exhaustedTtlSec * 1000),
				slowTtlMs: Math.round(slowTtlSec * 1000),
				firstTokenTimeoutMs: Math.round(firstTokenSec * 1000),
				idleTimeoutMs: Math.round(idleSec * 1000),
				refreshIntervalMs: Math.round(refreshMin * 60000),
			})
			apply(next.config, next)
			toast.info(t('cfg.configSaved'))
		} catch (e) {
			toast.from(e)
		} finally {
			setSaving(false)
		}
	}

	return (
		<section className={`extension-config-editor freerouter-config-editor${embedded ? ' embedded' : ''}`} data-testid={`extension-config-${name}`}>
			{embedded ? <div className="cfg-name"><span>{t('cfg.config')}</span></div> : <div className="cfg-name"><span>{t('cfg.config')} · {name}</span><button type="button" className="cfg-btn" onClick={onClose}>{t('cfg.configClose')}</button></div>}
			{loading ? <p className="cfg-empty">{t('file.loading')}</p> : (
				<form className="freerouter-config-form" onSubmit={event => void save(event)}>
					<div className="deep-web-search-config-scroll">
						<p className="cfg-desc">{copy('config.hint')}</p>
						<section className="deep-web-search-settings-section" aria-labelledby="freerouter-auth-title">
							<div className="deep-web-search-section-head">
								<div><h4 id="freerouter-auth-title">{copy('config.authTitle')}</h4><p>{copy('config.authHint')}</p></div>
							</div>
							<div className="form-grid two">
								<label className="form-control">
									<span>{copy('config.apiKey')}</span>
									<input data-testid="freerouter-api-key" type="password" value={apiKey} onChange={event => { setApiKey(event.target.value); setApiKeyConfigured(false) }} placeholder={apiKeyConfigured ? copy('config.configured') : copy('config.keyPlaceholder')} autoComplete="new-password" />
								</label>
								<label className="form-control">
									<span>{copy('config.baseUrl')}</span>
									<input data-testid="freerouter-base-url" value={baseUrl} onChange={event => setBaseUrl(event.target.value)} placeholder="https://openrouter.ai/api/v1" spellCheck={false} />
									<small className="form-hint">{copy('config.baseUrlHint')}</small>
								</label>
								<label className="form-control">
									<span>{copy('config.listen')}</span>
									<input data-testid="freerouter-listen" value={listen} onChange={event => setListen(event.target.value)} placeholder="127.0.0.1:18427" spellCheck={false} />
									<small className="form-hint">{copy('config.listenHint')}</small>
								</label>
							</div>
						</section>
						<section className="deep-web-search-settings-section" aria-labelledby="freerouter-routing-title">
							<div className="deep-web-search-section-head">
								<div><h4 id="freerouter-routing-title">{copy('config.routingTitle')}</h4><p>{copy('config.routingHint')}</p></div>
							</div>
							<div className="form-grid two">
								<label className="form-control">
									<span>{copy('config.raceWidth')}</span>
									<input data-testid="freerouter-race-width" min={1} max={8} step={1} {...numberField(raceWidth, setRaceWidth)} />
									<small className="form-hint">{copy('config.raceWidthHint')}</small>
								</label>
								<label className="form-control">
									<span>{copy('config.maxBatches')}</span>
									<input data-testid="freerouter-max-batches" min={1} max={6} step={1} {...numberField(maxBatches, setMaxBatches)} />
									<small className="form-hint">{copy('config.maxBatchesHint')}</small>
								</label>
							</div>
						</section>
						<section className="deep-web-search-settings-section" aria-labelledby="freerouter-timeouts-title">
							<div className="deep-web-search-section-head">
								<div><h4 id="freerouter-timeouts-title">{copy('config.timeoutsTitle')}</h4><p>{copy('config.timeoutsHint')}</p></div>
							</div>
							<div className="form-grid two">
								<label className="form-control"><span>{copy('config.exhaustedTtl')}</span><input data-testid="freerouter-exhausted-ttl" min={1} step={1} {...numberField(exhaustedTtlSec, setExhaustedTtlSec)} /><small className="form-hint">{copy('config.exhaustedTtlHint')}</small></label>
								<label className="form-control"><span>{copy('config.slowTtl')}</span><input data-testid="freerouter-slow-ttl" min={1} step={1} {...numberField(slowTtlSec, setSlowTtlSec)} /><small className="form-hint">{copy('config.slowTtlHint')}</small></label>
								<label className="form-control"><span>{copy('config.firstTokenTimeout')}</span><input data-testid="freerouter-first-token-timeout" min={1} step={1} {...numberField(firstTokenSec, setFirstTokenSec)} /><small className="form-hint">{copy('config.firstTokenTimeoutHint')}</small></label>
								<label className="form-control"><span>{copy('config.idleTimeout')}</span><input data-testid="freerouter-idle-timeout" min={1} step={1} {...numberField(idleSec, setIdleSec)} /><small className="form-hint">{copy('config.idleTimeoutHint')}</small></label>
								<label className="form-control"><span>{copy('config.refresh')}</span><input data-testid="freerouter-refresh" min={1} step={1} {...numberField(refreshMin, setRefreshMin)} /><small className="form-hint">{copy('config.refreshHint')}</small></label>
							</div>
						</section>
					</div>
					<div className="cfg-actions deep-web-search-config-actions"><button type="submit" className="primary-btn" data-testid="freerouter-config-save" disabled={saving || !config}>{saving ? t('cfg.configSaving') : t('cfg.configSave')}</button></div>
				</form>
			)}
		</section>
	)
}

export type { SessionCommand }
