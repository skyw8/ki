import type { LoopEvent, Message } from './types'

/** Decode before batching React updates: skipping a patch would lose its base. */
export function messageDecoder() {
  let message: Message | undefined
  let seq: number | undefined
  let stream: number | undefined
  return (event: LoopEvent): LoopEvent => {
    if (['message_start', 'message_end', 'agent_end'].includes(event.type)) message = undefined
    if (event.type !== 'message_update') return event
    if (event.messagePatch) {
      if (!message || seq !== event.messagePatch.baseSeq || stream !== event.messageStream) throw new Error('Message patch base mismatch')
      // Copy only the changed path. The previous yielded snapshot may still be
      // rendered or waiting in App's frame batch and must remain immutable.
      let next: unknown = message
      for (const change of event.messagePatch.changes) {
        const apply = (node: unknown, depth: number): unknown => {
          if (depth === change.path.length) {
            if (change.op === 'set') return change.value
            if (change.op === 'append' && typeof node === 'string' && typeof change.value === 'string') return node + change.value
            if (change.op === 'resize' && Array.isArray(node) && typeof change.value === 'number' && Number.isInteger(change.value) && change.value >= 0 && change.value <= 1_000_000) {
              const out = node.slice(0, change.value)
              while (out.length < change.value) out.push(null)
              return out
            }
            throw new Error('Invalid message patch operation')
          }
          const key = change.path[depth]
          if (!node || typeof node !== 'object') throw new Error('Invalid message patch path')
          const copy = (Array.isArray(node) ? [...node] : { ...node }) as Record<string, unknown>
          if (change.op === 'remove' && depth === change.path.length - 1 && !Array.isArray(node)) delete copy[key]
          else {
            const value = apply(Object.hasOwn(copy, key) ? copy[key] : undefined, depth + 1)
            // Tool arguments may legitimately contain __proto__/constructor.
            // Define a data property instead of invoking Object's setter.
            Object.defineProperty(copy, key, { value, enumerable: true, configurable: true, writable: true })
          }
          return copy
        }
        next = apply(next, 0)
      }
      event = { ...event, message: next as Message, messagePatch: undefined }
    }
    if (event.message) {
      message = event.message
      seq = event.seq
      stream = event.messageStream
    }
    return event
  }
}
