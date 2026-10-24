import { execFileSync } from 'node:child_process'
import { existsSync, readdirSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

export const webDir = join(dirname(fileURLToPath(import.meta.url)), '..')

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

export function buildWebDist(): void {
  execFileSync('bun', ['run', 'build'], { cwd: webDir, stdio: 'inherit' })
}
