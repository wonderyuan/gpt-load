<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ModelIntelligence, ParetoPoint } from '@modern/api/model-info'
import {
  AppBadge,
  AppButton,
  AppCheckbox,
  AppCollectionState,
  AppSegmentedControl,
  AppSelect,
  AppSvg,
} from '@modern/components/ui'
import {
  filterParetoPoints,
  frontierPath,
  infoNumber,
  infoUSD,
  paretoDefaultScale,
  paretoFrontier,
  paretoModels,
  paretoTrajectories,
  reconcileHiddenModels,
  scatterLabels,
  scatterLayout,
  type ParetoMetric,
  type ParetoModelLegend,
  type ParetoScale,
} from './data'

const props = defineProps<{ data: ModelIntelligence }>()
const { t, locale } = useI18n()
const host = ref<HTMLElement>()
const width = ref(960)
const metric = ref<ParetoMetric>('cost_usd')
const scale = ref<ParetoScale>('log')
const effort = ref('')
const selected = ref('')
const hovered = ref('')
const focused = ref('')
const showFrontier = ref(true)
const showTrajectories = ref(true)
const hoveredModel = ref('')
const focusedModel = ref('')
let textMeasure: CanvasRenderingContext2D | null = null
const measureReady = ref(false)
const models = ref<ParetoModelLegend[]>([])
const hidden = ref(new Set<string>())
const helpId = useId()
const detailId = useId()
const tooltipId = useId()
let observer: ResizeObserver | undefined
onMounted(() => {
  textMeasure = document.createElement('canvas').getContext('2d')
  if (textMeasure && host.value) {
    const style = getComputedStyle(host.value)
    textMeasure.font = `12px ${style.fontFamily}`
    measureReady.value = true
  }
  observer = new ResizeObserver(([entry]) => {
    if (entry?.contentRect.width) width.value = Math.round(entry.contentRect.width)
  })
  if (host.value) observer.observe(host.value)
})
onBeforeUnmount(() => observer?.disconnect())
watch(
  () => props.data,
  (data) => {
    models.value = paretoModels(data.pareto, models.value)
    hidden.value = reconcileHiddenModels(models.value, hidden.value)
    if (!data.efforts.includes(effort.value)) effort.value = ''
  },
  { immediate: true },
)
const metricKey = computed(() =>
  metric.value === 'cost_usd'
    ? 'cost'
    : metric.value === 'duration_minutes'
      ? 'duration'
      : 'composite',
)
const metricOptions = computed(() => [
  { value: 'cost_usd', label: t('modelInfo.costShort') },
  { value: 'duration_minutes', label: t('modelInfo.durationShort') },
  { value: 'composite_cost', label: t('modelInfo.compositeShort') },
])
const scaleOptions = computed(() => [
  { value: 'log', label: t('modelInfo.logScale') },
  { value: 'linear', label: t('modelInfo.linearScale') },
])
const effortOptions = computed(() => [
  { value: '', label: t('modelInfo.allEfforts') },
  ...props.data.efforts.map((value) => ({ value, label: value })),
])
const filtered = computed(() => filterParetoPoints(props.data.pareto, hidden.value, effort.value))
const layout = computed(() =>
  scatterLayout(filtered.value, metric.value, props.data.max_score, scale.value, {
    width: width.value,
    domainPoints: props.data.pareto,
  }),
)
const frontier = computed(() => paretoFrontier(filtered.value, metric.value))
const frontierLine = computed(() => frontierPath(layout.value, frontier.value))
const trajectories = computed(() => paretoTrajectories(layout.value.points, props.data.efforts))
const colors = computed(() => new Map(models.value.map((model) => [model.id, model.color])))
const shapes = computed(() => new Map(models.value.map((model) => [model.id, model.shape])))
const emphasizedModel = computed(() => {
  const id = hoveredModel.value || focusedModel.value
  return hidden.value.has(id) ? '' : id
})
const best = computed(() =>
  layout.value.points.reduce<(typeof layout.value.points)[number] | undefined>(
    (peak, point) => (!peak || point.score > peak.score ? point : peak),
    undefined,
  ),
)
const active = computed(
  () => layout.value.points.find((point) => point.id === selected.value) ?? best.value,
)
const labels = computed(() =>
  scatterLabels(layout.value, {
    frontier: showFrontier.value ? frontier.value : undefined,
    selected: active.value?.id,
    focusedModel: emphasizedModel.value,
    measure: measureReady.value ? (text) => textMeasure!.measureText(text).width * 1.06 : undefined,
  }),
)
function isDimmed(modelId: string): boolean {
  return Boolean(emphasizedModel.value && emphasizedModel.value !== modelId)
}
const inspected = computed(() =>
  layout.value.points.find((point) => point.id === (hovered.value || focused.value)),
)
const tooltipPosition = computed(() => {
  const point = inspected.value
  if (!point) return undefined
  return {
    left: Math.max(8, Math.min(layout.value.width - 288, point.x + 14)) + 'px',
    top: Math.max(8, Math.min(layout.value.height - 180, point.y - 145)) + 'px',
  }
})
watch([filtered, metric, scale], () => {
  hovered.value = ''
  focused.value = ''
})
function setMetric(value: string): void {
  if (value === 'cost_usd' || value === 'duration_minutes' || value === 'composite_cost') {
    metric.value = value
    scale.value = paretoDefaultScale(value)
  }
}
function setScale(value: string): void {
  scale.value = value === 'linear' ? 'linear' : 'log'
}
function toggleModel(id: string): void {
  const next = new Set(hidden.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  hidden.value = next
}
function clearFilters(): void {
  hidden.value = new Set()
  effort.value = ''
}
function metricLabel(value: number): string {
  return metric.value === 'cost_usd'
    ? infoUSD(value, locale.value)
    : infoNumber(value, locale.value, 3)
}
function pointSummary(point: ParetoPoint): string {
  return [
    `${point.model} · ${point.effort}`,
    `${t('modelInfo.score')} ${infoNumber(point.score, locale.value, 2)} / ${infoNumber(props.data.max_score, locale.value)}`,
    `${t('modelInfo.cost')} ${infoUSD(point.cost_usd, locale.value)}`,
    `${t('modelInfo.duration')} ${infoNumber(point.duration_minutes, locale.value, 2)}`,
    `${t('modelInfo.composite')} ${infoNumber(point.composite_cost, locale.value, 3)}`,
  ].join('\n')
}
function selectPoint(id: string): void {
  selected.value = id
}
function focusPoint(id: string): void {
  selected.value = id
  focused.value = id
}
function hoverPoint(id: string): void {
  hovered.value = id
}
function clearHover(): void {
  hovered.value = ''
}
function clearFocus(): void {
  focused.value = ''
}
async function navigate(event: KeyboardEvent, index: number): Promise<void> {
  if (event.altKey || event.ctrlKey || event.metaKey) return
  let next = index
  if (event.key === 'ArrowRight' || event.key === 'ArrowDown') next++
  else if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') next--
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = layout.value.points.length - 1
  else if (event.key === 'Escape') {
    clearHover()
    clearFocus()
    return
  } else if (event.key === 'Enter' || event.key === ' ') {
    event.preventDefault()
    selectPoint(layout.value.points[index]!.id)
    return
  } else return
  event.preventDefault()
  next = (next + layout.value.points.length) % layout.value.points.length
  selected.value = layout.value.points[next]!.id
  const svg = (event.currentTarget as SVGCircleElement).ownerSVGElement
  await nextTick()
  svg?.querySelectorAll<SVGCircleElement>('[data-model-point]')[next]?.focus()
}
</script>

<template>
  <div ref="host" class="model-pareto">
    <div class="model-pareto-toolbar">
      <AppSegmentedControl
        :model-value="metric"
        :options="metricOptions"
        :label="t('modelInfo.metric')"
        @update:model-value="setMetric"
      />
      <AppSegmentedControl
        :model-value="scale"
        :options="scaleOptions"
        :label="t('modelInfo.scale')"
        size="sm"
        @update:model-value="setScale"
      />
      <AppSelect
        v-model="effort"
        :options="effortOptions"
        :label="t('modelInfo.effort')"
        label-hidden
        class="model-pareto-effort"
      />
      <div class="model-pareto-toggles">
        <AppCheckbox v-model="showFrontier" :label="t('modelInfo.frontier')" />
        <AppCheckbox v-model="showTrajectories" :label="t('modelInfo.effortTrajectories')" />
      </div>
    </div>
    <div class="model-pareto-legend" role="group" :aria-label="t('modelInfo.modelVisibility')">
      <AppButton
        v-for="model in models"
        :key="model.id"
        size="xs"
        variant="ghost"
        class="model-pareto-model"
        :class="{ 'is-hidden': hidden.has(model.id) }"
        :aria-pressed="!hidden.has(model.id)"
        :style="{ '--modern-pareto-color': model.color }"
        @click="toggleModel(model.id)"
        @pointerenter="hoveredModel = model.id"
        @pointerleave="hoveredModel = ''"
        @focus="focusedModel = model.id"
        @blur="focusedModel = ''"
      >
        <AppSvg
          viewBox="-7 -7 14 14"
          width="14"
          height="14"
          class="model-pareto-swatch"
          aria-hidden="true"
        >
          <path :d="model.shape" /> </AppSvg
        >{{ model.name }}
      </AppButton>
      <AppButton v-if="hidden.size || effort" size="xs" variant="text" @click="clearFilters">
        {{ t('modelInfo.clear') }}
      </AppButton>
    </div>
    <AppCollectionState
      v-if="!filtered.length"
      :title="t(hidden.size || effort ? 'modelInfo.noMatches' : 'modelInfo.empty')"
    >
      <AppButton v-if="hidden.size || effort" @click="clearFilters">{{
        t('modelInfo.clear')
      }}</AppButton>
    </AppCollectionState>
    <template v-else>
      <div class="model-pareto-caption">
        <strong>{{ t('modelInfo.upperLeftBetter') }}</strong>
        <span v-if="best"
          >{{ t('modelInfo.bestScore') }} {{ best.model }} · {{ best.effort }} ·
          {{ infoNumber(best.score, locale, 2) }}</span
        >
        <span
          >{{ t('modelInfo.frontierCount', { count: frontier.size }) }} ·
          {{ t('modelInfo.pointsCount', { count: filtered.length }) }}</span
        >
      </div>
      <p v-if="scale === 'log' && layout.scale === 'linear'" class="model-pareto-help">
        {{ t('modelInfo.zeroScale') }}
      </p>
      <div class="model-pareto-scroll">
        <div class="model-pareto-plot" :style="{ width: layout.width + 'px' }">
          <AppSvg
            :viewBox="`0 0 ${layout.width} ${layout.height}`"
            :width="layout.width"
            :height="layout.height"
            class="model-pareto-svg"
            role="group"
            :aria-label="t('modelInfo.pareto')"
            :aria-describedby="helpId"
          >
            <g v-for="tick in layout.yTicks" :key="tick.value" aria-hidden="true">
              <line
                :x1="layout.frame.left"
                :x2="layout.frame.right"
                :y1="tick.y"
                :y2="tick.y"
                class="model-pareto-grid"
              />
              <text
                :x="layout.frame.left - 10"
                :y="tick.y + 4"
                text-anchor="end"
                class="model-pareto-tick"
                >{{ infoNumber(tick.value, locale) }}</text
              >
            </g>
            <g v-for="tick in layout.ticks" :key="tick.value" aria-hidden="true">
              <line
                :x1="tick.x"
                :x2="tick.x"
                :y1="layout.frame.top"
                :y2="layout.frame.bottom"
                class="model-pareto-grid model-pareto-grid--vertical"
              />
              <text
                :x="tick.x"
                :y="layout.frame.bottom + 22"
                text-anchor="middle"
                class="model-pareto-tick"
                >{{ metricLabel(tick.value) }}</text
              >
            </g>
            <text :x="layout.frame.left" y="18" class="model-pareto-axis">
              {{ t('modelInfo.scoreDirection') }} ·
              {{ t('modelInfo.maximum', { score: data.max_score }) }}
            </text>
            <text
              :x="(layout.frame.left + layout.frame.right) / 2"
              :y="layout.height - 9"
              text-anchor="middle"
              class="model-pareto-axis"
            >
              {{ t('modelInfo.' + metricKey + 'Direction') }} · {{ t('modelInfo.' + metricKey) }} ·
              {{ t(layout.scale === 'log' ? 'modelInfo.logScale' : 'modelInfo.linearScale') }}
            </text>
            <g v-if="showTrajectories" aria-hidden="true">
              <polyline
                v-for="track in trajectories.filter((track) => track.points.length > 1)"
                :key="track.model_id"
                :points="track.points.map((point) => `${point.x},${point.y}`).join(' ')"
                :style="{ color: colors.get(track.model_id) }"
                class="model-pareto-track"
                :class="{
                  'is-dimmed': isDimmed(track.model_id),
                  'is-emphasized': emphasizedModel === track.model_id,
                }"
              />
            </g>
            <path
              v-if="showFrontier && frontier.size > 1"
              :d="frontierLine"
              class="model-pareto-frontier"
              aria-hidden="true"
            />
            <g class="model-pareto-labels" aria-hidden="true">
              <text
                v-for="item in labels"
                :key="item.id"
                :x="item.x + 3"
                :y="item.y + 12"
                :style="{ color: colors.get(item.model_id) }"
                class="model-pareto-label"
                :class="{ 'is-important': item.important, 'is-dimmed': isDimmed(item.model_id) }"
                >{{ item.text }}</text
              >
            </g>
            <g
              v-for="(point, index) in layout.points"
              :key="point.id"
              :style="{ color: colors.get(point.model_id) }"
              :class="{ 'is-dimmed': isDimmed(point.model_id) }"
            >
              <circle
                v-if="showFrontier && frontier.has(point.id)"
                :cx="point.x"
                :cy="point.y"
                r="5.5"
                class="model-pareto-frontier-ring"
                aria-hidden="true"
              />
              <circle
                v-if="active?.id === point.id"
                :cx="point.x"
                :cy="point.y"
                r="7"
                class="model-pareto-selected-ring"
                aria-hidden="true"
              />
              <path
                :d="shapes.get(point.model_id)"
                :transform="`translate(${point.x},${point.y})`"
                class="model-pareto-point"
                aria-hidden="true"
              />
              <circle
                :cx="point.x"
                :cy="point.y"
                r="10"
                role="button"
                data-model-point
                :tabindex="active?.id === point.id ? 0 : -1"
                :aria-label="pointSummary(point)"
                :aria-pressed="active?.id === point.id"
                :aria-controls="detailId"
                :aria-describedby="inspected?.id === point.id ? tooltipId : undefined"
                class="model-pareto-hit"
                @click="selectPoint(point.id)"
                @pointerenter="hoverPoint(point.id)"
                @pointerleave="clearHover"
                @focus="focusPoint(point.id)"
                @blur="clearFocus"
                @keydown="navigate($event, index)"
              />
            </g>
          </AppSvg>
          <div
            v-if="inspected"
            :id="tooltipId"
            role="tooltip"
            class="model-pareto-tooltip"
            :style="tooltipPosition"
          >
            {{ pointSummary(inspected) }}
          </div>
        </div>
      </div>
      <div :id="detailId" class="model-pareto-detail" aria-live="polite" aria-atomic="true">
        <template v-if="active">
          <span class="model-pareto-kicker">{{ t('modelInfo.selectedPoint') }}</span>
          <strong :style="{ color: colors.get(active.model_id) }">{{ active.model }}</strong>
          <AppBadge size="xs">{{ active.effort }}</AppBadge>
          <AppBadge v-if="frontier.has(active.id)" tone="success" size="xs">
            {{ t('modelInfo.frontier') }}
          </AppBadge>
          <dl>
            <div>
              <dt>{{ t('modelInfo.score') }}</dt>
              <dd>{{ infoNumber(active.score, locale, 2) }}</dd>
            </div>
            <div :class="{ 'is-active': metric === 'cost_usd' }">
              <dt>{{ t('modelInfo.cost') }}</dt>
              <dd>{{ infoUSD(active.cost_usd, locale) }}</dd>
            </div>
            <div :class="{ 'is-active': metric === 'duration_minutes' }">
              <dt>{{ t('modelInfo.duration') }}</dt>
              <dd>{{ infoNumber(active.duration_minutes, locale, 2) }}</dd>
            </div>
            <div :class="{ 'is-active': metric === 'composite_cost' }">
              <dt>{{ t('modelInfo.composite') }}</dt>
              <dd>{{ infoNumber(active.composite_cost, locale, 3) }}</dd>
            </div>
          </dl>
        </template>
      </div>
      <p :id="helpId" class="model-pareto-help">{{ t('modelInfo.plotHelp') }}</p>
    </template>
  </div>
