import type { Entry } from '../api/types'

export type CompactionOperation = { start?: Entry; end?: Entry; summary?: Entry }

/**
 * Lifecycle entries are durable status, not model steps. Sequential operations
 * pair across page boundaries; a committed checkpoint owns the visible row
 * instead of leaving progress plus summary duplicates behind.
 */
export function compactionOperations(entries: Entry[]): CompactionOperation[] {
  const byId = new Map(entries.map(entry => [entry.id, entry]))
  const operations: CompactionOperation[] = []
  let pending: CompactionOperation | undefined
  for (const entry of entries) {
    if (entry.type === 'compaction_start') {
      pending = { start: entry }
      operations.push(pending)
    } else if (entry.type === 'compaction' && pending) {
      pending.summary = entry
    } else if (entry.type === 'compaction_end') {
      const operation = pending ?? {}
      if (!pending) operations.push(operation)
      operation.end = entry
      const details = entry.details as { entryId?: string } | undefined
      const summary = details?.entryId && byId.get(details.entryId)
      if (summary && summary.type === 'compaction') operation.summary = summary
      pending = undefined
    } else if (entry.message?.role === 'user' || entry.type === 'request_header') {
      // A new request cannot finish an interrupted earlier operation. Inline
      // server compaction does include its assistant before the checkpoint.
      pending = undefined
    }
  }
  return operations
}

export function isCompactionLifecycle(entry: Entry): boolean {
  return entry.type === 'compaction_start' || entry.type === 'compaction_end'
}

/** Lifecycle rows settle at the terminal identity; checkpoints replace them. */
export function compactionAnchors(entries: Entry[]): Set<string> {
  return new Set(compactionOperations(entries).filter(op => !op.summary).map(op => (op.end ?? op.start)!.id))
}
