<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CredentialObservation, CredentialQuota } from '@modern/api/credential-observation'
import type { AccountCycleEstimate } from '@modern/api/fork-account-quotas'
import { AppProgressBar, AppTooltip } from '@modern/components/ui'
import { useClock } from '@modern/components/ui/clock'
import {
  credentialTime,
  quotaPeriod,
  quotaRemaining,
  quotaTone,
  quotaWindowTitle,
} from '@modern/features/groups/credential-presentation'
import {
  accountCycleAmount,
  quotaWindowState,
  quotaWindowValuesAvailable,
  visibleQuotaWindows,
} from './data'

const props = defineProps<{
  window: CredentialQuota
  observation?: CredentialObservation
  estimate?: AccountCycleEstimate
}>()
const { t, n, te, locale } = useI18n()
const now = useClock()
const state = computed(() => quotaWindowState(props.observation, props.window, now.value))
const valid = computed(() => quotaWindowValuesAvailable(props.observation, props.window, now.value))
const percent = computed(() => {
  const remaining = valid.value ? quotaRemaining(props.window) : undefined
  return remaining === undefined ? undefined : 100 - remaining
})
const title = computed(() => {
  if ([18000, 604800].includes(props.window.windowSeconds ?? 0))
    return quotaPeriod(props.window.windowSeconds)
  const key = 'credentialCards.quotaLabels.' + props.window.labelKey
  return quotaWindowTitle(
    props.window,
    props.window.labelKey && te(key) ? t(key) : props.window.label,
  )
})
const scope = computed(() =>
  props.window.scope === 'account'
    ? ''
    : props.window.models.join(', ') ||
      t('accountQuotas.otherScope', { scope: props.window.scope || '—' }),
)
const estimate = computed(() => (valid.value ? props.estimate : undefined))
const estimateUnit = computed(() =>
  estimate.value ? t('accountQuotas.amountUnits.' + estimate.value.unit) : '',
)
const cycleAmount = (value: string, precision: 'display' | 'exact' = 'display') =>
  accountCycleAmount(value, estimate.value!.unit, locale.value, precision)
const exactEstimate = computed(() =>
  estimate.value
    ? t('accountQuotas.used') +
      ' ' +
      cycleAmount(estimate.value.used, 'exact') +
      ' / ' +
      t('accountQuotas.limit') +
      ' ' +
      cycleAmount(estimate.value.total, 'exact') +
      ' · ' +
      estimateUnit.value
    : undefined,
)
const amount = (value?: number) =>
  !valid.value || value === undefined
    ? '—'
    : n(value) + (props.window.unit === 'percent' ? '%' : ' ' + props.window.unit)
</script>

<template>
  <div v-if="visibleQuotaWindows([window]).length" class="modern-account-quota-window">
    <div class="modern-account-quota-window-heading">
      <strong
        >{{ title }}<span v-if="scope"> · {{ scope }}</span></strong
      >
      <AppTooltip v-if="estimate" :label="exactEstimate" touchable>
        <span class="modern-account-quota-window-value" tabindex="0" :aria-label="exactEstimate">
          {{ t('accountQuotas.used') }} {{ estimate.estimated ? '≈' : ''
          }}{{ cycleAmount(estimate.used) }} / {{ cycleAmount(estimate.total) }} {{ estimateUnit }}
          <template v-if="percent !== undefined">
            · {{ n(percent, { maximumFractionDigits: 1 }) }}%
          </template>
        </span>
      </AppTooltip>
      <span v-else class="modern-account-quota-window-value">
        {{ t('accountQuotas.used') }}
        {{
          valid && window.used !== undefined
            ? amount(window.used)
            : percent === undefined
              ? '—'
              : n(percent, { maximumFractionDigits: 1 }) + '%'
        }}
        <template
          v-if="percent !== undefined && window.used !== undefined && window.unit !== 'percent'"
        >
          · {{ n(percent, { maximumFractionDigits: 1 }) }}%
        </template>
      </span>
    </div>
    <AppProgressBar
      v-if="percent !== undefined"
      :label="title + (scope ? ' · ' + scope : '') + ' · ' + t('accountQuotas.used')"
      :value="percent"
      :tone="quotaTone(window)"
      size="sm"
    />
    <p v-else class="modern-account-quota-window-note">
      {{ t('accountQuotas.windowState.' + state) }} · {{ t('accountQuotas.noPercentage') }}
    </p>
    <div class="modern-account-quota-window-details">
      <span v-if="estimate">
        {{ t('accountQuotas.remaining') }} {{ cycleAmount(estimate.remaining) }} {{ estimateUnit }}
      </span>
      <span v-else-if="valid && window.remaining !== undefined">
        {{ t('accountQuotas.remaining') }} {{ amount(window.remaining) }}
        <template v-if="window.limit !== undefined && window.unit !== 'percent'">
          / {{ t('accountQuotas.limit') }} {{ amount(window.limit) }}
        </template>
      </span>
      <span v-if="valid && state === 'exhausted'">{{
        t('accountQuotas.windowState.exhausted')
      }}</span>
      <span>{{ t('accountQuotas.reset') }} · {{ credentialTime(window.resetsAt, locale) }}</span>
    </div>
  </div>
</template>

<style scoped>
.modern-account-quota-window {
  display: grid;
  align-content: start;
  min-width: 0;
  gap: var(--modern-space-1-5);
}
.modern-account-quota-window + .modern-account-quota-window {
  padding-top: var(--modern-space-2);
  border-top: var(--modern-line-width) solid var(--modern-border);
}
.modern-account-quota-window-heading,
.modern-account-quota-window-details {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--modern-space-1) var(--modern-space-2);
  overflow-wrap: anywhere;
}
.modern-account-quota-window-heading {
  font-size: var(--modern-font-size-small);
}
.modern-account-quota-window-heading strong {
  font-weight: var(--modern-weight-semibold);
}
.modern-account-quota-window-heading strong span,
.modern-account-quota-window-note,
.modern-account-quota-window-details {
  color: var(--modern-muted);
  font-size: var(--modern-font-size-caption);
}
.modern-account-quota-window-value {
  font-weight: var(--modern-weight-medium);
  font-variant-numeric: tabular-nums;
  border-radius: var(--modern-radius-small);
}
.modern-account-quota-window-value:focus-visible {
  outline: var(--modern-focus-width) solid var(--modern-accent);
  outline-offset: var(--modern-focus-offset);
}
</style>