</template>

<style scoped>
.model-pareto {
  min-width: 0;
}
.model-pareto-toolbar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--modern-space-3);
}
.model-pareto-effort {
  min-width: 160px;
}
.model-pareto-toggles {
  display: flex;
  flex-wrap: wrap;
  gap: var(--modern-space-4);
  margin-left: auto;
}
.model-pareto-legend {
  display: flex;
  flex-wrap: wrap;
  gap: var(--modern-space-1) var(--modern-space-2);
  margin: var(--modern-space-3) 0 var(--modern-space-2);
}
.model-pareto-model {
  --modern-pareto-color: var(--modern-chart-input);
  font-weight: var(--modern-weight-regular);
}
.model-pareto-swatch {
  flex-shrink: 0;
  fill: var(--modern-pareto-color);
}
.model-pareto-model.is-hidden {
  text-decoration: line-through;
  opacity: var(--modern-opacity-disabled);
}
.model-pareto-caption {
  display: flex;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: var(--modern-space-2);
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
}
.model-pareto-scroll {
  max-width: 100%;
  overflow-x: auto;
}
.model-pareto-plot {
  position: relative;
}
.model-pareto-svg {
  display: block;
  max-width: none;
}
.model-pareto-grid {
  stroke: var(--modern-border);
  stroke-width: var(--modern-line-width);
}
.model-pareto-grid--vertical {
  stroke-dasharray: 2 5;
}
.model-pareto-tick,
.model-pareto-axis {
  fill: var(--modern-muted);
  font-size: var(--modern-font-size-small);
  font-variant-numeric: tabular-nums;
}
.model-pareto-frontier {
  fill: none;
  stroke: var(--modern-muted);
  stroke-width: var(--modern-line-width);
  stroke-dasharray: 3 4;
}
.model-pareto-track {
  fill: none;
  stroke: currentColor;
  stroke-width: var(--modern-line-width);
  stroke-linejoin: round;
  stroke-linecap: round;
  opacity: var(--modern-opacity-quiet);
}
.model-pareto-track.is-emphasized {
  stroke-width: var(--modern-focus-width);
  opacity: 1;
}
.model-pareto .is-dimmed {
  opacity: calc(var(--modern-opacity-disabled) * 0.4);
}
.model-pareto-labels {
  pointer-events: none;
}
.model-pareto-label {
  fill: currentColor;
  font-size: var(--modern-font-size-small);
  font-weight: var(--modern-weight-regular);
  paint-order: stroke;
  stroke: var(--modern-surface);
  stroke-width: var(--modern-space-1);
  stroke-linejoin: round;
}
.model-pareto-label.is-important {
  font-weight: var(--modern-weight-semibold);
}
.model-pareto-point {
  fill: currentColor;
  stroke: var(--modern-surface);
  stroke-width: var(--modern-line-width);
  pointer-events: none;
}
.model-pareto-frontier-ring,
.model-pareto-selected-ring {
  fill: none;
  stroke: currentColor;
  stroke-width: var(--modern-line-width);
  pointer-events: none;
}
.model-pareto-selected-ring {
  stroke: var(--modern-text);
}
.model-pareto-hit {
  fill: transparent;
  cursor: pointer;
}
.model-pareto-hit:focus-visible {
  outline: none;
  stroke: var(--modern-accent);
  stroke-width: var(--modern-focus-width);
}
.model-pareto-tooltip {
  position: absolute;
  z-index: var(--modern-layer-tooltip);
  width: 280px;
  border: var(--modern-line-width) solid var(--modern-tooltip-border);
  border-radius: var(--modern-radius-control);
  background: var(--modern-tooltip-surface);
  padding: var(--modern-space-2) var(--modern-space-3);
  color: var(--modern-text);
  font-size: var(--modern-font-size-small);
  line-height: var(--modern-leading-compact);
  white-space: pre-line;
  overflow-wrap: anywhere;
  box-shadow: var(--modern-shadow-tooltip);
  pointer-events: none;
}
.model-pareto-help {
  color: var(--modern-muted);
  font-size: var(--modern-font-size-caption);
  margin-top: var(--modern-space-2);
}
.model-pareto-detail {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--modern-space-2) var(--modern-space-3);
  border-top: var(--modern-line-width) solid var(--modern-border);
  padding-top: var(--modern-space-3);
  font-size: var(--modern-font-size-small);
}
.model-pareto-kicker {
  color: var(--modern-muted);
}
.model-pareto-detail dl {
  display: flex;
  flex-wrap: wrap;
  gap: var(--modern-space-2) var(--modern-space-4);
  margin: 0;
}
.model-pareto-detail dl > div {
  display: inline-flex;
  gap: var(--modern-space-1-5);
}
.model-pareto-detail dt {
  color: var(--modern-muted);
}
.model-pareto-detail dd {
  margin: 0;
  font-weight: var(--modern-weight-semibold);
  font-variant-numeric: tabular-nums;
}
.model-pareto-detail .is-active dd {
  color: var(--modern-accent);
}
@media (max-width: 760px) {
  .model-pareto-toggles {
    margin-left: 0;
  }
  .model-pareto-detail dl {
    width: 100%;
  }
}
</style>
