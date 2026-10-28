import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { registerHooks } from 'node:module'
import { test } from 'node:test'
import ts from 'typescript'
import { fileURLToPath } from 'node:url'
import { compileTemplate, parse } from 'vue/compiler-sfc'

registerHooks({
  resolve(specifier, context, nextResolve) {
    for (const [prefix, root] of Object.entries({
      '@modern/': new URL('../src/frontends/modern/', import.meta.url),
      '@shared/': new URL('../src/shared/', import.meta.url),
    })) {
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
    if (!url.startsWith('file:') || !url.endsWith('.ts')) return nextLoad(url, context)
    return {
      format: 'module',
      shortCircuit: true,
      source: ts.transpileModule(readFileSync(fileURLToPath(url), 'utf8'), {
        compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
      }).outputText,
    }
  },
})
const { getHomeOverview } = await import('../src/frontends/modern/api/home.ts')

test('shared overview reads global display data without retaining secrets or management metadata', async () => {
  const calls = []
  const raw = {
    observed_at_ms: 1000,
    base: {
      server_now_ms: 1000,
      started_at_ms: 500,
      version: 'preview',
      inventory: {
        group_count: 3,
        credential_count: 3,
        available_credential_count: 3,
        model_count: 12,
      },
      concurrency: { current: 1, limit: 4 },
      access_keys: [
        { id: 9, name: 'Other Key', masked_key: 'sk-***a', key_value: 'not-a-public-secret' },
      ],
      current_access_key: null,
    },
    statistics: {
      observed_at_ms: 1000,
      from_ms: 0,
      to_ms: 1000,
      summary: { request_count: 30, failure_count: 2 },
      series: [{ bucket_start_ms: 0, request_count: 30, failure_count: 2 }],
    },
    groups: [
      {
        id: 3,
        name: 'Other Group',
        channel_id: 'codex',
        channel_name: 'Codex',
        channel_icon: '',
        channel_mark: 'C',
        enabled: true,
        availability: 'ready',
        model_count: 12,
        credential_count: 1,
        endpoint: 'https://private.example',
      },
    ],
    accounts: {
      observed_at_ms: 1000,
      items: [
        {
          credential_key: 'a'.repeat(64),
          channel_id: 'codex',
          channel_name: 'Codex',
          channel_icon: '',
          channel_mark: 'C',
          group_count: 1,
          available_group_count: 1,
          group_id: 3,
          credential: {
            credential_id: 1,
            name: 'Account',
            mask: 'masked',
            configured_status: 'active',
            effective_status: 'available',
            auth_state: 'ready',
            account: { email_mask: 'u***@example.com', email: 'raw@example.com' },
            data: 'private-token',
            observation: null,
          },
        },
      ],
    },
    health: {
      observed_at_ms: 1000,
      stats_window_seconds: 300,
      counts: { credentials: 3, available: 3, cooldown: 0, blacklisted: 0 },
      groups: [],
      cooldown_credentials: [],
      blacklisted_credentials: [],
      low_quota_credentials: [],
      expiring_reset_credits: [],
      blocked_access_keys: [],
    },
  }
  const signal = new AbortController().signal
  const result = await getHomeOverview(
    {
      request: async (path, options) => {
        calls.push({ path, options })
        return raw
      },
    },
    signal,
  )
  assert.equal(calls[0].path, '/api/fork/home-overview')
  assert.equal(calls[0].options.signal, signal)
  assert.equal(result.base.keys[0].id, 9)
  assert.equal(result.base.groups, 3)
  assert.equal(result.statistics.requests, 30)
  assert.equal(result.accounts.items[0].credential.account, 'u***@example.com')
  assert.doesNotMatch(
    JSON.stringify(result),
    /private-token|raw@example|not-a-public-secret|private\.example/u,
  )
})

test('changed homepage templates compile and readonly controls remain separate from admin actions', () => {
  const root = '../src/frontends/modern/features/home/'
  const read = (name) => readFileSync(new URL(root + name, import.meta.url), 'utf8')
  for (const name of [
    'HomeView.vue',
    'HomeStatusBar.vue',
    'HomeAccounts.vue',
    'HomeAttention.vue',
    'HomeTrend.vue',
    'GatewayConnection.vue',
    'ConnectTerminal.vue',
  ]) {
    const { descriptor } = parse(read(name), { filename: name })
    assert.deepEqual(
      compileTemplate({ source: descriptor.template.content, filename: name, id: name }).errors,
      [],
    )
  }
  const home = read('HomeView.vue')
  assert.match(home, /getHomeOverview/u)
  assert.match(home, /<AccountQuotasPanel ref=/u)
  assert.doesNotMatch(home, /<HomeAccessKey/u)
  assert.match(home, /:readonly="!admin"/u)
  const connection = read('GatewayConnection.vue')
  assert.match(connection, /props\.readonly \|\| !key\.value/u)
  assert.match(connection, /v-if="quickImport && !readonly"/u)
  assert.match(connection, /<AppCopyValue\s+v-if="!readonly"/u)
  assert.match(read('ConnectTerminal.vue'), /<AppCopyValue\s+v-if="!readonly"/u)
  assert.match(read('HomeAccounts.vue'), /<CredentialDisplay[\s\S]*v-if="readonly"/u)
  assert.match(read('HomeAttention.vue'), /readonly \? 'div' : RouterLink/u)
  const navigation = readFileSync(
    new URL('../src/frontends/modern/app/navigation.ts', import.meta.url),
    'utf8',
  )
  assert.match(navigation, /id: 'modelInfo'[\s\S]*?adminOnly: false/u)
})
