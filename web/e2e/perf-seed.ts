import { appendFileSync, readFileSync, writeFileSync } from 'node:fs'
import { randomUUID } from 'node:crypto'
import { join } from 'node:path'

export type PerfSeedSpec = {
  turns: number
  assistantBytes: number
  toolResultBytes?: number
  systemBytes?: number
  title: string
  repeatSamePrompt?: boolean
}

function entryID(): string {
  return randomUUID().replace(/-/g, '')
}

function line(obj: unknown): string {
  return JSON.stringify(obj)
}

// Append a synthetic transcript onto an existing session directory (header already present).
export function appendTranscript(dir: string, spec: PerfSeedSpec): { leafId: string; jsonlBytes: number } {
  const configPath = join(dir, 'config.json')
  const jsonlPath = join(dir, 'events.jsonl')
  const config = JSON.parse(readFileSync(configPath, 'utf8')) as {
    activeLeafId?: string
    title?: string
    provider?: string
    model?: string
  }
  const existing = readFileSync(jsonlPath, 'utf8').trim().split('\n')
  let parentId = ''
  for (const raw of existing.slice(1)) {
    if (!raw.trim()) continue
    const row = JSON.parse(raw) as { id?: string }
    if (row.id) parentId = row.id
  }
  const stamp = new Date().toISOString()
  const system = `perf-system ${'S'.repeat(spec.systemBytes ?? 256)}`
  const tools = [{ name: 'Read', description: 'Read a file', parameters: { type: 'object' } }]
  const assistant = 'A'.repeat(Math.max(1, spec.assistantBytes))
  const toolBody = spec.toolResultBytes ? 'T'.repeat(spec.toolResultBytes) : ''
  const chunks: string[] = []
  for (let i = 0; i < spec.turns; i++) {
    const userId = entryID()
    chunks.push(line({ type: 'message', id: userId, parentId, timestamp: stamp, message: { role: 'user', content: [{ type: 'text', text: `turn ${i}` }] } }))
    parentId = userId
    const headerId = entryID()
    const headerSys = spec.repeatSamePrompt === false ? `${system} ${i}` : system
    chunks.push(line({
      type: 'request_header', id: headerId, parentId, timestamp: stamp, system: headerSys, tools,
      provider: config.provider ?? 'openai', modelId: config.model ?? 'model',
    }))
    parentId = headerId
    const asstId = entryID()
    const callId = `call${i}`
    const content: Array<Record<string, unknown>> = [{ type: 'text', text: assistant }]
    if (toolBody) content.push({ type: 'toolCall', id: callId, name: 'Read', arguments: { file_path: 'f.txt' } })
    chunks.push(line({ type: 'message', id: asstId, parentId, timestamp: stamp, message: { role: 'assistant', content, usage: { input: 10, output: 4 } } }))
    parentId = asstId
    if (!toolBody) continue
    const toolId = entryID()
    chunks.push(line({
      type: 'message', id: toolId, parentId, timestamp: stamp,
      message: { role: 'toolResult', toolCallId: callId, toolName: 'Read', content: [{ type: 'text', text: toolBody }] },
    }))
    parentId = toolId
  }
  appendFileSync(jsonlPath, `${chunks.join('\n')}\n`)
  config.activeLeafId = parentId
  config.title = spec.title
  writeFileSync(configPath, `${JSON.stringify(config, null, 2)}\n`)
  return { leafId: parentId, jsonlBytes: readFileSync(jsonlPath).byteLength }
}
