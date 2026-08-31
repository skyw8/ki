import { execFileSync, spawn } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { baseURLForAddress, runID, statePath, storageStatePath } from './run-state.ts'
import { goBinary } from './go-toolchain.ts'

const root = join(dirname(fileURLToPath(import.meta.url)), '../..')
export { statePath, storageStatePath }

export function serverToken(): string {
  const state = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const server = JSON.parse(readFileSync(join(state.home, 'server.json'), 'utf8')) as { token?: string }
  if (!server.token) throw new Error('server.json has no token')
  return server.token
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

async function seedBrowserSession(baseURL: string, home: string): Promise<void> {
  const server = JSON.parse(readFileSync(join(home, 'server.json'), 'utf8')) as { token?: string }
  if (!server.token) throw new Error('server.json has no token')
  const res = await fetch(`${baseURL}/v1/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token: server.token }),
  })
  if (!res.ok) throw new Error(`browser session login failed: ${res.status}`)

  const headers = res.headers as Headers & { getSetCookie?: () => string[] }
  const setCookies = headers.getSetCookie?.() ?? [res.headers.get('set-cookie') ?? '']
  const host = new URL(baseURL).hostname
  const expires = Math.floor(Date.now() / 1000) + 12 * 60 * 60
  const cookies = setCookies.flatMap(header => {
    const pair = header.split(';', 1)[0]
    const i = pair.indexOf('=')
    if (i < 1) return []
    const name = pair.slice(0, i)
    const value = pair.slice(i + 1)
    return [{
      name,
      value,
      domain: host,
      path: '/',
      expires,
      httpOnly: name === 'ki_session',
      secure: false,
      sameSite: 'Strict' as const,
    }]
  })
  if (!cookies.some(cookie => cookie.name === 'ki_session') || !cookies.some(cookie => cookie.name === 'ki_csrf')) {
    throw new Error('browser session login did not set both cookies')
  }
  writeFileSync(storageStatePath, JSON.stringify({ cookies, origins: [] }))
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
  if (process.env.KI_SKIP_SERVER === '1') {
    // The Go harness (go test ./e2e) started the server itself and set
    // KI_HOME; keep the fixture home discoverable so specs can still drop
    // skills and extension fixtures next to the real session dir.
    writeFileSync(statePath, JSON.stringify({
      pid: 0,
      home: process.env.KI_HOME ?? '',
      addr: '',
      // Why: the Go harness owns the server and its cross-platform temporary
      // workspace, so Playwright cannot infer that workspace from this process.
      cwd: process.env.KI_PW_CWD ?? '',
    }))
    const baseURL = process.env.KI_BASE_URL
    if (baseURL && process.env.KI_HOME) {
      await waitHealth(`${baseURL}/v1/health`)
      await seedBrowserSession(baseURL, process.env.KI_HOME)
    }
    return
  }

  const live = process.env.KI_LIVE === '1'
  const addr = process.env.KI_SERVE_ADDR || (live ? '127.0.0.1:19833' : '127.0.0.1:19832')
  const serveURL = baseURLForAddress(addr)
  const home = mkdtempSync(join(tmpdir(), 'ki-pw-home-'))
  const cwd = mkdtempSync(join(tmpdir(), 'ki-pw-cwd-'))
  const executable = process.platform === 'win32' ? '.exe' : ''
  const bin = process.env.KI_BIN || join(tmpdir(), `ki-pw-${runID}${live ? '-live' : ''}${executable}`)
  if (!process.env.KI_BIN) {
    execFileSync(goBinary(), ['build', '-o', bin, './cmd/ki'], { cwd: root, stdio: 'inherit' })
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
  try {
    await waitHealth(`${serveURL}/v1/health`)
    await seedBrowserSession(serveURL, home)
  } catch (error) {
    // Why: setup failures happen before the normal teardown lifecycle is
    // guaranteed. Stop this run's server here so a bind/auth error cannot
    // strand a daemon that poisons the next invocation.
    try { process.kill(child.pid, 'SIGTERM') } catch { /* already gone */ }
    throw error
  }
}
