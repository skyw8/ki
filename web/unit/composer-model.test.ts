import { expect, test } from 'bun:test'
import { clampThinkingEffort, emptyView, keepComposer, pickComposerModel, reconcileComposerModel, sessionCreateBody } from '../src/lib/model.ts'
import type { ModelInfo } from '../src/api/types.ts'

function model(over: Partial<ModelInfo> & Pick<ModelInfo, 'provider' | 'id'>): ModelInfo {
  return {
    name: over.id,
    spec: `${over.provider}/${over.id}`,
    thinkingLevels: ['off', 'minimal', 'low', 'medium', 'high'],
    defaultThinking: 'medium',
    ...over,
  }
}

test('clampThinkingEffort prefers default over off and walks to the nearest level', () => {
  const gpt = model({ provider: 'openai', id: 'gpt' })
  expect(clampThinkingEffort('', gpt)).toBe('medium')
  expect(clampThinkingEffort('high', gpt)).toBe('high')
  expect(clampThinkingEffort('max', gpt)).toBe('high')
  expect(clampThinkingEffort('high', model({ provider: 'x', id: 'y', thinkingLevels: ['off'], defaultThinking: 'off' }))).toBe('off')
})

test('pickComposerModel keeps the current tab choice over the registry default', () => {
  const models = [
    model({ provider: 'anthropic', id: 'claude-sonnet-5' }),
    model({ provider: 'openai', id: 'gpt-5.6-terra', thinkingLevels: ['off', 'low', 'medium', 'high', 'xhigh', 'max'] }),
  ]
  expect(pickComposerModel(models, { provider: 'openai', model: 'gpt-5.6-terra', thinkingEffort: 'high' }, { provider: 'anthropic', model: 'claude-sonnet-5', thinkingEffort: 'medium' })).toEqual({
    provider: 'openai',
    model: 'gpt-5.6-terra',
    thinkingEffort: 'high',
  })
  expect(pickComposerModel(models, null, { provider: 'anthropic', model: 'claude-sonnet-5' })).toEqual({
    provider: 'anthropic',
    model: 'claude-sonnet-5',
    thinkingEffort: 'medium',
  })
})

test('sessionCreateBody sends the current composer instead of omitting model', () => {
  const models = [model({ provider: 'openai', id: 'gpt-5.6-terra' })]
  expect(sessionCreateBody('ws1', { provider: 'openai', model: 'gpt-5.6-terra', thinkingEffort: 'high' }, models)).toEqual({
    workspaceId: 'ws1',
    model: 'openai/gpt-5.6-terra',
    thinkingEffort: 'high',
  })
})

test('a missing extension preference falls back to the registry default then first model', () => {
  const models = [
    model({ provider: 'openrouter', id: 'free' }),
    model({ provider: 'deepseek', id: 'deepseek-flash' }),
  ]
  const stale = { provider: 'openai-codex', model: 'gpt-5.4', thinkingEffort: 'high fast' }
  expect(pickComposerModel(models, stale, { provider: 'deepseek', model: 'deepseek-flash' })).toEqual({
    provider: 'deepseek', model: 'deepseek-flash', thinkingEffort: 'high',
  })
  expect(pickComposerModel(models, stale, stale)).toEqual({
    provider: 'openrouter', model: 'free', thinkingEffort: 'high',
  })
  expect(pickComposerModel([], stale, stale)).toEqual({
    provider: '', model: '', thinkingEffort: '',
  })
})

test('new sessions never send an unvalidated cached model or its thinking effort', () => {
  const stale = { provider: 'openai-codex', model: 'gpt-5.4', thinkingEffort: 'high fast' }
  const models = [model({ provider: 'deepseek', id: 'deepseek-flash' })]
  // Catalog loading/failure and extension removal both delegate to the server
  // default instead of turning browser state into an explicit invalid request.
  for (const catalog of [[], models]) {
    expect(sessionCreateBody('ws1', stale, catalog)).toEqual({ workspaceId: 'ws1' })
    expect(sessionCreateBody(null, stale, catalog)).toEqual({})
  }
  expect(sessionCreateBody('ws1', { ...stale, provider: 'deepseek' }, models)).toEqual({ workspaceId: 'ws1' })
})

const baseLevels = ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']

function fastModel(over: Partial<ModelInfo> = {}): ModelInfo {
  return model({
    provider: 'openai-codex',
    id: 'codex',
    fastServiceTier: 'priority',
    thinkingLevels: baseLevels.flatMap(level => [level, `${level} fast`]),
    ...over,
  })
}

test('all advertised fast levels including off preserve their exact selection', () => {
  const codex = fastModel()
  expect(clampThinkingEffort('', codex)).toBe('medium')
  for (const base of baseLevels) {
    for (const effort of [base, `${base} fast`]) {
      expect(clampThinkingEffort(effort, codex)).toBe(effort)
      expect(sessionCreateBody('ws1', { provider: codex.provider, model: codex.id, thinkingEffort: effort }, [codex])).toEqual({
        workspaceId: 'ws1',
        model: codex.spec,
        thinkingEffort: effort,
      })
    }
  }
})

