import { execFileSync } from 'node:child_process'
import { existsSync, readdirSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

// web/dist is untracked build output that the test binary embeds
// (`go build -tags embed`). The helpers below keep the e2e runs honest about it.

export const webDir = join(dirname(fileURLToPath(import.meta.url)), '..')

/** Files and directories whose contents determine web/dist. */
const inputs = ['index.html', 'package.json', 'bun.lock', 'vite.config.ts', 'tsconfig.json']
const inputDirs = ['src', 'public']

function newestMtime(path: string): number {
  const info = statSync(path)
  if (!info.isDirectory()) return info.mtimeMs
  let newest = 0
  for (const name of readdirSync(path)) {
    const child = join(path, name)
    newest = Math.max(newest, newestMtime(child))
  }
  return newest
}

/**
 * webDistStale reports whether dist/index.html is missing or older than any
 * frontend input.
 *
 * Why: building only when dist is absent silently runs the e2e suite against
 * the previous UI after a source edit — the tests pass, the change is not
 * covered, and nothing says so. Comparing mtimes catches that without the cost
 * of hashing every input on each run.
 */
export function webDistStale(): boolean {
  const index = join(webDir, 'dist', 'index.html')
  if (!existsSync(index)) return true
  const built = statSync(index).mtimeMs
  for (const rel of [...inputs, ...inputDirs]) {
    const path = join(webDir, rel)
    if (existsSync(path) && newestMtime(path) > built) return true
  }
  return false
}

/** buildWebDist rebuilds the SPA that the e2e servers embed. */
export function buildWebDist(): void {
  execFileSync('bun', ['run', 'build'], { cwd: webDir, stdio: 'inherit' })
}
