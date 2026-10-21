import { execFileSync } from 'node:child_process'
import { existsSync, mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { goBinary } from './go-toolchain.ts'

const repo = join(dirname(fileURLToPath(import.meta.url)), '../..')

/**
 * writeExtensionMirror publishes a fake release with the real archive layout
 * and returns the file:// base URL the installer appends `release` to. The
 * WebUI install test points `extensions.source` at it, so the download,
 * checksum and unpack path runs without network access. The directory name is
 * passed as `release` so it matches whatever the server was configured with.
 *
 * Why a Go helper instead of a JS zip writer: the fixture must produce exactly
 * the bytes scripts/build-extensions.go publishes, and a hand-rolled zip in the
 * test would hide a layout mismatch instead of catching it.
 */
export function writeExtensionMirror(release = 'v0.0.0-test'): string {
  const root = mkdtempSync(join(tmpdir(), 'ki-pw-mirror-'))
  const bin = process.env.KI_E2E_EXTMIRROR || join(root, process.platform === 'win32' ? 'extmirror.exe' : 'extmirror')
  if (!process.env.KI_E2E_EXTMIRROR && !existsSync(bin)) {
    execFileSync(goBinary(), ['build', '-o', bin, './e2e/testdata/extmirror'], { cwd: repo, stdio: 'inherit' })
  }
  execFileSync(bin, ['-out', root, '-release', release], { cwd: repo, stdio: 'inherit' })
  return `file://${root}`
}
