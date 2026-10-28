<script setup lang="ts">
import { useMobileLayout } from '@modern/app/use-mobile-layout'
import { DialogRoot } from 'reka-ui'
import { Cable, Route as RouteIcon } from '@lucide/vue'
import { useQuery } from '@tanstack/vue-query'
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import {
  getHome,
  getHomeAccounts,
  getHomeStatistics,
  getHomeOverview,
  homeStatisticsKey,
} from '@modern/api/home'
import { getGroupWorkspace, groupQueryKey } from '@modern/api/groups'
import { getHealth } from '@modern/api/health'
import { getLogAccessKeys } from '@modern/api/logs'
import { useApiClient } from '@shared/http/client-context'
import { useAuthSession } from '@modern/features/auth/auth-session'
import { usePageRefresh } from '@modern/app/page-refresh'
import {
  AppButton,
  AppCollectionState,
  AppNotice,
  AppDialogContent,
  AppDialogHeader,
} from '@modern/components/ui'
import RouteInspector from '@modern/features/inspector/RouteInspector.vue'
import { compareHealthIssues, healthIssues } from '@modern/features/health/health-display'
import HomeAccounts from './HomeAccounts.vue'
import HomeAttention from './HomeAttention.vue'
import HomeRouteTool from './HomeRouteTool.vue'
import HomeSetupGuide from './HomeSetupGuide.vue'
import HomeStatusBar from './HomeStatusBar.vue'
import HomeTrend from './HomeTrend.vue'
import GatewayConnection from './GatewayConnection.vue'
import AccountQuotasPanel from '@modern/features/account-quotas/AccountQuotasPanel.vue'

const { t, locale } = useI18n()
const isMobile = useMobileLayout()
const connectionOpen = ref(false)
const client = useApiClient()
const session = useAuthSession()
const route = useRoute()
function closeMobileTool(): void {
  connectionOpen.value = false
  inspectorOpen.value = false
}
const inspector = ref<InstanceType<typeof RouteInspector>>()
const accountQuotas = ref<InstanceType<typeof AccountQuotasPanel>>()
const inspectorSection = ref<HTMLElement>()
const admin = computed(() => session.state.principalType === 'admin')
const baseQuery = useQuery({
  queryKey: ['modern', 'home', 'base'],
  queryFn: ({ signal }) => getHome(client, signal),
  enabled: admin,
})
const overview = useQuery({
  queryKey: ['modern', 'home', 'shared-overview'],
  queryFn: ({ signal }) => getHomeOverview(client, signal),
  enabled: computed(() => !admin.value),
})
const base = computed(() => (admin.value ? baseQuery.data.value : overview.data.value?.base))
const basePending = computed(() =>
  admin.value ? baseQuery.isPending.value : overview.isPending.value,
)
const baseFailed = computed(() => (admin.value ? baseQuery.isError.value : overview.isError.value))
const groups = useQuery({
  queryKey: groupQueryKey,
  queryFn: ({ signal }) => getGroupWorkspace(client, signal),
  enabled: admin,
})
const displayGroups = computed(() =>
  admin.value ? groups.data.value?.items : overview.data.value?.groups,
)
const groupMap = computed(() => new Map(displayGroups.value?.map((group) => [group.id, group])))
const displayChannels = computed(() => [
  ...new Map(
    displayGroups.value?.map((group) => [
      group.channelID,
      {
        id: group.channelID,
        name: group.channelName,
        icon: group.channelIcon,
        mark: group.channelMark,
      },
    ]),
  ).values(),
])
const keys = useQuery({
  queryKey: ['modern', 'log-access-key-options'],
  queryFn: ({ signal }) => getLogAccessKeys(client, signal),
  enabled: admin,
})
const accounts = useQuery({
  queryKey: ['modern', 'home', 'accounts'],
  queryFn: ({ signal }) => getHomeAccounts(client, signal),
  enabled: admin,
})
const statistics = useQuery({
  queryKey: homeStatisticsKey,
  queryFn: ({ signal }) => getHomeStatistics(client, signal),
  enabled: admin,
})
const health = useQuery({
  queryKey: ['modern', 'health'],
  queryFn: ({ signal }) => getHealth(client, signal),
  enabled: admin,
})
const trendReport = computed(() =>
  admin.value ? statistics.data.value : overview.data.value?.statistics,
)
const accountReport = computed(() =>
  admin.value ? accounts.data.value : overview.data.value?.accounts,
)
const healthReport = computed(() => (admin.value ? health.data.value : overview.data.value?.health))
const trendFailed = computed(() =>
  admin.value ? statistics.isError.value : overview.isError.value,
)
const accountsFailed = computed(() =>
  admin.value ? accounts.isError.value : overview.isError.value,
)
const accountsPending = computed(() =>
  admin.value ? accounts.isPending.value : overview.isPending.value,
)
const attentionFailed = computed(() =>
  admin.value ? health.isError.value || groups.isError.value : overview.isError.value,
)
// 初始化只看配置是否齐全，不把停用、冷却等运行状态当成尚未配置。
const showSetup = computed(() => {
  if (!admin.value || !groups.data.value || !keys.data.value) return false
  return (
    !groups.data.value.items.some((group) => group.credentials.total > 0 && group.modelCount > 0) ||
    !keys.data.value.length
  )
})
const emptyProject = computed(
  () => admin.value && groups.data.value?.items.length === 0 && keys.data.value?.length === 0,
)
const attentionIssues = computed(() => {
  if (!healthReport.value || !displayGroups.value) return undefined
  return healthIssues(healthReport.value, displayGroups.value, (key, values) =>
    t(key, values ?? {}),
  ).sort((left, right) => compareHealthIssues(left, right, locale.value))
})
const attention = computed(() => attentionIssues.value?.length ?? 0)
// 深链接 /monitor/inspector 会重定向到首页并带上 inspect_* 参数，那种情况直接展开。
const inspectorOpen = ref(
  route.hash === '#route-inspector' ||
    Object.keys(route.query).some((key) => key.startsWith('inspect_')),
)
async function refreshOptions(): Promise<void> {
  await Promise.all([groups.refetch(), keys.refetch()])
}
async function refreshHealth(): Promise<void> {
  if (!admin.value) {
    await overview.refetch()
    return
  }
  await Promise.all([health.refetch(), groups.refetch()])
}
async function refresh(): Promise<void> {
  if (!admin.value) {
    await Promise.all([overview.refetch(), accountQuotas.value?.refresh()])
    return
  }
  await Promise.all([
    baseQuery.refetch(),
    statistics.refetch(),
    ...(admin.value
      ? [
          refreshOptions(),
          accounts.refetch(),
          health.refetch(),
          inspector.value?.refresh(),
          accountQuotas.value?.refresh(),
        ]
      : []),
  ])
}
usePageRefresh({
  refresh,
  pending: () =>
    overview.isFetching.value ||
    baseQuery.isFetching.value ||
    statistics.isFetching.value ||
    groups.isFetching.value ||
    keys.isFetching.value ||
    accounts.isFetching.value ||
    health.isFetching.value ||
    Boolean(inspector.value?.pending) ||
    Boolean(accountQuotas.value?.pending),
  updatedAt: () =>
    Math.max(
      overview.data.value?.observedAt ?? 0,
      base.value?.observedAt ?? 0,
      statistics.data.value?.observedAt ?? 0,
      accounts.data.value?.observedAt ?? 0,
      health.data.value?.observedAt ?? 0,
      inspector.value?.updatedAt ?? 0,
      accountQuotas.value?.updatedAt ?? 0,
    ) || undefined,
})
const sectionReady = computed(
  () =>
    admin.value &&
    Boolean(base.value) &&
    !groups.isPending.value &&
    !keys.isPending.value &&
    !accounts.isPending.value,
)
watch(
  [() => route.hash, sectionReady],
  async ([hash, ready]) => {
    if (hash !== '#route-inspector' || !ready) return
    inspectorOpen.value = true
    await nextTick()
    if (route.hash !== hash || !sectionReady.value) return
    inspectorSection.value?.scrollIntoView({ block: 'start' })
    inspectorSection.value?.focus({ preventScroll: true })
  },
  { immediate: true },
)
</script>

