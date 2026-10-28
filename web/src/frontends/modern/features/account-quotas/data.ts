import type {
  CredentialObservation,
  CredentialQuota,
  ResetCreditKind,
  ResetCreditDetail,
} from '../../api/credential-observation'
import type { AccountCycleEstimate, AccountQuotaItem } from '../../api/fork-account-quotas'
import type {
  KeyAccounting,
  KeyQuotaItem,
  KeyQuotaRule,
  KeyQuotaWindow,
  QuotaGroup,
} from '../../api/fork-key-quotas'

export function maskedAccountLabel(
  id: number,
  name: string,
  mask: string,
  emailMask: string,
): string {
  const identity = emailMask.trim() || mask.trim()
  return [name.trim(), identity].filter(Boolean).join(' · ') || `#${id}`
}
export function quotaWindowState(
  observation: CredentialObservation | undefined,
  window: CredentialQuota,
  now: number,
): 'available' | 'exhausted' | 'unknown' | 'expired' | 'unavailable' {
  if (!observation || observation.state === 'unavailable') return 'unavailable'
  if (window.resetsAt !== undefined && window.resetsAt <= now) return 'expired'
  return window.state
}
export function quotaWindowValuesAvailable(
  observation: CredentialObservation | undefined,
  window: CredentialQuota,
  now: number,
): boolean {
  return ['available', 'exhausted'].includes(quotaWindowState(observation, window, now))
}
export function indexAccountQuotas(
  items: readonly AccountQuotaItem[],
): Map<number, AccountQuotaItem[]> {
  const index = new Map<number, AccountQuotaItem[]>()
  for (const item of items) {
    const rows = index.get(item.groupID) ?? []
    rows.push(item)
    index.set(item.groupID, rows)
  }
  return index
}
export function visibleQuotaWindows(windows: readonly CredentialQuota[]): CredentialQuota[] {
  return windows.filter(
    (window) =>
      !(
        window.scope === 'credits' &&
        window.unit === 'usd' &&
        window.state !== 'unknown' &&
        window.used === 0 &&
        window.limit === 0 &&
        window.remaining === 0
      ),
  )
}
export function compactQuotaWindows(windows: readonly CredentialQuota[]): CredentialQuota[] {
  // Do not substitute a model quota for an account quota, or merge distinct cycles.
  return visibleQuotaWindows(windows).filter(
    (window) => window.windowSeconds === 18000 || window.windowSeconds === 604800,
  )
}
export interface QuotaGroupSection {
  id: number
  name: string
  channelID: string
  channelName: string
  enabled?: boolean
  accounts: AccountQuotaItem[]
  keys: KeyQuotaItem[]
}
export function groupQuotaSections(
  groups: readonly QuotaGroup[],
  accounts: readonly AccountQuotaItem[],
  keys: readonly KeyQuotaItem[],
): { groups: QuotaGroupSection[]; shared: KeyQuotaItem[] } {
  const sections = new Map<number, QuotaGroupSection>(
    groups.map((group) => [group.id, { ...group, accounts: [], keys: [] }]),
  )
  for (const account of accounts) {
    let section = sections.get(account.groupID)
    if (!section) {
      // A failed/configuration-racing group read must not hide a stored account.
      section = {
        id: account.groupID,
        name: account.groupName,
        channelID: account.channelID,
        channelName: account.channelName,
        accounts: [],
        keys: [],
      }
      sections.set(section.id, section)
    }
    section.accounts.push(account)
  }
  const shared: KeyQuotaItem[] = []
  for (const key of keys) {
    const section = key.groupIDs.length === 1 ? sections.get(key.groupIDs[0]!) : undefined
    if (section) section.keys.push(key)
    else shared.push(key)
  }
  return { groups: [...sections.values()], shared }
}

