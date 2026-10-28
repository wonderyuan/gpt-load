<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ModelQuota } from '@modern/api/model-info'
import { AppBadge, AppCollectionState, AppSegmentedControl } from '@modern/components/ui'
import { infoUSD, quotaPeak, quotaPercent, quotaValue, type QuotaMetric } from './data'

const props = defineProps<{ data: ModelQuota }>()
const { t, locale } = useI18n()
const metric = ref<QuotaMetric>('api_value_usd')
const options = computed(() => [
  { value: 'api_value_usd', label: t('modelInfo.apiValue') },
  { value: 'weekly_tokens', label: t('modelInfo.weeklyTokens') },
])
const peak = computed(() => quotaPeak(props.data.plans, metric.value))
function change(value: string): void {
  if (value === 'api_value_usd' || value === 'weekly_tokens') metric.value = value
}
</script>

<template>
  <div class="model-quota-toolbar">
    <AppSegmentedControl
      :model-value="metric"
      :options="options"
      :label="t('modelInfo.quotaMetric')"
      @update:model-value="change"
    /><AppBadge v-if="data.recorded_at" variant="outline">{{
      t('modelInfo.recorded', { date: data.recorded_at })
    }}</AppBadge>
  </div>
  <AppCollectionState v-if="!data.plans.length" :title="t('modelInfo.empty')" />
  <template v-else>
    <div class="model-quota-grid">
      <article
        v-for="plan in data.plans"
        :key="plan.id"
        class="model-quota-plan"
        :aria-label="plan.name"
      >
        <header>
          <h3>{{ plan.name }}</h3>
          <div class="model-quota-monthly">
            <span>{{ t('modelInfo.monthly') }}</span
            ><strong>{{ infoUSD(plan.monthly_usd, locale) }}</strong>
          </div>
        </header>
        <p class="model-quota-unit">
          {{ t(metric === 'api_value_usd' ? 'modelInfo.apiValue' : 'modelInfo.weeklyTokens') }}
        </p>
        <div v-if="plan.models.length" class="model-quota-models">
          <div v-for="model in plan.models" :key="model.name" class="model-quota-model">
            <div class="model-quota-label">
              <span>{{ model.name }}</span
              ><strong>{{
                model[metric] === null
                  ? t('modelInfo.unknown')
                  : quotaValue(model[metric], metric, locale)
              }}</strong>
            </div>
            <div
              v-if="model[metric] !== null"
              class="model-quota-track"
              role="meter"
              :aria-label="
                model.name +
                ' · ' +
                t(metric === 'api_value_usd' ? 'modelInfo.apiValue' : 'modelInfo.weeklyTokens')
              "
              :aria-valuemin="0"
              :aria-valuemax="peak ?? undefined"
              :aria-valuenow="model[metric] ?? undefined"
              :aria-valuetext="quotaValue(model[metric], metric, locale)"
            >
              <span :style="{ width: (quotaPercent(model, metric, peak) ?? 0) + '%' }" />
            </div>
            <div v-else class="model-quota-unknown" aria-hidden="true" />
          </div>
        </div>
        <p v-else class="model-quota-empty">{{ t('modelInfo.empty') }}</p>
      </article>
    </div>
    <p class="model-quota-help">{{ t('modelInfo.quotaBars') }}</p>
  </template>
</template>

<style scoped>
.model-quota-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: var(--modern-space-3);
  margin-bottom: var(--modern-space-4);
}
.model-quota-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 250px), 1fr));
  gap: var(--modern-space-3);
}
.model-quota-plan {
  min-width: 0;
  padding: var(--modern-space-4);
  border: var(--modern-line-width) solid var(--modern-border);
  border-radius: var(--modern-radius-control);
  background: var(--modern-subtle);
}
.model-quota-plan header {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  align-items: flex-start;
  gap: var(--modern-space-3);
  padding-bottom: var(--modern-space-3);
  border-bottom: var(--modern-line-width) solid var(--modern-border);
}
.model-quota-plan h3 {
  font-size: var(--modern-font-size-section);
  font-weight: var(--modern-weight-semibold);
  overflow-wrap: anywhere;
}
.model-quota-monthly {
  text-align: right;
}
.model-quota-monthly span {
  display: block;
  color: var(--modern-muted);
  font-size: var(--modern-font-size-caption);
}
.model-quota-monthly strong {
  display: block;
  font-size: var(--modern-font-size-section);
  font-weight: var(--modern-weight-semibold);
  font-variant-numeric: tabular-nums;
}
.model-quota-unit {
  margin-top: var(--modern-space-3);
  color: var(--modern-accent);
  font-size: var(--modern-font-size-caption);
  font-weight: var(--modern-weight-medium);
}
.model-quota-models {
  display: grid;
  gap: var(--modern-space-4);
  margin-top: var(--modern-space-3);
}
.model-quota-label {
  display: flex;
  justify-content: space-between;
  gap: var(--modern-space-3);
  align-items: baseline;
  margin-bottom: var(--modern-space-1-5);
  font-size: var(--modern-font-size-small);
}
.model-quota-label > span {
  min-width: 0;
  overflow-wrap: anywhere;
}
.model-quota-label strong {
  flex: none;
  font-weight: var(--modern-weight-medium);
  font-variant-numeric: tabular-nums;
}
.model-quota-track,
.model-quota-unknown {
  height: var(--modern-progress-bar-sm);
  border-radius: var(--modern-radius-small);
  background: var(--modern-progress-track);
  overflow: hidden;
}
.model-quota-track > span {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: var(--modern-accent);
  transition: width var(--modern-motion-fast) var(--modern-motion-ease);
}
.model-quota-unknown {
  background: transparent;
  border: var(--modern-line-width) dashed var(--modern-border);
}
.model-quota-help,
.model-quota-empty {
  margin-top: var(--modern-space-4);
  color: var(--modern-muted);
  font-size: var(--modern-font-size-caption);
}
</style>
