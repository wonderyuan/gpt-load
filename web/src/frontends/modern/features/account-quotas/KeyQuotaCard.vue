<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { KeyQuotaItem, KeyQuotaWindow, QuotaGroup } from '@modern/api/fork-key-quotas'
import type { GroupChannel } from '@modern/api/group-create'
import {
  AppBadge,
  AppChannelIcon,
  AppCheckbox,
  AppProgressBar,
  AppTooltip,
} from '@modern/components/ui'
import type { KeyResetState } from './use-account-quotas'
import { useClock } from '@modern/components/ui/clock'
import { credentialTime, quotaPeriod } from '@modern/features/groups/credential-presentation'
import {
  keyAmount,
  keyDisplayEntries,
  keyQuotaState,
  keyQuotaValuesAvailable,
  keyRemainingPercent,
  keyUsedPercent,
  keyQuotaLabel,
  keyResetPeriods,
  keyResetRuleIDs,
  type KeyQuotaEntry,
} from './data'

const props = withDefaults(
  defineProps<{
    item: KeyQuotaItem
    readonly?: boolean
    groups: readonly QuotaGroup[]
    channels?: readonly Pick<GroupChannel, 'id' | 'icon' | 'mark' | 'name'>[]
    showScope?: boolean
    selected: boolean
    selectionDisabled: boolean
    resetState?: KeyResetState
  }>(),
  { readonly: false },
)
defineEmits<{ select: [value: boolean] }>()
const { t, locale } = useI18n()
const now = useClock()
const entries = computed(() => keyDisplayEntries(props.item))
const scope = computed(() =>
  props.item.groupIDs.length
    ? props.item.groupIDs
        .map((id) => props.groups.find((group) => group.id === id)?.name ?? '#' + id)
        .join(' / ')
    : t('accountQuotas.allGroups'),
)
const providers = computed(() => {
  const groups = props.item.groupIDs.length
    ? props.groups.filter((group) => props.item.groupIDs.includes(group.id))
    : props.groups
  const ids = new Set<string>()
  return groups
    .filter((group) => {
      if (ids.has(group.channelID)) return false
      ids.add(group.channelID)
      return true
    })
    .map((group) => ({
      id: group.channelID,
      name: group.channelName,
      ...props.channels?.find((channel) => channel.id === group.channelID),
    }))
})
const expired = computed(() => props.item.expiresAt !== null && props.item.expiresAt <= now.value)
const title = (entry: KeyQuotaEntry) => quotaPeriod(entry.periodSeconds)
const unit = computed(() => t('accountQuotas.amountUnits.' + props.item.accounting.unit))
const amount = (value?: string) =>
  value === undefined ? '—' : keyAmount(value, props.item.accounting, locale.value)
const used = (entry: KeyQuotaEntry) =>
  keyQuotaValuesAvailable(entry, now.value) ? entry.window?.usedUSD : undefined
const remaining = (entry: KeyQuotaEntry) =>
  keyQuotaValuesAvailable(entry, now.value) ? entry.window?.remainingUSD : undefined
const limit = (entry: KeyQuotaEntry) => entry.rule?.limitUSD ?? entry.window?.limitUSD
function exactValue(entry: KeyQuotaEntry): string | undefined {
  const value = used(entry)
  return value === undefined
    ? undefined
    : t('accountQuotas.exactValue', {
        label: t('accountQuotas.used'),
        value: keyAmount(value, props.item.accounting, locale.value, 'exact'),
        unit: unit.value,
      })
}
function percent(entry: KeyQuotaEntry): number | undefined {
  return keyQuotaValuesAvailable(entry, now.value) && entry.window
    ? keyUsedPercent(entry.window)
    : undefined
}
function tone(window?: KeyQuotaWindow): 'neutral' | 'success' | 'warning' | 'danger' {
  if (!window) return 'neutral'
  if (window.status === 'exhausted') return 'danger'
  const value = keyRemainingPercent(window)
  return value === undefined
    ? 'neutral'
    : value < 30
      ? 'danger'
      : value < 70
        ? 'warning'
        : 'success'
}
</script>

