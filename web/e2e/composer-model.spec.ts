import { expect, test } from '@playwright/test'
import { clampThinkingEffort, pickComposerModel, sessionCreateBody } from '../src/model.ts'
import type { ModelInfo } from '../src/types.ts'

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
