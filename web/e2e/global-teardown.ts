import { existsSync, readFileSync, unlinkSync } from 'node:fs'
import { statePath } from './global-setup.ts'

export default async function globalTeardown(): Promise<void> {
  // Do not skip on KI_SKIP_SERVER: the state file may still exist (pid 0)
  // and should be removed so a later npm run does not read stale fixtures.
  if (!existsSync(statePath)) return
  try {
    const { pid } = JSON.parse(readFileSync(statePath, 'utf8')) as { pid: number }
    if (pid) {
      try { process.kill(pid, 'SIGTERM') } catch { /* already gone */ }
    }
  } finally {
    try { unlinkSync(statePath) } catch { /* ignore */ }
  }
}
