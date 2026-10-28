<script setup lang="ts">
import { Search } from '@lucide/vue'
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ModelSpeed, SpeedModel } from '@modern/api/model-info'
import {
  AppBadge,
  AppButton,
  AppCollectionState,
  AppSparkline,
  AppTextField,
} from '@modern/components/ui'
import { infoNumber, infoTime, matchesModel, sortSpeedModels } from './data'

const props = defineProps<{ data: ModelSpeed }>()
const { t, locale } = useI18n()
const search = ref('')
const models = computed(() =>
  sortSpeedModels(props.data.models.filter((model) => matchesModel(model.name, search.value))),
)
function historyLabels(model: SpeedModel): string[] {
  return model.history.map(
    (point) =>
      `${infoTime(point.at_ms, locale.value)} · ${point.value === null ? t('modelInfo.unknown') : infoNumber(point.value, locale.value) + ' ' + t('modelInfo.speedUnit')}`,
  )
}
</script>

<template>
  <div class="model-speed-toolbar">
    <AppTextField
      v-model="search"
      type="search"
      :icon="Search"
      :label="t('modelInfo.search')"
      :placeholder="t('modelInfo.searchPlaceholder')"
      label-hidden
      class="model-speed-search"
    />
    <AppBadge variant="outline">{{
      t('modelInfo.modelsCount', { count: models.length })
    }}</AppBadge>
  </div>
  <AppCollectionState
    v-if="!models.length"
    :title="t(search ? 'modelInfo.noMatches' : 'modelInfo.empty')"
  >
    <AppButton v-if="search" @click="search = ''">{{ t('modelInfo.clear') }}</AppButton>
  </AppCollectionState>
  <div v-else class="model-speed-grid">
    <article
      v-for="model in models"
      :key="model.id"
      class="model-speed-card"
      :aria-label="model.name"
    >
      <div class="model-speed-heading">
        <h3>{{ model.name }}</h3>
        <AppBadge v-if="model.tokens_per_second === null" size="xs">{{
          t('modelInfo.unknown')
        }}</AppBadge>
      </div>
      <div class="model-speed-reading">
        <strong>{{ infoNumber(model.tokens_per_second, locale) }}</strong
        ><span
          >{{ t('modelInfo.speedUnit') }}<small>{{ t('modelInfo.meanSpeed') }}</small></span
        >
      </div>
      <AppSparkline
        v-if="model.history.some((point) => point.value !== null)"
        :label="model.name + ' · ' + t('modelInfo.trend')"
        :values="model.history.map((point) => point.value)"
        :point-labels="historyLabels(model)"
        show-isolated-points
        tone="accent"
        size="sm"
      />
      <div v-else class="model-speed-no-trend">
        {{ t('modelInfo.trend') }} · {{ t('modelInfo.unknown') }}
      </div>
      <dl class="model-speed-counts">
        <div>
          <dt>{{ t('modelInfo.requests') }}</dt>
          <dd>{{ infoNumber(model.requests, locale, 0) }}</dd>
        </div>
        <div>
          <dt>{{ t('modelInfo.devices') }}</dt>
          <dd>{{ infoNumber(model.devices, locale, 0) }}</dd>
        </div>
      </dl>
      <div class="model-speed-harnesses">
        <span>{{ t('modelInfo.harnesses') }}</span>
        <div v-if="model.harnesses.length">
          <AppBadge
            v-for="harness in model.harnesses"
            :key="harness.name"
            size="xs"
            variant="outline"
            >{{ harness.name }} · {{ infoNumber(harness.requests, locale, 0) }}</AppBadge
          >
        </div>
        <p v-else>{{ t('modelInfo.noHarnesses') }}</p>
      </div>
    </article>
  </div>
</template>

<style scoped>
.model-speed-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--modern-space-3);
  margin-bottom: var(--modern-space-4);
}
.model-speed-search {
  flex: 1 1 220px;
  max-width: 400px;
  min-width: 0;
}
.model-speed-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 260px), 1fr));
  gap: var(--modern-space-3);
}
.model-speed-card {
  min-width: 0;
  padding: var(--modern-space-4);
  border: var(--modern-line-width) solid var(--modern-border);
  border-radius: var(--modern-radius-control);
  background: var(--modern-surface);
}
.model-speed-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--modern-space-2);
}
.model-speed-heading h3 {
  font-size: var(--modern-font-size-secondary);
  font-weight: var(--modern-weight-semibold);
  overflow-wrap: anywhere;
}
.model-speed-reading {
  display: flex;
  align-items: center;
  gap: var(--modern-space-2);
  margin-block: var(--modern-space-3);
  font-variant-numeric: tabular-nums;
}
.model-speed-reading strong {
  font-size: var(--modern-font-size-title);
  font-weight: var(--modern-weight-semibold);
  color: var(--modern-accent);
  letter-spacing: var(--modern-tracking-title);
}
.model-speed-reading > span {
  font-size: var(--modern-font-size-small);
  color: var(--modern-muted);
}
.model-speed-reading small {
  display: block;
  font-size: var(--modern-font-size-caption);
}
.model-speed-no-trend {
  display: flex;
  align-items: center;
  height: var(--modern-trend-sm-height);
  color: var(--modern-muted);
  font-size: var(--modern-font-size-caption);
}
.model-speed-counts {
  display: flex;
  gap: var(--modern-space-6);
  margin: var(--modern-space-3) 0 0;
  padding-top: var(--modern-space-3);
  border-top: var(--modern-line-width) solid var(--modern-border);
}
.model-speed-counts > div {
  display: flex;
  align-items: baseline;
  gap: var(--modern-space-2);
}
.model-speed-counts dt {
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
}
.model-speed-counts dd {
  margin: 0;
  font-weight: var(--modern-weight-semibold);
  font-variant-numeric: tabular-nums;
}
.model-speed-harnesses {
  margin-top: var(--modern-space-3);
}
.model-speed-harnesses > span,
.model-speed-harnesses p {
  color: var(--modern-muted);
  font-size: var(--modern-font-size-caption);
}
.model-speed-harnesses > div {
  display: flex;
  flex-wrap: wrap;
  gap: var(--modern-space-1-5);
  margin-top: var(--modern-space-1);
}
@media (max-width: 760px) {
  .model-speed-search {
    max-width: none;
  }
}
</style>
