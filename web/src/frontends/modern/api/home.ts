import { readConcurrency, type ConcurrencyView } from '@shared/concurrency'
import type { ApiClient } from '@shared/http/client'
import { readAccessKeyRow, type AccessKeyRow } from './access-keys'
import { readCredential } from './group-detail'
import { readAccountQuotaCredential, type AccountQuotaCredential } from './fork-account-quotas'
import { readCredentialFilterKey } from './groups'
import { boolean, integer, list, oneOf, record, text } from './response'
import { readHealthReport, type HealthReport } from './health'
import { availabilityStates, type GroupAvailability } from './groups'

export interface HomeKey {
  id: number
  name: string
  mask: string
}
export interface HomeBase {
  concurrency: ConcurrencyView | null
  observedAt: number
  startedAt: number
  version: string
  groups: number
  credentials: number
  available: number
  models: number
  keys: HomeKey[]
  currentKey: AccessKeyRow | null
  creditAccounting: boolean
}
export interface HomeAccount {
  key: string
  channelID: string
  channelName: string
  channelIcon: string
  channelMark: string
  groups: number
  availableGroups: number
  groupID: number | null
  credential: AccountQuotaCredential
}
export async function getHome(client: ApiClient, signal: AbortSignal): Promise<HomeBase> {
  return readHomeBase(await client.request('/api/home', { signal }))
}

export function readHomeBase(value: unknown): HomeBase {
  const data = record(value)
  const inventory = record(data.inventory)
  const accounting = data.key_accounting == null ? null : record(data.key_accounting)
  if (accounting) {
    oneOf(accounting.unit, ['glm_coding_credits'])
    oneOf(accounting.internal_units_per_credit, ['0.01'])
  }
  return {
    creditAccounting: accounting !== null,
    concurrency: data.concurrency == null ? null : readConcurrency(data.concurrency),
    observedAt: integer(data.server_now_ms),
    startedAt: integer(data.started_at_ms),
    version: text(data.version),
    groups: integer(inventory.group_count),
    credentials: integer(inventory.credential_count),
    available: integer(inventory.available_credential_count),
    models: integer(inventory.model_count),
    keys: list(data.access_keys).map((value) => {
      const key = record(value)
      return {
        id: integer(key.id, 1),
        name: text(key.name),
        mask: text(key.masked_key),
      }
    }),
    currentKey: data.current_access_key == null ? null : readAccessKeyRow(data.current_access_key),
  }
}
export interface HomeOverviewGroup {
  id: number
  name: string
  channelID: string
  channelName: string
  channelIcon: string
  channelMark: string
  enabled: boolean
  availability: GroupAvailability
  modelCount: number
  credentialCount: number
}
export interface HomeOverview {
  observedAt: number
  base: HomeBase
  statistics: HomeStatistics
  groups: HomeOverviewGroup[]
  accounts: { observedAt: number; items: HomeAccount[] }
  health: HealthReport
}
export async function getHomeOverview(
  client: ApiClient,
  signal: AbortSignal,
): Promise<HomeOverview> {
  const raw = record(await client.request('/api/fork/home-overview', { signal }))
  const accounts = record(raw.accounts)
  return {
    observedAt: integer(raw.observed_at_ms),
    base: readHomeBase(raw.base),
    statistics: readHomeStatistics(raw.statistics),
    groups: list(raw.groups).map((value): HomeOverviewGroup => {
      const group = record(value)
      return {
        id: integer(group.id, 1),
        name: text(group.name),
        channelID: text(group.channel_id),
        channelName: text(group.channel_name),
        channelIcon: text(group.channel_icon),
        channelMark: text(group.channel_mark),
        enabled: boolean(group.enabled),
        availability: oneOf(group.availability, availabilityStates),
        modelCount: integer(group.model_count),
        credentialCount: integer(group.credential_count),
      }
    }),
    accounts: {
      observedAt: integer(accounts.observed_at_ms),
      items: list(accounts.items).map((value): HomeAccount => {
        const account = record(value)
        return {
          key: readCredentialFilterKey(account.credential_key),
          channelID: text(account.channel_id),
          channelName: text(account.channel_name),
          channelIcon: text(account.channel_icon),
          channelMark: text(account.channel_mark),
          groups: integer(account.group_count),
          availableGroups: integer(account.available_group_count),
          groupID: account.group_id == null ? null : integer(account.group_id, 1),
          credential: readAccountQuotaCredential(account.credential),
        }
      }),
    },
    health: readHealthReport(raw.health),
  }
}

export async function getHomeAccounts(client: ApiClient, signal: AbortSignal) {
  const data = record(await client.request('/api/home/subscription-accounts', { signal }))
  return {
    observedAt: integer(data.observed_at_ms),
    items: list(data.items).map((value): HomeAccount => {
      const account = record(value)
      return {
        key: readCredentialFilterKey(account.credential_key),
        channelID: text(account.channel_id),
        channelName: text(account.channel_name),
        channelIcon: text(account.channel_icon),
        channelMark: text(account.channel_mark),
        groups: integer(account.group_count),
        groupID: account.group_id == null ? null : integer(account.group_id, 1),
        availableGroups: integer(account.available_group_count),
        credential: readCredential(account.credential),
      }
    }),
  }
}

export interface HomeTrendPoint {
  startMs: number
  requests: number
  failures: number
}
export interface HomeStatistics {
  observedAt: number
  fromMs: number
  toMs: number
  requests: number
  failures: number
  series: HomeTrendPoint[]
}
export const homeStatisticsKey = ['modern', 'home', 'statistics'] as const
/* 首页只要 24 小时的请求量与失败数；排行留给用量统计页，不在这里重复一份。 */
export async function getHomeStatistics(
  client: ApiClient,
  signal: AbortSignal,
): Promise<HomeStatistics> {
  return readHomeStatistics(await client.request('/api/home/statistics?range=24h', { signal }))
}

export function readHomeStatistics(value: unknown): HomeStatistics {
  const data = record(value)
  const summary = record(data.summary)
  return {
    observedAt: integer(data.observed_at_ms),
    fromMs: integer(data.from_ms),
    toMs: integer(data.to_ms),
    requests: integer(summary.request_count),
    failures: integer(summary.failure_count),
    series: list(data.series).map((value) => {
      const point = record(value)
      return {
        startMs: integer(point.bucket_start_ms),
        requests: integer(point.request_count),
        failures: integer(point.failure_count),
      }
    }),
  }
}
