<script setup lang="ts">
import { ChevronDown, ChevronUp } from '@lucide/vue'
import { computed, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import type { AccountQuotaItem } from '@modern/api/fork-account-quotas'
import type { ResetCreditKind } from '@modern/api/credential-observation'
import type { GroupChannel } from '@modern/api/group-create'
import { AppBadge, AppButton, AppChannelIcon } from '@modern/components/ui'
import { useClock } from '@modern/components/ui/clock'
import {
  credentialStatus,
  credentialTime,
  sortedQuotaWindows,
} from '@modern/features/groups/credential-presentation'
import {
  accountCycleEstimate,
  accountResetTypes,
  resetCreditActionKey,
  resetCreditRows,
  visibleQuotaWindows,
} from './data'
import QuotaWindow from './QuotaWindow.vue'

const props = withDefaults(
  defineProps<{
    item: AccountQuotaItem
    readonly?: boolean
    channel?: Pick<GroupChannel, 'icon' | 'mark' | 'name'>
    syncing: boolean
    active: boolean
    failed: boolean
    reading: boolean
    resetSupported: boolean
    resetAllowed: (kind?: ResetCreditKind) => boolean
    resetPending: boolean
    resetWaiting: boolean
    resetError?: string
    resetNotice?: string
  }>(),
  { readonly: false },
)
defineEmits<{ sync: []; reset: [kind?: ResetCreditKind] }>()
const { t, n, locale } = useI18n()
const now = useClock()
const credential = computed(() => props.item.credential)
const windows = computed(() =>
  sortedQuotaWindows(visibleQuotaWindows(credential.value.observation?.windows ?? [])),
)
const cards = computed(() => resetCreditRows(credential.value.observation, now.value))
const resetCount = computed(() => {
  const observation = credential.value.observation
  return !observation ||
    observation.state === 'unavailable' ||
    observation.resetCreditsKnown === false ||
    (props.item.resetTypes?.length && observation.state !== 'fresh' && !cards.value.length)
    ? undefined
    : observation.resetCredits
})
const cardsExpanded = ref(false)
const cardsDetailsID = useId()
</script>

<template>
  <article class="modern-account-quota-card">
    <header class="modern-account-quota-card-header">
      <div class="modern-account-quota-identity">
        <RouterLink
          v-if="!readonly"
          class="modern-account-quota-group"
          :to="{ name: 'modern-group-detail', params: { id: item.groupID } }"
        >
          <AppChannelIcon
            :icon="channel?.icon"
            :mark="channel?.mark"
            :name="channel?.name || item.channelName"
            :group-name="item.groupName"
            size="sm"
          />
          <span>{{ item.groupName }}</span>
        </RouterLink>
        <span v-else class="modern-account-quota-group">
          <AppChannelIcon
            :icon="channel?.icon"
            :mark="channel?.mark"
            :name="channel?.name || item.channelName"
            :group-name="item.groupName"
            size="sm"
          />
          <span>{{ item.groupName }}</span>
        </span>
        <strong>{{
          credential.name.trim() || credential.account || credential.mask || '#' + credential.id
        }}</strong>
        <span v-if="credential.name.trim()" class="modern-account-quota-mask">{{
          credential.account || credential.mask || '#' + credential.id
        }}</span>
        <div class="modern-account-quota-badges">
          <AppBadge v-if="credential.observation?.plan" tone="brand" size="xs">{{
            credential.observation.plan
          }}</AppBadge>
          <AppBadge :tone="credentialStatus(credential).tone" size="xs" dot>{{
            t(credentialStatus(credential).key)
          }}</AppBadge>
          <AppBadge
            v-if="credential.expiresAt && credential.expiresAt <= now"
            tone="danger"
            size="xs"
            >{{ t('accountQuotas.credentialExpired') }}</AppBadge
          >
          <AppBadge
            :tone="credential.observation?.state === 'error' ? 'warning' : 'neutral'"
            size="xs"
            >{{
              t('credentialCards.observation.' + (credential.observation?.state ?? 'unavailable'))
            }}</AppBadge
          >
        </div>
      </div>
      <AppButton
        v-if="item.quotaSupported && !readonly"
        size="xs"
        :aria-label="t('accountQuotas.syncAccount', { name: credential.label })"
        :disabled="syncing || reading"
        :loading="active"
        @click="$emit('sync')"
        >{{ t('credentialCards.syncQuota') }}</AppButton
      >
    </header>
    <p v-if="failed" class="modern-account-quota-error" role="alert">
      {{ t('accountQuotas.syncFailed') }}
    </p>
    <p v-if="!item.quotaSupported" class="modern-account-quota-note">
      {{ t('accountQuotas.unsupported') }}
    </p>
    <div v-else-if="windows.length" class="modern-account-quota-windows">
      <QuotaWindow
        v-for="(window, index) in windows"
        :key="window.id + ':' + index"
        :window="window"
        :observation="credential.observation"
        :estimate="accountCycleEstimate(item, window)"
      />
    </div>
    <p v-else class="modern-account-quota-note">{{ t('accountQuotas.noWindows') }}</p>
    <section v-if="resetSupported || cards.length" class="modern-account-reset-cards">
      <header class="modern-account-reset-header">
        <AppButton
          variant="ghost"
          size="xs"
          :icon="cardsExpanded ? ChevronUp : ChevronDown"
          :aria-expanded="cardsExpanded"
          :aria-controls="cardsDetailsID"
          @click="cardsExpanded = !cardsExpanded"
        >
          {{ t('accountQuotas.resetCards') }} ·
          {{ resetCount === undefined ? '—' : n(resetCount) }}
        </AppButton>
      </header>
      <div v-show="cardsExpanded" :id="cardsDetailsID" class="modern-account-reset-details">
        <div v-if="!readonly && resetSupported" class="modern-account-reset-actions">
          <AppButton
            v-for="kind in accountResetTypes(item)"
            :key="kind ?? 'default'"
            size="xs"
            :disabled="!resetAllowed(kind)"
            :loading="resetPending"
            @click="$emit('reset', kind)"
            >{{ t(resetCreditActionKey(kind)) }}</AppButton
          >
        </div>
        <p v-if="!readonly" class="modern-account-quota-note">
          {{ t('accountQuotas.autoSelectCard') }}
        </p>
        <ul v-if="cards.length" class="modern-account-reset-list">
          <li v-for="(card, index) in cards" :key="index">
            <span
              >{{
                card.kind
                  ? t('accountQuotas.cardKind.' + card.kind)
                  : t('accountQuotas.resetCardIndex', { index: n(index + 1) })
              }}
              ·
              {{ t('accountQuotas.expiresAt') }}
              {{
                card.expiresAt === undefined
                  ? t('accountQuotas.notProvided')
                  : credentialTime(card.expiresAt, locale)
              }}</span
            >
            <AppBadge
              size="xs"
              :tone="
                card.state === 'available'
                  ? 'success'
                  : card.state === 'expired'
                    ? 'warning'
                    : 'neutral'
              "
              >{{ t('accountQuotas.resetCardState.' + card.state) }}</AppBadge
            >
          </li>
        </ul>
        <p v-else class="modern-account-quota-note">
          {{
            t(
              resetCount === undefined || resetCount > 0
                ? 'accountQuotas.cardDetailsNotProvided'
                : 'accountQuotas.noResetCards',
            )
          }}
        </p>
      </div>
      <p v-if="resetError" class="modern-account-quota-error" role="alert">{{ t(resetError) }}</p>
      <p v-if="resetNotice" class="modern-account-quota-note" role="status">{{ t(resetNotice) }}</p>
      <p v-else-if="resetWaiting" class="modern-account-quota-note">
        {{ t('credentialCards.resetPending') }}
      </p>
    </section>
    <footer class="modern-account-quota-times">
      <span
        >{{ t('accountQuotas.lastSync') }} ·
        {{ credentialTime(credential.observation?.observedAt, locale) }}</span
      >
      <span v-if="credential.expiresAt"
        >{{ t('accountQuotas.expiresAt') }} ·
        {{ credentialTime(credential.expiresAt, locale) }}</span
      >
    </footer>
  </article>
</template>

<style scoped>
.modern-account-quota-card {
  display: grid;
  gap: var(--modern-space-3);
  min-width: 0;
  padding: var(--modern-space-3);
  border: var(--modern-line-width) solid var(--modern-border);
  border-radius: var(--modern-radius-panel);
  background: var(--modern-surface);
}
.modern-account-quota-card-header {
  display: flex;
  flex-wrap: wrap;
  align-items: start;
  justify-content: space-between;
  gap: var(--modern-space-2);
}
.modern-account-quota-identity {
  display: grid;
  min-width: 0;
  gap: var(--modern-space-1);
  overflow-wrap: anywhere;
}
.modern-account-quota-identity > strong {
  font-size: var(--modern-font-size-body);
  font-weight: var(--modern-weight-semibold);
}
.modern-account-quota-group {
  display: flex;
  align-items: center;
  gap: var(--modern-space-2);
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
}
a.modern-account-quota-group:hover {
  color: var(--modern-accent);
}
.modern-account-quota-mask {
  color: var(--modern-muted);
  font-family: var(--modern-font-mono);
  font-size: var(--modern-font-size-small);
}
.modern-account-quota-badges {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--modern-space-1-5);
  margin-top: var(--modern-space-1);
}
.modern-account-quota-windows {
  display: grid;
  gap: var(--modern-space-2);
  min-width: 0;
  padding: var(--modern-space-2) var(--modern-space-3);
  border-radius: var(--modern-radius-control);
  background: var(--modern-subtle);
}
.modern-account-quota-times {
  display: grid;
  gap: var(--modern-space-1);
  padding-top: var(--modern-space-2);
  border-top: var(--modern-line-width) solid var(--modern-border);
}
.modern-account-quota-times,
.modern-account-quota-note {
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
  overflow-wrap: anywhere;
}
.modern-account-quota-error {
  color: var(--modern-danger);
  font-size: var(--modern-font-size-small);
}
.modern-account-reset-cards {
  display: grid;
  gap: var(--modern-space-2);
  border-top: var(--modern-line-width) solid var(--modern-border);
  padding-top: var(--modern-space-2);
}
.modern-account-reset-header,
.modern-account-reset-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--modern-space-2);
}
.modern-account-reset-details {
  display: grid;
  gap: var(--modern-space-2);
}
.modern-account-reset-list {
  display: grid;
  gap: var(--modern-space-1);
  margin: 0;
  padding: 0;
  list-style: none;
}
.modern-account-reset-list li {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--modern-space-1) var(--modern-space-2);
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
}
@media (max-width: 760px) {
  .modern-account-quota-windows {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
