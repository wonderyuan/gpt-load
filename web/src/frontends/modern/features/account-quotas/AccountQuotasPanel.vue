<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import type { QuotaGroup } from '@modern/api/fork-key-quotas'
import type { GroupChannel } from '@modern/api/group-create'
import {
  AppBadge,
  AppButton,
  AppChannelIcon,
  AppCheckbox,
  AppConfirmDialog,
  AppNotice,
  AppPagination,
  AppPanel,
} from '@modern/components/ui'
import {
  useAccountQuotas,
  useAccountQuotaSync,
  useKeyQuotas,
  useAccountResetCredits,
  useKeyQuotaReset,
} from './use-account-quotas'
import {
  groupQuotaSections,
  keyQuotaLabel,
  keyResetPeriods,
  keyResetRuleIDs,
  resetCreditActionKey,
} from './data'
import { quotaPeriod } from '@modern/features/groups/credential-presentation'
import AccountQuotaCard from './AccountQuotaCard.vue'
import KeyQuotaCard from './KeyQuotaCard.vue'

const props = withDefaults(
  defineProps<{
    groups?: readonly QuotaGroup[]
    channels?: readonly Pick<GroupChannel, 'id' | 'name' | 'icon' | 'mark'>[]
  }>(),
  { groups: () => [], channels: () => [] },
)
const { t, n } = useI18n()
const accountPage = ref(1)
const keyPage = ref(1)
const accounts = useAccountQuotas(accountPage)
const admin = computed(() => accounts.admin.value)
const keys = useKeyQuotas(keyPage)
const sync = useAccountQuotaSync()
const accountData = accounts.data
const keyData = keys.data
const keyReset = useKeyQuotaReset(() => keyData.value?.items ?? [], keyPage)
const groups = computed(() => keyData.value?.groups ?? props.groups)
const items = computed(() => accountData.value?.items ?? [])
const reset = useAccountResetCredits(items)
const channels = computed(() => (admin.value ? reset.channels.data.value : props.channels))
const channel = (id: string) => channels.value?.find((item) => item.id === id)
const sections = computed(() =>
  groupQuotaSections(groups.value, items.value, keyData.value?.items ?? []),
)
const accountPartial = computed(() =>
  Boolean(accountData.value && accountData.value.total > accountData.value.items.length),
)
const keyPartial = computed(() =>
  Boolean(keyData.value && keyData.value.total > keyData.value.items.length),
)
watch(accountData, (value) => {
  if (value)
    accountPage.value = Math.min(
      accountPage.value,
      Math.max(1, Math.ceil(value.total / value.pageSize)),
    )
})
watch(keyData, (value) => {
  if (value)
    keyPage.value = Math.min(keyPage.value, Math.max(1, Math.ceil(value.total / value.pageSize)))
})
async function refresh(): Promise<void> {
  await Promise.all([accounts.refresh(), keys.refresh()])
}
defineExpose({
  refresh,
  pending: computed(
    () =>
      accounts.isFetching.value ||
      keys.isFetching.value ||
      sync.syncing.value ||
      reset.pending.value ||
      keyReset.pending.value,
  ),
  updatedAt: computed(
    () => Math.max(accountData.value?.observedAt ?? 0, keyData.value?.observedAt ?? 0) || undefined,
  ),
})
</script>

