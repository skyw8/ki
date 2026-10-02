import { expect, test } from 'bun:test'
import { clampThinkingEffort, initialView, keepComposer, loadLastComposerModel, pickComposerModel, saveLastComposerModel, sessionCreateBody } from '../src/lib/model.ts'
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

test('pickComposerModel keeps last-used over the registry default', () => {
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

test('composer reload and provider changes retain advertised fast choices and base effort', () => {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  const values = new Map<string, string>()
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => { values.set(key, value) },
    },
  })
  try {
    const codex = fastModel()
    const standard = model({ provider: 'other', id: 'plain', thinkingLevels: ['off', 'medium', 'high'] })
    for (const base of baseLevels) {
      const choice = { provider: codex.provider, model: codex.id, thinkingEffort: `${base} fast` }
      saveLastComposerModel(choice)
      expect(loadLastComposerModel()).toEqual(choice)
      expect(initialView().thinkingEffort).toBe(choice.thinkingEffort)
      expect(keepComposer(initialView()).thinkingEffort).toBe(choice.thinkingEffort)
      expect(pickComposerModel([codex], loadLastComposerModel())).toEqual(choice)
    }
    const loaded = loadLastComposerModel()!
    const switched = pickComposerModel([standard], loaded)
    expect(switched).toEqual({ provider: standard.provider, model: standard.id, thinkingEffort: 'high' })
    saveLastComposerModel(switched)
    expect(pickComposerModel([standard], loadLastComposerModel())).toEqual(switched)
    expect(sessionCreateBody('ws1', switched, [standard]).thinkingEffort).toBe('high')
  } finally {
    if (descriptor) Object.defineProperty(globalThis, 'localStorage', descriptor)
    else Reflect.deleteProperty(globalThis, 'localStorage')
  }
})