<template>
  <div class="modern-home-workspace">
    <AppCollectionState
      v-if="!base"
      :loading="basePending"
      :error="baseFailed"
      :title="t(baseFailed ? 'home.baseFailed' : 'ui.loading')"
    >
      <AppButton v-if="baseFailed" @click="refresh">{{ t('ui.retry') }}</AppButton>
    </AppCollectionState>
    <template v-else>
      <div v-if="baseFailed" class="modern-home-error" role="status">
        <span>{{ t('home.baseFailed') }}</span>
        <AppButton size="xs" @click="admin ? baseQuery.refetch() : overview.refetch()">{{
          t('ui.retry')
        }}</AppButton>
      </div>
      <HomeStatusBar :mobile="isMobile" :base="base" :admin="admin" :attention="attention" />
      <AccountQuotasPanel ref="accountQuotas" :groups="displayGroups" :channels="displayChannels" />
      <AppNotice v-if="admin && (groups.isError.value || keys.isError.value)" tone="warning">
        <div class="modern-home-options-error">
          <span>{{ t('home.setupFailed') }}</span>
          <AppButton size="xs" variant="text" @click="refreshOptions">{{
            t('ui.retry')
          }}</AppButton>
        </div>
      </AppNotice>
      <HomeSetupGuide
        v-if="showSetup && groups.data.value && keys.data.value"
        :groups="groups.data.value.items"
        :key-count="keys.data.value.length"
      />
      <div v-if="!isMobile" class="modern-home-columns">
        <div class="modern-home-main">
          <GatewayConnection :keys="base.keys" :admin="admin" :readonly="!admin" />
          <div
            v-if="admin"
            id="route-inspector"
            ref="inspectorSection"
            class="modern-home-inspector"
            tabindex="-1"
          >
            <HomeRouteTool v-model:open="inspectorOpen" :access-keys="keys.data.value">
              <p v-if="emptyProject" class="modern-home-inspector-empty">
                {{ t('home.setup.inspectorLater') }}
              </p>
              <RouteInspector
                v-else
                ref="inspector"
                :groups="groups.data.value"
                :groups-loading="groups.isFetching.value"
                :groups-failed="groups.isError.value"
                :access-keys="keys.data.value"
                :keys-loading="keys.isFetching.value"
                :keys-failed="keys.isError.value"
                @retry-options="refreshOptions"
              />
            </HomeRouteTool>
          </div>
        </div>
        <div class="modern-home-side">
          <HomeTrend
            :report="trendReport"
            :failed="trendFailed"
            :readonly="!admin"
            @retry="admin ? statistics.refetch() : overview.refetch()"
          />
          <HomeAccounts
            :readonly="!admin"
            :accounts="accountReport?.items ?? []"
            :groups="groupMap"
            :failed="accountsFailed"
            :loading="accountsPending"
            @retry="admin ? accounts.refetch() : overview.refetch()"
          />
          <div class="modern-home-attention-section">
            <HomeAttention
              :issues="attentionIssues"
              :groups="groupMap"
              :failed="attentionFailed"
              :readonly="!admin"
              @retry="refreshHealth"
            />
          </div>
        </div>
      </div>
      <div v-else class="modern-home-mobile">
        <HomeTrend
          :report="trendReport"
          :failed="trendFailed"
          :readonly="!admin"
          @retry="admin ? statistics.refetch() : overview.refetch()"
        />
        <div class="modern-home-mobile-tools">
          <AppButton :icon="Cable" @click="connectionOpen = true">{{
            t('home.connection')
          }}</AppButton>
          <AppButton v-if="admin" :icon="RouteIcon" @click="inspectorOpen = true">{{
            t('pages.inspector.title')
          }}</AppButton>
        </div>
        <HomeAttention
          v-if="attention || attentionFailed"
          :issues="attentionIssues"
          :groups="groupMap"
          :failed="attentionFailed"
          :readonly="!admin"
          @retry="refreshHealth"
        />
        <HomeAccounts
          v-if="accountReport?.items.length || accountsFailed"
          :readonly="!admin"
          :accounts="accountReport?.items ?? []"
          :groups="groupMap"
          :failed="accountsFailed"
          :loading="accountsPending"
          @retry="admin ? accounts.refetch() : overview.refetch()"
        />
        <DialogRoot
          :open="connectionOpen || inspectorOpen"
          @update:open="
            (open) => {
              if (!open) {
                connectionOpen = false
                inspectorOpen = false
              }
            }
          "
        >
          <AppDialogContent
            placement="editor"
            :title="connectionOpen ? t('home.connection') : t('pages.inspector.title')"
            :description="connectionOpen ? t('home.connection') : t('pages.inspector.title')"
          >
            <AppDialogHeader
              :title="connectionOpen ? t('home.connection') : t('pages.inspector.title')"
              :close-label="t('ui.close')"
              @close="closeMobileTool"
            />
            <div class="modern-home-mobile-tool-body">
              <GatewayConnection
                v-if="connectionOpen"
                :keys="base.keys"
                :admin="admin"
                :readonly="!admin"
              />
              <RouteInspector
                v-else-if="admin"
                ref="inspector"
                :groups="groups.data.value"
                :groups-loading="groups.isFetching.value"
                :groups-failed="groups.isError.value"
                :access-keys="keys.data.value"
                :keys-loading="keys.isFetching.value"
                :keys-failed="keys.isError.value"
                @retry-options="refreshOptions"
              />
            </div>
          </AppDialogContent>
        </DialogRoot>
      </div>
    </template>
  </div>
