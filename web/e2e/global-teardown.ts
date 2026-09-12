import { existsSync, readFileSync, rmSync, unlinkSync } from 'node:fs'
import { statePath, storageStatePath } from './run-state.ts'

export default async function globalTeardown(): Promise<void> {
  // Do not skip on KI_SKIP_SERVER: the state file may still exist (pid 0)
  // and should be removed so a later bun run does not read stale fixtures.
  try {
    if (existsSync(statePath)) {
      const state = JSON.parse(readFileSync(statePath, 'utf8')) as {
        pid?: number
        home?: string
        cwd?: string
        owned?: boolean
      }
      if (state.pid) {
        try { process.kill(state.pid, 'SIGTERM') } catch { /* already gone */ }
      }
      // Why: the Go harness owns its KI_HOME/cwd and cleans them up itself, so
      // only the temp dirs this global setup created may be removed here. Without
      // this every invocation leaks one home and one cwd directory.
      if (state.owned) {
        for (const dir of [state.home, state.cwd]) {
          if (dir) {
            try { rmSync(dir, { recursive: true, force: true }) } catch { /* ignore */ }
          }
        }
      }
    }
  } finally {
    try { unlinkSync(statePath) } catch { /* ignore */ }
    try { unlinkSync(storageStatePath) } catch { /* ignore */ }
  }
}
