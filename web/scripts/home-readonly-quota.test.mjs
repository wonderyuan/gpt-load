import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { registerHooks } from 'node:module'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'
import { parse, compileScript, compileTemplate } from 'vue/compiler-sfc'

// Load only the quota composables and their dependencies; no frontend build or suite.
registerHooks({
  resolve(specifier, context, nextResolve) {
    for (const [prefix, root] of [
      ['@modern/', '../src/frontends/modern/'],
      ['@shared/', '../src/shared/'],
    ]) {
      if (specifier.startsWith(prefix))
        return nextResolve(
          new URL(root + specifier.slice(prefix.length) + '.ts', import.meta.url).href,
          context,
        )
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
        source: ts.transpileModule(readFileSync(fileURLToPath(url), 'utf8'), {
          compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
        }).outputText,
      }
    return nextLoad(url, context)
  },
})

const { createRenderer, reactive, ref, nextTick } = await import('vue')
const { QueryClient, VueQueryPlugin } = await import('@tanstack/vue-query')
const { apiClientKey } = await import('../src/shared/http/client-context.ts')
const { authSessionKey } = await import('../src/frontends/modern/features/auth/auth-session.ts')
const { RequestCancelledError } = await import('../src/shared/http/errors.ts')
const {
  useAccountQuotas,
  useKeyQuotas,
  useAccountResetCredits,
  useAccountQuotaSync,
  useKeyQuotaReset,
} = await import('../src/frontends/modern/features/account-quotas/use-account-quotas.ts')

function mount(principalType, setup) {
  const host = () => ({ children: [] })
  const renderer = createRenderer({
    createElement: host,
    createText: host,
    createComment: host,
    insert() {},
    remove() {},
    setText() {},
    setElementText() {},
    parentNode: () => null,
    nextSibling: () => null,
    patchProp() {},
  })
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const state = reactive({ principalType })
  const calls = []
  let result
  let revision = 1
  const app = renderer.createApp({
    setup() {
      result = setup()
      return () => null
    },
  })
  app.provide(apiClientKey, {
    async request(path, options = {}) {
      calls.push({ path, options })
      assert.equal(options.method, undefined, 'focused quota reads must never write')
      if (path === '/api/channels') return { items: [] }
      assert.match(path, /^\/api\/fork\/(account|key)-quotas\?page=\d+&page_size=100$/u)
      return {
        observed_at_ms: 1,
        page: Number(new URL(path, 'https://synthetic.test').searchParams.get('page')),
        page_size: 100,
        total: 0,
        items: [],
        ...(path.includes('/key-quotas?') ? { groups: [] } : {}),
      }
    },
  })
  app.provide(authSessionKey, { state, getRevision: () => revision })
  app.use(VueQueryPlugin, { queryClient: cache })
  app.mount(host())
  return {
    result,
    state,
    cache,
    calls,
    changeRevision() {
      revision++
    },
    dispose() {
      app.unmount()
      cache.clear()
    },
  }
}
async function settle() {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}

for (const principal of ['admin', 'access_key']) {
  test(`${principal} can read, refresh and paginate both quota feeds without widening admin`, async () => {
    const page = ref(1)
    const env = mount(principal, () => ({
      accounts: useAccountQuotas(page),
      keys: useKeyQuotas(page),
      reset: useAccountResetCredits(ref([])),
    }))
    try {
      await settle()
      const { accounts, keys } = env.result
      for (const query of [accounts, keys]) {
        assert.equal(query.admin.value, principal === 'admin')
        assert.equal(query.isSuccess.value, true)
        assert.equal(query.data.value.page, 1)
      }
      assert.equal(env.calls.length, principal === 'admin' ? 3 : 2)
      assert.equal(env.calls.some(({ path }) => path === '/api/channels'), principal === 'admin')
      env.calls.length = 0
      await Promise.all([accounts.refresh(), keys.refresh()])
      assert.equal(env.calls.length, 2)
      page.value = 2
      await settle()
      assert.equal(env.calls.length, 4)
      assert.equal(accounts.data.value.page, 2)
      assert.equal(keys.data.value.page, 2)
      env.calls.length = 0
      env.state.principalType = null
      await settle()
      await Promise.all([accounts.refresh(), keys.refresh()])
      assert.equal(env.calls.length, 0, 'logout must block manual refetch too')
      for (const query of env.cache.getQueryCache().getAll()) {
        await assert.rejects(
          Promise.resolve().then(() => query.options.queryFn({ signal: new AbortController().signal })),
          RequestCancelledError,
          'queryFn must reject callers that bypass enabled after logout',
        )
      }
      assert.equal(env.calls.length, 0)
    } finally {
      env.dispose()
    }
  })
}

