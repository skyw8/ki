import type { ProviderModel, ProviderView } from './types'

export function providerReady(provider: Pick<ProviderView, 'enabled' | 'credential'>): boolean {
  return provider.enabled && provider.credential.configured
}

function providerRank(provider: Pick<ProviderView, 'enabled' | 'credential'>): number {
  if (provider.credential.configured && provider.enabled) return 0
  if (provider.credential.configured) return 1
  if (provider.enabled) return 2
  return 3
}

export function sortProviders(providers: ProviderView[]): ProviderView[] {
  return providers
    .map((provider, index) => ({ provider, index }))
    .sort((a, b) => {
      const rank = providerRank(a.provider) - providerRank(b.provider)
      return rank !== 0 ? rank : a.index - b.index
    })
    .map(item => item.provider)
}

export function sortProviderModels(models: ProviderModel[]): ProviderModel[] {
  return models
    .map((model, index) => ({ model, index }))
    .sort((a, b) => {
      const enabled = Number(b.model.enabled) - Number(a.model.enabled)
      return enabled !== 0 ? enabled : a.index - b.index
    })
    .map(item => item.model)
}

export function pickSelectedProviderID(providers: ProviderView[], current: string, fallback = ''): string {
  const sorted = sortProviders(providers)
  if (current && sorted.some(provider => provider.id === current)) return current
  if (fallback && sorted.some(provider => provider.id === fallback && providerReady(provider))) return fallback
  return sorted.find(providerReady)?.id || sorted[0]?.id || ''
}
