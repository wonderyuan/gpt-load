import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { registerHooks, stripTypeScriptTypes } from 'node:module'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'
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
    const source = readFileSync(fileURLToPath(url), 'utf8')
    let output
    try {
      output = stripTypeScriptTypes(source)
    } catch (error) {
      if (error.code !== 'ERR_UNSUPPORTED_TYPESCRIPT_SYNTAX') throw error
      output = ts.transpileModule(source, {
        compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
      }).outputText
    }
    return { format: 'module', shortCircuit: true, source: output }
  },
})
const { readModelInfo, getModelInfo } = await import('../src/frontends/modern/api/model-info.ts')
const {
  paretoFrontier,
  paretoModels,
  paretoDefaultScale,
  reconcileHiddenModels,
  filterParetoPoints,
  paretoTrajectories,
  frontierPath,
  scatterLayout,
  scatterLabels,
  scatterPointLabel,
  quotaPeak,
  quotaPercent,
  quotaValue,
  matchesModel,
  sortSpeedModels,
} = await import('../src/frontends/modern/features/model-info/data.ts')
const { zhCN, enUS, jaJP } = await import('../src/frontends/modern/features/model-info/messages.ts')

const point = (patch = {}) => ({
  id: 'm-high',
  model_id: 'm',
  model: 'Model A',
  effort: 'high',
  score: 80,
  cost_usd: 0.3,
  duration_minutes: 2.5,
  composite_cost: 1.875,
  ...patch,
})
const section = (data, patch = {}) => ({
  status: 'ready',
  fetched_at_ms: 2000,
  updated_at_ms: 1000,
  data,
  ...patch,
})
function fixture() {
  return {
    observed_at_ms: 3000,
    intelligence: section({
      batch: 'public-test',
      tested_at: '2026-10-17',
      max_score: 100,
      efforts: ['high', 'low'],
      models: [
        {
          id: 'm',
          name: 'Model A',
          results: [{ effort: 'high', score: 80, cost_usd: 1.2, duration_seconds: 600 }],
        },
      ],
      pareto: [point()],
    }),
    speed: section({
      window_minutes: 5,
      models: [
        {
          id: 'm',
          name: 'Model A',
          tokens_per_second: 42.6,
          requests: 12,
          devices: 3,
          harnesses: [{ name: 'Codex', requests: 12 }],
          history: [
            { at_ms: 1000, value: null },
            { at_ms: 2000, value: 42.6 },
          ],
        },
      ],
    }),
    quota: section({
      recorded_at: '2026-10-17',
      plans: [
        {
          id: 'p',
          name: 'Public plan',
          monthly_usd: 20,
          models: [{ name: 'Model A', weekly_tokens: null, api_value_usd: 0 }],
        },
      ],
    }),
  }
}
test('authenticated client endpoint preserves total-vs-average units and whitelists data', async () => {
  const calls = []
  const raw = fixture()
  raw.advertisement = 'discard me'
  raw.intelligence.data.subtasks = [{ score: 99 }]
  const signal = new AbortController().signal
  const parsed = await getModelInfo(
    {
      request: async (path, options) => {
        calls.push({ path, options })
        return raw
      },
    },
    signal,
  )
  assert.equal(calls.length, 1)
  assert.equal(calls[0].path, '/api/fork/model-info')
  assert.equal(calls[0].options.signal, signal)
  assert.equal(calls[0].options.method, undefined)
  assert.equal(parsed.intelligence.data.models[0].results[0].cost_usd, 1.2)
  assert.equal(parsed.intelligence.data.models[0].results[0].duration_seconds, 600)
  assert.equal(parsed.intelligence.data.pareto[0].cost_usd, 0.3)
  assert.equal(parsed.intelligence.data.pareto[0].duration_minutes, 2.5)
  assert.equal(parsed.intelligence.data.pareto[0].composite_cost, 1.875)
  assert.equal(JSON.stringify(parsed).includes('discard me'), false)
  assert.equal(Object.hasOwn(parsed.intelligence.data, 'subtasks'), false)
})
test('missing observations remain null, explicit zero remains a measured zero', () => {
  const raw = fixture()
  Object.assign(raw.intelligence.data.models[0].results[0], {
    score: null,
    cost_usd: null,
    duration_seconds: null,
  })
  raw.speed.data.models[0].tokens_per_second = null
  const parsed = readModelInfo(raw)
  assert.equal(parsed.speed.data.models[0].tokens_per_second, null)
  assert.equal(parsed.intelligence.data.models[0].results[0].score, null)
  assert.equal(parsed.quota.data.plans[0].models[0].weekly_tokens, null)
  assert.equal(parsed.quota.data.plans[0].models[0].api_value_usd, 0)
  assert.equal(quotaValue(null, 'weekly_tokens', 'en-US'), '—')
  assert.equal(quotaValue(0, 'api_value_usd', 'en-US'), '$0.00')
})
test('new models and reasoning efforts expand without frontend changes', () => {
  const raw = fixture()
  raw.intelligence.data.efforts.push('adaptive-plus')
  raw.intelligence.data.models.push({
    id: 'future-model',
    name: 'Future Model',
    results: [{ effort: 'adaptive-plus', score: 77.56, cost_usd: 0.03, duration_seconds: 180 }],
  })
  raw.intelligence.data.pareto.push(
    point({
      id: 'future',
      model_id: 'future-model',
      model: 'Future Model',
      effort: 'adaptive-plus',
    }),
  )
  const parsed = readModelInfo(raw)
  assert.equal(parsed.intelligence.data.models[1].results[0].score, 77.56)
  assert.equal(parsed.intelligence.data.efforts.at(-1), 'adaptive-plus')
  assert.equal(parsed.intelligence.data.pareto.at(-1).model_id, 'future-model')
})
test('section freshness is independent, including fully unavailable initial data', () => {
  const raw = fixture()
  raw.intelligence.status = 'stale'
  raw.speed = section(null, { status: 'unavailable', fetched_at_ms: null, updated_at_ms: null })
  const parsed = readModelInfo(raw)
  assert.equal(parsed.intelligence.status, 'stale')
  assert.equal(parsed.speed.status, 'unavailable')
  assert.equal(parsed.speed.data, null)
  assert.equal(parsed.quota.status, 'ready')
})
test('malformed scalars, unknown statuses, duplicate identities and invalid scores are rejected', () => {
  const mutations = [
    (r) => {
      r.observed_at_ms = -1
    },
    (r) => {
      r.speed.status = 'fresh'
    },
    (r) => {
      r.quota.fetched_at_ms = 'now'
    },
    (r) => {
      r.quota.data = null
    },
    (r) => {
      r.speed.data.window_minutes = 0
    },
    (r) => {
      r.speed.data.models[0].tokens_per_second = NaN
    },
    (r) => {
      r.speed.data.models[0].tokens_per_second = undefined
    },
    (r) => {
      r.speed.data.models[0].devices = 1.5
    },
    (r) => {
      r.speed.data.models[0].harnesses.push(r.speed.data.models[0].harnesses[0])
    },
    (r) => {
      r.speed.data.models[0].history.push(r.speed.data.models[0].history[0])
    },
    (r) => {
      r.intelligence.data.models.push(r.intelligence.data.models[0])
    },
    (r) => {
      r.intelligence.data.models[0].results.push(r.intelligence.data.models[0].results[0])
    },
    (r) => {
      r.intelligence.data.models[0].results[0].score = 101
    },
    (r) => {
      r.intelligence.data.models[0].results[0].effort = 'invented'
    },
    (r) => {
      r.intelligence.data.efforts.push('high')
    },
    (r) => {
      r.intelligence.data.pareto[0].composite_cost = Infinity
    },
    (r) => {
      r.quota.data.plans[0].monthly_usd = -1
    },
    (r) => {
      r.quota.data.plans[0].models[0].weekly_tokens = '10M'
    },
    (r) => {
      r.quota.data.plans.push(r.quota.data.plans[0])
    },
  ]
  for (const mutate of mutations) {
    const raw = fixture()
    mutate(raw)
    assert.throws(() => readModelInfo(raw), /INVALID_API_RESPONSE/u)
  }
})
test('successful unavailable response clears expired sections while request failures keep query cache', async () => {
  const { QueryClient } = await import('@tanstack/vue-query')
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const key = ['model-info-expiry-test']
  let raw = fixture()
  let failed = false
  const load = () =>
    client.fetchQuery({
      queryKey: key,
      staleTime: 0,
      queryFn: () =>
        getModelInfo(
          {
            request: async () => {
              if (failed) throw new Error('network')
              return raw
            },
          },
          new AbortController().signal,
        ),
    })
  const first = await load()
  failed = true
  await assert.rejects(load(), /network/u)
  assert.equal(client.getQueryData(key), first)
  failed = false
  raw = fixture()
  raw.observed_at_ms = 5000
  raw.speed = section(null, { status: 'unavailable', updated_at_ms: null })
  raw.quota.data.plans[0].monthly_usd = 25
  const next = await load()
  assert.equal(next.speed.status, 'unavailable')
  assert.equal(next.speed.data, null)
  assert.equal(next.speed.updated_at_ms, null)
  assert.equal(next.quota.data.plans[0].monthly_usd, 25)
  assert.equal(first.quota.data.plans[0].monthly_usd, 20)
  client.clear()
})
test('frontier honors the chosen native metric, keeps ties and responds to filters', () => {
  const points = [
    point(),
    point({ id: 'dominated', score: 70, cost_usd: 0.4, duration_minutes: 1 }),
    point({ id: 'tie' }),
    point({ id: 'cheap', score: 60, cost_usd: 0 }),
  ]
  assert.deepEqual([...paretoFrontier(points, 'cost_usd')], ['m-high', 'tie', 'cheap'])
  assert.equal(paretoFrontier(points, 'duration_minutes').has('dominated'), true)
  assert.equal(paretoFrontier([points[1]], 'cost_usd').has('dominated'), true)
  assert.equal(matchesModel('Model A', '  MODEL a '), true)
  assert.deepEqual([...paretoFrontier([], 'composite_cost')], [])
})
test('scatter axes remain finite and bounded for zero, singleton and empty observations', () => {
  for (const points of [[], [point()], [point({ cost_usd: 0, score: 0 })]]) {
    for (const scale of ['log', 'linear']) {
      const chart = scatterLayout(points, 'cost_usd', 100, scale, { width: 320 })
      assert.ok(chart.width >= 780, 'narrow screens scroll without shrinking native text')
      assert.ok(chart.height >= 420)
      for (const item of chart.points) {
        assert.ok(Number.isFinite(item.x) && Number.isFinite(item.y))
        assert.ok(item.x >= chart.frame.left && item.x <= chart.frame.right)
        assert.ok(item.y >= chart.frame.top && item.y <= chart.frame.bottom)
      }
      for (const tick of [...chart.ticks, ...chart.yTicks]) assert.ok(Number.isFinite(tick.value))
      assert.equal(Object.hasOwn(points[0] ?? {}, 'x'), false)
    }
  }
  assert.deepEqual(scatterLayout([], 'duration_minutes', 100).points, [])
})
test('log axis pads actual extent, keeps data-domain score zoom and is stable when filtering', () => {
  const points = [0.11, 0.15, 0.25, 0.32].map((cost_usd, index) =>
    point({ id: String(index), cost_usd, score: 110 + index * 9 }),
  )
  const chart = scatterLayout(points, 'cost_usd', 150, 'log')
  assert.equal(chart.scale, 'log')
  assert.equal(chart.xDomain[0], 0.11 / 1.2)
  assert.equal(chart.xDomain[1], 0.32 * 1.2)
  assert.ok(chart.yDomain[0] > 0 && chart.yDomain[0] < 110)
  assert.equal(chart.yDomain[1], 150)
  assert.ok(chart.ticks.length >= 2)
  assert.ok(chart.ticks.every((tick) => tick.x >= chart.frame.left && tick.x <= chart.frame.right))
  assert.ok(chart.ticks.every((tick, i, ticks) => !i || tick.x - ticks[i - 1].x >= 76))
  const filtered = scatterLayout([points[2]], 'cost_usd', 150, 'log', { domainPoints: points })
  assert.deepEqual(filtered.xDomain, chart.xDomain)
  assert.deepEqual(filtered.yDomain, chart.yDomain)
  assert.equal(filtered.points[0].x, chart.points[2].x)
  assert.equal(filtered.points[0].y, chart.points[2].y)
  const zeros = scatterLayout([point({ cost_usd: 0 }), ...points], 'cost_usd', 150, 'log')
  assert.equal(zeros.scale, 'linear')
  assert.equal(zeros.xDomain[0], 0)
  const decades = scatterLayout(
    [0.001, 0.01, 0.1, 1].map((cost_usd, index) => point({ id: String(index), cost_usd })),
    'cost_usd',
    100,
  )
  const gaps = decades.points.slice(1).map((p, i) => p.x - decades.points[i].x)
  assert.ok(gaps.every((gap) => Math.abs(gap - gaps[0]) < 1e-8))
})
test('dynamic model legend uses eight distinct native tokens and preserves hidden colors on refresh', () => {
  const points = Array.from({ length: 10 }, (_, index) =>
    point({ id: String(index), model_id: 'model-' + index, model: 'Future Model ' + index }),
  )
  const initial = paretoModels(points)
  assert.equal(new Set(initial.slice(0, 8).map((model) => model.color)).size, 8)
  assert.equal(new Set(initial.slice(0, 8).map((model) => model.shape)).size, 8)
  assert.equal(paretoDefaultScale('duration_minutes'), 'linear')
  assert.equal(paretoDefaultScale('cost_usd'), 'log')
  assert.equal(paretoDefaultScale('composite_cost'), 'log')
  assert.ok(initial.every((model) => /^var\(--modern-(?:chart|protocol)-/u.test(model.color)))
  assert.equal(initial[8].color, initial[0].color, 'additional models cycle the native palette')
  const next = paretoModels([points[3], points[1], ...points.slice(4)], initial)
  assert.equal(next[0].color, initial[3].color)
  const hidden = reconcileHiddenModels(next, new Set(['model-3', 'removed']))
  assert.deepEqual([...hidden], ['model-3'])
  assert.ok(!hidden.has('model-4'), 'new models remain visible by default')
  assert.equal(filterParetoPoints(points, new Set()).length, points.length)
  assert.deepEqual(
    filterParetoPoints(points.slice(0, 3), new Set(['model-1'])).map((p) => p.model_id),
    ['model-0', 'model-2'],
  )
  assert.deepEqual(filterParetoPoints(points, hidden, 'missing-effort'), [])
  assert.deepEqual(filterParetoPoints(points, new Set(points.map((p) => p.model_id))), [])
})
test('source-style local labels are bounded, capped and never create remote label leaders', () => {
  const points = Array.from({ length: 48 }, (_, index) =>
    point({
      id: 'point-' + index,
      model_id: 'model-' + (index % 8),
      model: 'GPT-6.1 ' + ['Sol', 'Terra', 'Luna', 'Astra'][index % 4],
      effort: ['low', 'medium', 'high', 'xhigh', 'max', 'ultra'][Math.floor(index / 8)],
      score: 110 + (index % 3) * 0.01,
      cost_usd: 0.2 + (index % 4) * 0.0001,
    }),
  )
  for (const width of [320, 780, 960, 1440]) {
    const chart = scatterLayout(points, 'cost_usd', 150, 'log', { width })
    const options = { selected: points[0].id, frontier: paretoFrontier(points, 'cost_usd') }
    const labels = scatterLabels(chart, options)
    assert.ok(labels.length > 0 && labels.length <= (chart.width >= 1100 ? 24 : 16))
    assert.ok(
      labels.length < points.length,
      'crowded labels are omitted, not scattered across the plot',
    )
    assert.equal(labels[0].id, points[0].id, 'selected identity has first priority')
    assert.deepEqual(scatterLabels(chart, options), labels)
    assert.deepEqual(chart.yDomain, [50, 150])
    for (const label of labels) {
      const original = chart.points.find((p) => p.id === label.id)
      assert.ok([scatterPointLabel(original), original.effort].includes(label.text))
      assert.equal(Object.hasOwn(label, 'leader'), false)
      assert.ok(label.x >= chart.frame.left && label.y >= chart.frame.top)
      assert.ok(label.x + label.width <= chart.frame.right)
      assert.ok(label.y + label.height <= chart.frame.bottom)
      const gapX = Math.max(label.x - original.x, original.x - label.x - label.width, 0)
      const gapY = Math.max(label.y - original.y, original.y - label.y - label.height, 0)
      assert.ok(Math.hypot(gapX, gapY) <= 22, 'labels stay adjacent to their real data points')
      for (const other of labels.filter((item) => item.id !== label.id))
        assert.ok(
          label.x + label.width <= other.x ||
            other.x + other.width <= label.x ||
            label.y + label.height <= other.y ||
            other.y + other.height <= label.y,
          `labels ${label.id} and ${other.id} overlap at ${width}px`,
        )
    }
  }
  const spread = Array.from({ length: 5 }, (_, index) =>
    point({
      id: 'focus-' + index,
      effort: ['low', 'medium', 'high', 'xhigh', 'ultra'][index],
      cost_usd: 0.01 * 3 ** index,
      score: 65 + index * 15,
    }),
  )
  const focused = scatterLabels(scatterLayout(spread, 'cost_usd', 150), { focusedModel: 'm' })
  assert.equal(focused.length, spread.length)
  assert.ok(focused.every((label) => label.text.startsWith('Model A ·')))
  assert.equal(scatterPointLabel(point({ model: 'Future 中文模型' })), 'Future 中文模型 · high')
})
test('effort trajectories stay per model in reasoning order and frontier is a stepped path', () => {
  const points = [
    point({ id: 'a-high', effort: 'high' }),
    point({ id: 'b-medium', model_id: 'b', effort: 'medium' }),
    point({ id: 'a-low', effort: 'low', score: 60, cost_usd: 0.1 }),
    point({ id: 'a-future', effort: 'adaptive-plus' }),
  ]
  const tracks = paretoTrajectories(points, ['adaptive-plus', 'high', 'medium', 'low'])
  assert.deepEqual(
    tracks[0].points.map((p) => p.id),
    ['a-low', 'a-high', 'a-future'],
  )
  assert.deepEqual(
    tracks[1].points.map((p) => p.id),
    ['b-medium'],
  )
  assert.equal(points[0].id, 'a-high', 'trajectory sorting never mutates source metrics')
  const chart = scatterLayout(points, 'cost_usd', 100)
  assert.match(frontierPath(chart, paretoFrontier(points, 'cost_usd')), /^M.* H.* V/u)
  assert.equal(frontierPath(chart, new Set()), '')
})
test('quota bars share a metric-wide scale but do not sum, zero and unknown differ', () => {
  const plans = fixture().quota.data.plans
  plans.push({
    id: 'p2',
    name: 'Plan 2',
    monthly_usd: 200,
    models: [{ name: 'Model A', weekly_tokens: 1000, api_value_usd: 50 }],
  })
  assert.equal(quotaPeak(plans, 'api_value_usd'), 50)
  assert.equal(quotaPercent(plans[0].models[0], 'weekly_tokens', 1000), null)
  assert.equal(quotaPercent(plans[0].models[0], 'api_value_usd', 50), 0)
  assert.equal(quotaPercent(plans[1].models[0], 'api_value_usd', 50), 100)
  assert.equal(quotaPeak([], 'weekly_tokens'), null)
  assert.equal(quotaPercent({ weekly_tokens: 0 }, 'weekly_tokens', 0), 0)
})
test('mean speed uses supplied observations, never derives an unweighted cross-model average', () => {
  const rows = [
    { id: 'unknown', name: 'Unknown', tokens_per_second: null },
    { id: 'zero', name: 'Zero', tokens_per_second: 0 },
    { id: 'fast', name: 'Fast', tokens_per_second: 100 },
  ]
  assert.deepEqual(
    sortSpeedModels(rows).map((model) => model.id),
    ['fast', 'zero', 'unknown'],
  )
  assert.equal(rows[0].id, 'unknown')
  const raw = fixture()
  raw.speed.data.models[0].history.reverse()
  assert.deepEqual(
    readModelInfo(raw).speed.data.models[0].history.map((p) => p.at_ms),
    [1000, 2000],
  )
})
test('all feature messages have locale parity', () => {
  const keys = (value) =>
    Object.entries(value)
      .flatMap(([key, item]) =>
        typeof item === 'object' ? keys(item).map((nested) => key + '.' + nested) : [key],
      )
      .sort()
  assert.deepEqual(keys(enUS), keys(zhCN))
  assert.deepEqual(keys(jaJP), keys(zhCN))
})
test('modern route stays isolated and native dashboard has no outbound embeds or raw controls', () => {
  const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8')
  const modern = JSON.parse(read('../../internal/webui/modern_page_routes.json'))
  const classic = JSON.parse(read('../../internal/webui/page_routes.json'))
  assert.equal(
    modern.routes.some((r) => r.name === 'model-info' && r.path === '/model-info'),
    true,
  )
  assert.equal(
    classic.routes.some((r) => r.path === '/model-info'),
    false,
  )
  assert.match(
    read('../src/frontends/modern/router.ts'),
    /item\.id === 'modelInfo'[\s\S]*?ModelInfoView\.vue/u,
  )
  const root = '../src/frontends/modern/features/model-info/'
  const files = [
    'ModelInfoView.vue',
    'IntelligenceMatrix.vue',
    'ParetoChart.vue',
    'SpeedModels.vue',
    'QuotaRadar.vue',
    'SectionStatus.vue',
  ]
  for (const file of files) {
    const source = read(root + file)
    assert.doesNotMatch(source, /<(?:iframe|a|button|input|select|svg)\b|https?:\/\//u)
    const { descriptor } = parse(source, { filename: file })
    const compiled = compileTemplate({
      source: descriptor.template.content,
      filename: file,
      id: file,
    })
    assert.deepEqual(compiled.errors, [], `${file} template must compile, not only type-check`)
  }
  const matrix = read(root + 'IntelligenceMatrix.vue')
  assert.match(matrix, /cell\.result\.score/u)
  assert.match(matrix, /cost\(cell\.result\.cost_usd/u)
  assert.match(matrix, /grid-row: 1 \/ 3/u)
  assert.doesNotMatch(matrix, /<span class="modern-sr-only"/u)
  assert.match(matrix, /minutes\(cell\.result\.duration_seconds\)/u)
  assert.doesNotMatch(matrix, /selected|detailId|aria-pressed/u)
  const chart = read(root + 'ParetoChart.vue')
  assert.match(chart, /@keydown="navigate/u)
  assert.match(chart, /:aria-controls="detailId"/u)
  const page = read(root + 'ModelInfoView.vue')
  assert.match(page, /usePageRefresh/u)
  assert.match(page, /autoRefresh:/u)
  assert.doesNotMatch(page, /setInterval|refetchInterval/u)
})
