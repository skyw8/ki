import { existsSync, readFileSync, unlinkSync } from 'node:fs'
import { statePath } from './global-setup.ts'

export default async function globalTeardown(): Promise<void> {
  if (process.env.KI_SKIP_SERVER === '1') return
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
