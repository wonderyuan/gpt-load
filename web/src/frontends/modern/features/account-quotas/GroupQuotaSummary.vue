<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountQuotaItem } from '@modern/api/fork-account-quotas'
import type { CredentialQuota } from '@modern/api/credential-observation'
import { useClock } from '@modern/components/ui/clock'
import {
  credentialStatus,
  quotaRemaining,
  quotaWindowTitle,
  sortedQuotaWindows,
} from '@modern/features/groups/credential-presentation'
import { compactQuotaWindows, quotaWindowState, quotaWindowValuesAvailable } from './data'

const props = defineProps<{
  items?: readonly AccountQuotaItem[]
  loading: boolean
  failed: boolean
  partial: boolean
}>()
const { t, n, te } = useI18n()
const now = useClock()
const visible = computed(() => (props.items ?? []).slice(0, 2))
function windows(item: AccountQuotaItem): CredentialQuota[] {
  return sortedQuotaWindows(compactQuotaWindows(item.credential.observation?.windows ?? []))
}
function title(window: CredentialQuota): string {
  const key = 'credentialCards.quotaLabels.' + window.labelKey
  const label = quotaWindowTitle(window, window.labelKey && te(key) ? t(key) : window.label)
  const scope =
    window.scope === 'account'
      ? t('accountQuotas.accountScope')
      : window.models.join(', ') || window.scope || t('accountQuotas.modelScope')
  return `${label} · ${scope}`
}
function value(item: AccountQuotaItem, window: CredentialQuota): string {
  const observation = item.credential.observation
  if (!quotaWindowValuesAvailable(observation, window, now.value))
    return t('accountQuotas.windowState.' + quotaWindowState(observation, window, now.value))
  const percent = quotaRemaining(window)
  return percent !== undefined
    ? t('accountQuotas.remainingShort', { value: n(Math.round(percent)) + '%' })
    : window.remaining !== undefined
      ? t('accountQuotas.remainingShort', {
          value: n(window.remaining) + ' ' + (window.unit === 'percent' ? '%' : window.unit),
        })
      : t('accountQuotas.windowState.unknown')
}
</script>

<template>
  <div class="modern-group-quota-summary" :aria-label="t('accountQuotas.title')">
    <span v-if="failed">{{ t('accountQuotas.loadFailed') }}</span>
    <span v-else-if="loading && !items">{{ t('ui.loading') }}</span>
    <span v-else-if="!visible.length">{{
      t(partial ? 'accountQuotas.notInPage' : 'accountQuotas.noWindows')
    }}</span>
    <div v-for="item in visible" :key="item.credential.id" class="modern-group-quota-account">
      <span>{{ item.credential.label }} · {{ t(credentialStatus(item.credential).key) }}</span>
      <span v-if="!item.quotaSupported">{{ t('accountQuotas.unsupported') }}</span>
      <template v-else>
        <span>{{
          t('credentialCards.observation.' + (item.credential.observation?.state ?? 'unavailable'))
        }}</span>
        <span v-for="(window, index) in windows(item).slice(0, 4)" :key="window.id + ':' + index">
          {{ title(window) }} · {{ value(item, window) }}
        </span>
        <span v-if="!windows(item).length">{{ t('accountQuotas.noShortWindows') }}</span>
        <span v-else-if="windows(item).length > 4">{{
          t('accountQuotas.moreWindows', { count: n(windows(item).length - 4) })
        }}</span>
      </template>
    </div>
    <span v-if="(items?.length ?? 0) > visible.length">{{
      t('accountQuotas.moreAccounts', { count: n(items!.length - visible.length) })
    }}</span>
    <span v-if="partial">{{ t('accountQuotas.partial') }}</span>
  </div>
</template>

<style scoped>
.modern-group-quota-summary,
.modern-group-quota-account {
  display: grid;
  min-width: 0;
  gap: var(--modern-space-1);
  overflow-wrap: anywhere;
  color: var(--modern-muted);
  font-size: var(--modern-font-size-caption);
}
.modern-group-quota-summary {
  margin-top: var(--modern-space-2);
}
.modern-group-quota-account > :first-child {
  color: var(--modern-text);
}
</style>
