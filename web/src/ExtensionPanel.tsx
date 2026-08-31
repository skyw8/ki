import { useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from 'react'
import type { CatalogExtension, ExtensionI18n, ExtensionUI } from './types'
import { Markdown } from './Markdown'
import { interpolate, useI18n, type Lang } from './i18n'

const TONE_RANK: Record<string, number> = {
  error: 0,
  warning: 1,
  active: 2,
  success: 3,
  info: 4,
}

/** Soft cap: 4 chips all fit; past that keep 3 and fold the rest. */
export const EXT_CHIP_SOFT = 4
export const EXT_CHIP_HARD = 3

export function statusChips(list: ExtensionUI[] | undefined): ExtensionUI[] {
  return (list ?? [])
    .filter(ui => ui.status?.text)
    .slice()
    .sort((a, b) => {
      const ra = TONE_RANK[a.status?.tone || 'info'] ?? 5
      const rb = TONE_RANK[b.status?.tone || 'info'] ?? 5
      if (ra !== rb) return ra - rb
      return a.extension.localeCompare(b.extension)
    })
}

export function visibleStatusChips(chips: ExtensionUI[]): ExtensionUI[] {
  if (chips.length <= EXT_CHIP_SOFT) return chips
  return chips.slice(0, EXT_CHIP_HARD)
}

type InspectorItem = {
  name: string
  status?: ExtensionUI['status']
  ui?: ExtensionUI
  configurable?: boolean
  i18n?: ExtensionI18n
}

function runtimeTone(item: CatalogExtension): string {
  switch (item.runtime?.state) {
    case 'ready': return 'success'
    case 'starting':
    case 'restarting': return 'warning'
    case 'failed': return 'error'
    default: return 'info'
  }
}

function inspectorItems(items: ExtensionUI[], globalItems: CatalogExtension[]): InspectorItem[] {
  const byName = new Map<string, InspectorItem>()
  for (const item of globalItems) {
    byName.set(item.name, {
      name: item.name,
      configurable: item.configurable,
      status: item.ui?.status ?? { key: item.name, text: item.name, tone: runtimeTone(item) },
      ui: item.ui,
      i18n: item.i18n,
    })
  }
  for (const ui of items) {
    const previous = byName.get(ui.extension)
    byName.set(ui.extension, {
      name: ui.extension,
      configurable: previous?.configurable,
      status: ui.status ?? previous?.status,
      ui,
      i18n: previous?.i18n,
    })
  }
  return Array.from(byName.values())
}

export function seedExtFields(ui: ExtensionUI | undefined): Record<string, string> {
  const fields: Record<string, string> = {}
  if (!ui) return fields
  for (const f of ui.panel?.fields ?? []) fields[f.id] = f.value == null ? '' : String(f.value)
  return fields
}

function str(v: unknown): string {
  if (v == null) return ''
  if (typeof v === 'string') return v
  if (typeof v === 'number' || typeof v === 'boolean') return String(v)
  try { return JSON.stringify(v) } catch { return String(v) }
}

function localeCandidates(lang: Lang, i18n?: ExtensionI18n): string[] {
  const locales = [lang, lang === 'zh' ? 'zh-CN' : 'en-US', i18n?.defaultLocale || '', 'en']
  const out: string[] = []
  for (const locale of locales) {
    if (!locale || out.includes(locale)) continue
    out.push(locale)
    const base = locale.split(/[-_]/, 1)[0]
    if (base && !out.includes(base)) out.push(base)
  }
  return out
}

// Extension-owned text is resolved from the extension catalog. Keeping this
// resolver generic is important: the host chooses the browser locale, but it
// must not know the extension's message keys or copy.
export function localizedExtensionText(v: unknown, i18n: ExtensionI18n | undefined, lang: Lang): string {
  if (v == null) return ''
  if (typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean') return String(v)
  if (typeof v === 'object' && !Array.isArray(v)) {
    const text = v as { key?: unknown; params?: unknown; fallback?: unknown }
    const key = typeof text.key === 'string' ? text.key : ''
    if (key) {
      let translated: string | undefined
      for (const locale of localeCandidates(lang, i18n)) {
        translated = i18n?.resources?.[locale]?.[key]
        if (translated !== undefined) break
      }
      const fallback = typeof text.fallback === 'string' ? text.fallback : ''
      const params = text.params && typeof text.params === 'object' && !Array.isArray(text.params)
        ? text.params as Record<string, string | number>
        : undefined
      return interpolate(translated ?? (fallback || key), params)
    }
  }
  return str(v)
}

function sectionItems(sec: Record<string, unknown>, i18n: ExtensionI18n | undefined, lang: Lang): { label: string; value: string }[] {
  if (Array.isArray(sec.items)) {
    return sec.items.flatMap(row => {
      if (!row || typeof row !== 'object') return []
      const r = row as Record<string, unknown>
      const label = localizedExtensionText(r.label ?? r.key ?? r.name, i18n, lang)
      return [{ label, value: localizedExtensionText(r.value, i18n, lang) }]
    })
  }
  if (sec.kv && typeof sec.kv === 'object' && !Array.isArray(sec.kv)) {
    return Object.entries(sec.kv as Record<string, unknown>).map(([label, value]) => ({ label, value: localizedExtensionText(value, i18n, lang) }))
  }
  return []
}

export function ExtensionInspector({
  items,
  globalItems = [],
  selected,
  selectedName,
  fields,
  onSelect,
  onField,
  onAction,
  onSubmit,
  renderConfig,
}: {
  items: ExtensionUI[]
  globalItems?: CatalogExtension[]
  selected?: ExtensionUI | null
  selectedName: string
  fields: Record<string, string>
  onSelect: (name: string) => void
  onField: (id: string, value: string) => void
  onAction: (id: string) => void
  onSubmit: (fields: Record<string, unknown>) => void
  renderConfig?: (name: string) => ReactNode
}) {
  const { t, lang } = useI18n()
  const navRef = useRef<HTMLElement>(null)
  const navItems = useMemo(() => inspectorItems(items, globalItems), [globalItems, items])
  const selectedGlobal = globalItems.find(item => item.name === selectedName)
  const extensionI18n = selectedGlobal?.i18n
  const hasDetails = !!selected?.panel
  const hasConfig = !!selectedGlobal?.configurable && !!renderConfig
  const defaultView = hasDetails ? 'details' : 'config'
  const [viewChoice, setViewChoice] = useState<{ extension: string; view: 'details' | 'config' }>(() => ({
    extension: selectedName,
    view: defaultView,
  }))
  const requestedView = viewChoice.extension === selectedName ? viewChoice.view : defaultView
  const activeView = requestedView === 'details' && hasDetails
    ? 'details'
    : requestedView === 'config' && hasConfig
      ? 'config'
      : defaultView
  const selectView = (view: 'details' | 'config', focus = false) => {
    setViewChoice({ extension: selectedName, view })
    if (focus) window.requestAnimationFrame(() => document.getElementById(`ext-inspector-${view}-tab`)?.focus({ preventScroll: false }))
  }
  const onViewTabsKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
    const next = event.key === 'Home'
      ? 'details'
      : event.key === 'End'
        ? 'config'
        : activeView === 'details' ? 'config' : 'details'
    event.preventDefault()
    selectView(next, true)
  }

  useEffect(() => {
    // A newly selected extension should open on its details when available;
    // otherwise a config-only extension would inherit the previous tab.
    setViewChoice({ extension: selectedName, view: defaultView })
  }, [defaultView, hasConfig, selectedName])
  useEffect(() => {
    navRef.current?.querySelector<HTMLElement>('.ext-nav-item.on')?.scrollIntoView({ block: 'nearest', inline: 'center' })
  }, [selectedName])
  return (
    <div className="ext-inspector">
      <nav ref={navRef} className="ext-inspector-nav" aria-label={t('ext.nav')} data-testid="ext-nav">
        {navItems.map(item => {
          const on = item.name === selectedName
          return (
            <button
              key={item.name}
              type="button"
              className={`ext-nav-item${on ? ' on' : ''}`}
              data-testid={`ext-nav-${item.name}`}
              aria-current={on ? 'page' : undefined}
              onClick={() => onSelect(item.name)}
            >
              <span className={`ext-nav-dot tone-${item.status?.tone || 'info'}`} aria-hidden />
              <span className="ext-nav-copy">
                <span className="ext-nav-text">{localizedExtensionText(item.status?.text || item.name, item.i18n, lang)}</span>
                <span className="ext-nav-name">{item.name}</span>
              </span>
            </button>
          )
        })}
      </nav>
      <div className="ext-inspector-main" data-testid="ext-inspector-main">
        <header className="ext-inspector-head">
          <h3>{localizedExtensionText(selected?.panel?.title || selectedGlobal?.name || selectedName, extensionI18n, lang)}</h3>
          {hasDetails && hasConfig ? (
            <div className="tabs ext-inspector-view-tabs" role="tablist" aria-label={t('ext.view')} onKeyDown={onViewTabsKeyDown}>
              <button
                id="ext-inspector-details-tab"
                type="button"
                role="tab"
                aria-selected={activeView === 'details'}
                aria-controls="ext-inspector-details-panel"
                tabIndex={activeView === 'details' ? 0 : -1}
                className={`tab${activeView === 'details' ? ' active' : ''}`}
                data-testid="ext-inspector-details-tab"
                onClick={() => selectView('details')}
              >
                {t('ext.details')}
              </button>
              <button
                id="ext-inspector-config-tab"
                type="button"
                role="tab"
                aria-selected={activeView === 'config'}
                aria-controls="ext-inspector-config-panel"
                tabIndex={activeView === 'config' ? 0 : -1}
                className={`tab${activeView === 'config' ? ' active' : ''}`}
                data-testid="ext-inspector-config-tab"
                onClick={() => selectView('config')}
              >
                {t('ext.config')}
              </button>
            </div>
          ) : null}
        </header>
        {hasDetails && hasConfig ? (
          <>
            <div
              className="ext-inspector-view"
              id="ext-inspector-details-panel"
              role="tabpanel"
              aria-labelledby="ext-inspector-details-tab"
              tabIndex={activeView === 'details' ? 0 : -1}
              hidden={activeView !== 'details'}
            >
              {activeView === 'details' ? (
                <ExtensionPanel
                  ui={selected!}
                  fields={fields}
                  onField={onField}
                  onAction={onAction}
                  onSubmit={onSubmit}
                  extensionI18n={extensionI18n}
                  hideStatus
                />
              ) : null}
            </div>
            <div
              className="ext-inspector-view"
              id="ext-inspector-config-panel"
              role="tabpanel"
              aria-labelledby="ext-inspector-config-tab"
              tabIndex={activeView === 'config' ? 0 : -1}
              hidden={activeView !== 'config'}
            >
              {activeView === 'config' ? renderConfig(selectedGlobal!.name) : null}
            </div>
          </>
        ) : hasDetails ? (
          <div className="ext-inspector-view">
            <ExtensionPanel
              ui={selected!}
              fields={fields}
              onField={onField}
              onAction={onAction}
              onSubmit={onSubmit}
              extensionI18n={extensionI18n}
              hideStatus
            />
          </div>
        ) : hasConfig ? (
          <div className="ext-inspector-view">{renderConfig(selectedGlobal!.name)}</div>
        ) : null}
        {!hasDetails && !hasConfig ? (
          <p className="ext-inspector-empty">{t('ext.empty')}</p>
        ) : null}
      </div>
    </div>
  )
}

export function ExtensionPanel({
  ui,
  fields,
  onField,
  onAction,
  onSubmit,
  extensionI18n,
  hideStatus,
}: {
  ui: ExtensionUI
  fields: Record<string, string>
  onField: (id: string, value: string) => void
  onAction: (id: string) => void
  onSubmit: (fields: Record<string, unknown>) => void
  extensionI18n?: ExtensionI18n
  hideStatus?: boolean
}) {
  const { t, lang } = useI18n()
  const panel = ui.panel
  const tone = ui.status?.tone || 'info'
  return (
    <div className="ext-panel-body">
      {!hideStatus && ui.status?.text ? <span className={`ext-chip tone-${tone}`} data-testid="ext-panel-status">{localizedExtensionText(ui.status.text, extensionI18n, lang)}</span> : null}
      {panel?.summary ? <div className="ext-panel-summary">{localizedExtensionText(panel.summary, extensionI18n, lang)}</div> : null}
      {(panel?.sections ?? []).map((sec, i) => {
        const heading = localizedExtensionText(sec.heading || sec.title, extensionI18n, lang)
        const items = sectionItems(sec, extensionI18n, lang)
        const markdown = localizedExtensionText(sec.markdown, extensionI18n, lang)
        const text = localizedExtensionText(sec.text, extensionI18n, lang)
        return (
          <section key={i} className="ext-panel-section">
            {heading ? <h3>{heading}</h3> : null}
            {items.length ? (
              <dl className="ext-props">
                {items.map(item => (
                  <div key={item.label} className="ext-prop">
                    <dt>{item.label}</dt>
                    <dd title={item.value}>{item.value}</dd>
                  </div>
                ))}
              </dl>
            ) : markdown ? (
              <div className="md ext-section-md"><Markdown text={markdown} /></div>
            ) : text ? (
              <p className="ext-section-text">{text}</p>
            ) : !heading ? (
              <pre className="ext-section">{JSON.stringify(sec)}</pre>
            ) : null}
          </section>
        )
      })}
      {(panel?.fields ?? []).map(f => (
        <label key={f.id} className="ext-field">
          <span>{localizedExtensionText(f.label || f.id, extensionI18n, lang)}</span>
          {f.options?.length ? (
            <select
              data-testid={`ext-field-${f.id}`}
              value={fields[f.id] ?? (f.value == null ? '' : String(f.value))}
              onChange={e => onField(f.id, e.target.value)}
            >
              {f.options.map(opt => <option key={opt} value={opt}>{opt}</option>)}
            </select>
          ) : f.type === 'textarea' ? (
            <textarea
              data-testid={`ext-field-${f.id}`}
              rows={3}
              value={fields[f.id] ?? (f.value == null ? '' : String(f.value))}
              onChange={e => onField(f.id, e.target.value)}
            />
          ) : (
            <input
              data-testid={`ext-field-${f.id}`}
              value={fields[f.id] ?? (f.value == null ? '' : String(f.value))}
              onChange={e => onField(f.id, e.target.value)}
            />
          )}
        </label>
      ))}
      <div className="ext-actions">
        {(panel?.actions ?? []).map(a => (
          <button
            key={a.id}
            type="button"
            className={a.style === 'danger' ? 'ext-btn danger' : a.style === 'primary' ? 'primary-btn' : 'ext-btn'}
            data-testid={`ext-action-${a.id}`}
            disabled={!!a.disabled}
            title={a.title ? localizedExtensionText(a.title, extensionI18n, lang) : undefined}
            onClick={() => onAction(a.id)}
          >
            {localizedExtensionText(a.label, extensionI18n, lang)}
          </button>
        ))}
        {(panel?.fields ?? []).length ? (
          <button
            type="button"
            className="primary-btn"
            data-testid="ext-submit"
            onClick={() => {
              const next: Record<string, unknown> = { ...fields }
              for (const f of panel?.fields ?? []) {
                if (next[f.id] === undefined) next[f.id] = f.value ?? ''
              }
              onSubmit(next)
            }}
          >
            {panel?.submitLabel ? localizedExtensionText(panel.submitLabel, extensionI18n, lang) : t('ext.submit')}
          </button>
        ) : null}
      </div>
    </div>
  )
}
