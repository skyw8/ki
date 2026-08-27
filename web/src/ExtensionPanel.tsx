import { useEffect, useMemo, useRef, type ReactNode } from 'react'
import type { CatalogExtension, ExtensionUI } from './types'
import { Markdown } from './Markdown'
import { useI18n } from './i18n'

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
    })
  }
  for (const ui of items) {
    const previous = byName.get(ui.extension)
    byName.set(ui.extension, {
      name: ui.extension,
      configurable: previous?.configurable,
      status: ui.status ?? previous?.status,
      ui,
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

function sectionItems(sec: Record<string, unknown>): { label: string; value: string }[] {
  if (Array.isArray(sec.items)) {
    return sec.items.flatMap(row => {
      if (!row || typeof row !== 'object') return []
      const r = row as Record<string, unknown>
      const label = str(r.label ?? r.key ?? r.name)
      return [{ label, value: str(r.value) }]
    })
  }
  if (sec.kv && typeof sec.kv === 'object' && !Array.isArray(sec.kv)) {
    return Object.entries(sec.kv as Record<string, unknown>).map(([label, value]) => ({ label, value: str(value) }))
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
  const { t } = useI18n()
  const navRef = useRef<HTMLElement>(null)
  const navItems = useMemo(() => inspectorItems(items, globalItems), [globalItems, items])
  const selectedGlobal = globalItems.find(item => item.name === selectedName)
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
                <span className="ext-nav-text">{item.status?.text || item.name}</span>
                <span className="ext-nav-name">{item.name}</span>
              </span>
            </button>
          )
        })}
      </nav>
      <div className="ext-inspector-main" data-testid="ext-inspector-main">
        <header className="ext-inspector-head">
          <h3>{selected?.panel?.title || selectedGlobal?.name || selectedName}</h3>
        </header>
        {selected?.panel ? (
          <ExtensionPanel
            ui={selected}
            fields={fields}
            onField={onField}
            onAction={onAction}
            onSubmit={onSubmit}
            hideStatus
          />
        ) : null}
        {selectedGlobal?.configurable && renderConfig ? renderConfig(selectedGlobal.name) : null}
        {!selected?.panel && !selectedGlobal ? (
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
  hideStatus,
}: {
  ui: ExtensionUI
  fields: Record<string, string>
  onField: (id: string, value: string) => void
  onAction: (id: string) => void
  onSubmit: (fields: Record<string, unknown>) => void
  hideStatus?: boolean
}) {
  const { t } = useI18n()
  const panel = ui.panel
  const tone = ui.status?.tone || 'info'
  return (
    <div className="ext-panel-body">
      {!hideStatus && ui.status?.text ? <span className={`ext-chip tone-${tone}`} data-testid="ext-panel-status">{ui.status.text}</span> : null}
      {panel?.summary ? <div className="ext-panel-summary">{panel.summary}</div> : null}
      {(panel?.sections ?? []).map((sec, i) => {
        const heading = str(sec.heading || sec.title)
        const items = sectionItems(sec)
        const markdown = typeof sec.markdown === 'string' ? sec.markdown : ''
        const text = typeof sec.text === 'string' ? sec.text : ''
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
          <span>{f.label || f.id}</span>
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
            title={a.title || undefined}
            onClick={() => onAction(a.id)}
          >
            {a.label}
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
            {panel?.submitLabel || t('ext.submit')}
          </button>
        ) : null}
      </div>
    </div>
  )
}
