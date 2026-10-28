import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { registerHooks, stripTypeScriptTypes } from 'node:module'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

function strip(source) {
  try {
    return stripTypeScriptTypes(source)
  } catch (error) {
    // Existing API error classes use parameter properties, which Node 26 cannot strip.
    if (error.code !== 'ERR_UNSUPPORTED_TYPESCRIPT_SYNTAX') throw error
    return ts.transpileModule(source, {
      compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
    }).outputText
  }
}

// Run the real TypeScript projections without a test dependency or a frontend build.
registerHooks({
  resolve(specifier, context, nextResolve) {
    const aliases = {
      '@modern/': new URL('../src/frontends/modern/', import.meta.url),
      '@shared/': new URL('../src/shared/', import.meta.url),
    }
    for (const [prefix, root] of Object.entries(aliases)) {
      if (specifier.startsWith(prefix))
        return nextResolve(new URL(specifier.slice(prefix.length) + '.ts', root).href, context)
    }
    if (
      specifier.startsWith('.') &&
      context.parentURL?.endsWith('.ts') &&
      !/\.[a-z]+$/u.test(specifier)
    )
      return nextResolve(specifier + '.ts', context)
    return nextResolve(specifier, context)
  },
  load(url, context, nextLoad) {
    if (url.startsWith('file:') && url.endsWith('.ts'))
      return {
        format: 'module',
        shortCircuit: true,
        source: strip(readFileSync(fileURLToPath(url), 'utf8')),
      }
    return nextLoad(url, context)
  },
})
const {
  maskedAccountLabel,
  quotaWindowState,
  quotaWindowValuesAvailable,
  compactQuotaWindows,
  indexAccountQuotas,
  syncQuotaPage,
  groupQuotaSections,
  nanoPerCredit,
  quotaNano,
  keyAmount,
  keyRemainingPercent,
  keyUsedPercent,
  keyDisplayEntries,
  accountCycleAmount,
  accountCycleEstimate,
  keyQuotaEntries,
  keyQuotaState,
  keyQuotaValuesAvailable,
  resetCreditRows,
  canUseResetCredit,
  resetObservationUpdated,
  visibleQuotaWindows,
  keyResetRuleIDs,
  resetCreditSupported,
} = await import('../src/frontends/modern/features/account-quotas/data.ts')
const { getKeyQuotas } = await import('../src/frontends/modern/api/fork-key-quotas.ts')
const { getAccountQuotas } = await import('../src/frontends/modern/api/fork-account-quotas.ts')
const { quotaRemaining, credentialStatus } =
  await import('../src/frontends/modern/features/groups/credential-presentation.ts')
const { readCredential } = await import('../src/frontends/modern/api/group-detail.ts')
const { createRenderer, reactive, ref, nextTick } = await import('vue')
const { QueryClient, VueQueryPlugin } = await import('@tanstack/vue-query')
const { apiClientKey } = await import('../src/shared/http/client-context.ts')
const { authSessionKey } = await import('../src/frontends/modern/features/auth/auth-session.ts')
const { ApiError, NetworkError } = await import('../src/shared/http/errors.ts')
const { useKeyQuotaReset, useAccountResetCredits } =
  await import('../src/frontends/modern/features/account-quotas/use-account-quotas.ts')