test('anonymous quota queries remain disabled even for explicit refresh', async () => {
  const env = mount(null, () => [useAccountQuotas(), useKeyQuotas(), useAccountResetCredits(ref([]))])
  try {
    await settle()
    await Promise.all(env.result.slice(0, 2).map((query) => query.refresh()))
    assert.equal(env.calls.length, 0)
    assert.equal(env.result[0].admin.value, false)
    assert.equal(env.result[1].admin.value, false)
  } finally {
    env.dispose()
  }
})

test('access_key cannot sync, consume reset credits, select Keys or confirm forced reset targets', async () => {
  const account = {
    groupID: 7,
    channelID: 'synthetic',
    quotaSupported: true,
    resetSupported: true,
    credential: {
      id: 11,
      enabled: true,
      authState: 'ready',
      observation: { state: 'fresh', resetCredits: 1, creditExpirations: [] },
    },
  }
  const key = { id: 21, rules: [{ id: 31, kind: 'periodic', periodSeconds: 18000 }] }
  const env = mount('access_key', () => ({
    sync: useAccountQuotaSync(),
    reset: useAccountResetCredits(ref([account])),
    keyReset: useKeyQuotaReset(ref([key]), ref(1)),
  }))
  try {
    const { sync, reset, keyReset } = env.result
    await settle()
    assert.equal(reset.supported(account), true, 'viewer must retain reset-credit display capability')
    assert.equal(reset.allowed(account), false)
    reset.open(account)
    assert.equal(reset.target.value, undefined)
    reset.target.value = account
    await reset.consume()
    await sync.sync([account])
    assert.equal(sync.syncing.value, false)
    keyReset.select(key, true)
    keyReset.open()
    assert.equal(keyReset.selected.value.size, 0)
    assert.equal(keyReset.target.value.length, 0)
    keyReset.target.value = [key]
    assert.equal(keyReset.canConfirm.value, false, 'current() must reject access_key')
    await keyReset.reset()
    assert.equal(env.calls.length, 0, 'viewer cannot read admin channel configuration or mutate')
  } finally {
    env.dispose()
  }
})

test('existing mutation session revision guards still reject obsolete admin confirmations', async () => {
  const account = { groupID: 7, credential: { id: 11 } }
  const key = { id: 21, rules: [{ id: 31, kind: 'periodic', periodSeconds: 18000 }] }
  const env = mount('admin', () => ({
    reset: useAccountResetCredits(ref([account])),
    keyReset: useKeyQuotaReset(ref([key]), ref(1)),
  }))
  try {
    await settle()
    env.calls.length = 0
    const { reset, keyReset } = env.result
    keyReset.select(key, true)
    keyReset.open()
    assert.equal(keyReset.canConfirm.value, true)
    env.changeRevision()
    reset.target.value = account
    assert.equal(reset.allowed(account), false)
    // getRevision() is not reactive: reset() must still refuse the cached confirmation.
    await reset.consume()
    await keyReset.reset()
    assert.equal(env.calls.length, 0)
  } finally {
    env.dispose()
  }
})

function elements(node, parents = []) {
  const found = node.type === 1 ? [{ node, parents }] : []
  return found.concat((node.children ?? []).flatMap((child) => elements(child, [...parents, node])))
}
function directive(node, name, arg) {
  return node.props.find((prop) => prop.type === 7 && prop.name === name && (!arg || prop.arg?.content === arg))
}
function condition(node) {
  return directive(node, 'if')?.exp?.content
}
function binding(node, name) {
  return directive(node, 'bind', name)?.exp?.content
}
const compiled = new Map()
for (const name of ['AccountQuotasPanel', 'AccountQuotaCard', 'KeyQuotaCard']) {
  const filename = name + '.vue'
  const source = readFileSync(new URL('../src/frontends/modern/features/account-quotas/' + filename, import.meta.url), 'utf8')
  const parsed = parse(source, { filename })
  test(`${filename} script and template compile independently`, () => {
    assert.deepEqual(parsed.errors, [])
    const script = compileScript(parsed.descriptor, { id: name })
    const template = compileTemplate({
      id: name,
      filename,
      source: parsed.descriptor.template.content,
      compilerOptions: { bindingMetadata: script.bindings },
    })
    assert.deepEqual(template.errors, [])
  })
  compiled.set(name, { source, nodes: elements(parsed.descriptor.template.ast) })
}

