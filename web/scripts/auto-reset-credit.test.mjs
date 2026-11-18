import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { registerHooks } from 'node:module'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'
import { compileScript, compileTemplate, parse } from 'vue/compiler-sfc'

const cardURL = new URL('../src/frontends/modern/features/account-quotas/AccountQuotaCard.vue', import.meta.url)
const source = readFileSync(cardURL, 'utf8')
const parsed = parse(source, { filename: fileURLToPath(cardURL) })
const vueURL = import.meta.resolve('vue')
const uiStub = `
import { defineComponent, h } from ${JSON.stringify(vueURL)};
const wrapper = (tag) => defineComponent({ setup(_, { attrs, slots }) { return () => h(tag, attrs, slots.default?.()); } });
export const AppBadge = wrapper('span'), AppButton = wrapper('button'), AppTooltip = wrapper('span'), AppChannelIcon = wrapper('span');
export const AppSwitch = defineComponent({
  props: ['modelValue', 'label', 'disabled', 'loading'], emits: ['update:modelValue'],
  setup(props, { emit }) { return () => h('button', {
    role: 'switch', 'aria-label': props.label, 'aria-checked': props.modelValue,
    disabled: props.disabled || props.loading,
    onClick: () => { if (!props.disabled && !props.loading) emit('update:modelValue', !props.modelValue); }
  }); }
});`
const moduleURL = (code) => 'data:text/javascript,' + encodeURIComponent(code)
function transpile(content) {
  return ts.transpileModule(content, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
  }).outputText
}
// Exercise only the actual account parser/card. UI primitives are inert render stubs.
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === '@modern/components/ui') return nextResolve(moduleURL(uiStub), context)
    if (specifier === 'vue-router')
      return nextResolve(moduleURL(`import { h } from ${JSON.stringify(vueURL)}; export const RouterLink = (_, { slots }) => h('a', {}, slots.default?.());`), context)
    for (const [prefix, root] of [
      ['@modern/', '../src/frontends/modern/'],
      ['@shared/', '../src/shared/'],
    ])
      if (specifier.startsWith(prefix))
        return nextResolve(new URL(root + specifier.slice(prefix.length) + '.ts', import.meta.url).href, context)
    if (specifier.startsWith('.') && /\.(ts|vue)$/u.test(context.parentURL ?? '') && !/\.[a-z]+$/u.test(specifier))
      return nextResolve(specifier + '.ts', context)
    return nextResolve(specifier, context)
  },
  load(url, context, nextLoad) {
    if (url === cardURL.href)
      return { format: 'module', shortCircuit: true, source: transpile(compileScript(parsed.descriptor, { id: 'auto-reset-card', inlineTemplate: true }).content) }
    if (url.endsWith('.vue'))
      return { format: 'module', shortCircuit: true, source: 'export default { render() { return null; } }' }
    if (url.startsWith('file:') && url.endsWith('.ts'))
      return { format: 'module', shortCircuit: true, source: transpile(readFileSync(fileURLToPath(url), 'utf8')) }
    return nextLoad(url, context)
  },
})

const { createRenderer, h, nextTick, reactive } = await import('vue')
const { createI18n } = await import('vue-i18n')
const { QueryClient, VueQueryPlugin } = await import('@tanstack/vue-query')
const { apiClientKey } = await import('../src/shared/http/client-context.ts')
const { authSessionKey } = await import('../src/frontends/modern/features/auth/auth-session.ts')
const { accountQuotasKey, autoResetCreditStates, getAccountQuotas, readAutoResetCreditStatus, setAccountAutoResetCredit } = await import('../src/frontends/modern/api/fork-account-quotas.ts')
const { enUS, jaJP, zhCN } = await import('../src/frontends/modern/features/account-quotas/messages.ts')
const { default: Card } = await import(cardURL.href)