<template>
  <AppPanel :title="t('accountQuotas.title')" compact>
    <div class="modern-account-quotas">
      <section class="modern-account-quotas-region" :aria-label="t('accountQuotas.accounts')">
        <header class="modern-account-quotas-region-header">
          <h3>
            {{ t('accountQuotas.accounts') }}
            <span>{{ accountData ? n(accountData.total) : '—' }}</span>
          </h3>
          <AppButton
            v-if="admin"
            size="xs"
            :disabled="
              !items.some((item) => item.quotaSupported) ||
              accounts.isFetching.value ||
              sync.syncing.value ||
              reset.pending.value
            "
            :loading="sync.syncing.value"
            @click="sync.sync(items)"
            >{{ t('accountQuotas.syncPage') }}</AppButton
          >
        </header>
        <AppNotice v-if="accounts.isError.value || sync.failedReads.value" tone="warning">
          <div class="modern-account-quotas-notice">
            <span>{{
              t(accountData ? 'accountQuotas.staleRead' : 'accountQuotas.loadFailed')
            }}</span>
            <AppButton
              size="xs"
              :disabled="accounts.isFetching.value"
              @click="accounts.refresh()"
              >{{ t('ui.retry') }}</AppButton
            >
          </div>
        </AppNotice>
        <div v-if="items.length" class="modern-account-quota-cards">
          <AccountQuotaCard
            v-for="item in items"
            :key="sync.identity(item)"
            :item="item"
            :readonly="!accounts.admin.value"
            :channel="channel(item.channelID)"
            :syncing="sync.syncing.value || reset.pending.value"
            :active="sync.active.value === sync.identity(item)"
            :failed="sync.errors.value.has(sync.identity(item))"
            :reading="accounts.isFetching.value"
            :reset-supported="reset.supported(item)"
            :reset-allowed="
              (kind) =>
                reset.allowed(item, kind) && !sync.syncing.value && !accounts.isFetching.value
            "
            :reset-pending="
              reset.pending.value && reset.target.value?.credential.id === item.credential.id
            "
            :reset-waiting="reset.waiting.value.has(reset.identity(item))"
            :reset-error="reset.errors.value.get(reset.identity(item))"
            :reset-notice="reset.notices.value.get(reset.identity(item))"
            @reset="reset.open(item, $event)"
            @sync="sync.sync([item])"
          />
        </div>
        <p v-else class="modern-account-quotas-empty" role="status">
          {{
            t(
              !accountData
                ? accounts.isPending.value
                  ? 'ui.loading'
                  : 'accountQuotas.accountsUnavailable'
                : 'accountQuotas.empty',
            )
          }}
        </p>
        <template v-if="accountData && accountPartial">
          <p class="modern-account-quotas-help">
            {{
              t('accountQuotas.pageSummary', {
                page: n(accountData.page),
                pages: n(Math.max(1, Math.ceil(accountData.total / accountData.pageSize))),
                count: n(accountData.items.length),
                total: n(accountData.total),
              })
            }}
            <AppBadge tone="warning" size="xs">{{ t('accountQuotas.partialPage') }}</AppBadge>
          </p>
          <AppPagination
            mode="total"
            :page="accountPage"
            :page-size="accountData.pageSize"
            :page-sizes="[100]"
            :total="accountData.total"
            :pending="accounts.isFetching.value || sync.syncing.value || reset.pending.value"
            compact
            @update:page="accountPage = $event"
          />
        </template>
      </section>
      <section
        class="modern-account-quotas-region modern-account-quotas-keys"
        :aria-label="t('accountQuotas.keys')"
      >
        <header class="modern-account-quotas-region-header">
          <h3>
            {{ t('accountQuotas.keys') }} <span>{{ keyData ? n(keyData.total) : '—' }}</span>
          </h3>
          <AppButton
            v-if="admin"
            size="xs"
            :disabled="
              !keyReset.selectedItems.value.length ||
              keys.isFetching.value ||
              keys.isError.value ||
              keyReset.pending.value
            "
            :loading="keyReset.pending.value"
            @click="keyReset.open()"
          >
            {{ t('accountQuotas.resetSelectedUsage') }}
            <template v-if="keyReset.selectedItems.value.length">
              · {{ n(keyReset.selectedItems.value.length) }}</template
            >
          </AppButton>
        </header>
        <AppNotice v-if="keyReset.readFailed.value" tone="warning">
          <div class="modern-account-quotas-notice">
            <span>{{ t('accountQuotas.keyResetReadFailed') }}</span>
            <AppButton
              size="xs"
              :disabled="keys.isFetching.value || keyReset.pending.value"
              @click="keys.refresh()"
              >{{ t('ui.refresh') }}</AppButton
            >
          </div>
        </AppNotice>
        <AppNotice v-if="keys.isError.value" tone="warning">
          <div class="modern-account-quotas-notice">
            <span>{{
              t(keyData ? 'accountQuotas.keyStaleRead' : 'accountQuotas.keyLoadFailed')
            }}</span>
            <AppButton size="xs" :disabled="keys.isFetching.value" @click="keys.refresh()">{{
              t('ui.retry')
            }}</AppButton>
          </div>
        </AppNotice>
        <p v-if="!keyData" class="modern-account-quotas-empty" role="status">
          {{ t(keys.isPending.value ? 'ui.loading' : 'accountQuotas.keysUnavailable') }}
        </p>
        <template v-else>
          <section
            v-for="group in sections.groups"
            :key="group.id"
            class="modern-account-quota-group"
            :aria-label="group.name"
          >
            <header class="modern-account-quota-group-header">
              <AppChannelIcon
                :icon="channel(group.channelID)?.icon"
                :mark="channel(group.channelID)?.mark"
                :name="channel(group.channelID)?.name || group.channelName"
                :group-name="group.name"
                size="sm"
              />
              <RouterLink
                v-if="admin"
                :to="{ name: 'modern-group-detail', params: { id: group.id } }"
                >{{ group.name }}</RouterLink
              >
              <span v-else class="modern-account-quota-group-name">{{ group.name }}</span>
              <span>{{ n(group.keys.length) }}</span>
              <AppBadge v-if="group.enabled === false" size="xs">{{
                t('accountQuotas.groupDisabled')
              }}</AppBadge>
            </header>
            <div v-if="group.keys.length" class="modern-key-quota-cards">
              <KeyQuotaCard
                v-for="item in group.keys"
                :key="item.id"
                :item="item"
                :readonly="!accounts.admin.value"
                :groups="groups"
                :channels="channels"
                :selected="keyReset.selected.value.has(item.id)"
                :selection-disabled="
                  keys.isFetching.value || keys.isError.value || keyReset.pending.value
                "
                :reset-state="keyReset.results.value.get(item.id)"
                @select="keyReset.select(item, $event)"
              />
            </div>
            <p v-else class="modern-account-quotas-help">
              {{ t(keyPartial ? 'accountQuotas.keysNotInPage' : 'accountQuotas.noDedicatedKeys') }}
            </p>
          </section>
          <section
            v-if="sections.shared.length"
            class="modern-account-quota-group"
            :aria-label="t('accountQuotas.sharedKeys')"
          >
            <header class="modern-account-quota-group-header">
              <h4>{{ t('accountQuotas.sharedKeys') }}</h4>
              <span>{{ n(sections.shared.length) }}</span>
            </header>
            <div class="modern-key-quota-cards">
              <KeyQuotaCard
                v-for="item in sections.shared"
                :key="item.id"
                :item="item"
                :readonly="!accounts.admin.value"
                :groups="groups"
                :channels="channels"
                :selected="keyReset.selected.value.has(item.id)"
                :selection-disabled="
                  keys.isFetching.value || keys.isError.value || keyReset.pending.value
                "
                :reset-state="keyReset.results.value.get(item.id)"
                show-scope
                @select="keyReset.select(item, $event)"
              />
            </div>
          </section>
          <p
            v-if="!keyData.items.length && !sections.groups.length"
            class="modern-account-quotas-empty"
          >
            {{ t('accountQuotas.noSharedKeys') }}
          </p>
          <template v-if="keyPartial">
            <p class="modern-account-quotas-help">
              {{
                t('accountQuotas.pageSummary', {
                  page: n(keyData.page),
                  pages: n(Math.max(1, Math.ceil(keyData.total / keyData.pageSize))),
                  count: n(keyData.items.length),
                  total: n(keyData.total),
                })
              }}
              <AppBadge tone="warning" size="xs">{{ t('accountQuotas.partialPage') }}</AppBadge>
            </p>
            <AppPagination
              mode="total"
              :page="keyPage"
              :page-size="keyData.pageSize"
              :page-sizes="[100]"
              :total="keyData.total"
              :pending="keys.isFetching.value || keys.isError.value || keyReset.pending.value"
              compact
              @update:page="keyPage = $event"
            />
          </template>
        </template>
      </section>
    </div>
    <AppConfirmDialog
      v-if="admin"
      :open="Boolean(keyReset.target.value.length)"
      :title="t('accountQuotas.resetSelectedUsage')"
      :description="t('accountQuotas.confirmKeyReset')"
      :confirm-label="t('accountQuotas.resetSelectedUsage')"
      :pending="keyReset.pending.value"
      :disabled="!keyReset.canConfirm.value || keys.isFetching.value || keys.isError.value"
      @cancel="keyReset.target.value = []"
      @confirm="keyReset.reset()"
    >
      <div
        class="modern-key-reset-periods"
        role="group"
        :aria-label="t('accountQuotas.resetPeriods')"
      >
        <AppCheckbox
          v-for="seconds in keyResetPeriods"
          :key="seconds"
          :model-value="keyReset.periods.value.includes(seconds)"
          :label="quotaPeriod(seconds)"
          :disabled="keyReset.pending.value"
          @update:model-value="keyReset.selectPeriod(seconds, $event)"
        />
      </div>
      <ul class="modern-key-reset-targets">
        <li v-for="item in keyReset.target.value" :key="item.id">
          <strong>{{ keyQuotaLabel(item) }}</strong>
          <span>{{ item.mask }} · #{{ item.id }}</span>
          <span>{{
            keyResetPeriods
              .filter(
                (seconds) =>
                  keyReset.periods.value.includes(seconds) &&
                  keyResetRuleIDs(item, [seconds]).length,
              )
              .map(quotaPeriod)
              .join(' / ') || t('accountQuotas.keyResetState.noRules')
          }}</span>
        </li>
      </ul>
    </AppConfirmDialog>
    <AppConfirmDialog
      v-if="admin"
      :open="Boolean(reset.target.value)"
      :title="t('accountQuotas.useResetCard')"
      :subject="reset.target.value?.credential.label"
      :description="
        t(
          reset.targetType.value === 'week'
            ? 'accountQuotas.confirmWeekCard'
            : 'accountQuotas.confirmResetCard',
        )
      "
      :confirm-label="t(resetCreditActionKey(reset.targetType.value))"
      :pending="reset.pending.value"
      :disabled="sync.syncing.value || accounts.isFetching.value"
      :error="
        reset.target.value
          ? reset.errors.value.get(reset.identity(reset.target.value)) &&
            t(reset.errors.value.get(reset.identity(reset.target.value))!)
          : undefined
      "
      @cancel="reset.target.value = undefined"
      @confirm="reset.consume()"
    />
  </AppPanel>
