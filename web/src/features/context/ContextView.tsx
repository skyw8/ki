import { useDeferredValue, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { Entry, RequestView, ViewState } from '../../api/types'
import { copyText } from '../../lib/clipboard'
import { leafEntries, projectTurnStats, sessionStats } from '../../lib/model'
import { useI18n, type Lang } from '../../i18n/index'
import { Markdown } from '../markdown/Markdown'
import {
  buildContext, buildContextTrend, CATEGORY_ORDER,
  type ContextCategory, type ContextGroup, type ContextItem, type ContextTrendPoint, type SystemSection,
} from './model'
import './context.css'
import { trendRows } from './trend'
import { CATEGORY_COLORS } from './palette'

export type ContextViewProps = {
  view: ViewState
  selectedRequestId?: string | null
  onSelectRequest?: (id: string | null) => void
  onHydrate?: (entryId: string) => Promise<boolean>
  onLocateEntry?: (entryId: string) => void
  indexLoading?: boolean
  indexError?: boolean
  onRetryIndex?: () => void
}

const copy = {
  en: {
    title: 'Session context', stats: 'Session stats', current: 'Current context', trend: 'Context trend',
    browser: 'Context browser', events: 'Context events', turns: 'Turns', steps: 'Model steps',
    tools: 'Tool calls', input: 'Input tokens', output: 'Output tokens', cache: 'Cache hit', cost: 'Cost',
    elapsed: 'Active time', estimated: 'Estimated', actual: 'Reported input', available: 'Estimated composition',
    used: 'of context used', free: 'Free headroom', noWindow: 'Context window unavailable',
    currentPick: 'Current known context', request: 'Turn', step: 'Step', turn: 'Turn', total: 'Total', delta: 'Delta',
    approx: 'Reconstructed context · not the wire payload',
    approxHint: 'Category sizes are estimates of retained content. Extensions and provider encoders may change the actual request.',
    coverage: 'This view only includes available bodies; unloaded history and transformed content may be missing.',
    nextHint: 'The next request may change after reload, configuration changes, or extension hooks.',
    sourceHint: 'Sources are recognized from the recorded System text, not from today’s files or settings.',
    operatorHint: 'Global and project APPEND text has no separate historical markers; its attribution cannot be split reliably.',
    skillsHint: 'The Skills directory is in System. A loaded SKILL.md body belongs to its tool result, not a second copy here.',
    nonText: 'Non-text payload present; text estimates do not account for image or opaque-context tokens.',
    empty: 'No context recorded yet.', emptyTrend: 'No model requests recorded yet.', emptyEvents: 'No context changes in the loaded history.',
    noMatches: 'No matching content.', emptyCategory: 'No available items in this category.',
    search: 'Search available context…', searchLabel: 'Search context', clear: 'Clear search', items: 'items',
    sources: 'System sources', approximateSource: 'Source not fully attributable', load: 'Load full content',
    loadHistory: 'Load preceding context', loading: 'Loading…', loadFailed: 'Content could not be loaded. Try again.',
    truncated: 'Preview only', hydrateUnavailable: 'Full content is not available in this view.',
    showFull: 'Show complete loaded text', longPreview: 'Large content preview; render the full loaded text explicitly.',
    copy: 'Copy', copied: 'Copied', locate: 'Show in conversation', previous: 'vs previous request',
    earlier: 'Earlier', later: 'Later', chartHint: 'Colored stacks estimate category sizes from recorded content, including server-side estimates for unloaded bodies. Reported input has a separate chart and scale below.',
    deltaHint: 'Colored stacks show changes in category estimates. Comparable reported-input changes appear separately below; missing measurements are not zero.',
    reportedBar: 'Reported input / meter', loadedBar: 'Category estimate', unknownBar: 'No input measurement',
    remoteAbsorbed: 'Earlier messages are covered by an opaque remote checkpoint. Empty Agent / Compaction categories do not mean no agents ran or no compaction occurred. Select a pre-checkpoint request to inspect its earlier messages.',
    localAbsorbed: 'Earlier messages have been summarized. Compaction contains the summary, not copies of every original message; select a pre-compaction request to inspect those messages.',
    remoteCheckpoint: 'Remote checkpoint', localCheckpoint: 'Local compaction', categoryUnavailable: 'Earlier items may be summarized or encapsulated by the checkpoint; their original category sizes are unavailable.',
    time: 'Time', duration: 'Duration', status: 'Status', running: 'Running', complete: 'Complete', error: 'Error',
    missingPrompt: 'Prompt content has not been loaded.', known: 'known', unavailable: 'Unavailable',
    compaction: 'Compaction', modelChange: 'Model changed', promptChange: 'System / tools changed',
    firstPrompt: 'Initial request header', eventScope: 'Loaded branch history',
    schema: 'Tool schema', usage: 'Provider usage', categoryEstimate: 'Category token estimates',
    indexLoading: 'Loading request history…', indexFailed: 'Request history could not be loaded. Only the available tail is shown.',
    retry: 'Retry', retainedTail: 'Compacted messages are reconstructed from retained history; their original request ordering may differ.',
    retainedMissing: 'The retained compaction tail is not available.', remoteHint: 'A remote checkpoint exists. Whether this model reuses it or replays portable history is unknown.',
    failedReplay: 'Failed assistant messages are visible in history; provider replay filtering may exclude them.',
    partialContent: 'Some entries contain only metadata or shortened previews.',
    meter: 'Context meter', partial: 'Partial content',
    system: 'System', human: 'Human inputs', agent: 'Agent messages', extension: 'Extension messages',
    assistant: 'Assistant messages', tool: 'Tool results', remote: 'Remote context', toolSchemas: 'Tool schemas',
    sourceBuiltin: 'Ki foundation', sourceConfiguration: 'Ki configuration', sourceTools: 'Tool instructions',
    sourceGuidelines: 'General guidelines', sourceAppendBuiltin: 'Built-in APPEND',
    sourceAppendOperator: 'Operator APPEND (global / project)', sourceExtension: 'Extension instructions',
    sourceSkills: 'Skills directory', sourceProject: 'AGENTS / CLAUDE project instructions',
    sourceEnvironment: 'Runtime environment', sourceUnknown: 'Unattributed System content',
  },
  zh: {
    title: '会话上下文', stats: '会话统计', current: '当前上下文', trend: '上下文趋势',
    browser: '上下文浏览器', events: '上下文事件', turns: '人工输入轮数', steps: '模型步骤',
    tools: '工具调用', input: '输入 tokens', output: '输出 tokens', cache: '缓存命中', cost: '费用',
    elapsed: '活跃耗时', estimated: '估算', actual: '报告输入', available: '上下文分类估算',
    used: '上下文占用', free: '剩余空间', noWindow: '上下文窗口未知',
    currentPick: '当前已知上下文', request: '轮次', step: '步骤', turn: '轮次', total: '总量', delta: '变化',
    approx: '重建上下文 · 非实际 wire payload',
    approxHint: '分类大小是已保留内容的估算。扩展处理和 provider 编码可能改变实际请求。',
    coverage: '这里只包含可用正文；未加载历史和被改写的内容可能缺失。',
    nextHint: 'Reload、配置变更或扩展处理后，下一次请求的上下文可能改变。',
    sourceHint: '来源根据已记录的 System 文本识别，不读取当前文件或设置来替代历史。',
    operatorHint: '历史文本没有分别标记全局与项目 APPEND，无法可靠拆分归属。',
    skillsHint: 'System 中只有 Skills 目录；读取的 SKILL.md 正文属于对应工具结果，不在这里重复计入。',
    nonText: '包含非文本内容；文本估算不包含图片或不透明上下文的 token 开销。',
    empty: '尚未记录上下文。', emptyTrend: '尚未记录模型请求。', emptyEvents: '已加载历史中没有上下文变化。',
    noMatches: '没有匹配的内容。', emptyCategory: '此类别暂无可用条目。',
    search: '搜索已加载上下文…', searchLabel: '搜索上下文', clear: '清空搜索', items: '项',
    sources: 'System 来源', approximateSource: '来源无法完整归属', load: '加载完整内容',
    loadHistory: '加载前面的上下文', loading: '加载中…', loadFailed: '未能加载内容，请重试。',
    truncated: '仅预览', hydrateUnavailable: '此视图尚无完整内容。',
    showFull: '显示完整已加载文本', longPreview: '内容较大，目前只渲染预览；可手动显示全部已加载文本。',
    copy: '复制', copied: '已复制', locate: '在对话中查看', previous: '相较前一请求',
    earlier: '更早', later: '更新', chartHint: '彩色堆叠展示已记录内容的分类估算，含服务端对未加载正文的估算。报告输入在下方独立显示，两张图使用各自的刻度。',
    deltaHint: '彩色堆叠展示分类估算的增减；同口径报告输入的变化在下方单独展示，缺失计量不当作零。',
    reportedBar: '报告输入 / 仪表', loadedBar: '分类估算', unknownBar: '输入计量未知',
    remoteAbsorbed: '前面的消息已被远程压缩检查点覆盖，正文不可展开。Agent / Compaction 为空不表示没运行代理或没发生压缩；选择检查点之前的请求，可查看当时的消息。',
    localAbsorbed: '前面的消息已被摘要替换。Compaction 展示摘要，不重复展示全部原消息；选择压缩之前的请求，可查看当时的消息。',
    remoteCheckpoint: '远程检查点', localCheckpoint: '本地压缩', categoryUnavailable: '此前的条目可能已被摘要或远程检查点覆盖，无法恢复原分类的大小。',
    time: '时间', duration: '耗时', status: '状态', running: '运行中', complete: '已完成', error: '错误',
    missingPrompt: '尚未加载 prompt 正文。', known: '已知', unavailable: '不可用',
    compaction: '压缩上下文', modelChange: '模型切换', promptChange: 'System / 工具变更',
    firstPrompt: '首次请求头', eventScope: '已加载分支历史',
    schema: '工具定义', usage: 'Provider 用量', categoryEstimate: '分类 token 估算',
    indexLoading: '正在加载请求历史…', indexFailed: '未能加载请求历史，目前只展示可用尾部。',
    retry: '重试', retainedTail: '压缩后保留的消息由历史重建，原请求中的顺序可能不同。',
    retainedMissing: '尚无压缩后保留部分的完整内容。', remoteHint: '存在远程压缩检查点；当前模型是否复用它或回放 portable 历史尚不确定。',
    failedReplay: '历史包含失败的 Assistant 消息，provider 回放时可能会排除它们。',
    partialContent: '部分条目只有元数据或截短预览。',
    meter: '上下文仪表', partial: '内容不完整',
    system: 'System', human: '人工输入', agent: '代理消息', extension: '扩展消息',
    assistant: 'Assistant 消息', tool: '工具结果', remote: 'Remote 上下文', toolSchemas: '工具定义',
    sourceBuiltin: 'Ki 基础指令', sourceConfiguration: 'Ki 配置位置', sourceTools: '工具说明',
    sourceGuidelines: '通用行为约束', sourceAppendBuiltin: '内置 APPEND',
    sourceAppendOperator: 'Operator APPEND（全局 / 项目）', sourceExtension: '扩展指令',
    sourceSkills: 'Skills 目录', sourceProject: 'AGENTS / CLAUDE 项目指令',
    sourceEnvironment: '运行环境', sourceUnknown: '无法归属的 System 内容',
  },
} as const

type Copy = { [K in keyof typeof copy.en]: string }
type ColorSlice = { id: string; label: string; tokens: number; color: string }

const SOURCE_COLORS: Record<SystemSection['source'], string> = {
  builtin: '#7468e7', configuration: '#8d80dd', tools: '#b091db', guidelines: '#8272c1',
  'append-builtin': '#a46acd', 'append-operator': '#b55fa0', extension: '#ce6d9b',
  skills: '#a593dd', project: '#606ace', environment: '#889aca', unknown: '#8b859c',
}

function labels(t: Copy): Record<ContextCategory, string> {
  return {
    system: t.system, tools: t.toolSchemas, human: t.human, agent: t.agent, extension: t.extension,
    assistant: t.assistant, tool: t.tool, compaction: t.compaction, remote: t.remote,
  }
}

function sourceLabel(source: SystemSection['source'], t: Copy): string {
  const names: Record<SystemSection['source'], string> = {
    builtin: t.sourceBuiltin, configuration: t.sourceConfiguration, tools: t.sourceTools,
    guidelines: t.sourceGuidelines, 'append-builtin': t.sourceAppendBuiltin,
    'append-operator': t.sourceAppendOperator, extension: t.sourceExtension, skills: t.sourceSkills,
    project: t.sourceProject, environment: t.sourceEnvironment, unknown: t.sourceUnknown,
  }
  return names[source]
}

function number(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1).replace(/\.0$/, '')}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1).replace(/\.0$/, '')}k`
  return Math.round(value).toLocaleString()
}

function signed(value: number): string {
  return `${value > 0 ? '+' : value < 0 ? '−' : ''}${number(Math.abs(value))}`
}

function duration(value?: number): string {
  if (value == null || !Number.isFinite(value)) return '—'
  if (value < 1_000) return `${Math.round(value)} ms`
  if (value < 60_000) return `${(value / 1_000).toFixed(1)} s`
  return `${Math.floor(value / 60_000)}m ${Math.round((value % 60_000) / 1_000)}s`
}

function requestLabel(request: Pick<RequestView, 'turn' | 'step'>, t: Copy): string {
  return `${t.request} ${request.turn} · ${t.step} ${request.step}`
}

function groupSlices(groups: ContextGroup[], t: Copy): ColorSlice[] {
  const names = labels(t)
  return groups.filter(group => group.tokens > 0).map(group => ({
    id: group.id, label: names[group.id], tokens: group.tokens, color: CATEGORY_COLORS[group.id],
  }))
}

function sourceSlices(groups: ContextGroup[], t: Copy): ColorSlice[] {
  const slices = new Map<string, ColorSlice>()
  for (const item of groups.find(group => group.id === 'system')?.items ?? []) {
    for (const section of item.systemSections ?? []) {
      const previous = slices.get(section.source)
      if (previous) previous.tokens += section.tokens
      else slices.set(section.source, {
        id: section.source, label: sourceLabel(section.source, t), tokens: section.tokens,
        color: SOURCE_COLORS[section.source],
      })
    }
  }
  return [...slices.values()].filter(slice => slice.tokens > 0)
}

function StackedBar({ slices, label, className = '' }: { slices: ColorSlice[]; label: string; className?: string }) {
  const total = slices.reduce((sum, slice) => sum + slice.tokens, 0)
  return (
    <div className={`context-stacked ${className}`} role="img" aria-label={label}>
      {slices.map(slice => (
        <span
          key={slice.id} className="context-stack-segment"
          style={{ width: `${total > 0 ? slice.tokens / total * 100 : 0}%`, background: slice.color }}
          title={`${slice.label}: ≈${number(slice.tokens)} tokens`}
        />
      ))}
    </div>
  )
}

function Card({ title, children, className = '', testId, extra }: {
  title: string; children: ReactNode; className?: string; testId?: string; extra?: ReactNode
}) {
  return (
    <section className={`context-card ${className}`} data-testid={testId}>
      <header className="context-card-header"><h2>{title}</h2>{extra}</header>
      {children}
    </section>
  )
}

function Stats({ view, t }: { view: ViewState; t: Copy }) {
  const stats = useMemo(() => sessionStats(view), [view])
  const tools = useMemo(() => {
    // The body window omits old calls even with a complete metadata index.
    // Shared records retain their identities; compact projections account for
    // unseen calls and reconcile the current live suffix without double-counting.
    const counts = new Map<number, number>()
    for (const turn of projectTurnStats(view.nodes, view.turnBase, view.compactTurns).values()) {
      counts.set(turn.turn, Math.max(counts.get(turn.turn) ?? 0, turn.tools))
    }
    for (const turn of view.compactTurns ?? []) {
      counts.set(turn.stats.turn, Math.max(counts.get(turn.stats.turn) ?? 0, turn.stats.tools))
    }
    const known = new Map<number, Set<string>>()
    for (const record of view.records) {
      if (record.kind !== 'tool') continue
      const calls = known.get(record.turn) ?? new Set<string>()
      calls.add(record.id)
      known.set(record.turn, calls)
    }
    for (const [turn, calls] of known) counts.set(turn, Math.max(counts.get(turn) ?? 0, calls.size))
    return [...counts.values()].reduce((sum, value) => sum + value, 0)
  }, [view.nodes, view.turnBase, view.compactTurns, view.records])
  const input = stats.input + stats.cacheRead + stats.cacheWrite
  const cells = [
    [t.turns, number(stats.turns)], [t.steps, number(stats.steps)], [t.tools, number(tools)],
    [t.input, number(input)], [t.output, number(stats.output)],
    [t.cache, input > 0 ? `${(stats.cacheRead / input * 100).toFixed(1)}%` : '—'],
    [t.cost, stats.hasCost ? `$${stats.cost.toFixed(stats.cost < 1 ? 4 : 2)}` : '—'],
    [t.elapsed, duration(stats.elapsedMs + stats.turnElapsedMs)],
  ]
  return (
    <section className="context-stats" data-testid="context-stats" aria-label={t.stats}>
      {cells.map(([label, value]) => (
        <div className="context-stat" key={label}><span>{label}</span><strong>{value}</strong></div>
      ))}
    </section>
  )
}

function CurrentContext({ view, groups, totalTokens, t }: {
  view: ViewState; groups: ContextGroup[]; totalTokens: number; t: Copy
}) {
  const [sourcesOpen, setSourcesOpen] = useState(true)
  const used = view.contextUsage?.usedTokens
  const window = view.contextUsage?.contextWindow
  const share = used != null && window != null && window > 0 ? used / window * 100 : undefined
  const slices = groupSlices(groups, t)
  const sources = sourceSlices(groups, t)
  return (
    <Card title={t.current} testId="context-current" extra={<span className="context-model">{view.model || '—'}</span>}>
      <div className="context-occupancy-heading">
        <div><strong>{used == null || view.contextUsage?.estimated ? '≈' : ''}{number(used ?? totalTokens)}</strong>
          <span> / {window ? number(window) : '—'} tokens</span></div>
        <span>{share == null ? t.noWindow : `${Math.round(share)}% ${t.used}`}</span>
      </div>
      {share != null && (
        <div className="context-pressure" role="meter" aria-label={t.used}
          aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.min(100, Math.round(share))}>
          <span style={{ width: `${Math.max(0, Math.min(100, share))}%` }} />
        </div>
      )}
      <div className="context-composition-heading"><span>{t.available}</span><strong>≈{number(totalTokens)}</strong></div>
      <StackedBar slices={slices} label={`${t.categoryEstimate}: ${slices.map(slice => `${slice.label} ${slice.tokens}`).join(', ')}`} />
      <div className="context-legend">
        {slices.map(slice => (
          <div key={slice.id} className="context-legend-item">
            <i style={{ background: slice.color }} /><span>{slice.label}</span><strong>≈{number(slice.tokens)}</strong>
          </div>
        ))}
        {groups.filter(group => group.items.length > 0 && group.tokens === 0).map(group => (
          <div key={group.id} className="context-legend-item">
            <i style={{ background: CATEGORY_COLORS[group.id] }} /><span>{labels(t)[group.id]}</span>
            <strong>{group.items.some(item => item.tokensKnown === false) ? t.unavailable : '≈0'}</strong>
          </div>
        ))}
      </div>
      {groups.some(group => group.id === 'remote' && group.items.length > 0) && (
        <p className="context-notice" data-testid="context-checkpoint-notice">{t.remoteAbsorbed}</p>
      )}
      {sources.length > 0 && (
        <details className="context-source-breakdown" open={sourcesOpen}
          onToggle={event => setSourcesOpen(event.currentTarget.open)}>
          <summary>{t.sources}<span>≈{number(sources.reduce((sum, slice) => sum + slice.tokens, 0))}</span></summary>
          <StackedBar slices={sources} label={t.sources} />
          <div className="context-source-legend">
            {sources.map(slice => (
              <div key={slice.id} data-source-kind={slice.id}>
                <i style={{ background: slice.color }} /><span>{slice.label}</span><strong>≈{number(slice.tokens)}</strong>
              </div>
            ))}
          </div>
        </details>
      )}
      <p className="context-caption">{t.nextHint}</p>
    </Card>
  )
}

function RequestSummary({ request, t }: { request?: RequestView; t: Copy }) {
  if (!request) return null
  const usage = request.usage
  const input = usage ? (usage.input ?? 0) + (usage.cacheRead ?? 0) + (usage.cacheWrite ?? 0) : undefined
  const state = request.status === 'running' ? t.running : request.status === 'error' ? t.error : t.complete
  return (
    <div className="context-request-summary" data-testid="context-request-summary">
      <div><strong>{requestLabel(request, t)}</strong><span className={`context-status context-status-${request.status}`}>{state}</span></div>
      <span className="context-model">{[request.provider, request.model].filter(Boolean).join(' / ')}</span>
      <dl>
        <div><dt>{t.actual}</dt><dd>{input == null ? '—' : number(input)}</dd></div>
        <div><dt>{t.output}</dt><dd>{usage?.output == null ? '—' : number(usage.output)}</dd></div>
        <div><dt>{t.cache}</dt><dd>{input ? `${((usage?.cacheRead ?? 0) / input * 100).toFixed(1)}%` : '—'}</dd></div>
        <div><dt>{t.duration}</dt><dd>{duration(request.durationMs)}</dd></div>
      </dl>
      {request.error && <p className="context-error">{request.error}</p>}
    </div>
  )
}

function ContextTrend({ points, requests, selectedId, onSelect, t }: {
  points: ContextTrendPoint[]; requests: RequestView[]; selectedId: string | null;
  onSelect: (id: string | null) => void; t: Copy
}) {
  const [granularity, setGranularity] = useState<'step' | 'turn'>('step')
  const [mode, setMode] = useState<'total' | 'delta'>('total')
  const [pageEnd, setPageEnd] = useState<number | null>(null)
  const [pageSize, setPageSize] = useState(12)
  const chartRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const chart = chartRef.current
    if (!chart) return
    // Fit the visible window to actual card width instead of rendering a
    // multi-screen strip of 120 almost unreadable columns. Touch targets stay 44px.
    const observer = new ResizeObserver(() => setPageSize(Math.max(1, Math.min(24, Math.floor((chart.clientWidth - 4) / 46)))))
    observer.observe(chart)
    return () => observer.disconnect()
  }, [points.length > 0])
  const revealedSelection = useRef('')
  const display = useMemo(() => {
    if (granularity === 'step') return points
    const turns = new Map<number, ContextTrendPoint>()
    for (const point of points) turns.set(point.turn, point)
    return [...turns.values()]
  }, [granularity, points])
  const selectedRequest = requests.find(request => request.id === (selectedId ?? display.at(Math.min(pageEnd ?? display.length, display.length) - 1)?.requestId))
  const selectedGraphId = granularity === 'turn'
    ? display.find(point => selectedId != null && point.turn === selectedRequest?.turn)?.requestId
    : selectedId ?? undefined
  const end = Math.min(pageEnd ?? display.length, display.length)
  const start = Math.max(0, end - pageSize)
  const shown = display.slice(start, end)
  useLayoutEffect(() => {
    const key = `${granularity}:${pageSize}:${selectedId ?? 'current'}`
    if (revealedSelection.current === key) return
    if (selectedId == null) {
      revealedSelection.current = key
      setPageEnd(null)
      return
    }
    const index = display.findIndex(point => point.requestId === selectedGraphId)
    if (index < 0) return
    revealedSelection.current = key
    if (index < start || index >= end) setPageEnd(Math.min(display.length, index + Math.ceil(pageSize / 2)))
  }, [selectedId, selectedGraphId, granularity, display, start, end, pageSize])
  useLayoutEffect(() => {
    const chart = chartRef.current
    if (!chart) return
    if (!selectedGraphId) {
      chart.scrollLeft = chart.scrollWidth
      return
    }
    const selected = [...chart.querySelectorAll<HTMLElement>('.context-step')]
      .find(button => button.dataset.requestId === selectedGraphId)
    if (!selected) return
    const rect = selected.getBoundingClientRect()
    chart.scrollLeft += rect.left - chart.getBoundingClientRect().left - chart.clientWidth / 2 + rect.width / 2
  }, [selectedGraphId, start, end])
  const names = labels(t)
  const { rows, maximum: max, reportedMaximum } = useMemo(() => trendRows(display, start, end, mode), [display, start, end, mode])
  const active = shown.some(point => point.requestId === selectedGraphId) ? selectedGraphId : undefined
  const detail = rows.find(row => row.point.requestId === (selectedGraphId ?? selectedRequest?.id))
  const reportedDots = rows.map((row, index) => row.reference == null ? undefined : {
    id: row.point.requestId, value: row.reference,
    x: (index + 0.5) / rows.length * 1000,
    y: mode === 'delta' ? 40 - row.reference / reportedMaximum * 34 : 76 - row.reference / reportedMaximum * 70,
  })
  const reportedPaths: string[] = []
  let path = ''
  for (const dot of reportedDots) {
    if (!dot) { if (path) reportedPaths.push(path); path = ''; continue }
    path += `${path ? ' L' : 'M'}${dot.x},${dot.y}`
  }
  if (path) reportedPaths.push(path)
  return (
    <Card title={t.trend} testId="context-trend" className="context-trend-card">
      <div className="context-trend-controls">
        <div className="context-segment-control" aria-label={t.steps}>
          {(['step', 'turn'] as const).map(value => (
            <button type="button" key={value} data-testid={`context-trend-${value}`}
              aria-pressed={granularity === value} onClick={() => { setGranularity(value); setPageEnd(null) }}>{t[value]}</button>
          ))}
        </div>
        <div className="context-segment-control" aria-label={t.categoryEstimate}>
          {(['total', 'delta'] as const).map(value => (
            <button type="button" key={value} data-testid={`context-trend-${value}`}
              aria-pressed={mode === value} onClick={() => setMode(value)}>{t[value]}</button>
          ))}
        </div>
      </div>
      {display.length === 0 ? <p className="context-empty">{t.emptyTrend}</p> : (
        <>
          {(start > 0 || end < display.length) && (
            <div className="context-chart-pages">
              {start > 0 && <button className="context-button" type="button" onClick={() => setPageEnd(start)}>{t.earlier}</button>}
              <span>{start + 1}–{end} / {display.length}</span>
              {end < display.length && <button className="context-button" type="button"
                onClick={() => setPageEnd(Math.min(display.length, end + pageSize))}>{t.later}</button>}
            </div>
          )}
          <div className="context-composition-heading"><strong>{t.categoryEstimate}</strong><span>≈ tokens</span></div>
          <div className={`context-chart context-chart-${mode}`}>
            <div className="context-axis" aria-hidden>
              <span>{number(max)}</span><span>{mode === 'delta' ? '0' : number(max / 2)}</span><span>{mode === 'delta' ? `−${number(max)}` : '0'}</span>
            </div>
            <div className="context-chart-scroll" ref={chartRef} tabIndex={0} aria-label={t.trend}>
              <div className="context-chart-bars">
                {rows.map(({ point, values, positive, negative }) => {
                  const numericPartial = point.estimatesPartial ?? point.partial
                  const label = `${requestLabel(point, t)}, ${positive || negative || !numericPartial
                    ? `${t.categoryEstimate}: ≈${mode === 'delta' ? signed(positive - negative) : number(positive)} tokens` : t.unavailable}`
                  return (
                    <button type="button" key={point.requestId}
                      className={`context-step${active === point.requestId ? ' selected' : ''}`}
                      data-testid="context-step" data-request-id={point.requestId}
                      aria-label={label} aria-pressed={active === point.requestId}
                      onClick={() => onSelect(point.requestId)}
                      title={`${label}${numericPartial ? ` · ${t.partial}` : ''}${point.tokens != null
                        ? `\n${point.basis === 'usage' ? t.actual : point.basis === 'meter' ? t.meter : t.estimated}: ${number(point.tokens)}` : ''}