test('fast intent follows the nearest supported effort without inventing an option', () => {
  const codex = fastModel({ thinkingLevels: ['low', 'low fast', 'high', 'high fast'] })
  expect(clampThinkingEffort('medium fast', codex)).toBe('high fast')
  expect(clampThinkingEffort('max fast', codex)).toBe('high fast')
  expect(clampThinkingEffort('off fast', codex)).toBe('low fast')
  expect(clampThinkingEffort('medium', codex)).toBe('high')
  // Capability metadata cannot authorize labels absent from the actual catalog.
  const noFastOptions = fastModel({ thinkingLevels: ['medium', 'high'] })
  expect(clampThinkingEffort('high fast', noFastOptions)).toBe('high')
})

test('switching to standard models drops fast but retains the nearest base effort', () => {
  const standard = model({ provider: 'other', id: 'plain', thinkingLevels: ['off', 'medium', 'high'] })
  expect(clampThinkingEffort('high fast', standard)).toBe('high')
  expect(clampThinkingEffort('off fast', standard)).toBe('off')
  expect(clampThinkingEffort('xhigh fast', standard)).toBe('high')
  expect(clampThinkingEffort('low fast', standard)).toBe('medium')
  expect(clampThinkingEffort('high fast', model({ provider: 'other', id: 'none', thinkingLevels: [] }))).toBe('')
})

test('unknown thinking values still fall back rather than enabling fast', () => {
  const codex = fastModel()
  for (const invalid of ['fast', 'unknown fast', 'high fast fast', 'high  fast', 'HIGH fast', 'high fast ']) {
    expect(clampThinkingEffort(invalid, codex)).toBe('medium')
  }
})

test('tab model changes retain advertised fast choices and base effort without browser storage', () => {
  const codex = fastModel()
  const standard = model({ provider: 'other', id: 'plain', thinkingLevels: ['off', 'medium', 'high'] })
  for (const base of baseLevels) {
    const choice = { provider: codex.provider, model: codex.id, thinkingEffort: `${base} fast` }
    const view = { ...emptyView(), ...choice }
    expect(keepComposer(view).thinkingEffort).toBe(choice.thinkingEffort)
    expect(pickComposerModel([codex], view)).toEqual(choice)
  }
  const switched = pickComposerModel([standard], { provider: codex.provider, model: codex.id, thinkingEffort: 'max fast' })
  expect(switched).toEqual({ provider: standard.provider, model: standard.id, thinkingEffort: 'high' })
  expect(sessionCreateBody('ws1', switched, [standard]).thinkingEffort).toBe('high')
})

test('catalog refresh reconciles new composers but never changes an existing empty session', () => {
  const first = model({ provider: 'openrouter', id: 'free' })
  const defaultModel = model({ provider: 'deepseek', id: 'deepseek-flash' })
  const catalog = [first, defaultModel]
  const fallback = { provider: defaultModel.provider, model: defaultModel.id, thinkingEffort: 'medium' }
  const started = reconcileComposerModel(emptyView(), false, catalog, fallback)
  expect(started.provider).toBe('deepseek')
  expect(started.model).toBe('deepseek-flash')
  const choice = { ...started, provider: first.provider, model: first.id, thinkingEffort: 'high' }
  expect(reconcileComposerModel(choice, false, catalog, fallback)).toBe(choice)
  expect(reconcileComposerModel(choice, false, [defaultModel], fallback)).toEqual({ ...choice, provider: defaultModel.provider, model: defaultModel.id })
  expect(reconcileComposerModel(choice, false, [], fallback)).toEqual({ ...choice, provider: '', model: '', thinkingEffort: '' })
  // nodes.length is zero, but a persisted session is not a new composer.
  expect(reconcileComposerModel(choice, true, [defaultModel], fallback)).toBe(choice)
  expect(reconcileComposerModel(choice, true, [], fallback)).toBe(choice)
})

test('startup ignores old origin model preferences without reading or deleting browser storage', () => {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  const oldPreference = '{"provider":"openai-codex","model":"codex","thinkingEffort":"high fast"}'
  const values = new Map([['ki-last-model', oldPreference]])
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: () => { throw new Error('model startup must not read browser storage') },
      setItem: () => { throw new Error('model startup must not write browser storage') },
      removeItem: () => { throw new Error('model startup must not remove browser storage') },
    },
  })
  try {
    const available = model({ provider: 'deepseek', id: 'deepseek-flash' })
    const view = reconcileComposerModel(emptyView(), false, [available], {
      provider: available.provider, model: available.id, thinkingEffort: 'medium',
    })
    expect(view.provider).toBe('deepseek')
    expect(view.model).toBe('deepseek-flash')
    expect(values.get('ki-last-model')).toBe(oldPreference)
  } finally {
    if (descriptor) Object.defineProperty(globalThis, 'localStorage', descriptor)
    else Reflect.deleteProperty(globalThis, 'localStorage')
  }
})
