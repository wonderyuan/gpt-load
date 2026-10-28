import type { ApiClient } from '@shared/http/client'
import { InvalidResponseError } from '@shared/http/errors'
import { credentialStates, type CredentialRow } from './group-detail'
import { readObservation, type ResetCreditKind } from './credential-observation'
import { boolean, integer, list, oneOf, record, text } from './response'
import { maskedAccountLabel } from '../features/account-quotas/data'

export const accountQuotasKey = ['modern', 'fork-account-quotas'] as const
export const accountQuotasPageSize = 100
export type AccountQuotaCredential = Pick<
  CredentialRow,
  | 'id'
  | 'name'
  | 'label'
  | 'mask'
  | 'account'
  | 'state'
  | 'enabled'
  | 'authState'
  | 'expiresAt'
  | 'observation'
>

// The aggregate endpoint does not enrich proxy, recovery or activity details.
// Read only the fields this overview uses; never invent routing defaults.
export function readAccountQuotaCredential(value: unknown): AccountQuotaCredential {
  const raw = record(value)
  const account = raw.account == null ? null : record(raw.account)
  const id = integer(raw.credential_id, 1)
  const name = text(raw.name ?? '')
  const mask = text(raw.mask)
  const emailMask = account?.email_mask == null ? '' : text(account.email_mask)
  return {
    id,
    name,
    mask,
    label: maskedAccountLabel(id, name, mask, emailMask),
    account: emailMask,
    state: oneOf(raw.effective_status, credentialStates),
    enabled: oneOf(raw.configured_status, ['active', 'disabled']) === 'active',
    authState: oneOf(raw.auth_state, [
      'ready',
      'refreshing',
      'reauthorization_required',
      'outcome_unknown',
    ]),
    expiresAt: account?.expires_at_ms == null ? undefined : integer(account.expires_at_ms),
    observation: readObservation(raw.observation),
  }
}

export interface AccountCycleEstimate {
  windowID: string
  sourceID?: string
  windowSeconds: number
  resetsAt: number
  unit: 'usd' | 'glm_coding_credits'
  total: string
  used: string
  remaining: string
  method: 'provider' | 'fixed_plan' | 'observed_ratio'
  estimated: boolean
}
function readCycleEstimate(value: unknown): AccountCycleEstimate {
  const raw = record(value)
  const decimal = (value: unknown): string => {
    const result = text(value)
    if (!/^(0|[1-9]\d*)(\.\d{1,9})?$/u.test(result)) throw new InvalidResponseError()
    return result
  }
  const windowID = text(raw.window_id)
  const sourceID = raw.source_id === undefined ? undefined : text(raw.source_id)
  if (!windowID.trim() || (sourceID !== undefined && !sourceID.trim()))
    throw new InvalidResponseError()
  return {
    windowID,
    ...(sourceID === undefined ? {} : { sourceID }),
    windowSeconds: integer(raw.window_seconds, 1),
    resetsAt: integer(raw.reset_at_ms, 1),
    unit: oneOf(raw.unit, ['usd', 'glm_coding_credits']),
    total: decimal(raw.total),
    used: decimal(raw.used),
    remaining: decimal(raw.remaining),
    method: oneOf(raw.method, ['provider', 'fixed_plan', 'observed_ratio']),
    estimated: boolean(raw.estimated),
  }
}
export interface AccountQuotaItem {
  groupID: number
  groupName: string
  channelID: string
  channelName: string
  quotaSupported: boolean
  resetSupported?: boolean
  resetTypes?: ResetCreditKind[]
  credential: AccountQuotaCredential
  cycleEstimates?: AccountCycleEstimate[]
}
export interface AccountQuotaPage {
  observedAt: number
  items: AccountQuotaItem[]
  total: number
  page: number
  pageSize: number
}
export async function getAccountQuotas(
  client: ApiClient,
  page: number,
  signal: AbortSignal,
): Promise<AccountQuotaPage> {
  const params = new URLSearchParams({
    page: String(page),
    page_size: String(accountQuotasPageSize),
  })
  const data = record(await client.request(`/api/fork/account-quotas?${params}`, { signal }))
  const result: AccountQuotaPage = {
    observedAt: integer(data.observed_at_ms),
    total: integer(data.total),
    page: integer(data.page, 1),
    pageSize: integer(data.page_size, 1),
    items: list(data.items).map((value) => {
      const item = record(value)
      const credential = readAccountQuotaCredential(item.credential)
      const cycleEstimates = list(item.cycle_estimates ?? []).map(readCycleEstimate)
      const identities = cycleEstimates.map((estimate) =>
        JSON.stringify([
          estimate.windowID,
          estimate.sourceID,
          estimate.windowSeconds,
          estimate.resetsAt,
        ]),
      )
      if (new Set(identities).size !== identities.length) throw new InvalidResponseError()
      return {
        groupID: integer(item.group_id, 1),
        groupName: text(item.group_name),
        channelID: text(item.channel_id),
        channelName: text(item.channel_name),
        quotaSupported: boolean(item.quota_supported),
        ...(item.reset_supported === undefined
          ? {}
          : { resetSupported: boolean(item.reset_supported) }),
        ...(item.reset_types === undefined
          ? {}
          : {
              resetTypes: list(item.reset_types).map((kind) =>
                oneOf(kind, ['five_hour', 'week'] as const),
              ),
            }),
        credential,
        cycleEstimates,
      }
    }),
  }
  const identities = new Set(result.items.map((item) => `${item.groupID}:${item.credential.id}`))
  if (
    result.page !== page ||
    result.pageSize !== accountQuotasPageSize ||
    result.items.length > result.pageSize ||
    result.items.length > result.total ||
    identities.size !== result.items.length
  )
    throw new InvalidResponseError()
  return result
}
