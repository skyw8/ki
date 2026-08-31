import { execFileSync } from 'node:child_process'
import { existsSync } from 'node:fs'
import { join } from 'node:path'

export function goBinary(): string {
  const configured = process.env.GO?.trim()
  if (configured) return configured

  try {
    execFileSync('go', ['version'], { stdio: 'ignore' })
    return 'go'
  } catch {
    const goRoot = process.env.GOROOT?.trim()
    const executable = process.platform === 'win32' ? 'go.exe' : 'go'
    const candidate = goRoot ? join(goRoot, 'bin', executable) : ''
    // Why: tests run on Linux, macOS, and Windows; GOROOT is the only safe
    // fallback when the toolchain is not on PATH, rather than a host path.
    if (candidate && existsSync(candidate)) return candidate
  }

  throw new Error('go toolchain not found; add go to PATH or set GO/GOROOT')
}
