import { useI18n } from '../../i18n/index'
import type { Meta } from '../../api/types'

/**
 * Settings → About.
 *
 * The version comes from the server's /v1/meta, not from the SPA bundle: after
 * a port-forward is retargeted, or after `ki restart`, the tab may still hold
 * an older bundle, and "which build is serving me?" is exactly the question
 * this page answers. Everything is read-only.
 */
export function AboutSettings({ meta }: { meta: Meta | null }) {
  const { t, lang } = useI18n()
  const rows: { label: string; value: string; mono?: boolean }[] = []
  if (meta) {
    rows.push({ label: t('about.version'), value: meta.version || t('about.unknown'), mono: true })
    if (meta.releaseTag) rows.push({ label: t('about.release'), value: meta.releaseTag, mono: true })
    if (meta.pid) rows.push({ label: t('about.pid'), value: String(meta.pid), mono: true })
    if (meta.startedAt) {
      rows.push({ label: t('about.startedAt'), value: `${formatTime(meta.startedAt, lang)} · ${uptime(meta.startedAt, t)}` })
    }
    if (meta.exe) rows.push({ label: t('about.exe'), value: meta.exe, mono: true })
    if (meta.kiHome) rows.push({ label: t('about.home'), value: meta.kiHome, mono: true })
  }
  return (
    <div className="preference-page" data-testid="about-settings">
      <header className="settings-page-title">
        <div>
          <h3>{t('settings.about')}</h3>
          <p>{t('settings.aboutHint')}</p>
        </div>
      </header>
      <section className="preference-section">
        <div className="preference-copy">
          <h4>{t('about.server')}</h4>
        </div>
        {meta ? (
          <dl className="about-list" data-testid="about-server">
            {rows.map(row => (
              <div className="about-row" key={row.label}>
                <dt>{row.label}</dt>
                <dd className={row.mono ? 'mono' : undefined}>{row.value}</dd>
              </div>
            ))}
          </dl>
        ) : (
          <p className="about-empty">{t('about.loading')}</p>
        )}
      </section>
      <p className="preference-footnote">{t('settings.hint')}</p>
    </div>
  )
}

function formatTime(iso: string, lang: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString(lang === 'zh' ? 'zh-CN' : 'en-US', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
  })
}

function uptime(iso: string, t: (key: 'about.days' | 'about.hours' | 'about.minutes') => string): string {
  const started = new Date(iso).getTime()
  if (Number.isNaN(started)) return ''
  const seconds = Math.max(0, Math.round((Date.now() - started) / 1000))
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (days > 0) return t('about.days').replace('{n}', String(days)).replace('{h}', String(hours))
  if (hours > 0) return t('about.hours').replace('{n}', String(hours)).replace('{m}', String(minutes))
  return t('about.minutes').replace('{n}', String(minutes))
}