// In-memory Vue host + mock transport only. No browser, server, database or real Key is used.
function mountQuotaAction(setup, request) {
  const host = () => ({ children: [] })
  const renderer = createRenderer({
    createElement: host,
    createText: host,
    createComment: host,
    insert: () => {},
    remove: () => {},
    setText: () => {},
    setElementText: () => {},
    parentNode: () => null,
    nextSibling: () => null,
    patchProp: () => {},
  })
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  cache.setQueryData(['modern', 'group-channels'], [])
  const invalidations = []
  cache.invalidateQueries = async ({ queryKey }) => {
    invalidations.push(queryKey)
  }
  const state = reactive({ principalType: 'admin' })
  let revision = 1,
    result
  const app = renderer.createApp({
    setup() {
      result = setup()
      return () => null
    },
  })
  app.provide(apiClientKey, { request })
  app.provide(authSessionKey, { state, getRevision: () => revision })
  app.use(VueQueryPlugin, { queryClient: cache })
  app.mount(host())
  return {
    result,
    state,
    cache,
    invalidations,
    changeRevision() {
      revision++
    },
    dispose() {
      app.unmount()
      cache.clear()
    },
  }
}
function deferred() {
  let resolve, reject
  const promise = new Promise((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}
const resetKey = (id = 201) => ({
  id,
  name: `Mock Key ${id}`,
  mask: 'synthetic***',
  rules: [
    { id: id * 10 + 1, kind: 'periodic', periodSeconds: 18000, limitUSD: '5' },
    { id: id * 10 + 2, kind: 'periodic', periodSeconds: 604800, limitUSD: '25' },
    { id: id * 10 + 3, kind: 'total', limitUSD: '100' },
    { id: id * 10 + 4, kind: 'periodic', periodSeconds: 86400, limitUSD: '10' },
  ],
})

const window = (patch = {}) => ({
  id: 'quota',
  label: 'Session',
  scope: 'account',
  unit: 'percent',
  state: 'available',
  windowSeconds: 18000,
  models: [],
  ...patch,
})
const observation = (patch = {}) => ({ state: 'fresh', observedAt: 1000, windows: [], ...patch })
function credential(patch = {}) {
  return {
    credential_id: 1,
    name: '',
    mask: 'key***mask',
    account: { email: 'private@example.test', email_mask: 'p***e@example.test' },
    connection_type: 'subscription',
    effective_status: 'available',
    configured_status: 'active',
    weight: 50,
    recent_success_count: 0,
    recent_failure_count: 0,
    consecutive_failure_count: 0,
    auth_state: 'ready',
    secret_version: 1,
    last_failure_category: '',
    last_status_code: null,
    model_cooldowns: [],
    recovery: { mode: 'none', automatic: false, at_ms: null },
    // Aggregate endpoint intentionally does not resolve proxy routing.
    proxy: { configured_mode: '', effective_mode: '', effective_source: '', has_auth: false },
    ...patch,
  }
}
const item = (raw = credential()) => ({
  group_id: 7,
  group_name: 'Group',
  channel_id: 'glm',
  channel_name: 'GLM',
  quota_supported: true,
  credential: raw,
})
const report = (patch = {}) => ({
  observed_at_ms: 2000,
  items: [item()],
  total: 1,
  page: 1,
  page_size: 100,
  ...patch,
})
const read = (data, page = 1) =>
  getAccountQuotas({ request: async () => data }, page, new AbortController().signal)

test('mask-only labels prefer email mask, retain aliases, and fall back safely', () => {
  assert.equal(maskedAccountLabel(1, '', 'key***', 'a***@example.test'), 'a***@example.test')
  assert.equal(maskedAccountLabel(1, 'Work', 'key***', 'a***'), 'Work · a***')
  assert.equal(maskedAccountLabel(1, '', 'key***', ''), 'key***')
  assert.equal(maskedAccountLabel(1, '  ', '', ''), '#1')
})
test('unknown, missing and expired observations never become zero quota', () => {
  const unknown = window({ state: 'unknown', utilization: 0 })
  assert.equal(quotaWindowValuesAvailable(observation(), unknown, 1000), false)
  assert.equal(quotaWindowValuesAvailable(undefined, window(), 1000), false)
  assert.equal(
    quotaWindowValuesAvailable(observation({ state: 'unavailable' }), window(), 1000),
    false,
  )
  assert.equal(quotaWindowState(observation(), window({ resetsAt: 1000 }), 1000), 'expired')
  assert.equal(quotaWindowValuesAvailable(observation(), window({ resetsAt: 1000 }), 1000), false)
  assert.equal(quotaRemaining(window()), undefined)
})
test('true exhaustion remains zero and stale/error snapshots retain their state', () => {
  const exhausted = window({ state: 'exhausted', utilization: 1, resetsAt: 5000 })
  assert.equal(quotaRemaining(exhausted), 0)
  for (const state of ['fresh', 'stale', 'error', 'refreshing']) {
    const stored = observation({ state })
    assert.equal(quotaWindowValuesAvailable(stored, exhausted, 1000), true)
    assert.equal(stored.state, state)
  }
})
test('5h and 7d excerpts keep account, model, source and cycle distinctions', () => {
  const windows = [
    window(),
    window({ id: 'model', scope: 'model', models: ['m'], windowSeconds: 604800 }),
    window({ id: 'cycle', sourceId: 'second', windowSeconds: 604800 }),
    window({ id: 'monthly', windowSeconds: 2592000 }),
  ]
  assert.deepEqual(compactQuotaWindows(windows), windows.slice(0, 3))
  assert.equal(windows.length, 4)
})
test('group indexing preserves individual rows instead of summing account percentages', () => {
  const rows = [
    { groupID: 1, percent: 80 },
    { groupID: 2, percent: 90 },
    { groupID: 1, percent: 20 },
  ]
  const index = indexAccountQuotas(rows)
  assert.deepEqual(index.get(1), [rows[0], rows[2]])
  assert.equal(index.has(3), false)
})
test('current-page sync is serial, continues after row errors and reports each failure', async () => {
  const order = [],
    errors = []
  let running = 0,
    peak = 0
  await syncQuotaPage(
    [1, 2, 3],
    async (id) => {
      running++
      peak = Math.max(peak, running)
      await Promise.resolve()
      order.push(id)
      running--
      if (id === 2) throw new Error('upstream')
    },
    (id) => errors.push(id),
    () => true,
  )
  assert.equal(peak, 1)
  assert.deepEqual(order, [1, 2, 3])
  assert.deepEqual(errors, [2])
})
test('sync stops before the next row when disposed or principal changes', async () => {
  let allowed = true
  const order = []
  await syncQuotaPage(
    [1, 2],
    async (id) => {
      order.push(id)
      allowed = false
    },
    () => assert.fail('no error'),
    () => allowed,
  )
  assert.deepEqual(order, [1])
})
test('overview API is one store-only GET and never retains plaintext account email', async () => {
  const calls = []
  const result = await getAccountQuotas(
    {
      request: async (path, options) => {
        calls.push({ path, options })
        return report()
      },
    },
    1,
    new AbortController().signal,
  )
  assert.equal(calls.length, 1)
  assert.equal(calls[0].path, '/api/fork/account-quotas?page=1&page_size=100')
  assert.equal(calls[0].options.method, undefined)
  assert.equal(result.items[0].credential.label, 'p***e@example.test')
  assert.equal(JSON.stringify(result).includes('private@example.test'), false)
})
test('empty email mask falls back to credential mask, never account.email', async () => {
  const result = await read(
    report({
      items: [item(credential({ account: { email: 'private@example.test', email_mask: '' } }))],
    }),
  )
  assert.equal(result.items[0].credential.label, 'key***mask')
  assert.equal(result.items[0].credential.account, '')
})
test('pagination keeps total visible and rejects wrong pages, oversize, or duplicate rows', async () => {
  assert.equal((await read(report({ total: 250 }))).total, 250)
  assert.deepEqual((await read(report({ items: [], total: 0 }))).items, [])
  for (const invalid of [
    report({ page: 2 }),
    report({ page_size: 101 }),
    report({ total: 0 }),
    report({ items: [item(), item()], total: 2 }),
    report({
      items: Array.from({ length: 101 }, (_, i) => item(credential({ credential_id: i + 1 }))),
      total: 101,
    }),
  ])
    await assert.rejects(read(invalid), /INVALID_API_RESPONSE/u)
})
test('unsupported channels and absent windows stay explicit, never synthesized', async () => {
  const result = await read(report({ items: [{ ...item(), quota_supported: false }] }))
  assert.equal(result.items[0].quotaSupported, false)
  assert.equal(result.items[0].credential.observation, undefined)
})

// Sanitized aggregate wire fixture: deployed empty proxy shape, omitted account.email /
// last_refresh_at_ms, and reported GLM observations (5h used 17%, 7d used 64%).
// IDs, masks and timestamps are synthetic; no credentials or account identities are retained.
const deployedAggregate = {
  observed_at_ms: 1793095200000,
  total: 3,
  page: 1,
  page_size: 100,
  items: [
    {
      group_id: 1,
      group_name: 'Account A',
      channel_id: 'codex',
      channel_name: 'Codex',
      quota_supported: true,
      credential: {
        name: '',
        credential_id: 11,
        connection_type: 'subscription',
        secret_version: 1,
        mask: 'a***@example.test',
        account: { email_mask: 'a***@example.test', expires_at_ms: 1795690800000 },
        auth_state: 'ready',
        configured_status: 'active',
        effective_status: 'available',
        weight: 50,
        recent_success_count: 0,
        recent_failure_count: 0,
        consecutive_failure_count: 0,
        last_failure_category: 'ambiguous',
        last_status_code: null,
        cooldown_until_ms: null,
        model_cooldowns: [],
        recovery: { mode: 'none', automatic: false, at_ms: null },
        proxy: { configured_mode: '', effective_mode: '', effective_source: '', has_auth: false },
        observation: {
          state: 'unavailable',
          snapshot: null,
          observation_version: 0,
          observed_at_ms: null,
          last_attempt_at_ms: null,
        },
      },
    },
    {
      group_id: 2,
      group_name: 'Account B',
      channel_id: 'claude',
      channel_name: 'Claude',
      quota_supported: true,
      credential: {
        name: 'Work',
        credential_id: 12,
        connection_type: 'subscription',
        secret_version: 1,
        mask: 'b***@example.test',
        account: { email_mask: 'b***@example.test', expires_at_ms: 1795690800000 },
        auth_state: 'ready',
        configured_status: 'active',
        effective_status: 'available',
        weight: 50,
        recent_success_count: 0,
        recent_failure_count: 0,
        consecutive_failure_count: 0,
        last_failure_category: 'ambiguous',
        last_status_code: null,
        cooldown_until_ms: null,
        model_cooldowns: [],
        recovery: { mode: 'none', automatic: false, at_ms: null },
        proxy: { configured_mode: '', effective_mode: '', effective_source: '', has_auth: false },
        observation: {
          state: 'unavailable',
          snapshot: null,
          observation_version: 0,
          observed_at_ms: null,
          last_attempt_at_ms: null,
        },
      },
    },
    {
      group_id: 3,
      group_name: 'Coding',
      channel_id: 'zhipuai',
      channel_name: 'GLM',
      quota_supported: true,
      credential: {
        name: 'Coding',
        credential_id: 13,
        connection_type: 'api_key',
        secret_version: 1,
        mask: 'key***mask',
        account: {},
        auth_state: 'ready',
        configured_status: 'active',
        effective_status: 'available',
        weight: 50,
        recent_success_count: 1,
        recent_failure_count: 0,
        consecutive_failure_count: 0,
        last_failure_category: 'ambiguous',
        last_status_code: null,
        cooldown_until_ms: null,
        model_cooldowns: [],
        recovery: { mode: 'none', automatic: false, at_ms: null },
        proxy: { configured_mode: '', effective_mode: '', effective_source: '', has_auth: false },
        observation: {
          state: 'fresh',
          observation_version: 1,
          observed_at_ms: 1793095140000,
          last_attempt_at_ms: 1793095140000,
          snapshot: {
            plan_summary: { name: 'lite' },
            quota_windows: [
              {
                id: 'session',
                source_id: 'zhipuai_coding',
                label: '5h',
                label_key: 'session',
                scope: 'account',
                unit: 'percent',
                used: 17,
                limit: 100,
                remaining: 83,
                utilization: 0.17,
                reset_at_ms: 1793109600000,
                window_seconds: 18000,
                state: 'available',
              },
              {
                id: 'weekly',
                source_id: 'zhipuai_coding',
                label: '7d',
                label_key: 'weekly',
                scope: 'account',
                unit: 'percent',
                used: 64,
                limit: 100,
                remaining: 36,
                utilization: 0.64,
                reset_at_ms: 1793664000000,
                window_seconds: 604800,
                state: 'available',
                is_primary: true,
              },
            ],
          },
        },
      },
    },
  ],
}

test('deployed empty-proxy DTO fails the detail parser but succeeds in the overview', async () => {
  assert.throws(
    () => readCredential(deployedAggregate.items[0].credential),
    /INVALID_API_RESPONSE/u,
  )
  const result = await read(deployedAggregate)
  assert.equal(result.total, 3)
  assert.equal(result.items.length, 3)
  assert.equal(
    result.items.every((row) => row.quotaSupported),
    true,
  )
  const account = result.items[0].credential
  assert.equal(account.label, 'a***@example.test')
  assert.equal(account.expiresAt, 1795690800000)
  assert.deepEqual(credentialStatus(account), {
    key: 'groups.credentials.available',
    tone: 'success',
  })
  assert.equal(account.observation.state, 'unavailable')
  for (const row of result.items)
    for (const unused of ['proxy', 'recovery', 'daily', 'secretVersion', 'lastRefresh'])
      assert.equal(Object.hasOwn(row.credential, unused), false)
  const glm = result.items[2].credential.observation
  assert.equal(glm.state, 'fresh')
  assert.equal(glm.observedAt, 1793095140000)
  assert.deepEqual(
    glm.windows.map((entry) => [
      entry.windowSeconds,
      entry.used,
      Math.round(quotaRemaining(entry)),
    ]),
    [
      [18000, 17, 83],
      [604800, 64, 36],
    ],
  )
})

test('overview needs no proxy, recovery, daily or activity fields', async () => {
  const minimal = {
    credential_id: 1,
    mask: 'key***',
    effective_status: 'cooldown',
    configured_status: 'active',
    auth_state: 'ready',
  }
  const result = await read(report({ items: [item(minimal)] }))
  assert.equal(result.items[0].credential.label, 'key***')
  assert.equal(result.items[0].credential.state, 'cooldown')
  assert.equal(result.items[0].credential.expiresAt, undefined)
  assert.equal(result.items[0].credential.observation, undefined)
})

const keyRule = (patch = {}) => ({
  id: 1,
  kind: 'periodic',
  limit_usd: '0.6',
  period_seconds: 18000,
  ...patch,
})
const keyWindow = (patch = {}) => ({
  ...keyRule(),
  used_usd: '0.1',
  remaining_usd: '0.5',
  status: 'available',
  window_started_at_ms: 1000,
  window_ends_at_ms: 18001000,
  ...patch,
})
const keyWire = (patch = {}) => ({
  id: 1,
  name: 'Coding',
  masked_key: 'sk-***safe',
  status: 'active',
  expires_at_ms: null,
  group_ids: [3],
  accounting: { unit: 'glm_coding_credits', internal_units_per_credit: '0.01' },
  cost_limit_rules: [keyRule()],
  cost_limit_status: {
    allowed: true,
    recoverable: false,
    next_available_at_ms: null,
    observed_at_ms: 2000,
    rules: [keyWindow()],
  },
  ...patch,
})
const groupWire = (patch = {}) => ({
  id: 3,
  name: 'Configured Coding',
  channel_id: 'zhipuai',
  channel_name: 'GLM',
  enabled: true,
  ...patch,
})
const keyReport = (patch = {}) => ({
  observed_at_ms: 2000,
  groups: [groupWire()],
  items: [keyWire()],
  total: 1,
  page: 1,
  page_size: 100,
  ...patch,
})
const readKeys = (data, page = 1) =>
  getKeyQuotas({ request: async () => data }, page, new AbortController().signal)

test('key overview uses one store-only GET, exact scope and whitelist-only metadata', async () => {
  const calls = []
  const result = await getKeyQuotas(
    {
      request: async (path, options) => {
        calls.push({ path, options })
        return keyReport({
          items: [keyWire({ key: 'plaintext-must-not-survive', extra: { secret: 'sensitive' } })],
        })
      },
    },
    1,
    new AbortController().signal,
  )
  assert.equal(calls.length, 1)
  assert.equal(calls[0].path, '/api/fork/key-quotas?page=1&page_size=100')
  assert.equal(calls[0].options.method, undefined)
  assert.equal(JSON.stringify(result).includes('plaintext-must-not-survive'), false)
  assert.equal(JSON.stringify(result).includes('sensitive'), false)
  assert.deepEqual(result.items[0].groupIDs, [3])
  assert.deepEqual(result.items[0].accounting, {
    unit: 'glm_coding_credits',
    internalUnitsPerCredit: '0.01',
  })
  assert.equal(result.groups[0].name, 'Configured Coding')
})

test('server accounting basis is optional read-only metadata and never inferred', async () => {
  const result = await readKeys(
    keyReport({ items: [keyWire({ accounting: { unit: 'usd', basis: 'current_config' } })] }),
  )
  assert.deepEqual(result.items[0].accounting, { unit: 'usd', basis: 'current_config' })
  await assert.rejects(
    readKeys(keyReport({ items: [keyWire({ accounting: { unit: 'usd', basis: 'guessed' } })] })),
    /INVALID_API_RESPONSE/u,
  )
})

test('key parser rejects malformed, lossy, duplicate and wrong-page DTOs without management defaults', async () => {
  for (const invalid of [
    keyReport({ page: 2 }),
    keyReport({ page_size: 50 }),
    keyReport({ total: 0 }),
    keyReport({ groups: [groupWire(), groupWire()] }),
    keyReport({ items: [keyWire(), keyWire()], total: 2 }),
    keyReport({
      items: Array.from({ length: 101 }, (_, id) => keyWire({ id: id + 1 })),
      total: 101,
    }),
    ...[
      { id: 0 },
      { group_ids: [3, 3] },
      { group_ids: [0] },
      { status: 'unknown' },
      { expires_at_ms: Number.MAX_SAFE_INTEGER + 1 },
      { accounting: { unit: 'glm_coding_credits' } },
      { accounting: { unit: 'glm_coding_credits', internal_units_per_credit: '0.02' } },
      { accounting: { unit: 'usd', internal_units_per_credit: '0.01' } },
      { accounting: { unit: 'guessed' } },
      { cost_limit_rules: [keyRule({ limit_usd: 0.6 })] },
      { cost_limit_rules: [keyRule({ limit_usd: '-1' })] },
      { cost_limit_rules: [keyRule({ limit_usd: '1.0000000001' })] },
      { cost_limit_rules: [keyRule({ kind: 'unknown' })] },
      { cost_limit_rules: [keyRule(), keyRule()] },
      {
        cost_limit_status: {
          allowed: true,
          recoverable: false,
          observed_at_ms: 1,
          rules: [keyWindow({ status: 'unknown' })],
        },
      },
    ].map((patch) => keyReport({ items: [keyWire(patch)] })),
  ])
    await assert.rejects(readKeys(invalid), /INVALID_API_RESPONSE/u)
  assert.equal((await readKeys(keyReport({ total: 250 }))).total, 250)
  assert.deepEqual((await readKeys(keyReport({ items: [], total: 0 }))).items, [])
})

test('configured groups include empty and disabled groups; dedicated/shared keys appear exactly once', async () => {
  const keyData = await readKeys(
    keyReport({
      groups: [
        groupWire(),
        groupWire({ id: 4, name: 'Paused', enabled: false }),
        groupWire({ id: 5, name: 'Empty' }),
      ],
      items: [
        keyWire(),
        keyWire({ id: 2, group_ids: [3, 4] }),
        keyWire({ id: 3, group_ids: [] }),
        keyWire({ id: 4, group_ids: [99] }),
      ],
      total: 4,
    }),
  )
  const accounts = (await read(deployedAggregate)).items
  const sections = groupQuotaSections(keyData.groups, accounts, keyData.items)
  assert.equal(sections.groups[0].name, 'Configured Coding')
  assert.equal(sections.groups.find((group) => group.id === 4).enabled, false)
  assert.equal(sections.groups.find((group) => group.id === 5).accounts.length, 0)
  assert.deepEqual(
    sections.groups.flatMap((group) => group.keys).map((key) => key.id),
    [1],
  )
  assert.deepEqual(
    sections.shared.map((key) => key.id),
    [2, 3, 4],
  )
  assert.equal(sections.groups.flatMap((group) => group.accounts).length, 3)
  assert.equal(groupQuotaSections([], accounts, []).groups.length, 3)
  assert.equal(groupQuotaSections(keyData.groups, [], keyData.items).groups[0].keys.length, 1)
})

test('BigInt keeps nano precision, credit conversion and internal accounting without monetary Number coercion', () => {
  assert.equal(nanoPerCredit, 10000000n)
  assert.equal(quotaNano('9007199254740993.123456789'), 9007199254740993123456789n)
  const credit = { unit: 'glm_coding_credits', internalUnitsPerCredit: '0.01' }
  assert.equal(keyAmount('0.6', credit, 'en-US'), '60')
  assert.equal(keyAmount('0.000000001', credit, 'en-US'), '<0.001')
  assert.equal(keyAmount('0.000000001', credit, 'en-US', 'exact'), '0.0000001')
  assert.equal(
    keyAmount('9007199254740993.123456789', credit, 'en-US'),
    '900,719,925,474,099,312.346',
  )
  assert.equal(
    keyAmount('9007199254740993.123456789', credit, 'en-US', 'exact'),
    '900,719,925,474,099,312.3456789',
  )
  assert.equal(keyAmount('1.000000001', { unit: 'usd' }, 'en-US'), '$1')
  assert.equal(keyAmount('1.000000001', { unit: 'usd' }, 'en-US', 'exact'), '$1.000000001')
  for (const unit of ['mixed', 'unknown']) assert.equal(keyAmount('0.6', { unit }, 'en-US'), '0.6')
  assert.equal(keyAmount('0.6', credit, 'de-DE'), '60')
  assert.equal(
    keyRemainingPercent({ limitUSD: '9007199254740993', remainingUSD: '4503599627370496.5' }),
    50,
  )
  assert.equal(keyRemainingPercent({ limitUSD: '1', remainingUSD: '2' }), 100)
  assert.equal(keyRemainingPercent({ limitUSD: '0', remainingUSD: '0' }), undefined)
})

test('readable amounts round in BigInt while exact tooltips retain all native precision', () => {
  const usd = { unit: 'usd' }
  const credit = { unit: 'glm_coding_credits', internalUnitsPerCredit: '0.01' }
  assert.equal(keyAmount('189.730421957', usd, 'en-US'), '$189.7304')
  assert.equal(keyAmount('189.730421957', usd, 'en-US', 'exact'), '$189.730421957')
  assert.equal(keyAmount('1.99995', usd, 'en-US'), '$2')
  assert.equal(keyAmount('1.23455', usd, 'en-US'), '$1.2346')
  assert.equal(keyAmount('0.000000001', usd, 'en-US'), '<$0.0001')
  assert.equal(keyAmount('0', usd, 'en-US'), '$0')
  assert.equal(keyAmount('0.001234567', credit, 'en-US'), '0.123')
  assert.equal(keyAmount('0.001234567', credit, 'en-US', 'exact'), '0.1234567')
  assert.equal(keyAmount('0.009995', credit, 'en-US'), '1')
  assert.equal(keyAmount('189.730421957', usd, 'de-DE'), '$189,7304')
  for (const unit of ['mixed', 'unknown']) {
    assert.equal(keyAmount('189.730421957', { unit }, 'en-US'), '189.7304')
    assert.equal(keyAmount('189.730421957', { unit }, 'en-US', 'exact'), '189.730421957')
  }
})

test('all key periods/total survive, 5h/7d absence explicit and unknown is never synthesized', async () => {
  const rules = [
    keyRule(),
    keyRule({ id: 2, period_seconds: 86400 }),
    keyRule({ id: 3, kind: 'total' }),
  ]
  const windows = [
    keyWindow(),
    keyWindow({ id: 2, period_seconds: 86400 }),
    keyWindow({ id: 3, kind: 'total', window_ends_at_ms: null }),
  ]
  const parsed = await readKeys(
    keyReport({
      items: [
        keyWire({
          cost_limit_rules: rules,
          cost_limit_status: {
            allowed: true,
            recoverable: false,
            observed_at_ms: 2000,
            rules: windows,
          },
        }),
      ],
    }),
  )
  const entries = keyQuotaEntries(parsed.items[0])
  assert.deepEqual(
    entries.map((entry) => (entry.kind === 'total' ? 'total' : entry.periodSeconds)),
    [18000, 604800, 86400, 'total'],
  )
  assert.equal(keyQuotaState(entries[1], 2000), 'unconfigured')
  const missing = { ...entries[0], window: undefined }
  assert.equal(keyQuotaState(missing, 2000), 'unknown')
  assert.equal(keyQuotaValuesAvailable(missing, 2000), false)
  assert.equal(
    keyQuotaValuesAvailable(
      { ...entries[0], window: { ...entries[0].window, limitUSD: '1' } },
      2000,
    ),
    false,
  )
  assert.equal(keyQuotaState(entries[0], 18001000), 'expired')
  const inactive = {
    ...entries[0],
    window: { ...entries[0].window, status: 'inactive', startsAt: null, endsAt: null },
  }
  assert.equal(keyQuotaState(inactive, 2000), 'inactive')
  assert.equal(keyQuotaValuesAvailable(inactive, 2000), true)
  const noStatus = { ...parsed.items[0], quota: null }
  assert.equal(keyQuotaState(keyQuotaEntries(noStatus)[0], 2000), 'unknown')
  const extra = { ...parsed.items[0], rules: [], quota: parsed.items[0].quota }
  assert.equal(keyQuotaEntries(extra).filter((entry) => entry.window).length, 3)
  assert.equal(keyQuotaState(keyQuotaEntries(extra)[0], 2000), 'unknown')
  const empty = keyQuotaEntries({ ...parsed.items[0], rules: [], quota: null })
  assert.deepEqual(
    empty.map((entry) => (entry.kind === 'total' ? 'total' : entry.periodSeconds)),
    [18000, 604800, 'total'],
  )
  assert.equal(
    empty.every((entry) => keyQuotaState(entry, 2000) === 'unconfigured'),
    true,
  )
})

test('reset card rows preserve only provided details and mark expiry without claiming permanence', () => {
  assert.deepEqual(resetCreditRows(undefined, 2000), [])
  assert.deepEqual(
    resetCreditRows(observation({ resetCredits: 3, creditExpirations: [] }), 2000),
    [],
  )
  assert.deepEqual(
    resetCreditRows(
      observation({ resetCredits: 3, creditExpirations: [1000, 3000, undefined] }),
      2000,
    ).map((card) => card.state),
    ['expired', 'available', 'available'],
  )
  assert.equal(
    resetCreditRows(
      observation({ state: 'stale', resetCredits: 1, creditExpirations: [3000] }),
      2000,
    )[0].state,
    'unknown',
  )
})
test('reset card action requires channel capability, fresh available cards and ready account', () => {
  const account = {
    credential: {
      enabled: true,
      authState: 'ready',
      observation: observation({ resetCredits: 1, creditExpirations: [3000] }),
    },
  }
  assert.equal(canUseResetCredit(account, true, 2000), true)
  assert.equal(canUseResetCredit(account, false, 2000), false)
  for (const patch of [
    { enabled: false },
    { authState: 'refreshing' },
    { expiresAt: 2000 },
    { observation: observation({ state: 'stale', resetCredits: 1, creditExpirations: [3000] }) },
    { observation: observation({ resetCredits: 1, creditExpirations: [1000] }) },
  ])
    assert.equal(
      canUseResetCredit({ credential: { ...account.credential, ...patch } }, true, 2000),
      false,
    )
})
test('successful reset stays locked for stale cache or same timestamp, pending needs newer fresh observation', () => {
  assert.equal(resetObservationUpdated(undefined, 2000), false)
  assert.equal(resetObservationUpdated(observation({ observedAt: 1000 }), 2000), false)
  assert.equal(resetObservationUpdated(observation({ observedAt: 2000 }), 2000), false)
  assert.equal(
    resetObservationUpdated(observation({ state: 'stale', observedAt: 3000 }), 2000),
    false,
  )
  assert.equal(
    resetObservationUpdated(observation({ state: 'refreshing', observedAt: 3000 }), 2000),
    false,
  )
  assert.equal(resetObservationUpdated(observation({ observedAt: 3000 }), 2000), true)
})

const cycleWire = (patch = {}) => ({
  window_id: 'session',
  source_id: 'source',
  window_seconds: 18000,
  reset_at_ms: 18001000,
  unit: 'glm_coding_credits',
  total: '12000',
  used: '2040',
  remaining: '9960',
  method: 'fixed_plan',
  estimated: true,
  ...patch,
})
test('account estimates are optional strict decimal metadata and retain no extra fields', async () => {
  assert.deepEqual((await read(report())).items[0].cycleEstimates, [])
  const result = await read(
    report({
      items: [{ ...item(), cycle_estimates: [cycleWire({ complete: false, secret: 'discard' })] }],
    }),
  )
  assert.equal(result.items[0].cycleEstimates[0].total, '12000')
  assert.equal(result.items[0].cycleEstimates[0].estimated, true)
  assert.equal(JSON.stringify(result).includes('discard'), false)
  for (const patch of [
    { window_id: '' },
    { source_id: null },
    { window_seconds: 0 },
    { reset_at_ms: -1 },
    { unit: 'mixed' },
    { method: 'guessed' },
    { estimated: 'true' },
    { total: 12000 },
    { total: '-1' },
    { used: '1e3' },
    { remaining: '0.0000000001' },
  ])
    await assert.rejects(
      read(report({ items: [{ ...item(), cycle_estimates: [cycleWire(patch)] }] })),
      /INVALID_API_RESPONSE/u,
    )
  await assert.rejects(
    read(report({ items: [{ ...item(), cycle_estimates: [cycleWire(), cycleWire()] }] })),
    /INVALID_API_RESPONSE/u,
  )
})
test('account estimate matches exact source, period and reset without cross-model or old-cycle reuse', async () => {
  const parsed = (await read(report({ items: [{ ...item(), cycle_estimates: [cycleWire()] }] })))
    .items[0]
  const current = window({ id: 'session', sourceId: 'source', resetsAt: 18001000 })
  assert.equal(accountCycleEstimate(parsed, current).used, '2040')
  for (const patch of [
    { id: 'weekly' },
    { sourceId: undefined },
    { sourceId: 'other' },
    { windowSeconds: 604800 },
    { resetsAt: 18001001 },
    { scope: 'model' },
    { models: ['m'] },
  ])
    assert.equal(accountCycleEstimate(parsed, { ...current, ...patch }), undefined)
  const provider = {
    cycleEstimates: [{ ...parsed.cycleEstimates[0], method: 'provider', unit: 'usd' }],
  }
  assert.equal(
    accountCycleEstimate(provider, { ...current, scope: 'credits', unit: 'usd' }).method,
    'provider',
  )
  assert.equal(
    accountCycleEstimate(parsed, { ...current, scope: 'credits', unit: 'usd' }),
    undefined,
  )
})
test('account amounts use real credits with exact BigInt precision, never Key internal scaling', () => {
  assert.equal(accountCycleAmount('12000', 'glm_coding_credits', 'en-US'), '12,000')
  assert.equal(accountCycleAmount('2040', 'glm_coding_credits', 'en-US'), '2,040')
  assert.equal(
    accountCycleAmount('0.000000001', 'glm_coding_credits', 'en-US', 'exact'),
    '0.000000001',
  )
  assert.equal(
    accountCycleAmount('9007199254740993.123456789', 'glm_coding_credits', 'en-US', 'exact'),
    '9,007,199,254,740,993.123456789',
  )
  assert.equal(accountCycleAmount('189.730421957', 'usd', 'en-US'), '$189.7304')
})
test('display hides total rules only, retains other periods, and keeps overall block unchanged', async () => {
  const result = await readKeys(
    keyReport({
      items: [
        keyWire({
          cost_limit_rules: [
            keyRule(),
            keyRule({ id: 2, kind: 'total' }),
            keyRule({ id: 3, period_seconds: 86400 }),
          ],
          cost_limit_status: {
            allowed: false,
            recoverable: false,
            observed_at_ms: 2000,
            rules: [
              keyWindow(),
              keyWindow({ id: 2, kind: 'total' }),
              keyWindow({ id: 3, period_seconds: 86400 }),
            ],
          },
        }),
      ],
    }),
  )
  const key = result.items[0]
  assert.deepEqual(
    keyDisplayEntries(key).map((entry) => entry.periodSeconds),
    [18000, 604800, 86400],
  )
  assert.equal(key.quota.allowed, false)
  assert.equal(
    key.rules.some((rule) => rule.kind === 'total'),
    true,
  )
  assert.equal(keyUsedPercent(key.quota.windows[0]), 16.66)
  assert.equal(keyUsedPercent({ usedUSD: '2', limitUSD: '1' }), 100)
  assert.equal(keyUsedPercent({ usedUSD: '0', limitUSD: '0' }), undefined)
})
test('panel has separate account/key regions, catalog icons and no repeated introduction', () => {
  const source = readFileSync(
    new URL(
      '../src/frontends/modern/features/account-quotas/AccountQuotasPanel.vue',
      import.meta.url,
    ),
    'utf8',
  )
  assert.equal(source.includes('modern-account-quota-columns'), false)
  assert.equal(source.includes("t('accountQuotas.help')"), false)
  assert.equal(source.includes('reset.channels.data'), true)
  assert.equal(source.includes('<AppChannelIcon'), true)
  assert.equal(source.includes('v-for="item in sections.shared"'), true)
  const key = readFileSync(
    new URL('../src/frontends/modern/features/account-quotas/KeyQuotaCard.vue', import.meta.url),
    'utf8',
  )
  assert.equal(key.includes('keyDisplayEntries(props.item)'), true)
  assert.equal(key.includes('totalBudget'), false)
  assert.equal(key.includes('modern-key-quota-values'), false)
  assert.equal(key.includes('!item.quota.allowed'), true)
})

test('only known all-zero USD credit windows are hidden; depleted and unknown remain', () => {
  const empty = window({ scope: 'credits', unit: 'usd', used: 0, limit: 0, remaining: 0 })
  const retained = [
    { ...empty, state: 'unknown' },
    { ...empty, used: undefined },
    { ...empty, limit: undefined },
    { ...empty, remaining: undefined },
    { ...empty, state: 'exhausted', used: 10, limit: 10 },
    { ...empty, scope: 'account' },
    { ...empty, unit: 'percent' },
    { ...empty, remaining: 1 },
  ]
  const source = [empty, ...retained]
  assert.deepEqual(visibleQuotaWindows(source), retained)
  assert.deepEqual(compactQuotaWindows(source), retained)
  assert.equal(source.length, retained.length + 1)
})
test('reset cards are folded by local AppButton state with accessible controls, not native details', () => {
  const source = readFileSync(
    new URL(
      '../src/frontends/modern/features/account-quotas/AccountQuotaCard.vue',
      import.meta.url,
    ),
    'utf8',
  )
  assert.match(source, /const cardsExpanded = ref\(false\)/u)
  assert.match(source, /:aria-expanded="cardsExpanded"/u)
  assert.match(source, /:aria-controls="cardsDetailsID"/u)
  assert.match(source, /v-show="cardsExpanded"/u)
  assert.doesNotMatch(source, /<details\b/u)
  assert.match(source, /visibleQuotaWindows\(credential.value.observation/u)
  const quota = readFileSync(
    new URL('../src/frontends/modern/features/account-quotas/QuotaWindow.vue', import.meta.url),
    'utf8',
  )
  assert.match(quota, /visibleQuotaWindows\(\[window\]\)/u)
})
test('fresh partial observations keep absent card counts unknown and cannot release the consume lock', async () => {
  const partial = item(
    credential({
      observation: {
        state: 'fresh',
        observed_at_ms: 3000,
        last_error_code: 'observation_partial',
        snapshot: { plan_summary: {}, quota_windows: [] },
      },
    }),
  )
  const row = (
    await read(
      report({
        items: [{ ...partial, reset_supported: true, reset_types: ['five_hour', 'week'] }],
      }),
    )
  ).items[0]
  assert.equal(row.credential.observation.resetCreditsKnown, false)
  assert.equal(canUseResetCredit(row, true, 2000, 'five_hour'), false)
  assert.equal(resetObservationUpdated(row.credential.observation, 2000), false)
  assert.equal(
    resetObservationUpdated(observation({ observedAt: 3000, resetCreditsKnown: false }), 2000),
    false,
  )
  assert.equal(
    resetObservationUpdated(observation({ observedAt: 3000, error: 'observation_partial' }), 2000),
    false,
  )
  const source = readFileSync(
    new URL(
      '../src/frontends/modern/features/account-quotas/AccountQuotaCard.vue',
      import.meta.url,
    ),
    'utf8',
  )
  assert.match(source, /observation.resetCreditsKnown === false/u)
  assert.match(source, /resetCount === undefined \? '—'/u)
})
test('only persisted 5h/7d periodic rule IDs can be reset, never total or orphan status', () => {
  const key = {
    rules: [
      { id: 21, kind: 'periodic', periodSeconds: 18000 },
      { id: 22, kind: 'periodic', periodSeconds: 604800 },
      { id: 23, kind: 'periodic', periodSeconds: 86400 },
      { id: 24, kind: 'total', periodSeconds: 18000 },
    ],
    quota: { windows: [{ id: 25, kind: 'periodic', periodSeconds: 18000 }] },
  }
  assert.deepEqual(keyResetRuleIDs(key, [18000, 604800, 86400]), [21, 22])
  assert.deepEqual(keyResetRuleIDs(key, [604800]), [22])
  assert.deepEqual(keyResetRuleIDs(key, []), [])
  assert.deepEqual(keyResetRuleIDs({ ...key, rules: [] }, [18000, 604800]), [])
})
test('optional account reset capability is authoritative and typed cards preserve expiry/availability', async () => {
  const wire = item(
    credential({
      observation: {
        state: 'fresh',
        observed_at_ms: 1000,
        snapshot: {
          plan_summary: {},
          quota_windows: [],
          reset_credits_available: 2,
          reset_credits: [
            { kind: 'week', available: false, expires_at_ms: 5000 },
            { kind: 'five_hour', available: true, expires_at_ms: 5000 },
            { kind: 'week', available: true, expires_at_ms: 1000 },
            { kind: 'week', available: true, expires_at_ms: 5000 },
          ],
        },
      },
    }),
  )
  const row = (
    await read(
      report({ items: [{ ...wire, reset_supported: true, reset_types: ['five_hour', 'week'] }] }),
    )
  ).items[0]
  assert.equal(resetCreditSupported(row, false), true)
  assert.equal(resetCreditSupported({ ...row, resetSupported: false }, true), false)
  assert.equal(resetCreditSupported({ ...row, resetSupported: undefined }, true), true)
  const cards = resetCreditRows(row.credential.observation, 2000)
  assert.deepEqual(
    cards.map((card) => card.state),
    ['unknown', 'available', 'expired', 'available'],
  )
  assert.deepEqual(
    cards.map((card) => card.kind),
    ['week', 'five_hour', 'week', 'week'],
  )
  assert.equal(canUseResetCredit(row, true, 2000, 'five_hour'), true)
  assert.equal(canUseResetCredit(row, true, 2000, 'week'), true)
  assert.equal(canUseResetCredit(row, true, 2000), false)
  row.credential.observation.creditDetails = row.credential.observation.creditDetails.slice(0, 3)
  assert.equal(canUseResetCredit(row, true, 2000, 'week'), false)
  for (const patch of [{ reset_supported: 'true' }, { reset_types: ['month'] }])
    await assert.rejects(read(report({ items: [{ ...wire, ...patch }] })), /INVALID_API_RESPONSE/u)
})

test('native reset is serial, double-submit guarded, default empty selection, and invalidates native views', async () => {
  const rows = ref([resetKey(), resetKey(202)])
  const page = ref(1),
    gate = deferred(),
    calls = []
  const env = mountQuotaAction(
    () => useKeyQuotaReset(rows, page),
    async (path, options) => {
      calls.push({ path, options })
      if (calls.length === 1) await gate.promise
    },
  )
  try {
    const action = env.result
    assert.equal(action.selected.value.size, 0)
    action.open()
    assert.equal(action.target.value.length, 0)
    for (const row of rows.value) action.select(row, true)
    action.open()
    const running = action.reset()
    await action.reset()
    assert.equal(calls.length, 1)
    assert.equal(action.pending.value, true)
    gate.resolve()
    await running
    assert.equal(calls.length, 2)
    assert.deepEqual(
      calls.map(({ path }) => path),
      ['/api/access-keys/201/cost-limits/reset', '/api/access-keys/202/cost-limits/reset'],
    )
    assert.deepEqual(calls[0].options.json, { rule_ids: [2011, 2012] })
    assert.equal(calls[0].options.method, 'POST')
    assert.equal(action.results.value.get(201), 'succeeded')
    assert.equal(action.results.value.get(202), 'succeeded')
    assert.equal(action.selected.value.size, 0)
    assert.deepEqual(env.invalidations, [
      ['modern', 'fork-key-quotas'],
      ['modern', 'access-keys'],
      ['modern', 'access-key-detail', 201],
      ['modern', 'access-key-detail', 202],
      ['modern', 'home'],
      ['modern', 'health'],
    ])
  } finally {
    env.dispose()
  }
})
test('network unknown and per-Key failures never automatically replay and always refresh', async () => {
  const rows = ref([resetKey(), resetKey(202)]),
    calls = []
  const env = mountQuotaAction(
    () => useKeyQuotaReset(rows, ref(1)),
    async (path) => {
      calls.push(path)
      if (path.includes('/201/')) throw new NetworkError()
      throw new ApiError(409, 'RULE_CHANGED', 'changed')
    },
  )
  try {
    const action = env.result
    for (const row of rows.value) action.select(row, true)
    action.open()
    await action.reset()
    await action.reset()
    assert.equal(calls.length, 2)
    assert.equal(action.results.value.get(201), 'unknown')
    assert.equal(action.results.value.get(202), 'failed')
    assert.equal(action.selected.value.size, 0)
    assert.equal(env.invalidations.length, 6)
  } finally {
    env.dispose()
  }
})
test('page changes clear selection/confirmation, selected cycles exclude total and missing cycles skip calls', async () => {
  const rows = ref([resetKey()]),
    page = ref(1),
    calls = []
  const env = mountQuotaAction(
    () => useKeyQuotaReset(rows, page),
    async (path, options) => calls.push({ path, options }),
  )
  try {
    const action = env.result
    action.select(rows.value[0], true)
    action.open()
    page.value = 2
    assert.equal(action.selected.value.size, 0)
    assert.equal(action.target.value.length, 0)
    await action.reset()
    assert.equal(calls.length, 0)
    action.select(rows.value[0], true)
    action.open()
    action.selectPeriod(18000, false)
    action.selectPeriod(86400, true)
    await action.reset()
    assert.deepEqual(calls[0].options.json, { rule_ids: [2012] })
    action.select(rows.value[0], true)
    action.open()
    action.selectPeriod(18000, false)
    action.selectPeriod(604800, false)
    await action.reset()
    assert.equal(calls.length, 1)
    rows.value = []
    assert.equal(action.selected.value.size, 0)
    assert.equal(action.target.value.length, 0)
  } finally {
    env.dispose()
  }
})
test('principal revision changes suppress late results, later Keys and cache writes', async () => {
  const rows = ref([resetKey(), resetKey(202)]),
    gate = deferred(),
    calls = []
  const env = mountQuotaAction(
    () => useKeyQuotaReset(rows, ref(1)),
    async (path) => {
      calls.push(path)
      await gate.promise
    },
  )
  try {
    const action = env.result
    for (const row of rows.value) action.select(row, true)
    action.open()
    const running = action.reset()
    env.changeRevision()
    gate.resolve()
    await running
    assert.equal(calls.length, 1)
    assert.notEqual(action.results.value.get(201), 'succeeded')
    assert.equal(env.invalidations.length, 0)
  } finally {
    env.dispose()
  }
})
test('scope disposal aborts native mutation and prevents subsequent Key calls', async () => {
  const rows = ref([resetKey(), resetKey(202)]),
    gate = deferred(),
    calls = []
  const env = mountQuotaAction(
    () => useKeyQuotaReset(rows, ref(1)),
    async (path, options) => {
      calls.push({ path, options })
      await gate.promise
    },
  )
  const action = env.result
  for (const row of rows.value) action.select(row, true)
  action.open()
  const running = action.reset()
  env.dispose()
  assert.equal(calls[0].options.signal.aborted, true)
  gate.resolve()
  await running
  assert.equal(calls.length, 1)
  assert.equal(env.invalidations.length, 0)
})
test('typed account consume preserves unknown idempotency key and shares fresh-observation wait across card types', async () => {
  const now = Date.now(),
    calls = []
  const account = {
    groupID: 7,
    channelID: 'zhipuai',
    resetSupported: true,
    resetTypes: ['five_hour', 'week'],
    credential: {
      id: 301,
      label: 'Synthetic account',
      enabled: true,
      authState: 'ready',
      observation: observation({
        observedAt: now,
        resetCredits: 2,
        creditExpirations: [now + 999999, now + 999999],
        creditDetails: [
          { kind: 'five_hour', available: true, expiresAt: now + 999999 },
          { kind: 'week', available: true, expiresAt: now + 999999 },
        ],
      }),
    },
  }
  const rows = ref([account])
  const env = mountQuotaAction(
    () => useAccountResetCredits(rows),
    async (path, options) => {
      calls.push({ path, options })
      if (calls.length === 1) throw new NetworkError()
      return { status: 'succeeded', windows_reset: 1 }
    },
  )
  try {
    const action = env.result
    assert.equal(action.supported(rows.value[0]), true)
    action.open(rows.value[0], 'five_hour')
    await action.consume()
    assert.equal(action.errors.value.get('7:301'), 'credentialCards.resetUnknown')
    assert.equal(action.allowed(rows.value[0], 'week'), false)
    action.target.value = undefined
    action.open(rows.value[0], 'five_hour')
    await action.consume()
    assert.equal(calls.length, 2)
    assert.deepEqual(calls[0].options.json, { reset_type: 'five_hour' })
    assert.equal(
      calls[0].options.headers['Idempotency-Key'],
      calls[1].options.headers['Idempotency-Key'],
    )
    assert.equal(action.allowed(rows.value[0], 'week'), false)
    rows.value = [
      {
        ...rows.value[0],
        credential: {
          ...rows.value[0].credential,
          observation: { ...rows.value[0].credential.observation, observedAt: now + 1 },
        },
      },
    ]
    await nextTick()
    assert.equal(action.allowed(rows.value[0], 'week'), true)
    action.open(rows.value[0], 'week')
    await action.consume()
    assert.deepEqual(calls[2].options.json, { reset_type: 'week' })
    assert.notEqual(
      calls[1].options.headers['Idempotency-Key'],
      calls[2].options.headers['Idempotency-Key'],
    )
    assert.equal(
      calls.every(({ path }) => path.includes('/reset-credits/consume')),
      true,
    )
  } finally {
    env.dispose()
  }
})
test('legacy account consume keeps the original empty body and missing observations cannot spend cards', async () => {
  const calls = [],
    account = {
      groupID: 7,
      resetSupported: true,
      credential: {
        id: 301,
        enabled: true,
        authState: 'ready',
        observation: observation({ resetCredits: 1, creditExpirations: [] }),
      },
    }
  const rows = ref([account])
  const env = mountQuotaAction(
    () => useAccountResetCredits(rows),
    async (path, options) => {
      calls.push({ path, options })
      return { status: 'succeeded', windows_reset: 1 }
    },
  )
  try {
    assert.equal(
      canUseResetCredit(
        { ...account, credential: { ...account.credential, observation: undefined } },
        true,
        Date.now(),
      ),
      false,
    )
    env.result.open(rows.value[0])
    await env.result.consume()
    assert.deepEqual(calls[0].options.json, {})
  } finally {
    env.dispose()
  }
})

test('narrow projection still rejects invalid IDs, status, expiry and observation', async () => {
  for (const patch of [
    { credential_id: 0 },
    { configured_status: '' },
    { effective_status: '' },
    { auth_state: '' },
    { account: { expires_at_ms: 'tomorrow' } },
    { observation: { state: 'invalid' } },
  ])
    await assert.rejects(
      read(report({ items: [item(credential(patch))] })),
      /INVALID_API_RESPONSE/u,
    )
})
