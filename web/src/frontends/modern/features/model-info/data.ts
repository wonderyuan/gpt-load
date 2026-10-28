import type { ParetoPoint, QuotaModel, QuotaPlan, SpeedModel } from '../../api/model-info'
import { dateFormatter, numberFormatter } from '../../components/ui/intl-formatters'

export type ParetoMetric = 'cost_usd' | 'duration_minutes' | 'composite_cost'
export type ParetoScale = 'log' | 'linear'
export type QuotaMetric = 'api_value_usd' | 'weekly_tokens'

export function matchesModel(name: string, search: string): boolean {
  return name.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase())
}
export function metricValue(point: ParetoPoint, metric: ParetoMetric): number {
  return point[metric]
}
// The visible frontier follows the selected metric and filter. Equal points do not dominate.
export function paretoFrontier(points: readonly ParetoPoint[], metric: ParetoMetric): Set<string> {
  return new Set(
    points
      .filter(
        (point) =>
          !points.some(
            (other) =>
              other.score >= point.score &&
              metricValue(other, metric) <= metricValue(point, metric) &&
              (other.score > point.score ||
                metricValue(other, metric) < metricValue(point, metric)),
          ),
      )
      .map((point) => point.id),
  )
}
const paretoColors = [
  '--modern-chart-input',
  '--modern-chart-cache',
  '--modern-chart-write',
  '--modern-chart-output',
  '--modern-protocol-rerank',
  '--modern-protocol-anthropic',
  '--modern-protocol-gemini-embeddings',
  '--modern-protocol-completions',
] as const
// Small native SVG marks, shared by each series and its legend swatch.
export function paretoSymbol(slot: number): string {
  return [
    'M3.5,0 A3.5,3.5 0 1,0 -3.5,0 A3.5,3.5 0 1,0 3.5,0 Z',
    'M0,-5 L3.5,0 0,5 -3.5,0 Z',
    'M-3.5,-3.5 H3.5 V3.5 H-3.5 Z',
    'M0,-4.5 L4.5,3.5 -4.5,3.5 Z',
    'M0,-5 L1.5,-1.5 4.8,-1.5 2,0.8 3,4.5 0,2.3 -3,4.5 -2,0.8 -4.8,-1.5 -1.5,-1.5 Z',
    'M-1.5,-4.5 H1.5 V-1.5 H4.5 V1.5 H1.5 V4.5 H-1.5 V1.5 H-4.5 V-1.5 H-1.5 Z',
    'M-1.5,-4.5 H1.5 V-0.8 L4.5,1 3,3.5 0,1.8 -3,3.5 -4.5,1 -1.5,-0.8 Z',
    'M0,4.5 L4.5,-3.5 -4.5,-3.5 Z',
  ][slot % 8]!
}
export function paretoDefaultScale(metric: ParetoMetric): ParetoScale {
  return metric === 'duration_minutes' ? 'linear' : 'log'
}
export interface ParetoModelLegend {
  id: string
  name: string
  color: string
  shape: string
  slot: number
}
// Retain surviving models' colors across refreshes, including while they are hidden.
export function paretoModels(
  points: readonly ParetoPoint[],
  previous: readonly ParetoModelLegend[] = [],
): ParetoModelLegend[] {
  const names = new Map(points.map((point) => [point.model_id, point.model]))
  const slots = new Map(
    previous.filter((model) => names.has(model.id)).map((model) => [model.id, model.slot]),
  )
  const used = new Set(slots.values())
  return [...names].map(([id, name]) => {
    let slot = slots.get(id) ?? 0
    if (!slots.has(id)) {
      while (used.has(slot)) slot++
      used.add(slot)
    }
    return {
      id,
      name,
      slot,
      color: `var(${paretoColors[slot % paretoColors.length]})`,
      shape: paretoSymbol(slot),
    }
  })
}
export function reconcileHiddenModels(
  models: readonly ParetoModelLegend[],
  hidden: ReadonlySet<string>,
): Set<string> {
  return new Set(models.filter((model) => hidden.has(model.id)).map((model) => model.id))
}
export function filterParetoPoints(
  points: readonly ParetoPoint[],
  hidden: ReadonlySet<string>,
  effort = '',
): ParetoPoint[] {
  return points.filter(
    (point) => !hidden.has(point.model_id) && (!effort || point.effort === effort),
  )
}
export function scatterPointLabel(point: ParetoPoint): string {
  return `${point.model.replace(/^GPT[- ]/iu, '')} · ${point.effort}`
}
function niceStep(range: number, count: number): number {
  const rough = range / count
  const unit = 10 ** Math.floor(Math.log10(rough))
  return ([1, 2, 2.5, 5, 10].find((step) => step * unit >= rough) ?? 10) * unit
}
function linearTicks(low: number, high: number, count: number): number[] {
  const step = niceStep(high - low, count)
  const start = Math.ceil(low / step) * step
  return Array.from({ length: Math.floor((high - start) / step) + 1 }, (_, i) =>
    Number((start + i * step).toPrecision(12)),
  )
}
export function scatterLayout(
  points: readonly ParetoPoint[],
  metric: ParetoMetric,
  maxScore: number,
  requestedScale: ParetoScale = paretoDefaultScale(metric),
  options: { width?: number; height?: number; domainPoints?: readonly ParetoPoint[] } = {},
) {
  const cohort = options.domainPoints ?? points
  const values = cohort.map((point) => metricValue(point, metric))
  const peak = Math.max(...values, 0)
  const scale: ParetoScale =
    requestedScale === 'log' && values.length > 0 && values.every((value) => value > 0)
      ? 'log'
      : 'linear'
  // Like the public comparison, pad the actual extent rather than whole decades.
  const xDomain: [number, number] =
    scale === 'log' ? [Math.min(...values) / 1.2, peak * 1.2] : [0, peak > 0 ? peak * 1.06 : 1]
  const scoreCeiling = maxScore > 0 && Number.isFinite(maxScore) ? maxScore : 1
  const lowest = Math.min(...cohort.map((point) => point.score), scoreCeiling)
  const yDomain: [number, number] = [
    cohort.length ? Math.max(0, Math.min(scoreCeiling / 3, Math.floor(lowest / 10) * 10)) : 0,
    scoreCeiling,
  ]
  const width = Math.max(780, (options.width ?? 960) || 960)
  const height = Math.max(options.height ?? 420, 420)
  const frame = { left: 64, right: width - 24, top: 36, bottom: height - 56 }
  const transform = (value: number) => (scale === 'log' ? Math.log10(value) : value)
  const xLow = transform(xDomain[0])
  const xSpan = transform(xDomain[1]) - xLow
  const x = (value: number) =>
    frame.left + ((transform(value) - xLow) / xSpan) * (frame.right - frame.left)
  const y = (value: number) =>
    frame.bottom - ((value - yDomain[0]) / (yDomain[1] - yDomain[0])) * (frame.bottom - frame.top)
  let ticks: number[]
  if (scale === 'log') {
    const start = Math.floor(Math.log10(xDomain[0]))
    const end = Math.ceil(Math.log10(xDomain[1]))
    const candidates = Array.from({ length: end - start + 1 }, (_, i) =>
      [1, 2, 3, 5, 7].map((multiple) => Number((multiple * 10 ** (start + i)).toPrecision(12))),
    )
      .flat()
      .filter((value) => value >= xDomain[0] && value <= xDomain[1])
    // Select densely enough for narrow extents, without unreadable adjacent ticks.
    ticks = candidates.reduce<number[]>((kept, value) => {
      if (!kept.length || x(value) - x(kept[kept.length - 1]!) >= 76) kept.push(value)
      return kept
    }, [])
    if (ticks.length < 2) ticks = [xDomain[0], xDomain[1]]
  } else ticks = linearTicks(...xDomain, Math.max(3, Math.floor((width - 88) / 100)))
  const yTicks = linearTicks(...yDomain, 6)
  if (yTicks[yTicks.length - 1] !== scoreCeiling) yTicks.push(scoreCeiling)
  return {
    width,
    height,
    frame,
    scale,
    xDomain,
    yDomain,
    ticks: ticks.map((value) => ({ value, x: x(value) })),
    yTicks: yTicks.map((value) => ({ value, y: y(value) })),
    points: points.map((point) => ({
      ...point,
      x: x(metricValue(point, metric)),
      y: y(point.score),
    })),
  }
}
export type ScatterLayout = ReturnType<typeof scatterLayout>
export function scatterLabels(
  layout: ScatterLayout,
  options: {
    frontier?: ReadonlySet<string>
    selected?: string
    focusedModel?: string
    measure?: (text: string) => number
  } = {},
) {
  const { points, frame } = layout
  const candidates: { point: ScatterLayout['points'][number]; text: string; important: boolean }[] =
    []
  const seen = new Set<string>()
  function add(point: ScatterLayout['points'][number] | undefined, full = true) {
    if (!point || seen.has(point.id)) return
    seen.add(point.id)
    candidates.push({
      point,
      text: full ? scatterPointLabel(point) : point.effort,
      important: full,
    })
  }
  add(points.find((point) => point.id === options.selected))
  if (options.focusedModel)
    points.filter((point) => point.model_id === options.focusedModel).forEach((point) => add(point))
  // Anchor every series by its peak, then identify the efficient choices.
  for (const model of new Set(points.map((point) => point.model_id)))
    add(
      points
        .filter((point) => point.model_id === model)
        .reduce((a, b) => (b.score > a.score ? b : a)),
    )
  points
    .filter((point) => options.frontier?.has(point.id))
    .sort((a, b) => b.score - a.score)
    .forEach((point) => add(point))
  points.forEach((point) => add(point, false))
  const placed: {
    id: string
    model_id: string
    text: string
    x: number
    y: number
    width: number
    height: number
    important: boolean
  }[] = []
  const offsets = [
    [14, 0, 0],
    [-14, 0, 1],
    [0, -18, 0.5],
    [0, 18, 0.5],
    [12, -15, 0],
    [-12, -15, 1],
    [12, 15, 0],
    [-12, 15, 1],
  ]
  const measure =
    options.measure ??
    ((text: string) =>
      [...text].reduce((sum, char) => sum + (/[^\u0020-\u007e]/u.test(char) ? 12 : 6.4), 0))
  for (const { point, text, important } of candidates) {
    if (placed.length >= (layout.width >= 1100 ? 24 : 16)) break
    const width = measure(text) + 6
    for (const [dx, dy, anchor] of offsets) {
      const x = point.x + dx! - width * anchor!,
        y = point.y + dy! - 8
      if (x < frame.left || x + width > frame.right || y < frame.top || y + 16 > frame.bottom)
        continue
      if (
        placed.some(
          (label) =>
            x < label.x + label.width + 3 &&
            x + width + 3 > label.x &&
            y < label.y + 19 &&
            y + 19 > label.y,
        )
      )
        continue
      if (
        points.some(
          (dot) =>
            dot.id !== point.id &&
            dot.x > x - 5 &&
            dot.x < x + width + 5 &&
            dot.y > y - 5 &&
            dot.y < y + 21,
        )
      )
        continue
      placed.push({
        id: point.id,
        model_id: point.model_id,
        text,
        x,
        y,
        width,
        height: 16,
        important,
      })
      break
    }
  }
  return placed
}
export function paretoTrajectories<T extends ParetoPoint>(
  points: readonly T[],
  efforts: readonly string[],
) {
  const known = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra']
  const order = [...known, ...efforts.filter((effort) => !known.includes(effort))]
  return [...new Set(points.map((point) => point.model_id))].map((model_id) => ({
    model_id,
    points: points
      .filter((point) => point.model_id === model_id)
      .sort((a, b) => order.indexOf(a.effort) - order.indexOf(b.effort)),
  }))
}
export function frontierPath(layout: ScatterLayout, frontier: ReadonlySet<string>): string {
  const points = layout.points.filter((point) => frontier.has(point.id)).sort((a, b) => a.x - b.x)
  return points
    .map((point, index) => `${index ? 'H' + point.x + ' V' : 'M' + point.x + ','}${point.y}`)
    .join(' ')
}
export function quotaPeak(plans: readonly QuotaPlan[], metric: QuotaMetric): number | null {
  const values = plans.flatMap((plan) =>
    plan.models.map((model) => model[metric]).filter((value): value is number => value !== null),
  )
  return values.length ? Math.max(...values) : null
}
export function quotaPercent(
  model: QuotaModel,
  metric: QuotaMetric,
  peak: number | null,
): number | null {
  const value = model[metric]
  return value === null || peak === null ? null : peak === 0 ? 0 : (value / peak) * 100
}
export function sortSpeedModels(models: readonly SpeedModel[]): SpeedModel[] {
  return [...models].sort((a, b) => {
    if (a.tokens_per_second === null)
      return b.tokens_per_second === null ? a.name.localeCompare(b.name) : 1
    if (b.tokens_per_second === null) return -1
    return b.tokens_per_second - a.tokens_per_second || a.name.localeCompare(b.name)
  })
}
export function infoNumber(value: number | null | undefined, locale: string, digits = 1): string {
  return value == null
    ? '—'
    : numberFormatter(locale, { maximumFractionDigits: digits }).format(value)
}
export function infoUSD(value: number | null | undefined, locale: string): string {
  return value == null
    ? '—'
    : numberFormatter(locale, {
        style: 'currency',
        currency: 'USD',
        maximumFractionDigits: 3,
      }).format(value)
}
export function infoTime(value: number | null, locale: string): string {
  return value === null
    ? '—'
    : dateFormatter(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(value)
}
export function quotaValue(value: number | null, metric: QuotaMetric, locale: string): string {
  if (value === null) return '—'
  return metric === 'api_value_usd'
    ? infoUSD(value, locale)
    : numberFormatter(locale, { notation: 'compact', maximumFractionDigits: 1 }).format(value)
}
