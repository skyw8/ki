import { execFileSync, spawn } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '../..')
export const statePath = join(tmpdir(), 'ki-pw-serve.json')

function goBin(): string {
  if (process.env.GO) return process.env.GO
  try {
    execFileSync('go', ['version'], { stdio: 'ignore' })
    return 'go'
  } catch {
    for (const c of ['/home/hgy/sdk/go/bin/go', '/usr/local/go/bin/go']) {
      if (existsSync(c)) return c
    }
  }
  throw new Error('go toolchain not found')
}

async function waitHealth(url: string): Promise<void> {
  for (let i = 0; i < 80; i++) {
    try {
      const res = await fetch(url)
      if (res.ok) return
    } catch {
      // still booting
    }
    await new Promise(r => setTimeout(r, 100))
  }
  throw new Error(`ki serve did not become healthy: ${url}`)
}

function dashscopeKey(): string {
  for (const k of ['DASHSCOPE_CN_API_KEY', 'DASHSCOPE_API_KEY']) {
    const v = (process.env[k] ?? '').trim()
    if (v) return v
  }
  const tomlPath = join(homedir(), '.ki', 'ki.toml')
  if (!existsSync(tomlPath)) {
    throw new Error('no dashscope-cn key; set DASHSCOPE_CN_API_KEY or ~/.ki/ki.toml')
  }
  let section = ''
  let fallback = ''
  for (const raw of readFileSync(tomlPath, 'utf8').split('\n')) {
    const line = raw.trim()
    if (line.startsWith('[') && line.endsWith(']')) {
      section = line.slice(1, -1).trim()
      continue
    }
    if (!line.startsWith('api_key')) continue
    const i = line.indexOf('=')
    if (i < 0) continue
    const v = line.slice(i + 1).trim().replace(/^['"]|['"]$/g, '')
    if (!v) continue
    if (section === 'providers.dashscope-cn') return v
    if (section === 'providers.dashscope' && !fallback) fallback = v
  }
  if (fallback) return fallback
  throw new Error('no dashscope-cn key; set DASHSCOPE_CN_API_KEY or ~/.ki/ki.toml')
}

export default async function globalSetup(): Promise<void> {
  if (process.env.KI_SKIP_SERVER === '1') return

  const live = process.env.KI_LIVE === '1'
  const addr = process.env.KI_SERVE_ADDR || (live ? '127.0.0.1:19833' : '127.0.0.1:19832')
  const home = mkdtempSync(join(tmpdir(), 'ki-pw-home-'))
  const cwd = mkdtempSync(join(tmpdir(), 'ki-pw-cwd-'))
  const bin = process.env.KI_BIN || join(tmpdir(), live ? 'ki-pw-live-bin' : 'ki-pw-bin')
  if (!process.env.KI_BIN) {
    execFileSync(goBin(), ['build', '-o', bin, './cmd/ki'], { cwd: root, stdio: 'inherit' })
  }

  const env: NodeJS.ProcessEnv = {
    ...process.env,
    KI_HOME: home,
    KI_SERVER_ADDR: '',
  }
  if (live) {
    const key = dashscopeKey()
    env.KI_FAKE = ''
    env.DASHSCOPE_CN_API_KEY = key
    env.DASHSCOPE_API_KEY = key
    writeFileSync(join(home, 'ki.toml'), [
      '[defaults]',
      'provider = "dashscope-cn"',
      'model = "qwen3.7-plus"',
      '',
      '[providers.dashscope-cn]',
      `api_key = "${key}"`,
      'base_url = "https://dashscope.aliyuncs.com/compatible-mode/v1"',
      '',
    ].join('\n'))
    writeFileSync(join(cwd, 'pw-live.txt'), 'KI-LIVE-MARKER-77\n')
  } else {
    env.KI_FAKE = '1'
  }

  const child = spawn(bin, ['serve', '--addr', addr], {
    env,
    cwd,
    stdio: 'inherit',
    detached: true,
  })
  if (child.pid == null) throw new Error('failed to start ki serve')
  child.unref()
  writeFileSync(statePath, JSON.stringify({ pid: child.pid, home, addr, cwd }))
  await waitHealth(`http://${addr}/v1/health`)
}