test('panel only exposes mutation buttons, dialogs and group-management links to admin', () => {
  const { source, nodes } = compiled.get('AccountQuotasPanel')
  const panel = nodes.find(({ node }) => node.tag === 'AppPanel').node
  assert.equal(condition(panel), undefined, 'panel itself must remain visible to viewers')
  const buttons = nodes.filter(({ node }) => node.tag === 'AppButton')
  for (const action of ['sync.sync(items)', 'keyReset.open()']) {
    const { node } = buttons.find(({ node }) => directive(node, 'on', 'click')?.exp?.content === action)
    assert.equal(condition(node), 'admin')
  }
  const dialogs = nodes.filter(({ node }) => node.tag === 'AppConfirmDialog')
  assert.equal(dialogs.length, 2)
  assert.equal(dialogs.every(({ node }) => condition(node) === 'admin'), true)
  const link = nodes.find(({ node }) => node.tag === 'RouterLink')
  assert.equal(condition(link.node), 'admin')
  const siblings = link.parents.at(-1).children.filter((node) => node.type === 1)
  assert.equal(siblings[siblings.indexOf(link.node) + 1].tag, 'span')
  assert.ok(directive(siblings[siblings.indexOf(link.node) + 1], 'else'))
  const cards = nodes.filter(({ node }) => ['AccountQuotaCard', 'KeyQuotaCard'].includes(node.tag))
  assert.equal(cards.length, 3)
  assert.equal(cards.every(({ node }) => binding(node, 'readonly') === '!accounts.admin.value'), true)
  assert.match(source, /channels\?: readonly Pick<GroupChannel, 'id' \| 'name' \| 'icon' \| 'mark'>\[\]/u)
  assert.match(source, /admin.value \? reset.channels.data.value : props.channels/u)
  assert.equal(binding(cards.find(({ node }) => node.tag === 'AccountQuotaCard').node, 'reset-supported'), 'reset.supported(item)')
  assert.equal(nodes.filter(({ node }) => node.tag === 'AppPagination').length, 2)
})

test('readonly account card preserves count, expiry details and expansion but no sync/reset/link/instruction', () => {
  const { source, nodes } = compiled.get('AccountQuotaCard')
  assert.match(source, /readonly\?: boolean/u)
  assert.match(source, /\{ readonly: false \}/u)
  const sync = nodes.find(({ node }) => directive(node, 'on', 'click')?.exp?.content === "$emit('sync')")
  assert.equal(condition(sync.node), 'item.quotaSupported && !readonly')
  const reset = nodes.find(({ node }) => directive(node, 'on', 'click')?.exp?.content === "$emit('reset', kind)")
  assert.equal(reset.parents.some((parent) => parent.type === 1 && condition(parent) === '!readonly && resetSupported'), true)
  const link = nodes.find(({ node }) => node.tag === 'RouterLink')
  assert.equal(condition(link.node), '!readonly')
  const siblings = link.parents.at(-1).children.filter((node) => node.type === 1)
  const label = siblings[siblings.indexOf(link.node) + 1]
  assert.equal(label.tag, 'span')
  assert.ok(directive(label, 'else'))
  const instruction = nodes.find(({ node }) => node.tag === 'p' && node.loc.source.includes('accountQuotas.autoSelectCard'))
  assert.equal(condition(instruction.node), '!readonly')
  const expand = nodes.find(({ node }) => directive(node, 'on', 'click')?.exp?.content === 'cardsExpanded = !cardsExpanded')
  assert.equal(condition(expand.node), undefined)
  assert.equal(binding(expand.node, 'aria-expanded'), 'cardsExpanded')
  assert.equal(expand.parents.some((parent) => parent.type === 1 && condition(parent) === 'resetSupported || cards.length'), true)
  assert.match(source, /resetCount === undefined \? '—' : n\(resetCount\)/u)
  assert.match(source, /credentialTime\(card.expiresAt, locale\)/u)
  assert.match(source, /v-show="cardsExpanded"/u)
  assert.match(source, /credential\.account \|\| credential\.mask/u)
})

test('readonly Key card hides selection while keeping masked identity and quota windows', () => {
  const { source, nodes } = compiled.get('KeyQuotaCard')
  assert.match(source, /readonly\?: boolean/u)
  assert.match(source, /\{ readonly: false \}/u)
  const checkbox = nodes.find(({ node }) => node.tag === 'AppCheckbox')
  assert.equal(condition(checkbox.node), '!readonly')
  assert.match(source, /\{\{ item.mask \}\}/u)
  assert.match(source, /keyDisplayEntries\(props.item\)/u)
  assert.match(source, /amount\(limit\(entry\)\)/u)
  assert.equal(nodes.some(({ node }) => ['RouterLink', 'AppButton'].includes(node.tag)), false)
})
