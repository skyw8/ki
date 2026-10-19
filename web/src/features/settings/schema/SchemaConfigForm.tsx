import { useEffect, useMemo, useState, type FormEvent } from 'react'
import type { Client } from '../../../api/client'
import type { ExtensionConfig, ExtensionI18n, ModelInfo } from '../../../api/types'
import { Switch } from '../../../components/Switch'
import { toast } from '../../../components/toast'
import { useI18n, type Lang } from '../../../i18n/index'
import { clampThinkingEffort } from '../../../lib/model'
import { localizedExtensionText } from '../ExtensionPanel'
import { ModelField } from './ModelField'
import {
  asObject,
  asString,
  compileForm,
  getPath,
  setPath,
  type CardDef,
  type CompiledForm,
  type FieldDef,
  type JsonObject,
  type SectionDef,
} from './model'

export type ExtensionConfigEditorProps = {
  api: Client
  name: string
  onClose: () => void
  embedded?: boolean
  models?: ModelInfo[]
  defaultModel?: string
}

const SECRET = '<configured>'

function extensionCopy(i18n: ExtensionI18n | undefined, lang: Lang, key: string): string {
  return localizedExtensionText({ key }, i18n, lang)
}

function initialValues(form: CompiledForm, config: JsonObject): { values: JsonObject; configured: Record<string, boolean> } {
  const values: JsonObject = {}
  const configured: Record<string, boolean> = {}
  for (const field of Object.values(form.fieldsByName)) {
    const raw = config[field.name]
    if (field.secret) {
      if (raw === SECRET) {
        configured[field.name] = true
        values[field.name] = ''
      } else {
        values[field.name] = typeof raw === 'string' ? raw : ''
      }
      continue
    }
    if (field.widget === 'number') {
      const parsed = typeof raw === 'number' ? raw : typeof raw === 'string' && raw.trim() !== '' ? Number(raw) : NaN
      const fallback = typeof field.schema.default === 'number' ? field.schema.default : 0
      const displayed = (Number.isFinite(parsed) ? parsed : fallback) * field.displayScale
      // Scaling milliseconds to minutes can produce 60.00000000000001, which a
      // step=1 input rejects before submit. Keep display values stable without
      // changing the stored unit.
      values[field.name] = Math.round(displayed * 1e6) / 1e6
      continue
    }
    if (asString(field.schema.type) === 'boolean' || field.widget === 'switch' || field.widget === 'checkbox') {
      values[field.name] = raw === true
      continue
    }
    if (asString(field.schema.type) === 'object') {
      values[field.name] = asObject(raw)
      continue
    }
    values[field.name] = typeof raw === 'string' ? raw : raw == null ? '' : String(raw)
  }
  return { values, configured }
}

function fieldHelp(field: FieldDef, copy: (key: string | undefined) => string): string {
  return copy(field.hint)
}

function fieldLabel(field: FieldDef, copy: (key: string | undefined) => string): string {
  return copy(field.label) || field.name
}

