import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { registerHooks } from 'node:module'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'
import * as vue from 'vue'
import { renderToString } from 'vue/server-renderer'
import { parse, compileScript } from 'vue/compiler-sfc'

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier.startsWith('@modern/'))
      return nextResolve(
        new URL('../src/frontends/modern/' + specifier.slice(8) + '.ts', import.meta.url).href,
        context,
      )
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
        source: ts.transpileModule(readFileSync(fileURLToPath(url), 'utf8'), {
          compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
        }).outputText,
      }
    return nextLoad(url, context)
  },
})

const data = await import('../src/frontends/modern/features/account-quotas/data.ts')
const presentation =
  await import('../src/frontends/modern/features/groups/credential-presentation.ts')
const now = Date.now()
const passthrough = (_, { slots }) => slots.default?.()
const modules = {
  vue,
  'vue-i18n': {
    useI18n: () => ({
      t: (key) => key.split('.').at(-1),
      n: (value, options) => new Intl.NumberFormat('en-US', options).format(value),
      te: () => false,
      locale: vue.ref('en-US'),
    }),
  },
  '@modern/components/ui': {
    AppTooltip: passthrough,
    AppBadge: passthrough,
    AppCheckbox: () => null,
    AppChannelIcon: () => null,
    AppProgressBar: () => null,
  },
  '@modern/components/ui/clock': { useClock: () => vue.ref(now) },
  '@modern/features/groups/credential-presentation': presentation,
  './data': data,
}
const components = new Map()
for (const name of ['QuotaWindow', 'KeyQuotaCard']) {
  const filename = name + '.vue'
  const source = readFileSync(
    new URL('../src/frontends/modern/features/account-quotas/' + filename, import.meta.url),
    'utf8',
  )
  const parsed = parse(source, { filename })
  assert.deepEqual(parsed.errors, [])
  const script = compileScript(parsed.descriptor, { id: name, inlineTemplate: true })
  const output = ts.transpileModule(script.content, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
  }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', output)(
    (specifier) => {
      assert.ok(Object.hasOwn(modules, specifier), 'Unexpected dependency: ' + specifier)
      return modules[specifier]
    },
    module,
    module.exports,
  )
  components.set(name, module.exports.default)
}

async function text(name, props) {
  const html = await renderToString(vue.createSSRApp(components.get(name), props))
  return html
    .replace(/<[^>]*>/gu, ' ')
    .replace(/\s+/gu, ' ')
    .trim()
}
const window = {
  id: 'five-hour',
  scope: 'account',
  models: [],
  state: 'available',
  windowSeconds: 18000,
  resetsAt: now + 18000000,
  unit: 'usd',
  used: 1,
  limit: 4,
}
const observation = { state: 'fresh' }

test('account amount and cycle estimate show the authoritative used percentage', async () => {
  assert.match(await text('QuotaWindow', { window, observation }), /used 1 usd · 25%/u)
  assert.match(
    await text('QuotaWindow', {
      window: { ...window, utilization: 0.255 },
      observation,
      estimate: {
        used: '2500',
        total: '10000',
        remaining: '7500',
        unit: 'glm_coding_credits',
        estimated: true,
      },
    }),
    /used ≈2,500 \/ 10,000 glm_coding_credits · 25\.5%/u,
  )
})

test('account percentages preserve zero, exhaustion and existing percent-only displays', async () => {
  for (const [used, expected] of [
    [0, '0%'],
    [4, '100%'],
  ])
    assert.match(
      await text('QuotaWindow', { window: { ...window, used }, observation }),
      new RegExp(expected),
    )
  for (const current of [
    { ...window, used: 25, limit: 100, unit: 'percent', utilization: 0.25 },
    { ...window, used: undefined, utilization: 0.25 },
  ]) {
    const rendered = await text('QuotaWindow', { window: current, observation })
    assert.equal(rendered.match(/25%/gu)?.length, 1)
  }
  for (const props of [
    { window: { ...window, limit: 0 }, observation },
    { window: { ...window, resetsAt: now - 1 }, observation },
    { window, observation: { state: 'unavailable' } },
  ])
    assert.doesNotMatch(await text('QuotaWindow', props), /\d(?:\.\d+)?%/u)
})

function keyProps(usedUSD, limitUSD = '100', endsAt = now + 18000000) {
  const rule = { id: 1, kind: 'periodic', periodSeconds: 18000, limitUSD }
  return {
    item: {
      id: 1,
      name: 'Example',
      mask: 'sk-…test',
      status: 'active',
      expiresAt: null,
      groupIDs: [],
      accounting: { unit: 'glm_coding_credits', internalUnitsPerCredit: '0.01' },
      rules: [rule],
      quota: {
        observedAt: now,
        allowed: true,
        windows: [{ ...rule, usedUSD, remainingUSD: '0', endsAt, status: 'available' }],
      },
    },
    groups: [],
    readonly: true,
    selected: false,
    selectionDisabled: false,
  }
}

test('Key amounts show fractional, zero and capped used percentages', async () => {
  for (const [used, limit, expected] of [
    ['25.5', '100', '25.5%'],
    ['0', '100', '0%'],
    ['150', '100', '100%'],
  ]) {
    const rendered = await text('KeyQuotaCard', keyProps(used, limit))
    assert.ok(rendered.includes('glm_coding_credits · ' + expected), rendered)
  }
})

test('Key zero limits, expired and missing snapshots do not invent percentages', async () => {
  const missing = keyProps('25')
  missing.item.quota.windows = []
  for (const props of [keyProps('0', '0'), keyProps('25', '100', now - 1), missing])
    assert.doesNotMatch(await text('KeyQuotaCard', props), /\d(?:\.\d+)?%/u)
})