${values.filter(item => item.value).map(item => `${names[item.id]}: ${signed(item.value)}`).join('\n')}`}>
                      <span className="context-step-plot" aria-hidden>
                        <span className="context-step-positive" style={{ height: `${positive / max * (mode === 'delta' ? 50 : 100)}%` }}>
                          {values.filter(item => item.value > 0).map(item => (
                            <i key={item.id} data-testid="context-category-segment" data-category={item.id} data-tokens={item.value}
                              style={{ background: CATEGORY_COLORS[item.id], flex: item.value }} />
                          ))}
                        </span>
                        {mode === 'delta' && (
                          <span className="context-step-negative" style={{ height: `${negative / max * 50}%` }}>
                            {values.filter(item => item.value < 0).map(item => (
                              <i key={item.id} data-testid="context-category-segment" data-category={item.id} data-tokens={item.value}
                                style={{ background: CATEGORY_COLORS[item.id], flex: -item.value }} />
                            ))}
                          </span>
                        )}
                        {positive === 0 && negative === 0 && numericPartial && <span className="context-step-unavailable" title={t.unavailable}>?</span>}
                        {point.checkpoint && <span className={`context-checkpoint context-checkpoint-${point.checkpoint}`}
                          data-testid="context-checkpoint"
                          style={{ color: CATEGORY_COLORS[point.checkpoint === 'remote' ? 'remote' : 'compaction'] }}
                          title={point.checkpoint === 'remote' ? t.remoteCheckpoint : t.localCheckpoint}>◆</span>}
                      </span>
                      <span className="context-step-label">{point.turn}.{point.step}</span>
                    </button>
                  )
                })}
              </div>
            </div>
          </div>
          <div className="context-chart-categories">
            {CATEGORY_ORDER.map(id => <span key={id}><i style={{ background: CATEGORY_COLORS[id] }} />{names[id]}</span>)}
            <span><i className="context-key-checkpoint" style={{ color: CATEGORY_COLORS.compaction }}>◆</i>{t.compaction}</span>
          </div>
          <p className="context-caption">{mode === 'delta' ? t.deltaHint : t.chartHint}</p>
          <div className="context-reported-series" data-testid="context-reported-series">
            <div className="context-composition-heading"><strong>{t.reportedBar}</strong><span>{t.usage} · tokens</span></div>
            <div className="context-reported-plot">
              <div className="context-reported-axis" aria-hidden><span>{number(reportedMaximum)}</span><span>{mode === 'delta' ? `−${number(reportedMaximum)}` : '0'}</span></div>
              <svg data-testid="context-reported-line" role="img" aria-label={t.reportedBar}
                viewBox="0 0 1000 80" preserveAspectRatio="none">
                <line x1="0" x2="1000" y1={mode === 'delta' ? 40 : 76} y2={mode === 'delta' ? 40 : 76} className="context-reported-baseline" />
                {reportedPaths.map((value, index) => <path key={index} d={value} />)}
                {reportedDots.filter(dot => dot && (dot.id === active || rows.length === 1)).map(dot => dot && (
                  <circle key={dot.id} cx={dot.x} cy={dot.y} r={3} data-value={dot.value}>
                    <title>{number(dot.value)} tokens</title>
                  </circle>
                ))}
              </svg>
            </div>
          </div>
          {detail && <div className="context-chart-values" data-testid="context-trend-values">
            <span>{t.reportedBar}: <strong>{detail.reference == null ? '—' : mode === 'delta' ? signed(detail.reference) : number(detail.reference)}</strong></span>
            <span>{t.loadedBar}: <strong>{detail.positive || detail.negative || !(detail.point.estimatesPartial ?? detail.point.partial)
              ? `≈${mode === 'delta' ? signed(detail.positive - detail.negative) : number(detail.positive)}` : t.unavailable}</strong>
              {(detail.point.estimatesPartial ?? detail.point.partial) && ` · ${t.partial}`}</span>
          </div>}
          <RequestSummary request={selectedRequest} t={t} />
        </>
      )}
    </Card>
  )
}

const TEXT_PREVIEW_LIMIT = 24 * 1024

function ContentBody({ text, t }: { text: string; t: Copy }) {
  const [mode, setMode] = useState<'raw' | 'markdown'>('raw')
  const [full, setFull] = useState(false)
  const [copied, setCopied] = useState(false)
  // Large bodies and Markdown are mounted only after explicit expansion. A
  // folded browser must not parse megabytes of off-screen tool output.
  const preview = !full && text.length > TEXT_PREVIEW_LIMIT
  const shown = preview ? text.slice(0, TEXT_PREVIEW_LIMIT) : text
  return (
    <div className="context-content">
      <div className="context-content-toolbar">
        <div className="context-segment-control">
          <button type="button" aria-pressed={mode === 'raw'} onClick={() => setMode('raw')}>Raw</button>
          <button type="button" aria-pressed={mode === 'markdown'} onClick={() => setMode('markdown')}>Markdown</button>
        </div>
        <button className="context-button" type="button" onClick={() => {
          void copyText(text).then(ok => setCopied(ok))
        }}>{copied ? t.copied : t.copy}</button>
      </div>
      {mode === 'raw'
        ? <pre className="context-raw" data-testid="context-item-raw">{shown || '—'}</pre>
        : <div data-testid="context-item-markdown"><Markdown className="context-markdown" text={shown || '—'} /></div>}
      {preview && (
        <div className="context-large-content"><p>{t.longPreview}</p>
          <button className="context-button" type="button" onClick={() => setFull(true)}>{t.showFull}</button></div>
      )}
    </div>
  )
}

function SystemSource({ section, t }: { section: SystemSection; t: Copy }) {
  const [open, setOpen] = useState(false)
  return (
    <details className="context-system-source" data-source-kind={section.source}
      onToggle={event => setOpen(event.currentTarget.open)}>
      <summary><span>{sourceLabel(section.source, t)}</span><strong>≈{number(section.tokens)}</strong></summary>
      {open && (
        <div className="context-system-source-body">
          {section.title && section.title !== sourceLabel(section.source, t) && <p className="context-source-title">{section.title}</p>}
          {section.source === 'append-operator' && <p className="context-notice">{t.operatorHint}</p>}
          {section.source === 'skills' && <p className="context-caption">{t.skillsHint}</p>}
          {section.attribution === 'unknown' && <p className="context-caption">{t.approximateSource}</p>}
          <ContentBody text={section.text} t={t} />
        </div>
      )}
    </details>
  )
}

function HydrateButton({ entryId, onHydrate, t, history = false }: {
  entryId: string; onHydrate: NonNullable<ContextViewProps['onHydrate']>; t: Copy; history?: boolean
}) {
  const [status, setStatus] = useState<'idle' | 'loading' | 'failed'>('idle')
  return (
    <div className="context-hydrate">
      <button className="context-button" type="button" data-testid="context-hydrate" disabled={status === 'loading'}
        onClick={() => {
          setStatus('loading')
          void onHydrate(entryId).then(ok => setStatus(ok ? 'idle' : 'failed')).catch(() => setStatus('failed'))
        }}>{status === 'loading' ? t.loading : history ? t.loadHistory : t.load}</button>
      {status === 'failed' && <span className="context-error" role="status">{t.loadFailed}</span>}
    </div>
  )
}

function BrowserItem({ item, t, onHydrate, onLocateEntry }: {
  item: ContextItem; t: Copy; onHydrate?: ContextViewProps['onHydrate']; onLocateEntry?: ContextViewProps['onLocateEntry']
}) {
  const [open, setOpen] = useState(false)
  // Header entries supply System/schemas but deliberately have no chat row.
  // A conversation jump for them would page history to a nonexistent target.
  const canLocate = item.id !== 'system' && !item.id.startsWith('schema:')
  return (
    <details className="context-item" data-entry-id={item.entryId} data-tokens={item.tokens}
      onToggle={event => setOpen(event.currentTarget.open)}>
      <summary>
        <span className="context-item-title">{item.title}</span>
        {item.truncated && <span className="context-tag">{t.truncated}</span>}
        <strong>{item.tokensKnown === false ? t.unavailable : `≈${number(item.tokens)}`}</strong>
      </summary>
      {open && (
        <div className="context-item-body">
          <div className="context-item-meta">
            {item.sourceTags.map(tag => <span className="context-tag" key={tag}>{tag}</span>)}
            {canLocate && item.entryId && onLocateEntry && <button className="context-button" type="button" data-testid="context-locate-entry"
              onClick={() => onLocateEntry(item.entryId!)}>{t.locate}</button>}
          </div>
          {item.hasNonText && <p className="context-notice">{t.nonText}</p>}
          {item.truncated && (item.entryId && onHydrate
            ? <HydrateButton entryId={item.entryId} onHydrate={onHydrate} t={t} />
            : <p className="context-caption">{t.hydrateUnavailable}</p>)}
          <ContentBody text={item.text} t={t} />
          {item.systemSections && item.systemSections.length > 0 && (
            <div className="context-system-sources">
              <h3>{t.sources}</h3><p className="context-caption">{t.sourceHint}</p>
              {item.systemSections.map(section => <SystemSource key={section.id} section={section} t={t} />)}
            </div>
          )}
        </div>
      )}
    </details>
  )
}

function BrowserCategory({ group, allTokens, query, previousTokens, t, onHydrate, onLocateEntry, checkpoint }: {
  group: ContextGroup; allTokens: number; query: string; previousTokens?: number; t: Copy;
  onHydrate?: ContextViewProps['onHydrate']; onLocateEntry?: ContextViewProps['onLocateEntry']
  checkpoint?: 'remote' | 'local'
}) {
  const [open, setOpen] = useState(false)
  const [count, setCount] = useState(40)
  const names = labels(t)
  const items = useMemo(() => {
    if (!query) return group.items
    return group.items.filter(item => [item.title, ...item.sourceTags, item.text].join('\n').toLocaleLowerCase().includes(query))
  }, [query, group.items])
  const difference = previousTokens == null ? undefined : group.tokens - previousTokens
  const unpriced = group.id === 'remote' || (group.items.length > 0 && group.tokens === 0 && group.items.some(item => item.tokensKnown === false))
  return (
    <details className="context-category" data-category={group.id}
      onToggle={event => setOpen(event.currentTarget.open)}>
      <summary>
        <i style={{ background: CATEGORY_COLORS[group.id] }} />
        <span className="context-category-name">{names[group.id]}</span>
        <span className="context-category-count">{query ? `${items.length} / ` : ''}{group.items.length} {t.items}</span>
        {difference != null && difference !== 0 && <span className={`context-delta${difference < 0 ? ' negative' : ''}`}>{signed(difference)}</span>}
        <strong>{unpriced ? t.unavailable : `≈${number(group.tokens)}`}</strong>
        <span className="context-category-share">{unpriced ? '—' : `${allTokens ? Math.round(group.tokens / allTokens * 100) : 0}%`}</span>
      </summary>
      {open && (
        <div className="context-category-body">
          {items.length === 0 ? <p className="context-empty">{query ? t.noMatches : checkpoint && ['human', 'agent', 'extension', 'assistant', 'tool', 'compaction'].includes(group.id)
            ? group.id === 'compaction' && checkpoint === 'remote' ? t.remoteAbsorbed : t.categoryUnavailable : t.emptyCategory}</p>
            : items.slice(0, count).map(item => (
              <BrowserItem key={item.id} item={item} t={t} onHydrate={onHydrate} onLocateEntry={onLocateEntry} />
            ))}
          {items.length > count && <button className="context-button" type="button"
            onClick={() => setCount(value => value + 40)}>+{Math.min(40, items.length - count)} {t.items}</button>}
        </div>
      )}
    </details>
  )
}

function ContextEvents({ entries, leafId, requests, onSelect, onLocateEntry, t, lang }: {
  entries: Entry[]; leafId?: string; requests: RequestView[]; onSelect: (id: string | null) => void;
  onLocateEntry?: ContextViewProps['onLocateEntry']; t: Copy; lang: Lang
}) {
  const [count, setCount] = useState(30)
  const events = useMemo(() => {
    const headerRequests = new Map(requests.map(request => [request.id, request]))
    return leafEntries(entries, leafId).filter(entry => entry.type === 'compaction' || entry.type === 'model_change'
      || (entry.type === 'request_header' && headerRequests.get(entry.id)?.promptChange))
      .reverse()
  }, [entries, leafId, requests])
  return (
    <Card title={t.events} testId="context-events" extra={<span className="context-caption">{t.eventScope}</span>}>
      {events.length === 0 ? <p className="context-empty">{t.emptyEvents}</p> : (
        <div className="context-event-list">
          {events.slice(0, count).map(entry => {
            const request = requests.find(item => item.id === entry.id)
            const title = entry.type === 'compaction' ? t.compaction
              : entry.type === 'model_change' ? t.modelChange : request?.promptChange?.kind === 'initial' ? t.firstPrompt : t.promptChange
            const color = entry.type === 'compaction' ? CATEGORY_COLORS.compaction
              : entry.type === 'model_change' ? CATEGORY_COLORS.assistant : CATEGORY_COLORS.system
            const date = entry.timestamp ? new Date(entry.timestamp) : undefined
            return (
              <div className="context-event" key={entry.id} data-testid="context-event" data-event-id={entry.id} data-event-type={entry.type}>
                <i style={{ background: color }} />
                <div><strong>{title}</strong><span>
                  {request ? requestLabel(request, t) : [entry.provider, entry.modelId].filter(Boolean).join(' / ')}
                  {entry.tokensBefore != null && ` · ≈${number(entry.tokensBefore)} tokens`}
                </span></div>
                {date && Number.isFinite(date.getTime()) && <time dateTime={date.toISOString()}>
                  {date.toLocaleTimeString(lang === 'zh' ? 'zh-CN' : 'en', { hour: '2-digit', minute: '2-digit' })}
                </time>}
                {(request || onLocateEntry) && <button className="context-button" type="button" data-testid="context-event-action"
                  onClick={() => request ? onSelect(request.id) : onLocateEntry?.(entry.id)}>{request ? t.browser : t.locate}</button>}
              </div>
            )
          })}
          {events.length > count && <button className="context-button" type="button" onClick={() => setCount(value => value + 30)}>
            +{Math.min(30, events.length - count)} {t.items}
          </button>}
        </div>
      )}
    </Card>
  )
}

export function ContextView({
  view, selectedRequestId, onSelectRequest, onHydrate, onLocateEntry, indexLoading, indexError, onRetryIndex,
}: ContextViewProps) {
  const { lang } = useI18n()
  const t: Copy = copy[lang]
  const [localSelection, setLocalSelection] = useState<string | null>(null)
  const [search, setSearch] = useState('')
  const query = useDeferredValue(search.trim().toLocaleLowerCase())
  const selectedId = selectedRequestId === undefined ? localSelection : selectedRequestId
  const onSelect = (id: string | null) => { setLocalSelection(id); onSelectRequest?.(id) }
  const current = useMemo(() => buildContext({
    entries: view.allEntries, requests: view.requests, leafId: view.leafId,
  }), [view.allEntries, view.requests, view.leafId])
  const selected = useMemo(() => selectedId == null ? current : buildContext({
    entries: view.allEntries, requests: view.requests, leafId: view.leafId, requestId: selectedId,
  }), [current, selectedId, view.allEntries, view.requests, view.leafId])
  const points = useMemo(() => buildContextTrend({
    entries: view.allEntries, requests: view.requests, leafId: view.leafId,
  }), [view.allEntries, view.requests, view.leafId])
  const selectedIndex = points.findIndex(point => point.requestId === selectedId)
  const previous = points[selectedIndex - 1]
  const names = labels(t)
  const slices = groupSlices(selected.categories, t)
  const noticeText: Record<string, string> = {
    'partial-history': t.coverage, 'request-unavailable': t.coverage, 'prompt-unavailable': t.missingPrompt,
    'append-source-unknown': t.operatorHint, 'retained-tail-reconstructed': t.retainedTail,
    'retained-tail-unavailable': t.retainedMissing, 'compaction-content-unavailable': t.retainedMissing,
    'remote-context': t.remoteHint, 'remote-history-encapsulated': t.remoteAbsorbed, 'local-history-summarized': t.localAbsorbed, 'partial-content': t.partialContent,
    'non-text-estimate': t.nonText, 'failed-assistant-replay-unknown': t.failedReplay,
  }
  const coverageNotices = [...new Set(selected.notices.map(notice => noticeText[notice]).filter(Boolean))]
  const missing = selected.missingParentIds.length > 0 || coverageNotices.length > 0
  const headerToHydrate = selected.request?.id ?? view.requests.at(-1)?.id
  const missingPrompt = selected.notices.includes('prompt-unavailable')
  return (
    <div className="context-view" data-testid="context-view">
      <div className="context-page-heading"><h1>{t.title}</h1><span className="context-approx-badge">{t.estimated}</span></div>
      <Stats view={view} t={t} />
      <div className="context-page-notice" role="note">
        <strong>{t.approx}</strong><span>{t.approxHint}</span>
      </div>
      {indexLoading && <p className="context-index-status" role="status" data-testid="context-index-loading">{t.indexLoading}</p>}
      {indexError && (
        <div className="context-index-status context-index-error" role="alert" data-testid="context-index-error">
          <span>{t.indexFailed}</span>{onRetryIndex && <button className="context-button" type="button"
            onClick={onRetryIndex}>{t.retry}</button>}
        </div>
      )}
      <ContextTrend points={points} requests={view.requests} selectedId={selectedId} onSelect={onSelect} t={t} />
      <div className="context-grid">
        <div className="context-overview-column">
          <CurrentContext view={view} groups={current.categories} totalTokens={current.totalTokens} t={t} />
          <ContextEvents entries={view.allEntries} leafId={view.leafId} requests={view.requests}
            onSelect={onSelect} onLocateEntry={onLocateEntry} t={t} lang={lang} />
        </div>
        <Card title={t.browser} testId="context-browser" className="context-browser-card">
          <label className="context-request-picker">
            <span>{t.browser}</span>
            <select data-testid="context-request-select" value={selectedId ?? ''}
              onChange={event => onSelect(event.target.value || null)}>
              <option value="">{t.currentPick}</option>
              {view.requests.map(request => <option key={request.id} value={request.id}>{requestLabel(request, t)}</option>)}
            </select>
          </label>
          <div className="context-browser-heading"><strong>{selected.request ? requestLabel(selected.request, t) : t.currentPick}</strong>
            <span>≈{number(selected.totalTokens)} tokens</span></div>
          <StackedBar slices={slices} label={`${t.categoryEstimate}: ${slices.map(slice => `${slice.label} ${slice.tokens}`).join(', ')}`} />
          {selected.request && <RequestSummary request={selected.request} t={t} />}
          {previous && <p className="context-caption">{t.previous} · {requestLabel(previous, t)}</p>}
          {missing && (
            <div className="context-coverage" data-testid="context-coverage">
              {coverageNotices.length > 0 ? coverageNotices.map(notice => <p key={notice}>{notice}</p>) : <p>{t.coverage}</p>}
              {selected.missingParentIds[0] && onHydrate && (
                <HydrateButton key={selected.missingParentIds[0]} entryId={selected.missingParentIds[0]} onHydrate={onHydrate} t={t} history />
              )}
            </div>
          )}
          {missingPrompt && headerToHydrate && onHydrate && (
            <HydrateButton key={`header:${headerToHydrate}`} entryId={headerToHydrate} onHydrate={onHydrate} t={t} />
          )}
          <div className="context-search">
            <input type="search" data-testid="context-search" aria-label={t.searchLabel}
              placeholder={t.search} value={search} onChange={event => setSearch(event.target.value)} />
            {search && <button className="context-button" type="button" onClick={() => setSearch('')}>{t.clear}</button>}
          </div>
          <div className="context-browser-categories" aria-label={t.browser}>
            {selected.categories.map(group => (
              <BrowserCategory key={`${selectedId ?? 'current'}:${group.id}`} group={group} query={query}
                allTokens={selected.totalTokens} previousTokens={previous?.categories[group.id]} t={t}
                checkpoint={selected.notices.includes('remote-context') ? 'remote' : selected.notices.includes('local-history-summarized') ? 'local' : undefined}
                onHydrate={onHydrate} onLocateEntry={onLocateEntry} />
            ))}
          </div>
          {selected.categories.every(group => group.items.length === 0) && <p className="context-empty">{t.empty}</p>}
          <div className="context-browser-key" aria-hidden>
            {CATEGORY_ORDER.filter(id => selected.categories.some(group => group.id === id && group.tokens > 0))
              .map(id => <span key={id}><i style={{ background: CATEGORY_COLORS[id] }} />{names[id]}</span>)}
          </div>
        </Card>
      </div>
    </div>
  )
}