<template>
  <article class="modern-key-quota-card">
    <header class="modern-key-quota-header">
      <div class="modern-key-quota-identity">
        <div class="modern-key-quota-name">
          <AppCheckbox
            v-if="!readonly"
            :model-value="selected"
            :label="t('accountQuotas.selectKey', { name: keyQuotaLabel(item) })"
            label-hidden
            :disabled="selectionDisabled || !keyResetRuleIDs(item, keyResetPeriods).length"
            @update:model-value="$emit('select', $event)"
          />
          <AppChannelIcon
            v-for="provider in providers"
            :key="provider.id"
            :icon="provider.icon"
            :mark="provider.mark"
            :name="provider.name"
            size="sm"
          />
          <strong>{{ keyQuotaLabel(item) }}</strong>
        </div>
        <span>{{ item.mask }}</span>
      </div>
      <div class="modern-key-quota-badges">
        <AppBadge :tone="item.status === 'active' ? 'success' : 'neutral'" size="xs" dot>{{
          t('accountQuotas.keyState.' + item.status)
        }}</AppBadge>
        <AppBadge v-if="expired" tone="danger" size="xs">{{
          t('accountQuotas.keyState.expired')
        }}</AppBadge>
        <AppBadge v-if="item.quota && !item.quota.allowed" tone="warning" size="xs">{{
          t('accountQuotas.budgetBlocked')
        }}</AppBadge>
      </div>
    </header>
    <p
      v-if="resetState"
      class="modern-key-quota-reset-state"
      :class="{ 'is-error': resetState === 'failed' || resetState === 'unknown' }"
      :role="resetState === 'failed' || resetState === 'unknown' ? 'alert' : 'status'"
    >
      {{ t('accountQuotas.keyResetState.' + resetState) }}
    </p>
    <p v-if="showScope" class="modern-key-quota-note">
      {{ t('accountQuotas.accessScope') }} · {{ scope }}
    </p>
    <div class="modern-key-quota-windows">
      <section v-for="(entry, index) in entries" :key="index" class="modern-key-quota-window">
        <div class="modern-key-quota-window-heading">
          <strong>{{ title(entry) }}</strong>
          <AppTooltip v-if="used(entry) !== undefined" :label="exactValue(entry)" touchable>
            <span class="modern-key-quota-amount" tabindex="0" :aria-label="exactValue(entry)">
              {{ t('accountQuotas.used') }} <strong>{{ amount(used(entry)) }}</strong> {{ unit }}
            </span>
          </AppTooltip>
          <span v-else class="modern-key-quota-note">{{
            t('accountQuotas.keyWindowState.' + keyQuotaState(entry, now))
          }}</span>
        </div>
        <AppProgressBar
          v-if="percent(entry) !== undefined"
          :label="title(entry) + ' · ' + t('accountQuotas.used')"
          :value="percent(entry)"
          :tone="tone(entry.window)"
          size="sm"
        />
        <p v-else-if="keyQuotaState(entry, now) !== 'unconfigured'" class="modern-key-quota-note">
          {{ t('accountQuotas.noPercentage') }}
        </p>
        <div v-if="keyQuotaState(entry, now) !== 'unconfigured'" class="modern-key-quota-details">
          <span
            >{{ t('accountQuotas.remaining') }} {{ amount(remaining(entry)) }} /
            {{ t('accountQuotas.limit') }} {{ amount(limit(entry)) }} {{ unit }}</span
          >
          <span
            >{{ t('accountQuotas.reset') }} ·
            {{
              keyQuotaState(entry, now) === 'inactive'
                ? t('accountQuotas.nextRequest')
                : credentialTime(entry.window?.endsAt, locale)
            }}</span
          >
        </div>
      </section>
    </div>
    <footer class="modern-key-quota-times">
      <span
        >{{ t('accountQuotas.observedAt') }} ·
        {{ credentialTime(item.quota?.observedAt, locale) }}</span
      >
      <span v-if="item.expiresAt !== null"
        >{{ t('accountQuotas.expiresAt') }} · {{ credentialTime(item.expiresAt, locale) }}</span
      >
      <span v-if="item.quota && !item.quota.allowed"
        >{{ t('accountQuotas.recovery') }} ·
        {{
          item.quota.recoverable
            ? credentialTime(item.quota.nextAvailableAt, locale)
            : t('accountQuotas.noScheduledRecovery')
        }}</span
      >
    </footer>
  </article>
</template>

<style scoped>
.modern-key-quota-card {
  display: grid;
  min-width: 0;
  gap: var(--modern-space-2);
  padding: var(--modern-space-3);
  border: var(--modern-line-width) solid var(--modern-border);
  border-radius: var(--modern-radius-panel);
  background: var(--modern-surface);
}
.modern-key-quota-header,
.modern-key-quota-badges,
.modern-key-quota-name {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--modern-space-1-5);
}
.modern-key-quota-header {
  align-items: start;
  justify-content: space-between;
  gap: var(--modern-space-2);
}
.modern-key-quota-identity {
  display: grid;
  min-width: 0;
  gap: var(--modern-space-1);
  overflow-wrap: anywhere;
}
.modern-key-quota-name strong {
  font-size: var(--modern-font-size-body);
  font-weight: var(--modern-weight-semibold);
}
.modern-key-quota-identity > span {
  color: var(--modern-muted);
  font-family: var(--modern-font-mono);
  font-size: var(--modern-font-size-caption);
}
.modern-key-quota-windows {
  display: grid;
  min-width: 0;
  gap: var(--modern-space-2);
  padding: var(--modern-space-2) var(--modern-space-3);
  border-radius: var(--modern-radius-control);
  background: var(--modern-subtle);
}
.modern-key-quota-window {
  display: grid;
  min-width: 0;
  gap: var(--modern-space-1-5);
}
.modern-key-quota-window + .modern-key-quota-window {
  padding-top: var(--modern-space-2);
  border-top: var(--modern-line-width) solid var(--modern-border);
}
.modern-key-quota-window-heading,
.modern-key-quota-details {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--modern-space-1) var(--modern-space-2);
  overflow-wrap: anywhere;
}
.modern-key-quota-window-heading {
  font-size: var(--modern-font-size-small);
}
.modern-key-quota-window-heading strong {
  font-weight: var(--modern-weight-semibold);
}
.modern-key-quota-amount {
  font-variant-numeric: tabular-nums;
  border-radius: var(--modern-radius-small);
}
.modern-key-quota-amount:focus-visible {
  outline: var(--modern-focus-width) solid var(--modern-accent);
  outline-offset: var(--modern-focus-offset);
}
.modern-key-quota-note,
.modern-key-quota-times,
.modern-key-quota-details {
  color: var(--modern-muted);
  font-size: var(--modern-font-size-caption);
  overflow-wrap: anywhere;
}
.modern-key-quota-reset-state {
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
  overflow-wrap: anywhere;
}
.modern-key-quota-reset-state.is-error {
  color: var(--modern-danger);
}
.modern-key-quota-times {
  display: flex;
  flex-wrap: wrap;
  gap: var(--modern-space-1) var(--modern-space-3);
}
</style>