export const nanoPerCredit = 10_000_000n
const nanoPerInternalUnit = 1_000_000_000n
export function quotaNano(value: string): bigint {
  if (!/^(0|[1-9]\d*)(\.\d{1,9})?$/u.test(value)) throw new Error('INVALID_QUOTA_AMOUNT')
  const [whole = '0', fraction = ''] = value.split('.')
  return BigInt(whole) * nanoPerInternalUnit + BigInt(fraction.padEnd(9, '0'))
}
function formatQuotaAmount(
  nano: bigint,
  exactDigits: number,
  displayDigits: number,
  usd: boolean,
  locale: string,
  precision: 'display' | 'exact',
): string {
  const digits = precision === 'exact' ? exactDigits : displayDigits
  const scale = 10n ** BigInt(exactDigits - digits)
  const divisor = 10n ** BigInt(digits)
  // Round only the display integer, never the stored amount or accounting data.
  const rounded = precision === 'exact' ? nano : (nano + scale / 2n) / scale
  const lessThanMinimum = nano > 0n && rounded === 0n
  const shown = lessThanMinimum ? 1n : rounded
  const whole = new Intl.NumberFormat(locale).format(shown / divisor)
  const fraction = (shown % divisor).toString().padStart(digits, '0').replace(/0+$/u, '')
  const separator =
    new Intl.NumberFormat(locale).formatToParts(1.1).find((part) => part.type === 'decimal')
      ?.value ?? '.'
  const amount = whole + (fraction ? separator + fraction : '')
  return (lessThanMinimum ? '<' : '') + (usd ? '$' : '') + amount
}
export function keyAmount(
  value: string,
  accounting: KeyAccounting,
  locale: string,
  precision: 'display' | 'exact' = 'display',
): string {
  const credits =
    accounting.unit === 'glm_coding_credits' && accounting.internalUnitsPerCredit === '0.01'
  return formatQuotaAmount(
    quotaNano(value),
    credits ? 7 : 9,
    credits ? 3 : 4,
    accounting.unit === 'usd',
    locale,
    precision,
  )
}
export function accountCycleAmount(
  value: string,
  unit: AccountCycleEstimate['unit'],
  locale: string,
  precision: 'display' | 'exact' = 'display',
): string {
  // The cycle DTO reports real credits, unlike the Key ledger's internal 0.01 units.
  return formatQuotaAmount(
    quotaNano(value),
    9,
    unit === 'usd' ? 4 : 3,
    unit === 'usd',
    locale,
    precision,
  )
}
export function accountCycleEstimate(
  item: Pick<AccountQuotaItem, 'cycleEstimates'>,
  window: CredentialQuota,
): AccountCycleEstimate | undefined {
  // Never reuse a prior cycle or another model/source's estimate.
  return item.cycleEstimates?.find(
    (estimate) =>
      (window.scope === 'account' ||
        (window.scope === 'credits' && window.unit === 'usd' && estimate.method === 'provider')) &&
      !window.models.length &&
      estimate.windowID === window.id &&
      estimate.sourceID === window.sourceId &&
      estimate.windowSeconds === window.windowSeconds &&
      estimate.resetsAt === window.resetsAt,
  )
}
export function keyRemainingPercent(window: KeyQuotaWindow): number | undefined {
  const limit = quotaNano(window.limitUSD)
  if (limit === 0n) return undefined
  const remaining = quotaNano(window.remainingUSD)
  const bounded = remaining > limit ? limit : remaining
  // Only the bounded two-decimal percentage enters Number, never a monetary value.
  return Number((bounded * 10_000n) / limit) / 100
}
export interface KeyQuotaEntry {
  kind: 'total' | 'periodic'
  periodSeconds?: number
  rule?: KeyQuotaRule
  window?: KeyQuotaWindow
}
export function keyQuotaEntries(item: KeyQuotaItem): KeyQuotaEntry[] {
  const entries: KeyQuotaEntry[] = item.rules.map((rule) => ({
    kind: rule.kind,
    periodSeconds: rule.periodSeconds,
    rule,
    window: item.quota?.windows.find((window) => window.id === rule.id),
  }))
  for (const window of item.quota?.windows ?? []) {
    if (!item.rules.some((rule) => rule.id === window.id))
      entries.push({ kind: window.kind, periodSeconds: window.periodSeconds, window })
  }
  for (const seconds of [18000, 604800]) {
    if (!entries.some((entry) => entry.kind === 'periodic' && entry.periodSeconds === seconds))
      entries.push({ kind: 'periodic', periodSeconds: seconds })
  }
  if (!entries.some((entry) => entry.kind === 'total')) entries.push({ kind: 'total' })
  const order = (entry: KeyQuotaEntry) =>
    entry.periodSeconds === 18000 ? 0 : entry.periodSeconds === 604800 ? 1 : 2
  return entries.sort((a, b) => order(a) - order(b))
}
export function keyDisplayEntries(item: KeyQuotaItem): KeyQuotaEntry[] {
  // Total rules still govern item.quota.allowed; they are not a cycle chart.
  return keyQuotaEntries(item).filter((entry) => entry.kind === 'periodic')
}
export const keyResetPeriods = [18000, 604800] as const
export function keyResetRuleIDs(item: KeyQuotaItem, periods: readonly number[]): number[] {
  // Only persisted rules are resettable; display entries can contain synthetic placeholders.
  return item.rules
    .filter(
      (rule) =>
        rule.kind === 'periodic' &&
        keyResetPeriods.some((seconds) => seconds === rule.periodSeconds) &&
        periods.includes(rule.periodSeconds!),
    )
    .map((rule) => rule.id)
}
export function keyQuotaLabel(item: KeyQuotaItem): string {
  return item.name.trim() || item.mask || '#' + item.id
}
export function keyUsedPercent(window: KeyQuotaWindow): number | undefined {
  const limit = quotaNano(window.limitUSD)
  if (limit === 0n) return undefined
  const used = quotaNano(window.usedUSD)
  return Number(((used > limit ? limit : used) * 10_000n) / limit) / 100
}
export function keyQuotaState(
  entry: KeyQuotaEntry,
  now: number,
): 'unconfigured' | 'unknown' | 'expired' | 'available' | 'inactive' | 'exhausted' {
  const { rule, window } = entry
  if (!rule && !window) return 'unconfigured'
  if (
    !rule ||
    !window ||
    rule.kind !== window.kind ||
    rule.periodSeconds !== window.periodSeconds ||
    quotaNano(rule.limitUSD) !== quotaNano(window.limitUSD)
  )
    return 'unknown'
  if (window.endsAt !== null && window.endsAt <= now) return 'expired'
  return window.status
}
export function keyQuotaValuesAvailable(entry: KeyQuotaEntry, now: number): boolean {
  return ['available', 'inactive', 'exhausted'].includes(keyQuotaState(entry, now))
}

