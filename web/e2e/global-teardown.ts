import { existsSync, readFileSync, rmSync, unlinkSync } from 'node:fs'
import { statePath, storageStatePath } from './run-state.ts'

export default async function globalTeardown(): Promise<void> {
  try {
    if (existsSync(statePath)) {
      const state = JSON.parse(readFileSync(statePath, 'utf8')) as {
        pid?: number
        home?: string
        cwd?: string
        owned?: boolean
      }
      if (state.pid) {
        try { process.kill(state.pid, 'SIGTERM') } catch {}
      }
      if (state.owned) {
        for (const dir of [state.home, state.cwd]) {
          if (dir) {
            try { rmSync(dir, { recursive: true, force: true }) } catch {}
          }
        }
      }
    }
  } finally {
    try { unlinkSync(statePath) } catch {}
    try { unlinkSync(storageStatePath) } catch {}
  }
}
