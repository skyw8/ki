import type { ContextCategory } from './model'

// Keep high-volume input/result categories in different hue families. Close
// green/teal swatches were indistinguishable in thin stacks and small legends.
export const CATEGORY_COLORS: Record<ContextCategory, string> = {
  system: '#7468e7',
  tools: '#d88d13',
  human: '#e45756',
  agent: '#b95091',
  extension: '#8b6545',
  assistant: '#438bdd',
  tool: '#13a99a',
  compaction: '#859b3d',
  remote: '#7d8998',
}
