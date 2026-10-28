import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { computed, onScopeDispose, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'
import { useApiClient } from '@shared/http/client-context'
import { ApiError, RequestCancelledError } from '@shared/http/errors'
import { useAuthSession } from '@modern/features/auth/auth-session'
import {
  accountQuotasKey,
  accountQuotasPageSize,
  getAccountQuotas,
  type AccountQuotaItem,
} from '@modern/api/fork-account-quotas'
import {
  getKeyQuotas,
  keyQuotasKey,
  keyQuotasPageSize,
  type KeyQuotaItem,
} from '@modern/api/fork-key-quotas'
import { accessDetailKey, accessKeysKey, resetAccessQuota } from '@modern/api/access-keys'
import {
  credentialDetailKey,
  refreshCredentialQuota,
  resetCredentialQuota,
} from '@modern/api/credential-actions'
import { getGroupChannels } from '@modern/api/group-create'
import type { ResetCreditKind } from '@modern/api/credential-observation'
import { groupCredentialsKey } from '@modern/api/group-detail'
import { groupQueryKey } from '@modern/api/groups'
import { createOperationKey } from '@modern/features/groups/group-create-operation'
import {
  canUseResetCredit,
  resetCreditSupported,
  keyResetPeriods,
  keyResetRuleIDs,
  resetObservationUpdated,
  syncQuotaPage,
} from './data'

export function useAccountQuotas(page: MaybeRefOrGetter<number> = 1) {
  const client = useApiClient()
  const session = useAuthSession()
  const admin = computed(() => session.state.principalType === 'admin')
  const readable = computed(() => admin.value || session.state.principalType === 'access_key')
  const query = useQuery({
    queryKey: computed(() => [...accountQuotasKey, toValue(page), accountQuotasPageSize]),
    queryFn: ({ signal }) => {
      if (!readable.value) throw new RequestCancelledError()
      return getAccountQuotas(client, toValue(page), signal)
    },
    enabled: readable,
    staleTime: 30_000,
    gcTime: 60_000,
    retry: false,
  })
  // refetch bypasses enabled, so page-refresh and retry must also check the principal.
  const refresh = () => (readable.value ? query.refetch() : Promise.resolve())
  return { ...query, admin, refresh }
}

export function useKeyQuotas(page: MaybeRefOrGetter<number> = 1) {
  const client = useApiClient()
  const session = useAuthSession()
  const admin = computed(() => session.state.principalType === 'admin')
  const readable = computed(() => admin.value || session.state.principalType === 'access_key')
  const query = useQuery({
    queryKey: computed(() => [...keyQuotasKey, toValue(page), keyQuotasPageSize]),
    queryFn: ({ signal }) => {
      if (!readable.value) throw new RequestCancelledError()
      return getKeyQuotas(client, toValue(page), signal)
    },
    enabled: readable,
    staleTime: 30_000,
    gcTime: 60_000,
    retry: false,
  })
  const refresh = () => (readable.value ? query.refetch() : Promise.resolve())
  return { ...query, admin, refresh }
}

export type KeyResetState = 'pending' | 'succeeded' | 'failed' | 'unknown' | 'noRules'

export function useKeyQuotaReset(
  items: MaybeRefOrGetter<readonly KeyQuotaItem[]>,
  page: MaybeRefOrGetter<number>,
) {
  const client = useApiClient()
  const session = useAuthSession()
  const cache = useQueryClient()
  const revision = session.getRevision()
  const controller = new AbortController()
  const current = () =>
    !controller.signal.aborted &&
    session.state.principalType === 'admin' &&
    session.getRevision() === revision
  const selected = ref(new Set<number>())
  const periods = ref<number[]>([...keyResetPeriods])
  const target = ref<KeyQuotaItem[]>([])
  const pending = ref(false)
  const results = ref(new Map<number, KeyResetState>())
  const readFailed = ref(false)
  const selectedItems = computed(() => toValue(items).filter((item) => selected.value.has(item.id)))
  const canConfirm = computed(
    () => current() && target.value.some((item) => keyResetRuleIDs(item, periods.value).length),
  )
  watch(
    () => session.state.principalType,
    () => {
      if (!current()) {
        controller.abort()
        selected.value.clear()
        target.value = []
        results.value.clear()
      }
    },
    { flush: 'sync' },
  )
  watch(
    () => toValue(page),
    () => {
      selected.value.clear()
      target.value = []
    },
    { flush: 'sync' },
  )
  watch(
    () => toValue(items),
    (rows) => {
      selected.value = new Set(
        [...selected.value].filter((id) => rows.some((item) => item.id === id)),
      )
      if (!pending.value) target.value = []
    },
    { flush: 'sync' },
  )
  onScopeDispose(() => controller.abort())
  function select(item: KeyQuotaItem, value: boolean): void {
    if (!current() || pending.value || !toValue(items).some((row) => row.id === item.id)) return
    if (value && keyResetRuleIDs(item, keyResetPeriods).length) selected.value.add(item.id)
    else selected.value.delete(item.id)
  }
  function selectPeriod(seconds: number, value: boolean): void {
    if (pending.value || !keyResetPeriods.some((period) => period === seconds)) return
    periods.value = keyResetPeriods.filter((period) =>
      period === seconds ? value : periods.value.includes(period),
    )
  }
  function open(): void {
    if (!current() || pending.value || !selectedItems.value.length) return
    periods.value = [...keyResetPeriods]
    target.value = [...selectedItems.value]
  }
  async function reset(): Promise<void> {
    if (pending.value || !canConfirm.value) return
    const pageAtStart = toValue(page)
    const active = () => current() && toValue(page) === pageAtStart
    const targets = target.value.map((item) => ({
      item,
      ruleIDs: keyResetRuleIDs(item, periods.value),
    }))
    // Reject obsolete confirmations rather than reset a hidden or reconfigured Key.
    if (
      targets.some(({ item, ruleIDs }) => {
        const row = toValue(items).find((row) => row.id === item.id)
        return (
          !row || JSON.stringify(keyResetRuleIDs(row, periods.value)) !== JSON.stringify(ruleIDs)
        )
      })
    ) {
      target.value = []
      return
    }
    pending.value = true
    readFailed.value = false
    const attempted: number[] = []
    try {
      for (const { item, ruleIDs } of targets) {
        if (!active()) break
        selected.value.delete(item.id)
        if (!ruleIDs.length) {
          results.value.set(item.id, 'noRules')
          continue
        }
        attempted.push(item.id)
        results.value.set(item.id, 'pending')
        try {
          // Native mutation has no idempotency contract: one call per Key, never retry.
          await resetAccessQuota(client, item.id, ruleIDs, controller.signal)
          if (active()) results.value.set(item.id, 'succeeded')
        } catch (cause) {
          if (!active()) break
          results.value.set(
            item.id,
            cause instanceof ApiError && cause.status >= 400 && cause.status < 500
              ? 'failed'
              : 'unknown',
          )
        }
      }
    } finally {
      if (current() && attempted.length) {
        const reads = await Promise.allSettled([
          cache.invalidateQueries({ queryKey: keyQuotasKey }, { throwOnError: true }),
          cache.invalidateQueries({ queryKey: accessKeysKey }, { throwOnError: true }),
          ...attempted.map((id) =>
            cache.invalidateQueries({ queryKey: accessDetailKey(id) }, { throwOnError: true }),
          ),
          cache.invalidateQueries({ queryKey: ['modern', 'home'] }, { throwOnError: true }),
          cache.invalidateQueries({ queryKey: ['modern', 'health'] }, { throwOnError: true }),
        ])
        if (current()) readFailed.value = reads.some((read) => read.status === 'rejected')
      }
      if (current()) target.value = []
      pending.value = false
    }
  }
  return {
    selected,
    selectedItems,
    periods,
    target,
    pending,
    results,
    readFailed,
    canConfirm,
    select,
    selectPeriod,
    open,
    reset,
  }
}

export function useAccountResetCredits(items: MaybeRefOrGetter<readonly AccountQuotaItem[]>) {
  const client = useApiClient()
  const session = useAuthSession()
  const cache = useQueryClient()
  const sessionRevision = session.getRevision()
  const admin = computed(() => session.state.principalType === 'admin')
  const channels = useQuery({
    queryKey: ['modern', 'group-channels'],
    queryFn: ({ signal }) => {
      if (!admin.value) throw new RequestCancelledError()
      return getGroupChannels(client, signal)
    },
    enabled: admin,
    retry: false,
    staleTime: 60_000,
  })
  const supported = (item: AccountQuotaItem) =>
    resetCreditSupported(
      item,
      Boolean(channels.data.value?.find((channel) => channel.id === item.channelID)?.resetCredit),
    )
  const target = ref<AccountQuotaItem>()
  const targetType = ref<ResetCreditKind>()
  const pending = ref(false)
  const errors = ref(new Map<string, string>())
  const notices = ref(new Map<string, string>())
  const waiting = ref(new Map<string, number>())
  const keys = new Map<string, string>()
  const identity = (item: AccountQuotaItem) => `${item.groupID}:${item.credential.id}`
  const controller = new AbortController()
  watch(admin, (value) => {
    if (!value) controller.abort()
  })
  onScopeDispose(() => controller.abort())
  watch(
    () => toValue(items),
    (rows) => {
      for (const item of rows) {
        const id = identity(item)
        const after = waiting.value.get(id)
        if (after !== undefined && resetObservationUpdated(item.credential.observation, after)) {
          waiting.value.delete(id)
          notices.value.delete(id)
        }
      }
    },
  )
  const operationIdentity = (item: AccountQuotaItem, kind?: ResetCreditKind) =>
    `${identity(item)}:${kind ?? 'default'}`
  function allowed(item: AccountQuotaItem, kind?: ResetCreditKind): boolean {
    return (
      admin.value &&
      !controller.signal.aborted &&
      session.getRevision() === sessionRevision &&
      !pending.value &&
      !waiting.value.has(identity(item)) &&
      // An unresolved operation must be replayed with its original type/key first.
      ![...keys.keys()].some(
        (key) => key.startsWith(identity(item) + ':') && key !== operationIdentity(item, kind),
      ) &&
      (keys.has(operationIdentity(item, kind)) ||
        canUseResetCredit(item, supported(item), Date.now(), kind))
    )
  }
  function open(item: AccountQuotaItem, kind?: ResetCreditKind): void {
    if (!allowed(item, kind)) return
    const id = identity(item)
    errors.value.delete(id)
    target.value = item
    targetType.value = kind
  }
  async function consume(): Promise<void> {
    const item = target.value
    const kind = targetType.value
    if (!item || !allowed(item, kind) || controller.signal.aborted) return
    const id = identity(item)
    const operation = operationIdentity(item, kind)
    if (!keys.has(operation)) keys.set(operation, createOperationKey())
    const key = keys.get(operation)!
    const revision = session.getRevision()
    const current = () =>
      !controller.signal.aborted && admin.value && session.getRevision() === revision
    pending.value = true
    errors.value.delete(id)
    try {
      const result = await resetCredentialQuota(
        client,
        item.groupID,
        item.credential.id,
        key,
        controller.signal,
        kind,
      )
      if (!current()) return
      keys.delete(operation)
      target.value = undefined
      notices.value.set(
        id,
        result.pending ? 'credentialCards.resetPending' : 'credentialCards.resetSucceeded',
      )
      const previousAt = item.credential.observation?.observedAt ?? 0
      waiting.value.set(
        id,
        result.pending ? Math.max(previousAt, result.observation?.observedAt ?? 0) : previousAt,
      )
      const reads = await Promise.allSettled([
        cache.invalidateQueries({ queryKey: accountQuotasKey }, { throwOnError: true }),
        cache.invalidateQueries({
          queryKey: credentialDetailKey(item.groupID, item.credential.id),
        }),
        cache.invalidateQueries({ queryKey: groupCredentialsKey(item.groupID) }),
        cache.invalidateQueries({ queryKey: groupQueryKey }),
        cache.invalidateQueries({ queryKey: ['modern', 'home', 'accounts'] }),
        cache.invalidateQueries({ queryKey: ['modern', 'health'] }),
      ])
      if (current() && reads.some((read) => read.status === 'rejected'))
        notices.value.set(id, 'accountQuotas.resetReadFailed')
    } catch (cause) {
      if (!current()) return
      const preparation =
        cause instanceof ApiError && cause.code === 'RESET_CREDIT_PREPARATION_FAILED'
      const unknown =
        !preparation &&
        (!(cause instanceof ApiError) ||
          cause.code === 'RESET_CREDIT_OUTCOME_UNKNOWN' ||
          cause.status >= 500)
      if (!unknown) keys.delete(operation)
      errors.value.set(
        id,
        preparation
          ? 'credentialCards.resetPreparationFailed'
          : unknown
            ? 'credentialCards.resetUnknown'
            : 'credentialCards.actionFailed',
      )
    } finally {
      pending.value = false
    }
  }
  return {
    channels,
    supported,
    target,
    targetType,
    pending,
    errors,
    notices,
    waiting,
    identity,
    allowed,
    open,
    consume,
  }
}

export function useAccountQuotaSync() {
  const client = useApiClient()
  const session = useAuthSession()
  const queryClient = useQueryClient()
  const syncing = ref(false)
  const active = ref('')
  const errors = ref(new Set<string>())
  const failedReads = ref(false)
  const controller = new AbortController()
  const identity = (item: AccountQuotaItem) => `${item.groupID}:${item.credential.id}`
  watch(
    () => session.state.principalType,
    (principal) => {
      if (principal !== 'admin') controller.abort()
    },
  )
  onScopeDispose(() => controller.abort())

  async function sync(items: readonly AccountQuotaItem[]): Promise<void> {
    if (syncing.value || session.state.principalType !== 'admin' || controller.signal.aborted)
      return
    const revision = session.getRevision()
    const allowed = () =>
      !controller.signal.aborted &&
      session.state.principalType === 'admin' &&
      session.getRevision() === revision
    // Snapshot only the current page. Serial execution prevents upstream bursts and duplicate clicks.
    const targets = items.filter((item) => item.quotaSupported)
    if (!targets.length) return
    syncing.value = true
    failedReads.value = false
    try {
      await syncQuotaPage(
        targets,
        async (item) => {
          active.value = identity(item)
          errors.value.delete(active.value)
          const observation = await refreshCredentialQuota(
            client,
            item.groupID,
            item.credential.id,
            controller.signal,
          )
          if (allowed() && (!observation || ['error', 'unavailable'].includes(observation.state)))
            errors.value.add(identity(item))
        },
        (item) => errors.value.add(identity(item)),
        allowed,
      )
    } finally {
      active.value = ''
      if (allowed()) {
        // These GETs read stored observations only; none triggers an upstream quota refresh.
        const results = await Promise.allSettled([
          queryClient.invalidateQueries({ queryKey: accountQuotasKey }, { throwOnError: true }),
          ...[...new Set(targets.map((item) => item.groupID))].map((id) =>
            queryClient.invalidateQueries({ queryKey: groupCredentialsKey(id) }),
          ),
          ...targets.map((item) =>
            queryClient.invalidateQueries({
              queryKey: credentialDetailKey(item.groupID, item.credential.id),
            }),
          ),
        ])
        failedReads.value = results.some((result) => result.status === 'rejected')
      }
      syncing.value = false
    }
  }
  return { syncing, active, errors, failedReads, identity, sync }
}
