import { CATEGORY_ORDER, type ContextTrendPoint } from './model'

export type TrendMode = 'total' | 'delta'

export function trendRows(points: ContextTrendPoint[], start: number, end: number, mode: TrendMode) {
  const rows = points.slice(start, end).map((point, offset) => {
    const previous = points[start + offset - 1]
    const values = CATEGORY_ORDER.map(id => ({
      id,
      value: mode === 'total' ? point.categories[id] ?? 0
        : previous ? (point.categories[id] ?? 0) - (previous.categories[id] ?? 0) : 0,
    }))
    const reported = point.basis === 'usage' || point.basis === 'meter'
    const reference = reported && point.tokens != null
      ? mode === 'total' ? point.tokens
        : previous?.basis === point.basis && previous.tokens != null ? point.tokens - previous.tokens : undefined
      : undefined
    return {
      point, previous, values, reference,
      positive: values.reduce((sum, item) => sum + Math.max(0, item.value), 0),
      negative: values.reduce((sum, item) => sum + Math.max(0, -item.value), 0),
    }
  })
  const scale = (peak: number) => {
    const unit = 10 ** Math.floor(Math.log10(Math.max(1, peak)))
    return [1, 2, 2.5, 5, 10].map(n => n * unit).find(n => n >= peak) ?? 10 * unit
  }
  const maximum = scale(Math.max(1, ...rows.flatMap(row => [row.positive, row.negative])))
  const reportedMaximum = scale(Math.max(1, ...rows.map(row => Math.abs(row.reference ?? 0))))
  return { rows, maximum, reportedMaximum }
}
