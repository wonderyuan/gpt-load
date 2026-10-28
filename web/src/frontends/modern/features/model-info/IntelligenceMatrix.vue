<script setup lang="ts">
import { Search } from '@lucide/vue'
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ModelIntelligence } from '@modern/api/model-info'
import { AppBadge, AppButton, AppCollectionState, AppTextField } from '@modern/components/ui'
import { numberFormatter } from '@modern/components/ui/intl-formatters'
import { infoNumber, matchesModel } from './data'

const props = defineProps<{ data: ModelIntelligence }>()
const { t, locale } = useI18n()
const search = ref('')
const models = computed(() =>
  props.data.models
    .filter((model) => matchesModel(model.name, search.value))
    .map((model) => ({
      ...model,
      cells: props.data.efforts.map((effort) => ({
        effort,
        result: model.results.find((result) => result.effort === effort),
      })),
    })),
)
function band(score: number | null | undefined): string {
  return score == null
    ? 'unknown'
    : score / props.data.max_score >= 0.8
      ? 'high'
      : score / props.data.max_score >= 0.5
        ? 'mid'
        : 'low'
}
function cost(value: number | null | undefined): string {
  return value == null
    ? '—'
    : numberFormatter(locale.value, {
        style: 'currency',
        currency: 'USD',
        currencyDisplay: 'narrowSymbol',
        minimumFractionDigits: 2,
        maximumFractionDigits: 2,
      }).format(value)
}
function minutes(seconds: number | null | undefined): string {
  return seconds == null
    ? '—'
    : t('modelInfo.cellMinutes', { value: infoNumber(seconds / 60, locale.value, 2) })
}
</script>

<template>
  <div class="model-matrix-toolbar">
    <AppTextField
      v-model="search"
      type="search"
      :icon="Search"
      :label="t('modelInfo.search')"
      :placeholder="t('modelInfo.searchPlaceholder')"
      label-hidden
      class="model-matrix-search"
    />
    <AppBadge variant="outline">{{
      t('modelInfo.maximum', { score: infoNumber(data.max_score, locale) })
    }}</AppBadge>
    <span class="model-matrix-count">{{
      t('modelInfo.modelsCount', { count: models.length })
    }}</span>
  </div>
  <AppCollectionState
    v-if="!models.length"
    :title="t(search ? 'modelInfo.noMatches' : 'modelInfo.empty')"
  >
    <AppButton v-if="search" @click="search = ''">{{ t('modelInfo.clear') }}</AppButton>
  </AppCollectionState>
  <div
    v-else
    class="model-matrix-scroll"
    tabindex="0"
    role="region"
    :aria-label="t('modelInfo.intelligence')"
  >
    <table class="model-matrix">
      <caption class="modern-sr-only">
        {{
          t('modelInfo.intelligenceHelp')
        }}
      </caption>
      <thead>
        <tr>
          <th scope="col">{{ t('modelInfo.model') }}</th>
          <th v-for="effort in data.efforts" :key="effort" scope="col">{{ effort }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="model in models" :key="model.id">
          <th scope="row">{{ model.name }}</th>
          <td v-for="cell in model.cells" :key="cell.effort">
            <div
              v-if="
                cell.result &&
                (cell.result.score !== null ||
                  cell.result.cost_usd !== null ||
                  cell.result.duration_seconds !== null)
              "
              class="model-matrix-cell"
              :data-band="band(cell.result.score)"
            >
              <strong>{{ infoNumber(cell.result.score, locale, 2) }}</strong>
              <span class="model-matrix-cost">{{ cost(cell.result.cost_usd) }}</span>
              <span class="model-matrix-duration">{{ minutes(cell.result.duration_seconds) }}</span>
            </div>
            <span v-else class="model-matrix-missing" :aria-label="t('modelInfo.noResult')">—</span>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.model-matrix-toolbar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--modern-space-3);
  margin-bottom: var(--modern-space-4);
}
.model-matrix-search {
  flex: 1 1 220px;
  min-width: 0;
  max-width: 400px;
}
.model-matrix-count {
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
}
.model-matrix-scroll {
  overflow-x: auto;
  border: var(--modern-line-width) solid var(--modern-border);
  border-radius: var(--modern-radius-control);
}
.model-matrix {
  width: 100%;
  border-collapse: separate;
  border-spacing: 0;
  font-size: var(--modern-font-size-secondary);
}
.model-matrix th {
  padding: var(--modern-space-3);
  font-weight: var(--modern-weight-medium);
  white-space: nowrap;
}
.model-matrix thead th {
  color: var(--modern-muted);
  background: var(--modern-subtle);
  font-size: var(--modern-font-size-small);
  text-transform: uppercase;
}
.model-matrix th:first-child {
  position: sticky;
  left: 0;
  z-index: var(--modern-layer-raised);
  background: var(--modern-surface);
  min-width: 160px;
  max-width: 250px;
  white-space: normal;
  overflow-wrap: anywhere;
  border-right: var(--modern-line-width) solid var(--modern-border);
}
.model-matrix thead th:first-child {
  background: var(--modern-subtle);
}
.model-matrix td {
  padding: var(--modern-space-1);
  min-width: 142px;
}
.model-matrix tbody tr + tr > * {
  border-top: var(--modern-line-width) solid var(--modern-border);
}
.model-matrix-cell {
  display: grid;
  grid-template-columns: minmax(0, 1.25fr) minmax(0, 1fr);
  grid-template-rows: repeat(2, minmax(0, 1fr));
  min-height: 72px;
  text-align: center;
  border-radius: var(--modern-radius-small);
  font-variant-numeric: tabular-nums;
  overflow: hidden;
}
.model-matrix-cell strong {
  display: grid;
  align-items: center;
  grid-row: 1 / 3;
  padding: var(--modern-space-2) var(--modern-space-1);
  font-size: var(--modern-font-size-section);
  font-weight: var(--modern-weight-semibold);
}
.model-matrix-cell > span {
  display: grid;
  align-items: center;
  padding: var(--modern-space-1) var(--modern-space-0-5);
  border-left: var(--modern-line-width) solid color-mix(in srgb, currentColor 14%, transparent);
  color: var(--modern-muted);
  font-size: var(--modern-font-size-caption);
  white-space: nowrap;
}
.model-matrix-duration {
  border-top: var(--modern-line-width) solid color-mix(in srgb, currentColor 14%, transparent);
}
.model-matrix-cell[data-band='high'] {
  background: var(--modern-accent-soft);
  color: var(--modern-accent);
}
.model-matrix-cell[data-band='mid'] {
  background: var(--modern-badge-brand-surface);
  color: var(--modern-text);
}
.model-matrix-cell[data-band='low'] {
  background: var(--modern-subtle);
  color: var(--modern-muted);
}
.model-matrix-missing {
  display: block;
  text-align: center;
  color: var(--modern-muted);
}
@media (max-width: 760px) {
  .model-matrix th:first-child {
    min-width: 112px;
    max-width: 140px;
  }
  .model-matrix-search {
    max-width: none;
    flex-basis: 100%;
  }
}
</style>
