import { randomUUID } from 'node:crypto'
import { join } from 'node:path'
import { tmpdir } from 'node:os'

function safeRunID(value: string): string {
  const safe = value.replace(/[^a-zA-Z0-9_.-]+/g, '-').replace(/^-+|-+$/g, '')
  return safe || randomUUID()
}

// Why: separate Playwright invocations may run at the same time (for example,
// Go e2e plus a local responsive run). Fixed files let one teardown erase the
// other run's auth and server state, which also leaves the first server alive.
export const runID = safeRunID(
  process.env.KI_PW_RUN_ID?.trim() || `${process.pid}-${randomUUID()}`,
)

process.env.KI_PW_RUN_ID = runID

export const statePath = join(tmpdir(), `ki-pw-${runID}-serve.json`)
export const storageStatePath = join(tmpdir(), `ki-pw-${runID}-storage.json`)

export function baseURLForAddress(address: string): string {
  const value = address.trim().startsWith(':') ? `127.0.0.1${address.trim()}` : address.trim()
  const url = new URL(`http://${value}`)
  if (url.hostname === '0.0.0.0' || url.hostname === '[::]' || url.hostname === '::') {
    url.hostname = '127.0.0.1'
  }
  return url.origin
}
