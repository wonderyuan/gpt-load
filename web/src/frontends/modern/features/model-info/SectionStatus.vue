<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ModelInfoStatus } from '@modern/api/model-info'
import { AppBadge, AppNotice } from '@modern/components/ui'
import { infoTime } from './data'

const props = defineProps<{
  section: {
    status: ModelInfoStatus
    updated_at_ms: number | null
    fetched_at_ms: number | null
    data: unknown
  }
  readFailed?: boolean
}>()
const { t, locale } = useI18n()
const status = computed(() =>
  props.readFailed && props.section.data !== null ? 'stale' : props.section.status,
)
</script>

<template>
  <div class="model-info-section-status">
    <div class="model-info-section-times">
      <AppBadge
        :tone="status === 'ready' ? 'success' : status === 'stale' ? 'warning' : 'neutral'"
        dot
        size="xs"
      >
        {{ t('modelInfo.status.' + status) }}
      </AppBadge>
      <span>{{ t('modelInfo.updated', { time: infoTime(section.updated_at_ms, locale) }) }}</span>
      <span>{{ t('modelInfo.fetched', { time: infoTime(section.fetched_at_ms, locale) }) }}</span>
    </div>
    <AppNotice v-if="status !== 'ready'" :tone="status === 'stale' ? 'warning' : 'info'" inline>
      {{ t('modelInfo.' + (status === 'stale' ? 'stale' : 'unavailable')) }}
    </AppNotice>
  </div>
</template>

<style scoped>
.model-info-section-status {
  display: grid;
  gap: var(--modern-space-3);
  margin-bottom: var(--modern-space-4);
}
.model-info-section-times {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--modern-space-2) var(--modern-space-4);
  color: var(--modern-muted);
  font-size: var(--modern-font-size-caption);
}
</style>
