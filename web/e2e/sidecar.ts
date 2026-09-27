import { execFileSync } from 'node:child_process'
import { copyFileSync, existsSync, mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { goBinary } from './go-toolchain.ts'

export function sidecarBin(home: string, name: string): string {
  const dir = join(home, 'playwright-bin')
  mkdirSync(dir, { recursive: true })
  const suffix = process.platform === 'win32' ? '.exe' : ''
  const shared = process.env.KI_E2E_SIDECAR || join(dir, `sidecar-fixture${suffix}`)
  // The parallel runner supplies a fixture built once for the whole run. Direct
  // Playwright invocations build once in their own home, including serial runs.
  if (!process.env.KI_E2E_SIDECAR && !existsSync(shared)) {
    const root = join(dirname(fileURLToPath(import.meta.url)), '../..')
    execFileSync(goBinary(), ['build', '-o', shared, './e2e/testdata/extensions/sidecar'], { cwd: root, stdio: 'inherit' })
  }
  const bin = join(dir, `${name}${suffix}`)
  copyFileSync(shared, bin)
  return bin
}