const rawCredential = { credential_id: 11, name: '', mask: 'mock***', configured_status: 'active', effective_status: 'available', auth_state: 'ready' }
const rawItem = { group_id: 7, group_name: 'Mock group', channel_id: 'codex', channel_name: 'Codex', quota_supported: true, credential: rawCredential }
async function readItem(patch = {}) {
  const page = await getAccountQuotas({ request: async () => ({ observed_at_ms: 1, page: 1, page_size: 100, total: 1, items: [{ ...rawItem, ...patch }] }) }, 1, new AbortController().signal)
  return page.items[0]
}
const baseItem = await readItem({ reset_supported: true, auto_reset_credit: { enabled: false, state: 'waiting' } })
const status = { enabled: true, state: 'ready', expires_at_ms: 2000, last_attempt_at_ms: 1000 }
const pageKey = [...accountQuotasKey, 1, 100]
async function settle() {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}
function deferred() {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
function mount({ principal = 'admin', readonly = false, item = baseItem, request = async () => status, reading = false } = {}) {
  const host = (type, text = '') => ({ type, text, props: {}, style: {}, children: [], parent: null })
  const renderer = createRenderer({
    createElement: host,
    createText: (text) => host('#text', text),
    createComment: (text) => host('#comment', text),
    setText: (node, text) => { node.text = text },
    setElementText: (node, text) => { node.text = text; node.children = [] },
    parentNode: (node) => node.parent,
    nextSibling: (node) => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
    patchProp: (node, key, previous, value) => { node.props[key] = value },
    insert(node, parent, anchor) {
      if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1)
      const index = anchor ? parent.children.indexOf(anchor) : -1
      parent.children.splice(index < 0 ? parent.children.length : index, 0, node)
      node.parent = parent
    },
    remove(node) {
      if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1)
      node.parent = null
    },
  })
  const root = host('root')
  const props = reactive({ item, readonly, syncing: false, active: false, failed: false, reading, resetSupported: true, resetAllowed: () => false, resetPending: false, resetWaiting: false })
  const state = reactive({ principalType: principal })
  const cache = new QueryClient({ defaultOptions: { queries: { gcTime: Infinity, retry: false } } })
  cache.setQueryData(pageKey, { items: [item, { ...item, groupID: 8 }, { ...item, credential: { ...item.credential, id: 12 } }] })
  const calls = [], invalidations = []
  cache.invalidateQueries = async ({ queryKey }) => { invalidations.push(queryKey) }
  let revision = 1
  const app = renderer.createApp({ setup: () => () => h(Card, props) })
  app.provide(apiClientKey, { request: async (...args) => { calls.push(args); return request(...args) } })
  app.provide(authSessionKey, { state, getRevision: () => revision })
  app.use(VueQueryPlugin, { queryClient: cache })
  app.use(createI18n({ legacy: false, locale: 'en-US', missingWarn: false, fallbackWarn: false, messages: { 'en-US': { accountQuotas: enUS, credentialCards: { observation: { unavailable: 'Unavailable' } } } } }))
  app.mount(root)
  const nodes = (node = root) => [node, ...node.children.flatMap((child) => nodes(child))]
  return { props, state, cache, calls, invalidations, root, nodes, switch: () => nodes().find((node) => node.props.role === 'switch'), text: () => nodes().map((node) => node.text).join(' '), changeRevision: () => revision++, dispose() { app.unmount(); cache.clear() } }
}