export function resetCreditRows(observation: CredentialObservation | undefined, now: number) {
  const details: ResetCreditDetail[] =
    observation?.creditDetails ??
    observation?.creditExpirations.map((expiresAt) => ({ expiresAt })) ??
    []
  return details.map((card, index) => ({
    ...card,
    state:
      card.expiresAt !== undefined && card.expiresAt <= now
        ? ('expired' as const)
        : observation?.state === 'fresh' && (card.available ?? index < observation.resetCredits)
          ? ('available' as const)
          : ('unknown' as const),
  }))
}
export function resetCreditSupported(item: AccountQuotaItem, channelSupported: boolean): boolean {
  return item.resetSupported ?? channelSupported
}
export function accountResetTypes(item: AccountQuotaItem): (ResetCreditKind | undefined)[] {
  return item.resetTypes?.length ? [...new Set(item.resetTypes)] : [undefined]
}
export function resetCreditActionKey(kind?: ResetCreditKind): string {
  return kind === 'five_hour'
    ? 'accountQuotas.useFiveHour'
    : kind === 'week'
      ? 'accountQuotas.useWeek'
      : 'accountQuotas.useOne'
}
export function resetObservationUpdated(
  observation: CredentialObservation | undefined,
  after: number,
): boolean {
  return (
    observation?.state === 'fresh' &&
    observation.error !== 'observation_partial' &&
    observation.resetCreditsKnown !== false &&
    (observation.observedAt ?? 0) > after
  )
}
export function canUseResetCredit(
  item: AccountQuotaItem,
  supported: boolean,
  now: number,
  kind?: ResetCreditKind,
): boolean {
  const observation = item.credential.observation
  const available = resetCreditRows(observation, now).some(
    (card) => card.kind === kind && card.state === 'available',
  )
  return (
    supported &&
    accountResetTypes(item).includes(kind) &&
    item.credential.enabled &&
    item.credential.authState === 'ready' &&
    (item.credential.expiresAt === undefined || item.credential.expiresAt > now) &&
    observation?.state === 'fresh' &&
    observation.resetCreditsKnown !== false &&
    observation.resetCredits > 0 &&
    (available || (kind === undefined && !observation.creditExpirations.length))
  )
}

export async function syncQuotaPage<T>(
  items: readonly T[],
  run: (item: T) => Promise<void>,
  failed: (item: T) => void,
  canContinue: () => boolean,
): Promise<void> {
  for (const item of items) {
    if (!canContinue()) break
    try {
      await run(item)
    } catch {
      if (canContinue()) failed(item)
    }
  }
}
