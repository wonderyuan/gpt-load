import type { ApiClient } from '@shared/http/client'
import { InvalidResponseError } from '@shared/http/errors'
import { boolean, integer, list, oneOf, record, text } from './response'

export const keyQuotasKey = ['modern', 'fork-key-quotas'] as const
export const keyQuotasPageSize = 100
export interface QuotaGroup {
  id: number
  name: string
  channelID: string
  channelName: string
  enabled: boolean
}
export interface KeyAccounting {
  unit: 'usd' | 'glm_coding_credits' | 'mixed' | 'unknown'
  internalUnitsPerCredit?: '0.01'
  basis?: 'current_config'
}
export interface KeyQuotaRule {
  id: number
  kind: 'total' | 'periodic'
  limitUSD: string
  periodSeconds?: number
}
export interface KeyQuotaWindow extends KeyQuotaRule {
  usedUSD: string
  remainingUSD: string
  status: 'available' | 'inactive' | 'exhausted'
  startsAt: number | null
  endsAt: number | null
}
export interface KeyQuotaItem {
  id: number
  name: string
  mask: string
  status: 'active' | 'disabled'
  expiresAt: number | null
  groupIDs: number[]
  accounting: KeyAccounting
  rules: KeyQuotaRule[]
  quota: {
    allowed: boolean
    recoverable: boolean
    nextAvailableAt: number | null
    observedAt: number
    windows: KeyQuotaWindow[]
  } | null
}
export interface KeyQuotaPage {
  observedAt: number
  groups: QuotaGroup[]
  items: KeyQuotaItem[]
  total: number
  page: number
  pageSize: number
}

const timestamp = (value: unknown) => (value == null ? null : integer(value))
function decimal(value: unknown): string {
  const result = text(value)
  if (!/^(0|[1-9]\d*)(\.\d{1,9})?$/u.test(result)) throw new InvalidResponseError()
  return result
}
// The access-key parser is intentionally not reused: its full management DTO requires
// unrelated fields and could accidentally retain secrets. Whitelist this read-only view.
function readRule(value: unknown): KeyQuotaRule {
  const row = record(value)
  const kind = oneOf(row.kind, ['total', 'periodic'])
  return {
    id: integer(row.id, 1),
    kind,
    limitUSD: decimal(row.limit_usd),
    ...(kind === 'periodic' ? { periodSeconds: integer(row.period_seconds, 60) } : {}),
  }
}
function readWindow(value: unknown): KeyQuotaWindow {
  const row = record(value)
  return {
    ...readRule(row),
    usedUSD: decimal(row.used_usd),
    remainingUSD: decimal(row.remaining_usd),
    status: oneOf(row.status, ['available', 'inactive', 'exhausted']),
    startsAt: timestamp(row.window_started_at_ms),
    endsAt: timestamp(row.window_ends_at_ms),
  }
}
function unique<T>(values: T[], identity: (value: T) => number): T[] {
  if (new Set(values.map(identity)).size !== values.length) throw new InvalidResponseError()
  return values
}
function readItem(value: unknown): KeyQuotaItem {
  const row = record(value)
  const accounting = record(row.accounting)
  const unit = oneOf(accounting.unit, ['usd', 'glm_coding_credits', 'mixed', 'unknown'])
  if (
    unit === 'glm_coding_credits'
      ? accounting.internal_units_per_credit !== '0.01'
      : accounting.internal_units_per_credit != null
  )
    throw new InvalidResponseError()
  const quota = row.cost_limit_status == null ? null : record(row.cost_limit_status)
  return {
    id: integer(row.id, 1),
    name: text(row.name),
    mask: text(row.masked_key),
    status: oneOf(row.status, ['active', 'disabled']),
    expiresAt: timestamp(row.expires_at_ms),
    groupIDs: unique(
      list(row.group_ids).map((id) => integer(id, 1)),
      (id) => id,
    ),
    accounting: {
      unit,
      ...(unit === 'glm_coding_credits' ? { internalUnitsPerCredit: '0.01' } : {}),
      ...(accounting.basis == null ? {} : { basis: oneOf(accounting.basis, ['current_config'] as const) }),
    },
    rules: unique(list(row.cost_limit_rules).map(readRule), (rule) => rule.id),
    quota: quota
      ? {
          allowed: boolean(quota.allowed),
          recoverable: boolean(quota.recoverable),
          nextAvailableAt: timestamp(quota.next_available_at_ms),
          observedAt: integer(quota.observed_at_ms),
          windows: unique(list(quota.rules).map(readWindow), (window) => window.id),
        }
      : null,
  }
}
export async function getKeyQuotas(
  client: ApiClient,
  page: number,
  signal: AbortSignal,
): Promise<KeyQuotaPage> {
  const params = new URLSearchParams({ page: String(page), page_size: String(keyQuotasPageSize) })
  const data = record(await client.request(`/api/fork/key-quotas?${params}`, { signal }))
  const result: KeyQuotaPage = {
    observedAt: integer(data.observed_at_ms),
    total: integer(data.total),
    page: integer(data.page, 1),
    pageSize: integer(data.page_size, 1),
    groups: unique(
      list(data.groups).map((value): QuotaGroup => {
        const group = record(value)
        return {
          id: integer(group.id, 1),
          name: text(group.name),
          channelID: text(group.channel_id),
          channelName: text(group.channel_name),
          enabled: boolean(group.enabled),
        }
      }),
      (group) => group.id,
    ),
    items: unique(list(data.items).map(readItem), (item) => item.id),
  }
  if (
    result.page !== page ||
    result.pageSize !== keyQuotasPageSize ||
    result.items.length > result.pageSize ||
    result.items.length > result.total
  )
    throw new InvalidResponseError()
  return result
}