export function SchemaConfigForm({ api, name, onClose, embedded = false, models = [], defaultModel = '' }: ExtensionConfigEditorProps) {
  const { t, lang } = useI18n()
  const [config, setConfig] = useState<ExtensionConfig | null>(null)
  const [form, setForm] = useState<CompiledForm | null>(null)
  const [values, setValues] = useState<JsonObject>({})
  const [configured, setConfigured] = useState<Record<string, boolean>>({})
  const [rawText, setRawText] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  const copy = (key: string | undefined) => key ? extensionCopy(config?.i18n, lang, key) : ''

  const apply = (next: ExtensionConfig) => {
    const compiled = compileForm(next.schema)
    const initial = initialValues(compiled, asObject(next.config))
    setConfig(next)
    setForm(compiled)
    setValues(initial.values)
    setConfigured(initial.configured)
    setRawText(JSON.stringify(next.config ?? {}, null, 2))
  }

  useEffect(() => {
    let alive = true
    setLoading(true)
    void api.extensionConfig(name).then(next => {
      if (alive) apply(next)
    }).catch(e => toast.from(e)).finally(() => {
      if (alive) setLoading(false)
    })
    return () => { alive = false }
    // apply only initializes state from the response; including it would restart
    // the request on every field edit.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [api, name])

  const setField = (path: string | string[], value: unknown) => {
    setValues(prev => setPath(prev, path, value))
  }

  const saveRaw = async () => {
    let parsed: unknown
    try {
      parsed = JSON.parse(rawText)
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error(t('cfg.configObject'))
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
      return
    }
    setSaving(true)
    try {
      const next = await api.patchExtensionConfig(name, parsed as JsonObject)
      apply(next)
      toast.info(t('cfg.configSaved'))
    } catch (e) {
      toast.from(e)
    } finally {
      setSaving(false)
    }
  }

  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!form || !config) return
    if (form.mode === 'json') {
      await saveRaw()
      return
    }
    const patch: JsonObject = {}
    const errors: string[] = []
    for (const field of Object.values(form.fieldsByName)) {
      const value = values[field.name]
      if (field.secret) {
        if (configured[field.name] && (value === '' || value === SECRET)) patch[field.name] = SECRET
        else patch[field.name] = typeof value === 'string' ? value.trim() : ''
        if (field.required && patch[field.name] === '') errors.push(copy(field.requiredMessage) || t('cfg.configRequired'))
        continue
      }
      if (field.widget === 'number') {
        const display = typeof value === 'number' ? value : Number(value)
        if (!Number.isFinite(display)) {
          errors.push(copy(field.invalidMessage) || t('cfg.configInvalidNumber'))
          continue
        }
        if (field.displayMinimum !== undefined && display < field.displayMinimum) {
          errors.push(copy(field.invalidMessage) || t('cfg.configInvalidNumber'))
        }
        if (field.displayMaximum !== undefined && display > field.displayMaximum) {
          errors.push(copy(field.invalidMessage) || t('cfg.configInvalidNumber'))
        }
        const raw = display / field.displayScale
        const serialized = asString(field.schema.type) === 'integer' ? Math.round(raw) : raw
        if (field.displayMinimum === undefined && typeof field.schema.minimum === 'number' && serialized < field.schema.minimum) {
          errors.push(copy(field.invalidMessage) || t('cfg.configInvalidNumber'))
        }
        if (field.displayMaximum === undefined && typeof field.schema.maximum === 'number' && serialized > field.schema.maximum) {
          errors.push(copy(field.invalidMessage) || t('cfg.configInvalidNumber'))
        }
        patch[field.name] = serialized
        continue
      }
      if (asString(field.schema.type) === 'object') {
        patch[field.name] = asObject(value)
        continue
      }
      if (asString(field.schema.type) === 'boolean' || field.widget === 'switch' || field.widget === 'checkbox') {
        patch[field.name] = value === true
        continue
      }
      const text = typeof value === 'string' ? value.trim() : value == null ? '' : String(value).trim()
      patch[field.name] = text
      if (field.required && text === '') errors.push(copy(field.requiredMessage) || t('cfg.configRequired'))
    }
    if (errors.length > 0) {
      toast.error(errors[0])
      return
    }

    for (const field of Object.values(form.fieldsByName)) {
      const effect = field.writeCredential
      const rawKey: unknown = patch[field.name]
      const key: string = typeof rawKey === 'string' ? rawKey : ''
      if (!effect || !key || key === SECRET || configured[field.name]) continue
      try {
        await api.setCredential(effect.provider, key)
        if (effect.clearOnSuccess) patch[field.name] = ''
      } catch {
        if (effect.failureMessage) toast.error(copy(effect.failureMessage))
      }
    }

    setSaving(true)
    try {
      const next = await api.patchExtensionConfig(name, patch)
      apply(next)
      toast.info(t('cfg.configSaved'))
    } catch (e) {
      toast.from(e)
    } finally {
      setSaving(false)
    }
  }

  const renderField = (fieldName: string) => {
    if (!form) return null
    const field = form.fieldsByName[fieldName]
    if (!field || field.hidden) return null
    const label = fieldLabel(field, copy)
    const hint = fieldHelp(field, copy)
    const value = values[field.name]
    const testid = field.testid || `${name}-${field.name}`

    if (field.widget === 'model') {
      const current = typeof value === 'string' ? value : ''
      let pickerValue = current
      if (field.legacyBareProvider && current && !current.includes('/')) pickerValue = `${field.legacyBareProvider}/${current}`
      if (field.store === 'bare' && field.providerFilter && current && !current.includes('/')) pickerValue = `${field.providerFilter}/${current}`
      const displayValue = field.useDefaultModel && !current ? defaultModel : current
      const available = field.providerFilter ? models.filter(item => item.provider === field.providerFilter) : models
      return (
        <ModelField
          key={field.name}
          fieldLabel={label}
          hint={hint}
          emptyLabel={copy(field.emptyLabel) || label}
          emptyDetail={copy(field.emptyDetail) || hint}
          customDetail={copy(field.customDetail) || label}
          dialogTitle={copy(field.dialogTitle) || t('model.title')}
          testid={testid}
          dialogTestid={field.dialogTestid || `${testid}-dialog`}
          value={displayValue}
          pickerValue={pickerValue || displayValue}
          models={available}
          onSelect={spec => {
            const provider = field.providerFilter || spec.split('/')[0]
            const next = field.store === 'bare' && spec.startsWith(`${provider}/`) ? spec.slice(provider.length + 1) : spec
            setField(field.name, next)
            if (field.thinkingField) {
              setValues(prev => {
                const effort = typeof getPath(prev, field.thinkingField!) === 'string' ? getPath(prev, field.thinkingField!) as string : ''
                return effort ? setPath(prev, field.thinkingField!, clampThinkingEffort(effort, available.find(item => item.spec === spec))) : prev
              })
            }
          }}
          onClear={field.clearable ? () => setField(field.name, '') : undefined}
          clearLabel={copy(field.clearLabel)}
          canClear={field.clearable && current.trim() !== ''}
          thinkingLabel={t('cfg.thinkingEffort')}
          thinkingEffort={field.thinkingField ? String(getPath(values, field.thinkingField) ?? '') : undefined}
          onThinking={field.thinkingField ? next => setField(field.thinkingField!, next) : undefined}
        />
      )
    }

    if (field.widget === 'checkbox') {
      return (
        <label key={field.name} className="extension-config-check">
          <input
            data-testid={testid}
            type="checkbox"
            checked={value === true}
            onChange={event => setField(field.name, event.target.checked)}
          />
          <span>{label}</span>
        </label>
      )
    }

    if (field.widget === 'switch') {
      return (
        <div key={field.name} className="extension-config-switch-row">
          <span>{label}</span>
          <Switch ariaLabel={label} testid={testid} on={value === true} onChange={on => setField(field.name, on)} />
        </div>
      )
    }

    const control = field.widget === 'select' ? (
      <select
        data-testid={testid}
        value={typeof value === 'string' ? value : ''}
        onChange={event => setField(field.name, event.target.value)}
      >
        {field.enumValues.map(option => {
          const raw = String(option)
          const optionLabel = copy(field.enumLabels[raw]) || raw
          return <option key={raw} value={raw}>{optionLabel}</option>
        })}
      </select>
    ) : field.widget === 'number' ? (
      <input
        data-testid={testid}
        type="number"
        min={field.displayMinimum ?? (typeof field.schema.minimum === 'number' ? field.schema.minimum * field.displayScale : undefined)}
        max={field.displayMaximum ?? (typeof field.schema.maximum === 'number' ? field.schema.maximum * field.displayScale : undefined)}
        step={field.step}
        value={typeof value === 'number' && Number.isFinite(value) ? String(value) : ''}
        onChange={event => {
          const next = Number(event.target.value)
          setField(field.name, Number.isFinite(next) ? next : NaN)
        }}
      />
    ) : field.widget === 'textarea' ? (
      <textarea
        data-testid={testid}
        value={typeof value === 'string' ? value : ''}
        onChange={event => setField(field.name, event.target.value)}
        spellCheck={field.spellCheck}
      />
    ) : (
      <input
        data-testid={testid}
        type={field.widget === 'password' ? 'password' : 'text'}
        value={typeof value === 'string' ? value : ''}
        placeholder={field.secret && configured[field.name] ? copy(field.configuredPlaceholder) || t('cfg.configConfigured') : copy(field.placeholder)}
        autoComplete={field.autoComplete}
        inputMode={field.inputMode as 'none' | 'text' | 'tel' | 'url' | 'email' | 'numeric' | 'decimal' | 'search' | undefined}
        spellCheck={field.spellCheck}
        onChange={event => {
          if (field.secret) {
            setConfigured(prev => ({ ...prev, [field.name]: false }))
          }
          setField(field.name, event.target.value)
        }}
      />
    )

    return (
      <label key={field.name} className={`form-control${field.span === 'full' ? ' full' : ''}`}>
        <span>{label}</span>
        {control}
        {hint ? <small className="form-hint">{hint}</small> : null}
      </label>
    )
  }

  const renderFields = (names: string[], columns: number) => (
    <div className={columns > 1 ? 'form-grid two extension-config-fields' : 'extension-config-fields'}>
      {names.map(renderField)}
    </div>
  )

  const renderOptions = (names: string[]) => (
    <div className="extension-config-options">
      {names.map(renderField)}
    </div>
  )

  const renderCard = (card: CardDef, section: SectionDef) => {
    if (!form) return null
    const enabled = getPath(values, card.toggle) !== false
    return (
      <article key={card.id} className={`extension-config-card${enabled ? '' : ' is-off'}`} data-testid={card.testid || `${name}-provider-${card.id}`}>
        <div className="extension-config-card-copy">
          <div className="extension-config-card-head">
            <div className="extension-config-card-title">
              <span className="extension-config-card-mark" data-accent={card.accent || 'neutral'} aria-hidden />
              <strong>{copy(card.title) || card.id}</strong>
              {card.headerStatus ? <span className="extension-config-card-status">{copy(card.headerStatus)}</span> : null}
            </div>
            {card.toggle.length > 0 ? (
              <label className="extension-config-card-toggle" title={copy(section.toggleHint) || copy('config.toggleHint')}>
                <span className="extension-config-toggle-copy"><small>{enabled ? copy('status.enabled') : copy('status.disabled')}</small></span>
                <Switch
                  ariaLabel={`${copy('config.toggles') || 'Provider'} ${copy(card.title) || card.id}`}
                  testid={card.toggleTestid || `${name}-toggle-${card.id}`}
                  on={enabled}
                  onChange={on => setField(card.toggle, on)}
                />
              </label>
            ) : null}
          </div>
          {card.description ? <p>{copy(card.description)}</p> : null}
        </div>
        <div className="extension-config-card-controls">
          {renderFields(card.fields, 1)}
          {card.link ? (
            <div className="extension-config-card-value">
              {card.link.label ? <span className="extension-config-badge">{copy(card.link.label)}</span> : null}
              {card.link.text ? <code>{card.link.text}</code> : null}
            </div>
          ) : null}
          {card.badges.map((badge, index) => (
            <div key={index} className="extension-config-card-value">
              {badge.label ? <span className={`extension-config-badge${badge.tone === 'free' ? ' free' : ''}`}>{copy(badge.label)}</span> : null}
            </div>
          ))}
        </div>
      </article>
    )
  }

  const renderSection = (section: SectionDef) => {
    const sectionID = section.id.replace(/[^a-zA-Z0-9_-]+/g, '-')
    const titleID = section.title ? `${name}-${sectionID}-title` : undefined
    return (
      <section key={section.id} className="extension-config-section" aria-labelledby={titleID}>
        {section.title || section.description ? (
          <div className="extension-config-section-head">
            <div>
              {section.title ? <h4 id={titleID}>{copy(section.title)}</h4> : null}
              {section.description ? <p>{copy(section.description)}</p> : null}
            </div>
          </div>
        ) : null}
        {section.fields.length > 0 ? renderFields(section.fields, section.columns) : null}
        {section.layout === 'cards' ? (
          <div className="extension-config-cards">
            {section.cards.map(card => renderCard(card, section))}
          </div>
        ) : null}
        {section.options.length > 0 ? renderOptions(section.options) : null}
      </section>
    )
  }

  const body = useMemo(() => {
    if (!form) return null
    const content = (
      <>
        {form.intro ? <p className="cfg-desc">{copy(form.intro)}</p> : null}
        {form.fields.length > 0 ? renderFields(form.fields, form.columns) : null}
        {form.options.length > 0 ? renderOptions(form.options) : null}
        {form.sections.map(renderSection)}
      </>
    )
    return form.scroll ? <div className="extension-config-scroll">{content}</div> : content
    // Rendering depends on every form/value edit; the helpers close over both.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [form, values, configured, config, lang, models, defaultModel])

  return (
    <section className={`extension-config-editor${form?.scroll ? ' schema-config-scroll' : ''}${embedded ? ' embedded' : ''}`} data-testid={`extension-config-${name}`}>
      {embedded ? <div className="cfg-name"><span>{t('cfg.config')}</span></div> : <div className="cfg-name"><span>{t('cfg.config')} · {name}</span><button type="button" className="cfg-btn" onClick={onClose}>{t('cfg.configClose')}</button></div>}
      {loading ? <p className="cfg-empty">{t('file.loading')}</p> : form?.mode === 'json' ? (
        <>
          <p className="cfg-desc">{t('cfg.configHint')}</p>
          <textarea className="extension-config-textarea" data-testid="extension-config-input" value={rawText} onChange={event => setRawText(event.target.value)} spellCheck={false} />
          <div className="cfg-actions extension-config-actions">
            <button type="button" className="primary-btn" data-testid={form.saveTestid || 'extension-config-save'} disabled={saving || !config} onClick={() => void saveRaw()}>
              {saving ? t('cfg.configSaving') : t('cfg.configSave')}
            </button>
          </div>
        </>
      ) : form ? (
        <form className={`extension-config-form${form.scroll ? ' scroll' : ''}`} onSubmit={event => void save(event)}>
          {body}
          <div className={`cfg-actions extension-config-actions${form.scroll ? ' fixed' : ''}`}>
            <button type="submit" className="primary-btn" data-testid={form.saveTestid || `${name}-config-save`} disabled={saving || !config}>
              {saving ? t('cfg.configSaving') : t('cfg.configSave')}
            </button>
          </div>
        </form>
      ) : null}
    </section>
  )
}