test('modified AccountQuotaCard script and template compile independently', () => {
  assert.deepEqual(parsed.errors, [])
  const script = compileScript(parsed.descriptor, { id: 'auto-reset-card' })
  const template = compileTemplate({ id: 'auto-reset-card', filename: fileURLToPath(cardURL), source: parsed.descriptor.template.content, compilerOptions: { bindingMetadata: script.bindings } })
  assert.deepEqual(template.errors, [])
})
test('optional status preserves older aggregates; strict safe projection rejects malformed data', async () => {
  assert.equal(Object.hasOwn(await readItem(), 'autoResetCredit'), false)
  assert.deepEqual(readAutoResetCreditStatus(status), { enabled: true, state: 'ready', expiresAt: 2000, lastAttemptAt: 1000 })
  for (const state of autoResetCreditStates) assert.equal(readAutoResetCreditStatus({ enabled: false, state }).state, state)
  for (const value of [null, [], {}, { enabled: 'true', state: 'waiting' }, { enabled: 1, state: 'waiting' }, { enabled: true, state: 'invalid' }, { enabled: true, state: 'waiting', expires_at_ms: -1 }, { enabled: true, state: 'waiting', expires_at_ms: '1' }, { enabled: true, state: 'waiting', last_attempt_at_ms: null }, { enabled: true, state: 'waiting', last_attempt_at_ms: Number.MAX_SAFE_INTEGER + 1 }])
    assert.throws(() => readAutoResetCreditStatus(value))
  await assert.rejects(readItem({ auto_reset_credit: null }))
  const projected = readAutoResetCreditStatus({ ...status, token: 'must-not-project', request_id: 'private' })
  assert.equal(Object.hasOwn(projected, 'token'), false)
  assert.equal(Object.hasOwn(projected, 'request_id'), false)
})
test('setting PUT uses confirmed direct status contract without operation header', async () => {
  const calls = [], signal = new AbortController().signal
  const result = await setAccountAutoResetCredit({ request: async (...args) => { calls.push(args); return status } }, 7, 11, true, signal)
  assert.deepEqual(calls, [['/api/groups/7/credentials/11/auto-reset-credit', { method: 'PUT', json: { enabled: true }, signal }]])
  assert.deepEqual(result, readAutoResetCreditStatus(status))
})
test('administrator sees per-account switch; viewer retains localized status and no mutation control', async () => {
  for (const [principal, readonly] of [['admin', false], ['access_key', true], ['access_key', false], ['admin', true]]) {
    const env = mount({ principal, readonly })
    try {
      assert.equal(Boolean(env.switch()), principal === 'admin' && !readonly)
      assert.match(env.text(), /Auto-use expiring reset cards/u)
      assert.match(env.text(), /Off/u)
      assert.equal(env.calls.length, 0)
    } finally { env.dispose() }
  }
  for (const state of autoResetCreditStates) {
    const env = mount({ principal: 'access_key', readonly: true, item: { ...baseItem, autoResetCredit: { enabled: true, state } } })
    try { assert.ok(env.text().includes(enUS.autoResetCreditState[state])); assert.equal(env.switch(), undefined) }
    finally { env.dispose() }
    for (const messages of [enUS, jaJP, zhCN]) assert.ok(messages.autoResetCreditState[state])
  }
})
test('capability requires optional supported-target object and codex or zhipuai channel', () => {
  for (const [channelID, autoResetCredit, expected] of [['codex', undefined, false], ['zhipuai', undefined, false], ['claude', baseItem.autoResetCredit, false], ['codex', baseItem.autoResetCredit, true], ['zhipuai', baseItem.autoResetCredit, true]]) {
    const env = mount({ item: { ...baseItem, channelID, autoResetCredit } })
    try { assert.equal(Boolean(env.switch()), expected) }
    finally { env.dispose() }
  }
})
test('toggle waits for confirmed PUT, prevents duplicate clicks, patches only this account and invalidates GET', async () => {
  const pending = deferred(), env = mount({ request: () => pending.promise })
  try {
    env.switch().props.onClick()
    await settle()
    assert.equal(env.calls.length, 1)
    assert.equal(env.switch().props['aria-checked'], false, 'not optimistic')
    assert.equal(env.switch().props.disabled, true)
    env.switch().props.onClick()
    assert.equal(env.calls.length, 1)
    pending.resolve(status)
    await settle()
    const items = env.cache.getQueryData(pageKey).items
    assert.equal(items[0].autoResetCredit.enabled, true)
    assert.equal(items[1].autoResetCredit.enabled, false)
    assert.equal(items[2].autoResetCredit.enabled, false)
    assert.deepEqual(env.invalidations, [accountQuotasKey])
    env.props.item = items[0]
    await nextTick()
    assert.equal(env.switch().props['aria-checked'], true)
  } finally { env.dispose() }
})
test('failed PUT retains prior preference, reports error and never retries', async () => {
  const env = mount({ request: async () => { throw new Error('mock transport failure') } })
  try {
    env.switch().props.onClick()
    await settle()
    assert.equal(env.calls.length, 1)
    assert.equal(env.cache.getQueryData(pageKey).items[0].autoResetCredit.enabled, false)
    assert.ok(env.text().includes(enUS.autoResetCreditSaveFailed))
    assert.equal(env.switch().props.disabled, false)
  } finally { env.dispose() }
})
test('revision/logout/unmount guards discard stale mutation results and abort in-flight requests', async () => {
  const obsolete = mount()
  try { obsolete.changeRevision(); obsolete.switch().props.onClick(); await settle(); assert.equal(obsolete.calls.length, 0) }
  finally { obsolete.dispose() }
  for (const invalidate of [(env) => env.changeRevision(), (env) => { env.state.principalType = 'access_key' }, (env) => { env.props.readonly = true }, (env) => env.dispose()]) {
    const pending = deferred(), env = mount({ request: () => pending.promise })
    try {
      env.switch().props.onClick()
      await settle()
      const signal = env.calls[0][1].signal
      invalidate(env)
      pending.resolve(status)
      await settle()
      assert.equal(env.invalidations.length, 0)
      if (env.state.principalType !== 'admin' || env.props.readonly) assert.equal(signal.aborted, true)
      assert.notEqual(env.cache.getQueryData(pageKey)?.items[0].autoResetCredit.enabled, true)
    } finally { if (env.root.children.length) env.dispose() }
  }
})
