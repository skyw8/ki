import { useI18n } from '../../i18n'
import { durationText } from '../../lib/duration'

/** A stable time slot prevents jitter without rounding away elapsed seconds. */
export function Duration({ ms, label }: { ms: number; label?: 'elapsed' | 'ttft' }) {
  const { t } = useI18n()
  const text = durationText(ms)
  const [prefix, suffix] = label ? t(label === 'elapsed' ? 'turn.elapsed' : 'stats.ttft', { duration: '\ufffc' }).split('\ufffc') : ['', '']
  const description = t('duration.ms', { n: Number.isFinite(ms) ? Math.max(0, Math.round(ms)) : 0 })
  return <span className="duration" aria-label={label ? t(label === 'elapsed' ? 'turn.elapsed' : 'stats.ttft', { duration: description }) : description} title={description}>
    {prefix}<span className="duration-value" aria-hidden="true"><span style={{ fontSize: `${Math.min(1, 12 / text.length)}em` }}>{text}</span></span>{suffix}
  </span>
}
