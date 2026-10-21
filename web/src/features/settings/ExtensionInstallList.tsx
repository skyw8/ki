import { useCallback, useEffect, useRef, useState } from 'react'
import type { Client } from '../../api/client'
import type { AvailableExtension } from '../../api/types'
import { toast } from '../../components/toast'
import { useI18n } from '../../i18n/index'

/** Capabilities that need user-supplied configuration right after install. */
const CONFIGURED_CAPABILITIES = new Set(['channel', 'provider'])

/**
 * ExtensionInstallList is the "available to install" half of Settings →
 * Extensions. It talks to GET /v1/extensions/available and follows progress
 * through the extensions invalidation pushed by the server, so a slow download
 * survives a closed tab and a second tab sees the same stage.
 *
 * `refreshKey` changes whenever the extension catalog is refetched; the server
 * publishes that invalidation on every install stage, so this component never
 * polls.
 */
export function ExtensionInstallList({ api, refreshKey, onInstalled }: {
  api: Client
  refreshKey: number
  /** Refresh the installed list once a new package becomes discoverable. */
  onInstalled?: () => void | Promise<void>
}) {
  const { t } = useI18n()
  const [items, setItems] = useState<AvailableExtension[]>([])
  const [release, setRelease] = useState('')
  const [latest, setLatest] = useState(false)
  const [source, setSource] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const pending = useRef(false)
  // Names this tab asked to install. When discovery starts reporting them as
  // installed, the toggle list above is stale and has to refetch.
  const awaiting = useRef(new Set<string>())
  // Why a ref: the parent passes an inline callback, and a dependency on its
  // identity would re-run the fetch on every render.
  const installedCallback = useRef(onInstalled)
  installedCallback.current = onInstalled

  const load = useCallback(async () => {
    try {
      const got = await api.availableExtensions()
      const next = got.items ?? []
      setItems(next)
      setRelease(got.release ?? '')
      setLatest(!!got.latest)
      setSource(got.source ?? '')
      setError('')
      const arrived = next.filter(item => item.installed && awaiting.current.has(item.name))
      if (arrived.length) {
        for (const item of arrived) awaiting.current.delete(item.name)
        void installedCallback.current?.()
      }
    } catch (e) {
      setError(String(e instanceof Error ? e.message : e))
    } finally {
      setLoading(false)
    }
  }, [api])

  useEffect(() => { void load() }, [load, refreshKey])

  const install = async (item: AvailableExtension) => {
    if (pending.current) return
    const needsConfig = (item.capabilities ?? []).some(cap => CONFIGURED_CAPABILITIES.has(cap))
    if (needsConfig && !window.confirm(t('ext.installConfirm', { name: item.name }))) return
    pending.current = true
    awaiting.current.add(item.name)
    setItems(prev => prev.map(row => row.name === item.name ? { ...row, install: { state: 'download' } } : row))
    try {
      await api.installExtension(item.name)
      await load()
    } catch (e) {
      setItems(prev => prev.map(row => row.name === item.name
        ? { ...row, install: { state: 'failed', error: String(e instanceof Error ? e.message : e) } }
        : row))
      toast.from(e)
    } finally {
      pending.current = false
    }
  }

  const candidates = items.filter(item => !item.installed || item.updateAvailable || item.install?.state === 'failed')
  if (loading) return null
  if (!candidates.length) return null

  return (
    <section className="ext-install" data-testid="extensions-available">
      <header className="ext-install-head">
        <div>
          <h4>{t('ext.available')}</h4>
          <p>{t('ext.availableHint', {
            release: !release ? t('ext.availableNoRelease') : latest ? t('ext.availableLatest') : release.replace(/^download\//, ''),
          })}</p>
        </div>
        {source ? <code className="ext-install-source" title={source}>{source}</code> : null}
      </header>
      {error ? <p className="settings-error-inline" role="alert">{error}</p> : null}
      {!release ? <p className="cfg-hint" data-testid="extensions-no-release">{t('ext.availableNoReleaseHint')}</p> : null}
      <ul className="cfg-list">
        {candidates.map(item => {
          const stage = item.install?.state === 'download'
            ? t('ext.installDownloading')
            : item.install?.state === 'extract'
              ? t('ext.installExtracting')
              : ''
          const busy = !!stage
          return (
            <li key={item.name} className="cfg-row" data-testid={`ext-available-${item.name}`} data-name={item.name}>
              <div className="cfg-copy">
                <div className="cfg-name">
                  {item.name}
                  <span className="cfg-src">{item.version ? `v${item.version}` : item.asset}</span>
                  {item.installed && item.updateAvailable ? <span className="cfg-src">{t('ext.updateBadge')}</span> : null}
                </div>
                {item.description ? <p className="cfg-desc">{item.description}</p> : null}
                {item.install?.state === 'failed' && item.install.error ? (
                  <p className="cfg-desc settings-error-inline" role="alert" data-testid={`ext-install-error-${item.name}`}>
                    {item.install.error}
                  </p>
                ) : null}
                {busy ? <p className="cfg-desc" data-testid={`ext-install-stage-${item.name}`}>{stage}</p> : null}
              </div>
              <button
                type="button"
                className="primary-btn ext-install-btn"
                data-testid={`ext-install-${item.name}`}
                disabled={busy || !release}
                onClick={() => void install(item)}
              >
                {busy ? <span className="spin" aria-hidden /> : null}
                {busy
                  ? stage
                  : item.install?.state === 'failed'
                    ? t('ext.retry')
                    : item.installed && item.updateAvailable
                      ? t('ext.update')
                      : t('ext.install')}
              </button>
            </li>
          )
        })}
      </ul>
      <p className="cfg-hint">{t('ext.installHint')}</p>
    </section>
  )
}
