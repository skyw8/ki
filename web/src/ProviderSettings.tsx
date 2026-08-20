import { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import type { Client } from './api'
import { ICheck, IEdit, IPlus, IRegen, ITrash } from './icons'
import { useI18n } from './i18n'
import { Select } from './Select'
import type { ProviderCatalog, ProviderModel } from './types'

type Props = { api: Client; onChanged: () => void }

const apiOptions = [
  { value: 'completions', label: 'Completions' },
  { value: 'responses', label: 'Responses' },
  { value: 'anthropic', label: 'Anthropic Messages' },
]

const copy = {
  zh: {
    title: '模型供应商', subtitle: '管理连接、凭据和模型。目录保存在本地，不会自动联网刷新。', addProvider: '添加供应商',
    providers: '供应商', missingKey: '缺少密钥', builtIn: '内置', custom: '自定义', enabled: '启用', disabled: '已停用',
    connection: '连接设置', connectionHint: '修改连接后，新请求会立即使用新配置。', displayName: '显示名称', providerID: '供应商 ID', baseURL: 'Base URL', apiProtocol: 'API 协议', saveChanges: '保存更改',
    credential: 'API 凭据', credentialHint: '密钥只保存在本机，页面不会回显。', apiKey: 'API key', saveKey: '保存密钥', clear: '清除', stored: '已保存凭据',
    models: '模型', modelCount: '{n} 个模型', addModel: '添加模型', modelID: '模型 ID', modelName: '显示名称', contextWindow: '上下文窗口', maxOutput: '最大输出', add: '添加',
    edit: '编辑', restore: '恢复', remove: '删除', reasoning: '推理', vision: '视觉',
    advanced: '模型高级字段', advancedHint: '用于精确配置 thinking map、价格和兼容参数。', cancel: '取消', save: '保存',
    dangerZone: '危险操作', restoreProvider: '恢复供应商内置值', deleteProvider: '删除供应商',
    newProvider: '新供应商', newProviderHint: '先填写连接和首个模型，创建后可继续补充高级字段。', create: '创建供应商', firstModel: '首个模型 ID',
    loading: '正在加载供应商…', saving: '正在保存…', source: '来源：{source}', selectProvider: '选择一个供应商以编辑。',
  },
  en: {
    title: 'Model providers', subtitle: 'Manage connections, credentials, and models. The catalog stays local and never refreshes online.', addProvider: 'Add provider',
    providers: 'Providers', missingKey: 'Missing key', builtIn: 'Built-in', custom: 'Custom', enabled: 'Enabled', disabled: 'Disabled',
    connection: 'Connection', connectionHint: 'New requests use saved connection changes immediately.', displayName: 'Display name', providerID: 'Provider ID', baseURL: 'Base URL', apiProtocol: 'API protocol', saveChanges: 'Save changes',
    credential: 'API credential', credentialHint: 'The key stays on this machine and is never shown again.', apiKey: 'API key', saveKey: 'Save key', clear: 'Clear', stored: 'Credential configured',
    models: 'Models', modelCount: '{n} models', addModel: 'Add model', modelID: 'Model ID', modelName: 'Display name', contextWindow: 'Context window', maxOutput: 'Max output', add: 'Add model',
    edit: 'Edit', restore: 'Restore', remove: 'Delete', reasoning: 'Reasoning', vision: 'Vision',
    advanced: 'Advanced model fields', advancedHint: 'Precisely configure thinking maps, pricing, and compatibility.', cancel: 'Cancel', save: 'Save',
    dangerZone: 'Danger zone', restoreProvider: 'Restore built-in values', deleteProvider: 'Delete provider',
    newProvider: 'New provider', newProviderHint: 'Start with a connection and one model; advanced fields can be added afterward.', create: 'Create provider', firstModel: 'First model ID',
    loading: 'Loading providers…', saving: 'Saving…', source: 'Source: {source}', selectProvider: 'Select a provider to edit.',
  },
} as const

function format(template: string, vars: Record<string, string | number>) {
  return template.replace(/\{(\w+)\}/g, (_, key: string) => String(vars[key] ?? `{${key}}`))
}

export function ProviderSettings({ api, onChanged }: Props) {
  const { lang } = useI18n()
  const c = copy[lang]
  const [data, setData] = useState<ProviderCatalog | null>(null)
  const [selected, setSelected] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [key, setKey] = useState('')
  const [newProvider, setNewProvider] = useState(false)
  const [newModel, setNewModel] = useState(false)
  const [editingModel, setEditingModel] = useState('')
  const [modelJSON, setModelJSON] = useState('')
  const [connection, setConnection] = useState({ name: '', api: 'completions', baseUrl: '' })
  const [form, setForm] = useState({ id: '', name: '', api: 'completions', baseUrl: '', model: '' })
  const [modelForm, setModelForm] = useState({ id: '', name: '', contextWindow: '128000', maxTokens: '16384' })
  const newProviderID = useRef<HTMLInputElement>(null)
  const modelJSONRef = useRef<HTMLTextAreaElement>(null)

  const load = async () => {
    const next = await api.providers()
    setData(next)
    setSelected(id => id && next.providers.some(p => p.id === id) ? id : (next.default.provider || next.providers[0]?.id || ''))
    onChanged()
  }
  useEffect(() => { void load().catch(e => setError(e instanceof Error ? e.message : String(e))) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  const current = useMemo(() => data?.providers.find(p => p.id === selected), [data, selected])
  useEffect(() => {
    if (!current) return
    setConnection({ name: current.name, api: current.api, baseUrl: current.baseUrl })
    setKey('')
    setEditingModel('')
  }, [current?.id, current?.name, current?.api, current?.baseUrl]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!newProvider && !editingModel) return
    if (newProvider) newProviderID.current?.focus()
    else modelJSONRef.current?.focus()
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      // Capture prevents the parent settings dialog from handling the same
      // Escape press and closing both dialog layers at once.
      event.stopImmediatePropagation()
      if (busy) return
      if (editingModel) setEditingModel('')
      else setNewProvider(false)
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [newProvider, editingModel, busy])

  const run = async (label: string, op: () => Promise<unknown>) => {
    setBusy(label)
    setError('')
    try { await op(); await load(); return true } catch (e) { setError(e instanceof Error ? e.message : String(e)); return false } finally { setBusy('') }
  }
  const editModel = (model: ProviderModel) => {
    setEditingModel(model.id)
    setModelJSON(JSON.stringify({
      name: model.name, enabled: model.enabled, api: model.api, baseUrl: model.baseUrl,
      contextWindow: model.contextWindow, maxTokens: model.maxTokens, input: model.input,
      applyPatchToolType: model.applyPatchToolType,
      reasoning: model.reasoning, thinkingLevelMap: model.thinkingLevelMap, cost: model.cost, compat: model.compat,
    }, null, 2))
  }

  return (
    <section className="provider-settings" data-testid="provider-settings" aria-busy={!!busy}>
      <header className="settings-page-title provider-page-title">
        <div><h3>{c.title}</h3><p>{c.subtitle}</p></div>
        <button type="button" className="ui-button primary" data-testid="add-provider" onClick={() => { setError(''); setNewProvider(true) }}><IPlus />{c.addProvider}</button>
      </header>
      {error ? <div className="settings-error" role="alert"><span>{error}</span><button type="button" onClick={() => setError('')} aria-label="Close">×</button></div> : null}
      {busy ? <div className="settings-progress" role="status">{c.saving}</div> : null}

      <div className="provider-workbench">
        <aside className="provider-sidebar" aria-label={c.providers}>
          <div className="provider-sidebar-head"><span>{c.providers}</span><span>{data?.providers.length ?? 0}</span></div>
          <div className="provider-nav" role="listbox" aria-label={c.providers}>
            {data?.providers.map(provider => (
              // Name only: id + "API key needed" read as a second grey copy of the same row.
              <button type="button" role="option" aria-selected={provider.id === selected} key={provider.id} data-provider-id={provider.id} className={provider.id === selected ? 'on' : ''} title={provider.name} onClick={() => setSelected(provider.id)}>
                <span className={`provider-status-dot${provider.credential.configured ? ' ready' : ''}`} aria-hidden />
                <span className="provider-nav-copy">{provider.name}</span>
              </button>
            ))}
          </div>
        </aside>

        <div className="provider-content">
          {!data ? <div className="settings-empty">{c.loading}</div> : !current ? <div className="settings-empty">{c.selectProvider}</div> : (
            <>
              <header className="provider-detail-head">
                <div><div className="provider-title-line"><h3>{current.name}</h3><span className="type-badge">{current.builtin ? c.builtIn : c.custom}</span></div><code>{current.id}</code></div>
                <label className="setting-switch"><input type="checkbox" checked={current.enabled} disabled={!!busy} onChange={e => void run('enabled', () => api.patchProvider(current.id, { enabled: e.target.checked }))} /><span className="switch-track" aria-hidden><span /></span><span>{current.enabled ? c.enabled : c.disabled}</span></label>
              </header>

              <form className="settings-section" data-testid="provider-connection-form" onSubmit={event => { event.preventDefault(); void run('connection', () => api.patchProvider(current.id, connection)) }}>
                <div className="settings-section-head"><div><h4>{c.connection}</h4><p>{c.connectionHint}</p></div></div>
                <div className="form-grid two">
                  <label className="form-control"><span>{c.displayName}</span><input value={connection.name} onChange={e => setConnection(value => ({ ...value, name: e.target.value }))} /></label>
                  <label className="form-control"><span>{c.providerID}</span><input value={current.id} readOnly aria-readonly="true" /></label>
                  <label className="form-control full"><span>{c.baseURL}</span><input data-testid="provider-base-url" type="url" value={connection.baseUrl} onChange={e => setConnection(value => ({ ...value, baseUrl: e.target.value }))} /></label>
                  <label className="form-control"><span>{c.apiProtocol}</span><Select testid="provider-api" ariaLabel={c.apiProtocol} value={connection.api} options={apiOptions} onChange={api => setConnection(value => ({ ...value, api }))} /></label>
                </div>
                <div className="form-actions"><button className="ui-button primary" type="submit" disabled={!!busy}>{c.saveChanges}</button></div>
              </form>

              <section className="settings-section credential-section">
                <div className="settings-section-head"><div><h4>{c.credential}</h4><p>{c.credentialHint}</p></div><span className={`credential-badge${current.credential.configured ? ' ready' : ''}`}>{current.credential.configured ? <ICheck /> : null}{current.credential.configured ? c.stored : c.missingKey}</span></div>
                <div className="credential-control">
                  <label className="form-control"><span>{c.apiKey}</span><input type="password" autoComplete="new-password" placeholder={current.credential.configured ? format(c.source, { source: current.credential.source || 'stored' }) : 'sk-…'} value={key} onChange={e => setKey(e.target.value)} /></label>
                  <button type="button" className="ui-button secondary" disabled={!key || !!busy} onClick={() => void run('credential', () => api.setCredential(current.id, key)).then(ok => { if (ok) setKey('') })}>{c.saveKey}</button>
                  {current.credential.source === 'stored' ? <button type="button" className="ui-button ghost danger" disabled={!!busy} onClick={() => void run('credential', () => api.setCredential(current.id, null))}>{c.clear}</button> : null}
                </div>
              </section>

              <section className="settings-section models-section">
                <div className="settings-section-head"><div><h4>{c.models}</h4><p>{format(c.modelCount, { n: current.models.length })}</p></div><button type="button" className="ui-button secondary" data-testid="add-model" onClick={() => setNewModel(value => !value)}><IPlus />{c.addModel}</button></div>
                {newModel ? (
                  <form className="model-create-form" data-testid="new-model-form" onSubmit={event => {
                    event.preventDefault()
                    void run('model', () => api.createModel(current.id, { id: modelForm.id, name: modelForm.name || modelForm.id, contextWindow: Number(modelForm.contextWindow), maxTokens: Number(modelForm.maxTokens), input: ['text'], reasoning: false, cost: null })).then(ok => { if (ok) { setNewModel(false); setModelForm({ id: '', name: '', contextWindow: '128000', maxTokens: '16384' }) } })
                  }}>
                    <div className="form-grid two">
                      <label className="form-control"><span>{c.modelID}</span><input required value={modelForm.id} onChange={e => setModelForm(value => ({ ...value, id: e.target.value }))} /></label>
                      <label className="form-control"><span>{c.modelName}</span><input value={modelForm.name} onChange={e => setModelForm(value => ({ ...value, name: e.target.value }))} /></label>
                      <label className="form-control"><span>{c.contextWindow}</span><input required type="number" min="1" value={modelForm.contextWindow} onChange={e => setModelForm(value => ({ ...value, contextWindow: e.target.value }))} /></label>
                      <label className="form-control"><span>{c.maxOutput}</span><input required type="number" min="1" value={modelForm.maxTokens} onChange={e => setModelForm(value => ({ ...value, maxTokens: e.target.value }))} /></label>
                    </div>
                    <div className="form-actions"><button type="button" className="ui-button secondary" onClick={() => setNewModel(false)}>{c.cancel}</button><button type="submit" className="ui-button primary" disabled={!!busy}>{c.add}</button></div>
                  </form>
                ) : null}

                <div className="model-table">
                  {current.models.map(model => {
                    return <div className="model-row" key={model.id} data-testid="provider-model-row">
                      <div className="model-main"><strong>{model.name || model.id}</strong><small>{model.id}</small><div className="model-tags"><span>{model.contextWindow?.toLocaleString()} ctx</span>{model.reasoning ? <span>{c.reasoning}</span> : null}{model.input?.includes('image') ? <span>{c.vision}</span> : null}<span>{model.builtin ? c.builtIn : c.custom}</span></div></div>
                      <div className="model-actions">
                        <button type="button" className="icon-text-button" data-testid="edit-model" onClick={() => editModel(model)}><IEdit />{c.edit}</button>
                        <label className="compact-switch" title={model.enabled ? c.enabled : c.disabled}><input type="checkbox" checked={model.enabled} disabled={!!busy} onChange={e => void run('model-enabled', () => api.patchModel(current.id, { id: model.id, enabled: e.target.checked }))} /><span aria-hidden /></label>
                        {model.builtin && model.customized ? <button type="button" className="icon-text-button" onClick={() => void run('restore-model', () => api.deleteModel(current.id, model.id))}><IRegen />{c.restore}</button> : null}
                        {!model.builtin ? <button type="button" className="icon-text-button danger" disabled={!!busy} onClick={() => void run('delete-model', () => api.deleteModel(current.id, model.id))}><ITrash />{c.remove}</button> : null}
                      </div>
                    </div>
                  })}
                </div>

              </section>

              {(current.customized || !current.builtin) ? <section className="settings-section danger-section"><div><h4>{c.dangerZone}</h4></div>{current.builtin ? <button type="button" className="ui-button danger-outline" onClick={() => void run('restore-provider', () => api.deleteProvider(current.id))}>{c.restoreProvider}</button> : <button type="button" className="ui-button danger-outline" onClick={() => void run('delete-provider', () => api.deleteProvider(current.id))}><ITrash />{c.deleteProvider}</button>}</section> : null}
            </>
          )}
        </div>
      </div>

      {editingModel && current ? createPortal(
        <div className="provider-dialog-mask" data-testid="model-edit-dialog-mask" onMouseDown={() => { if (!busy) setEditingModel('') }}>
          <div className="provider-dialog model-edit-dialog" data-testid="model-advanced" role="dialog" aria-modal="true" aria-labelledby="model-edit-title" onMouseDown={event => event.stopPropagation()}>
            <header className="provider-dialog-head">
              <div>
                <h3 id="model-edit-title">{c.advanced}</h3>
                <p>{current.models.find(model => model.id === editingModel)?.name || editingModel} · {c.advancedHint}</p>
              </div>
              <button type="button" className="provider-dialog-close" aria-label={c.cancel} disabled={!!busy} onClick={() => setEditingModel('')}>×</button>
            </header>
            {error ? <div className="settings-error provider-dialog-error" role="alert"><span>{error}</span></div> : null}
            <form className="model-advanced" onSubmit={event => {
              event.preventDefault()
              try {
                const override = JSON.parse(modelJSON) as Record<string, unknown>
                void run('edit-model', () => api.patchModel(current.id, { id: editingModel, ...override })).then(ok => { if (ok) setEditingModel('') })
              } catch (e) { setError(e instanceof Error ? e.message : String(e)) }
            }}>
              <textarea ref={modelJSONRef} rows={16} value={modelJSON} onChange={e => setModelJSON(e.target.value)} spellCheck={false} />
              <div className="form-actions provider-dialog-actions"><button type="button" className="ui-button secondary" disabled={!!busy} onClick={() => setEditingModel('')}>{c.cancel}</button><button className="ui-button primary" type="submit" disabled={!!busy}>{busy ? c.saving : c.save}</button></div>
            </form>
          </div>
        </div>,
        document.body,
      ) : null}

      {newProvider ? createPortal(
        <div className="provider-dialog-mask" data-testid="new-provider-dialog-mask" onMouseDown={() => { if (!busy) setNewProvider(false) }}>
          <div className="provider-dialog" data-testid="new-provider-dialog" role="dialog" aria-modal="true" aria-labelledby="new-provider-title" onMouseDown={event => event.stopPropagation()}>
            <header className="provider-dialog-head">
              <div><h3 id="new-provider-title">{c.newProvider}</h3><p>{c.newProviderHint}</p></div>
              <button type="button" className="provider-dialog-close" aria-label={c.cancel} disabled={!!busy} onClick={() => setNewProvider(false)}>×</button>
            </header>
            {error ? <div className="settings-error provider-dialog-error" role="alert"><span>{error}</span></div> : null}
            <form data-testid="new-provider-form" onSubmit={event => {
              event.preventDefault()
              void run('provider', () => api.createProvider({
                id: form.id, name: form.name || form.id, api: form.api, baseUrl: form.baseUrl, enabled: true,
                models: [{ id: form.model, name: form.model, contextWindow: 128000, maxTokens: 16384, input: ['text'], reasoning: false, cost: null }],
              })).then(ok => { if (ok) { setNewProvider(false); setSelected(form.id); setForm({ id: '', name: '', api: 'completions', baseUrl: '', model: '' }) } })
            }}>
              <div className="form-grid two">
                <label className="form-control"><span>{c.providerID}</span><input ref={newProviderID} required value={form.id} onChange={e => setForm(value => ({ ...value, id: e.target.value }))} placeholder="my-provider" /></label>
                <label className="form-control"><span>{c.displayName}</span><input required value={form.name} onChange={e => setForm(value => ({ ...value, name: e.target.value }))} placeholder="My Provider" /></label>
                <label className="form-control full"><span>{c.baseURL}</span><input required type="url" value={form.baseUrl} onChange={e => setForm(value => ({ ...value, baseUrl: e.target.value }))} placeholder="https://example.com/v1" /></label>
                <label className="form-control"><span>{c.apiProtocol}</span><Select ariaLabel={c.apiProtocol} value={form.api} options={apiOptions} onChange={api => setForm(value => ({ ...value, api }))} /></label>
                <label className="form-control"><span>{c.firstModel}</span><input required value={form.model} onChange={e => setForm(value => ({ ...value, model: e.target.value }))} placeholder="model-id" /></label>
              </div>
              <div className="form-actions provider-dialog-actions"><button type="button" className="ui-button secondary" disabled={!!busy} onClick={() => setNewProvider(false)}>{c.cancel}</button><button className="ui-button primary" type="submit" disabled={!!busy}>{busy ? c.saving : c.create}</button></div>
            </form>
          </div>
        </div>,
        document.body,
      ) : null}
    </section>
  )
}
