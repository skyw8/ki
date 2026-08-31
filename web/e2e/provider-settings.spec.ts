import { expect, test } from '@playwright/test'
import { pickSelectedProviderID, providerReady, sortProviderModels, sortProviders } from '../src/provider-order.ts'
import type { ProviderModel, ProviderView } from '../src/types.ts'

function provider(over: Partial<ProviderView> & Pick<ProviderView, 'id'>): ProviderView {
  return {
    name: over.id,
    api: 'completions',
    baseUrl: 'https://example.test',
    enabled: true,
    builtin: true,
    defaultModel: 'm',
    models: [],
    credential: { configured: false },
    ...over,
  }
}

function model(over: Partial<ProviderModel> & Pick<ProviderModel, 'id'>): ProviderModel {
  return {
    provider: 'p',
    name: over.id,
    enabled: true,
    builtin: true,
    baseUrl: 'https://example.test',
    ...over,
  }
}

test('sortProviders puts ready providers first and keeps catalog order inside a group', () => {
  const sorted = sortProviders([
    provider({ id: 'openrouter' }),
    provider({ id: 'openai', enabled: false, credential: { configured: true } }),
    provider({ id: 'anthropic', credential: { configured: true } }),
    provider({ id: 'dashscope-cn', credential: { configured: true } }),
    provider({ id: 'xai', enabled: false }),
  ])
  expect(sorted.map(item => item.id)).toEqual(['anthropic', 'dashscope-cn', 'openai', 'openrouter', 'xai'])
  expect(sorted.filter(providerReady).map(item => item.id)).toEqual(['anthropic', 'dashscope-cn'])
})

test('pickSelectedProviderID prefers the current selection, then a ready fallback', () => {
  const providers = [
    provider({ id: 'openrouter' }),
    provider({ id: 'dashscope-cn', credential: { configured: true } }),
    provider({ id: 'openai', credential: { configured: true } }),
  ]
  expect(pickSelectedProviderID(providers, 'openrouter')).toBe('openrouter')
  expect(pickSelectedProviderID(providers, '', 'openrouter')).toBe('dashscope-cn')
  expect(pickSelectedProviderID(providers, '', 'openai')).toBe('openai')
  expect(pickSelectedProviderID(providers, '', '')).toBe('dashscope-cn')
  expect(pickSelectedProviderID([provider({ id: 'openrouter' })], '', 'openrouter')).toBe('openrouter')
})

test('sortProviderModels puts enabled models first', () => {
  expect(sortProviderModels([
    model({ id: 'off-a', enabled: false }),
    model({ id: 'on-b' }),
    model({ id: 'off-c', enabled: false }),
    model({ id: 'on-d' }),
  ]).map(item => item.id)).toEqual(['on-b', 'on-d', 'off-a', 'off-c'])
})