</template>

<style scoped>
.modern-account-quotas,
.modern-account-quotas-region {
  display: grid;
  min-width: 0;
  gap: var(--modern-space-3);
}
.modern-account-quotas {
  gap: var(--modern-space-5);
}
.modern-account-quotas-help,
.modern-account-quotas-empty {
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
  overflow-wrap: anywhere;
}
.modern-account-quotas-region-header,
.modern-account-quotas-notice {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--modern-space-2);
}
.modern-account-quotas-region-header h3 {
  color: var(--modern-text);
  font-size: var(--modern-font-size-section);
  font-weight: var(--modern-weight-semibold);
}
.modern-account-quotas-region-header h3 span {
  margin-left: var(--modern-space-2);
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
  font-weight: var(--modern-weight-regular);
}
.modern-account-quota-cards,
.modern-key-quota-cards {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  align-items: start;
  gap: var(--modern-space-3);
  min-width: 0;
}
.modern-account-quota-cards {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}
.modern-account-quotas-keys {
  border-top: var(--modern-line-width) solid var(--modern-border);
  padding-top: var(--modern-space-4);
}
.modern-account-quota-group {
  display: grid;
  min-width: 0;
  gap: var(--modern-space-2);
}
.modern-account-quota-group + .modern-account-quota-group {
  border-top: var(--modern-line-width) solid var(--modern-border);
  padding-top: var(--modern-space-3);
}
.modern-account-quota-group-header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--modern-space-2);
  overflow-wrap: anywhere;
}
.modern-account-quota-group-header a,
.modern-account-quota-group-header > .modern-account-quota-group-name,
.modern-account-quota-group-header h4 {
  color: var(--modern-text);
  font-size: var(--modern-font-size-small);
  font-weight: var(--modern-weight-medium);
}
.modern-account-quota-group-header a:hover {
  color: var(--modern-accent);
}
.modern-account-quota-group-header > span {
  color: var(--modern-muted);
  font-size: var(--modern-font-size-caption);
}
.modern-key-reset-periods {
  display: flex;
  flex-wrap: wrap;
  gap: var(--modern-space-3);
}
.modern-key-reset-targets {
  display: grid;
  gap: var(--modern-space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}
.modern-key-reset-targets li {
  display: grid;
  gap: var(--modern-space-1);
  overflow-wrap: anywhere;
}
.modern-key-reset-targets strong {
  color: var(--modern-text);
  font-weight: var(--modern-weight-medium);
}
@media (max-width: 1150px) {
  .modern-account-quota-cards {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (max-width: 760px) {
  .modern-account-quota-cards,
  .modern-key-quota-cards {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
