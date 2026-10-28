<script setup lang="ts">
import { Radar } from '@lucide/vue'
import { useQuery } from '@tanstack/vue-query'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { getModelInfo, modelInfoKey } from '@modern/api/model-info'
import { usePageRefresh } from '@modern/app/page-refresh'
import {
  AppBadge,
  AppButton,
  AppCollectionState,
  AppIcon,
  AppNotice,
  AppPanel,
} from '@modern/components/ui'
import { useLoadingActivity } from '@modern/components/ui/loading'
import { useApiClient } from '@shared/http/client-context'
import IntelligenceMatrix from './IntelligenceMatrix.vue'
import ParetoChart from './ParetoChart.vue'
import QuotaRadar from './QuotaRadar.vue'
import SectionStatus from './SectionStatus.vue'
import SpeedModels from './SpeedModels.vue'

const { t } = useI18n()
const client = useApiClient()
const query = useQuery({
  queryKey: modelInfoKey,
  // TanStack retains failed reads; an explicit unavailable/null report replaces expired data.
  queryFn: ({ signal }) => getModelInfo(client, signal),
})
const report = computed(() => query.data.value)
async function refresh(): Promise<void> {
  await query.refetch()
}
usePageRefresh({
  refresh,
  pending: query.isFetching,
  updatedAt: computed(() => query.dataUpdatedAt.value || undefined),
  autoRefresh: async () => {
    const result = await query.refetch()
    return !result.isError && result.data?.speed.status === 'ready'
  },
})
useLoadingActivity(query.isFetching)
</script>

<template>
  <div class="model-info-page">
    <header class="model-info-intro">
      <div class="model-info-intro-mark" aria-hidden="true">
        <AppIcon :icon="Radar" size="lg" />
      </div>
      <div class="model-info-intro-copy">
        <span class="model-info-eyebrow">{{ t('modelInfo.eyebrow') }}</span>
        <h1>{{ t('modelInfo.heading') }}</h1>
        <p>{{ t('modelInfo.description') }}</p>
      </div>
      <AppBadge tone="brand" dot>{{ t('modelInfo.publicSample') }}</AppBadge>
    </header>
    <AppCollectionState
      v-if="!report && query.isPending.value"
      :title="t('modelInfo.loading')"
      loading
    />
    <AppCollectionState
      v-else-if="!report"
      :title="t('modelInfo.failed')"
      :description="t('modelInfo.failedHelp')"
      error
      ><AppButton :loading="query.isFetching.value" @click="refresh">{{
        t('modelInfo.retry')
      }}</AppButton></AppCollectionState
    >
    <template v-else>
      <AppNotice v-if="query.isError.value" tone="warning">{{
        t('modelInfo.refreshFailed')
      }}</AppNotice>
      <AppPanel :title="t('modelInfo.intelligence')" :description="t('modelInfo.intelligenceHelp')">
        <template #actions
          ><AppBadge v-if="report.intelligence.data?.batch" variant="outline" size="xs">{{
            t('modelInfo.batch', { batch: report.intelligence.data.batch })
          }}</AppBadge
          ><AppBadge v-if="report.intelligence.data?.tested_at" variant="outline" size="xs">{{
            t('modelInfo.tested', { date: report.intelligence.data.tested_at })
          }}</AppBadge></template
        >
        <SectionStatus :section="report.intelligence" :read-failed="query.isError.value" />
        <IntelligenceMatrix v-if="report.intelligence.data" :data="report.intelligence.data" />
      </AppPanel>
      <AppPanel :title="t('modelInfo.pareto')" :description="t('modelInfo.paretoHelp')">
        <ParetoChart v-if="report.intelligence.data" :data="report.intelligence.data" />
        <SectionStatus v-else :section="report.intelligence" :read-failed="query.isError.value" />
      </AppPanel>
      <AppPanel
        :title="t('modelInfo.speed')"
        :description="
          report.speed.data
            ? t('modelInfo.speedHelp', { minutes: report.speed.data.window_minutes })
            : undefined
        "
      >
        <SectionStatus :section="report.speed" :read-failed="query.isError.value" />
        <SpeedModels v-if="report.speed.data" :data="report.speed.data" />
      </AppPanel>
      <AppPanel :title="t('modelInfo.quota')" :description="t('modelInfo.quotaHelp')">
        <SectionStatus :section="report.quota" :read-failed="query.isError.value" />
        <QuotaRadar v-if="report.quota.data" :data="report.quota.data" />
      </AppPanel>
    </template>
    <footer class="model-info-footer">
      <span>{{ t('modelInfo.source') }}</span
      ><span>{{ t('modelInfo.scope') }}</span>
    </footer>
  </div>
</template>

<style scoped>
.model-info-page {
  display: grid;
  gap: var(--modern-space-5);
  padding-top: var(--modern-space-6);
  padding-bottom: var(--modern-space-4);
  min-width: 0;
}
.model-info-intro {
  display: flex;
  align-items: center;
  gap: var(--modern-space-4);
}
.model-info-intro-mark {
  display: flex;
  align-items: center;
  justify-content: center;
  width: var(--modern-channel-hero);
  height: var(--modern-channel-hero);
  border: var(--modern-line-width) solid var(--modern-segmented-active-border);
  border-radius: var(--modern-radius-dialog);
  background: var(--modern-accent-soft);
  color: var(--modern-accent);
}
.model-info-intro-copy {
  flex: 1;
  min-width: 0;
}
.model-info-eyebrow {
  color: var(--modern-accent);
  font-size: var(--modern-font-size-caption);
  font-weight: var(--modern-weight-semibold);
  letter-spacing: var(--modern-tracking-label);
}
.model-info-intro h1 {
  margin-top: var(--modern-space-1);
  font-size: var(--modern-font-size-title);
  font-weight: var(--modern-weight-semibold);
  letter-spacing: var(--modern-tracking-title);
  line-height: var(--modern-leading-title);
}
.model-info-intro p {
  margin-top: var(--modern-space-2);
  color: var(--modern-muted);
  font-size: var(--modern-font-size-secondary);
}
.model-info-footer {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  gap: var(--modern-space-2) var(--modern-space-6);
  color: var(--modern-muted);
  font-size: var(--modern-font-size-caption);
}
@media (max-width: 760px) {
  .model-info-page {
    padding-top: var(--modern-space-4);
    gap: var(--modern-space-4);
  }
  .model-info-intro {
    flex-wrap: wrap;
    gap: var(--modern-space-3);
  }
  .model-info-intro-copy {
    flex-basis: calc(100% - var(--modern-channel-hero) - var(--modern-space-3));
  }
  .model-info-intro h1 {
    font-size: var(--modern-font-size-section);
  }
}
</style>