</template>

<style scoped>
.modern-home-mobile {
  display: grid;
  gap: var(--modern-space-4);
  min-width: 0;
}
.modern-home-mobile-tools {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--modern-space-2);
}
.modern-home-mobile-tool-body {
  min-height: 0;
  overflow-y: auto;
  padding: var(--modern-space-3);
}

.modern-home-workspace {
  display: grid;
  flex: none;
  align-content: start;
  min-width: 0;
  gap: var(--modern-page-gap);
  padding-block: var(--modern-space-5);
}
.modern-home-error {
  display: flex;
  align-items: center;
  gap: var(--modern-space-3);
  color: var(--modern-danger);
  font-size: var(--modern-font-size-secondary);
}
.modern-home-options-error {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--modern-space-3);
}
.modern-home-columns {
  display: grid;
  grid-template-columns: minmax(0, 1fr) var(--modern-home-side);
  align-items: start;
  gap: var(--modern-page-gap);
}
.modern-home-main,
.modern-home-side {
  display: grid;
  align-content: start;
  min-width: 0;
  gap: var(--modern-page-gap);
}
.modern-home-inspector,
.modern-home-attention-section {
  min-width: 0;
  scroll-margin-top: var(--modern-space-5);
}
.modern-home-inspector-empty {
  color: var(--modern-muted);
  font-size: var(--modern-font-size-secondary);
}
@media (max-width: 1150px) {
  .modern-home-columns {
    grid-template-columns: minmax(0, 1fr);
  }
  .modern-home-side {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    align-items: start;
  }
}
@media (max-width: 760px) {
  .modern-home-side {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
