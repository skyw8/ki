import { useI18n } from '../../i18n'
import { durationColumns, durationText } from '../../lib/duration'

/** A compact format-sized slot prevents tick jitter without hiding seconds. */
export function Duration({ ms, label }: { ms: number; label?: 'elapsed' | 'ttft' }) {
  const { t } = useI18n()
  const text = durationText(ms)
  const [prefix, suffix] = label ? t(label === 'elapsed' ? 'turn.elapsed' : 'stats.ttft', { duration: '\ufffc' }).split('\ufffc') : ['', '']
  const description = t('duration.ms', { n: Number.isFinite(ms) ? Math.max(0, Math.round(ms)) : 0 })
  return <span className="duration" aria-label={label ? t(label === 'elapsed' ? 'turn.elapsed' : 'stats.ttft', { duration: description }) : description} title={description}>
    {prefix}<span className="duration-value" aria-hidden="true" style={{ width: `${durationColumns(text)}ch` }}>{text}</span>{suffix}
  </span>
}
