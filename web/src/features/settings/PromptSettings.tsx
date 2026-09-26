import { useCallback, useEffect, useState } from 'react'
import type { Client } from '../../api/client'
import { toast } from '../../components/toast'
import { useI18n } from '../../i18n/index'
import type { PromptAppendItem, PromptAppendView } from '../../api/types'

// Mirrors maxAppendSystemPromptBytes in internal/server/prompt.go. Every byte
// lands in the system prompt of each following request, so the editor warns
// before the server has to reject a paste.
const MAX_BYTES = 64 * 1024

const EDITABLE_SOURCES = ['global', 'project'] as const
type EditableSource = (typeof EDITABLE_SOURCES)[number]

function isEditable(item: PromptAppendItem): item is PromptAppendItem & { source: EditableSource } {
  return item.editable && (EDITABLE_SOURCES as readonly string[]).includes(item.source)
}

/**
 * The appended-system-prompt editor. Sources are listed in render order and only
 * the operator files are writable: the built-in layer and extension layers are
 * read-only because they belong to the harness and to extension packages.
 *
 * Why no workspace picker: the project file is relative to the selected
 * workspace, which the sidebar already owns, so the card explains the missing
 * workspace instead of guessing a cwd.
 */
export function PromptSettings({ api, workspaceId }: { api: Client; workspaceId: string | null }) {
  const { t } = useI18n()
  const [view, setView] = useState<PromptAppendView | null>(null)
  const [drafts, setDrafts] = useState<Partial<Record<EditableSource, string>>>({})
  const [saving, setSaving] = useState<EditableSource | null>(null)

  const load = useCallback(async () => {
    try {
      const next = await api.promptAppend(workspaceId)
      setView(next)
      // Drafts are dropped on every load: switching workspace or saving returns
      // the stored text, and keeping a stale draft would show edits that are not
      // what the next prompt uses.
      setDrafts({})
    } catch (e) {
      toast.from(e)
    }
  }, [api, workspaceId])

  useEffect(() => { void load() }, [load])

  const items = view?.items ?? []
  const textOf = (item: PromptAppendItem & { source: EditableSource }) => drafts[item.source] ?? item.text
  const dirty = (item: PromptAppendItem & { source: EditableSource }) => textOf(item) !== item.text

  const save = async (item: PromptAppendItem & { source: EditableSource }) => {
    const text = textOf(item)
    if (!text.trim()) {
      toast.error(t('settings.promptEmpty'))
      return
    }
    setSaving(item.source)
    try {
      setView(await api.putPromptAppend(item.source, text, workspaceId))
      setDrafts(prev => {
        const next = { ...prev }
        delete next[item.source]
        return next
      })
      toast.info(t('settings.promptSaved'))
    } catch (e) {
      toast.from(e)
    } finally {
      setSaving(null)
    }
  }

  const remove = async (item: PromptAppendItem & { source: EditableSource }) => {
    if (!window.confirm(t('settings.promptDeleteConfirm', { path: item.path ?? '' }))) return
    setSaving(item.source)
    try {
      setView(await api.deletePromptAppend(item.source, workspaceId))
      setDrafts(prev => {
        const next = { ...prev }
        delete next[item.source]
        return next
      })
    } catch (e) {
      toast.from(e)
    } finally {
      setSaving(null)
    }
  }

  return (
    <div className="preference-page" data-testid="prompt-settings">
      <header className="settings-page-title">
        <div>
          <h3>{t('settings.prompt')}</h3>
          <p>{t('settings.promptHint')}</p>
        </div>
      </header>
      {items.map((item, index) => {
        if (!isEditable(item)) {
          const readOnlyTestId = item.source === 'extension' ? 'prompt-extension' : 'prompt-builtin'
          return (
            <section key={`${item.source}-${item.name ?? index}`} className="prompt-source-card readonly" data-testid={`prompt-source-${item.source}`}>
              <header className="prompt-source-head">
                <div>
                  <h4>{item.source === 'extension' ? t('settings.promptExtension', { name: item.name ?? '' }) : t('settings.promptBuiltin')}</h4>
                  <p className="prompt-source-hint">{item.source === 'extension' ? t('settings.promptExtensionHint') : t('settings.promptBuiltinHint')}</p>
                  <code className="prompt-source-path">{(item.paths ?? []).join(' ') || item.path || ''}</code>
                </div>
                <span className="prompt-source-badge">{t('settings.promptReadOnly')}</span>
              </header>
              <pre className="prompt-source-text" data-testid={readOnlyTestId} data-name={item.name}>{item.text}</pre>
            </section>
          )
        }
        const text = textOf(item)
        const overLimit = text.length > MAX_BYTES
        return (
          <section key={item.source} className="prompt-source-card" data-testid={`prompt-source-${item.source}`}>
            <header className="prompt-source-head">
              <div>
                <h4>{item.source === 'project' ? t('settings.promptProject') : t('settings.promptGlobal')}</h4>
                <p className="prompt-source-hint">{item.source === 'project' ? t('settings.promptProjectHint') : t('settings.promptGlobalHint')}</p>
                {item.path ? <code className="prompt-source-path">{item.path}</code> : null}
              </div>
              <span className={`prompt-source-badge${item.exists ? ' on' : ''}`}>
                {item.exists ? t('settings.promptFileExists') : t('settings.promptFileMissing')}
              </span>
            </header>
            {item.available ? (
              <>
                <textarea
                  className={`prompt-editor${overLimit ? ' over-limit' : ''}`}
                  data-testid={`prompt-edit-${item.source}`}
                  aria-label={item.source === 'project' ? t('settings.promptProject') : t('settings.promptGlobal')}
                  spellCheck={false}
                  value={text}
                  onChange={e => setDrafts(prev => ({ ...prev, [item.source]: e.target.value }))}
                  onKeyDown={e => {
                    // Ctrl/Cmd+S is the editor shortcut users expect; the modal
                    // has no form, so nothing else claims the chord.
                    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
                      e.preventDefault()
                      if (!overLimit && dirty(item)) void save(item)
                    }
                  }}
                />
                <div className="prompt-editor-actions">
                  <span className="prompt-bytes" data-testid={`prompt-bytes-${item.source}`}>
                    {t('settings.promptBytes', { used: String(text.length), max: String(MAX_BYTES) })}
                  </span>
                  <button
                    type="button"
                    className="ui-button ghost danger"
                    data-testid={`prompt-delete-${item.source}`}
                    disabled={!item.exists || saving !== null}
                    onClick={() => void remove(item)}
                  >
                    {t('settings.promptDelete')}
                  </button>
                  <button
                    type="button"
                    className="ui-button primary"
                    data-testid={`prompt-save-${item.source}`}
                    disabled={saving !== null || overLimit || !dirty(item)}
                    onClick={() => void save(item)}
                  >
                    {saving === item.source ? t('settings.promptSaving') : t('settings.promptSave')}
                  </button>
                </div>
              </>
            ) : (
              <p className="prompt-source-blocked" data-testid={`prompt-blocked-${item.source}`}>
                {item.reason === 'home-required' ? t('settings.promptHomeRequired') : t('settings.promptWorkspaceRequired')}
              </p>
            )}
          </section>
        )
      })}
      <section className="preference-section">
        <div className="preference-copy">
          <h4>{t('settings.promptEffective')}</h4>
          <p>{t('settings.promptEffectiveHint')}</p>
        </div>
        <pre className="prompt-effective" data-testid="prompt-effective">{view?.effective ?? ''}</pre>
      </section>
    </div>
  )
}
