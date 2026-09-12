// e2e-parallel.ts runs the Playwright e2e project as isolated parallel processes.
//
// Why: a single `playwright test` run drives one `ki serve` with one KI_HOME, and
// several specs mutate global state (extension toggles, skills/commands under
// KI_HOME, one serial narrative in webui.spec.ts). Per-invocation isolation is
// already guaranteed by run-state.ts (random run id) and global-setup.ts (temp
// home/cwd plus a free port), so separate processes are safe to run concurrently.
//
// Coverage is never traded for speed: every executed spec is reconciled against
// `playwright test --list`, and the run fails if a test was dropped or duplicated.
//
// Env:
//   KI_E2E_PROJECT      Playwright project to run (default: fake)
//   KI_E2E_JOBS         Max concurrent processes (default: min(CPUs, 32))
//   KI_E2E_SPLIT        Min tests before a non-serial file is split (default: 12)
//   KI_BIN              Reuse an already-built ki binary instead of building one
//   KI_SKIP_WEB_BUILD   Fail instead of running `bun run build` when dist is absent
//
// A spec file may opt in to one-process-per-test by declaring
// `test.describe.configure({ mode: 'parallel' })`; that is the file's assertion
// that its tests are independent (verify by running each one standalone first).
// Files that declare `mode: 'serial'` are always kept in a single process.
import { spawn } from 'node:child_process'
import { closeSync, mkdirSync, openSync, readFileSync, rmSync } from 'node:fs'
import { createServer } from 'node:net'
import { cpus } from 'node:os'
import { basename, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const webDir = join(dirname(fileURLToPath(import.meta.url)), '..')
const rootDir = join(webDir, '..')
const project = (process.env.KI_E2E_PROJECT ?? 'fake').trim() || 'fake'

function positiveInt(value: string | undefined, fallback: number): number {
  const parsed = Number.parseInt(value ?? '', 10)
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback
}

const jobs = positiveInt(process.env.KI_E2E_JOBS, Math.max(2, Math.min(cpus().length, 32)))
const splitThreshold = positiveInt(process.env.KI_E2E_SPLIT, 12)
const runDir = join(webDir, 'test-results', 'e2e-parallel')

interface FileTests {
  total: number
  topLevel: number
  describes: Map<string, number>
  paths: string[]
}

interface Unit {
  label: string
  file: string
  args: string[]
  expected: number
}

interface UnitResult {
  unit: Unit
  code: number
  seconds: number
  executed: number
  failed: number
  logFile: string
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const probe = createServer()
    probe.once('error', reject)
    probe.listen(0, '127.0.0.1', () => {
      const address = probe.address()
      const port = typeof address === 'object' && address ? address.port : 0
      if (!port) {
        probe.close(() => reject(new Error('no free port')))
        return
      }
      probe.close(() => resolve(port))
    })
  })
}

function runCapture(bin: string, args: string[], env: NodeJS.ProcessEnv, logFile: string, cwd: string): Promise<number> {
  return new Promise(resolve => {
    const fd = openSync(logFile, 'w')
    let settled = false
    const done = (code: number): void => {
      if (settled) return
      settled = true
      closeSync(fd)
      resolve(code)
    }
    const child = spawn(bin, args, { cwd, env, stdio: ['ignore', fd, fd] })
    child.on('error', () => done(127))
    child.on('close', code => done(code ?? 1))
  })
}

function parseList(output: string): Map<string, FileTests> {
  const files = new Map<string, FileTests>()
  for (const line of output.split('\n')) {
    const match = /^\s*\[[^\]]+\]\s*›\s*(.+?):\d+:\d+\s*›\s*(.+?)\s*$/.exec(line)
    if (!match) continue
    const file = basename(match[1])
    const path = match[2]
    const segments = path.split(' › ')
    const entry = files.get(file) ?? { total: 0, topLevel: 0, describes: new Map<string, number>(), paths: [] }
    entry.total += 1
    entry.paths.push(path)
    if (segments.length > 1) {
      const describe = segments[0]
      entry.describes.set(describe, (entry.describes.get(describe) ?? 0) + 1)
    } else {
      entry.topLevel += 1
    }
    files.set(file, entry)
  }
  return files
}

function listTests(): Promise<Map<string, FileTests>> {
  return new Promise((resolve, reject) => {
    const child = spawn('bun', ['x', 'playwright', 'test', `--project=${project}`, '--list', '--reporter=list'], {
      cwd: webDir,
      env: process.env,
      stdio: ['ignore', 'pipe', 'inherit'],
    })
    let output = ''
    child.stdout.setEncoding('utf8')
    child.stdout.on('data', chunk => {
      output += chunk
    })
    child.on('error', reject)
    child.on('close', () => resolve(parseList(output)))
  })
}

// grepPattern turns a "describe › title" path into a -g pattern. Why: Playwright
// matches -g against that joined path but does not accept the literal separator,
// so the escaped parts are joined with ".*".
function grepPattern(path: string): string {
  return path.split(' › ').map(escapeRegExp).join('.*')
}

function buildUnits(files: Map<string, FileTests>): Unit[] {
  const units: Unit[] = []
  for (const [file, info] of files) {
    const source = readFileSync(join(webDir, 'e2e', file), 'utf8')
    const relative = `e2e/${file}`
    if (/mode:\s*['"]serial['"]/.test(source)) {
      // Serial files encode an ordered narrative and must stay in one process.
      units.push({ label: file, file, args: [relative], expected: info.total })
      continue
    }
    if (/mode:\s*['"]parallel['"]/.test(source)) {
      // "parallel" is the spec's assertion that its tests are independent (each
      // was verified standalone), so every test gets its own isolated process.
      for (const path of info.paths) {
        units.push({ label: path, file, args: [relative, '-g', grepPattern(path)], expected: 1 })
      }
      continue
    }
    const splittable = info.total > splitThreshold && info.topLevel === 0 && info.describes.size > 1
    if (splittable) {
      for (const [describe, count] of info.describes) {
        units.push({
          label: `${file} › ${describe}`,
          file,
          args: [relative, '-g', escapeRegExp(describe)],
          expected: count,
        })
      }
    } else {
      units.push({ label: file, file, args: [relative], expected: info.total })
    }
  }
  // Start the longest units first so the tail of the run stays balanced.
  units.sort((a, b) => b.expected - a.expected)
  return units
}

async function ensureBinary(): Promise<string> {
  const provided = process.env.KI_BIN?.trim()
  if (provided) return provided
  try {
    readFileSync(join(webDir, 'dist', 'index.html'))
  } catch (error) {
    if (process.env.KI_SKIP_WEB_BUILD) throw error
    const buildLog = join(runDir, 'web-build.log')
    if (await runCapture('bun', ['run', 'build'], process.env, buildLog, webDir) !== 0) {
      throw new Error(`web build failed; see ${buildLog}`)
    }
  }
  const bin = join(runDir, 'ki')
  const env = { ...process.env, KI_BIN: '' }
  const buildLog = join(runDir, 'go-build.log')
  if (await runCapture('go', ['build', '-tags', 'embed', '-o', bin, './cmd/ki'], env, buildLog, rootDir) !== 0) {
    throw new Error(`go build failed; see ${buildLog}`)
  }
  return bin
}

function collectSpecs(node: unknown, inheritedFile: string, out: Array<{ file: string; ok: boolean }>): void {
  const record = node as { file?: string; specs?: Array<{ ok?: boolean }>; suites?: unknown[] }
  const file = record.file ? basename(record.file) : inheritedFile
  for (const spec of record.specs ?? []) out.push({ file, ok: spec.ok !== false })
  for (const child of record.suites ?? []) collectSpecs(child, file, out)
}

const maxAttempts = 2

async function runUnit(unit: Unit, index: number, kiBin: string): Promise<UnitResult> {
  const logFile = join(runDir, `unit-${index}.log`)
  const jsonFile = join(runDir, `unit-${index}.json`)
  const started = Date.now()
  let code = 1
  let executed = 0
  let failed = 0
  for (let attempt = 1; attempt <= maxAttempts; attempt++) {
    try {
      const port = await freePort()
      const env: NodeJS.ProcessEnv = {
        ...process.env,
        KI_BIN: kiBin,
        KI_SERVE_ADDR: `127.0.0.1:${port}`,
        PLAYWRIGHT_JSON_OUTPUT_NAME: jsonFile,
      }
      // A stale base URL would override KI_SERVE_ADDR in playwright.config.ts.
      delete env.KI_BASE_URL
      delete env.KI_SKIP_SERVER
      const args = ['x', 'playwright', 'test', `--project=${project}`, ...unit.args, '--reporter=line', '--reporter=json']
      code = await runCapture('bun', args, env, logFile, webDir)
    } catch {
      code = 1
    }
    executed = 0
    failed = 0
    try {
      const report = JSON.parse(readFileSync(jsonFile, 'utf8')) as { suites?: unknown[] }
      const specs: Array<{ file: string; ok: boolean }> = []
      for (const suite of report.suites ?? []) collectSpecs(suite, '', specs)
      executed = specs.length
      failed = specs.filter(spec => !spec.ok).length
    } catch {
      // A missing report means the unit ran no test; the coverage guard catches it.
    }
    // Why: freePort() closes its probe socket before ki binds the port, so two
    // units can race for it. A bind failure runs no test at all (executed === 0),
    // so retrying on a fresh port cannot mask a real test failure.
    if (attempt === maxAttempts || executed > 0 || !/address already in use/.test(readFileSync(logFile, 'utf8'))) break
    process.stdout.write(`  retry    ${unit.label} (port race)\n`)
  }
  return { unit, code, seconds: (Date.now() - started) / 1000, executed, failed, logFile }
}

async function main(): Promise<number> {
  rmSync(runDir, { recursive: true, force: true })
  mkdirSync(runDir, { recursive: true })

  const kiBin = await ensureBinary()
  const files = await listTests()
  const units = buildUnits(files)
  const expectedTotal = [...files.values()].reduce((sum, info) => sum + info.total, 0)
  if (units.length === 0) {
    process.stderr.write('e2e-parallel: no tests found\n')
    return 1
  }
  const concurrent = Math.max(1, Math.min(jobs, units.length))
  process.stdout.write(`e2e-parallel: ${expectedTotal} tests in ${units.length} units, ${concurrent} concurrent\n`)

  const results: UnitResult[] = new Array(units.length)
  let cursor = 0
  const started = Date.now()
  const worker = async (): Promise<void> => {
    for (;;) {
      const index = cursor++
      if (index >= units.length) return
      const result = await runUnit(units[index], index, kiBin)
      results[index] = result
      const state = result.code === 0 && result.failed === 0 ? 'ok' : 'FAIL'
      process.stdout.write(`  ${state.padEnd(4)} ${result.seconds.toFixed(1).padStart(6)}s  ${result.executed}/${result.unit.expected}  ${result.unit.label}\n`)
    }
  }
  await Promise.all(Array.from({ length: concurrent }, worker))
  const wall = (Date.now() - started) / 1000

  const executedByFile = new Map<string, number>()
  for (const result of results) {
    executedByFile.set(result.unit.file, (executedByFile.get(result.unit.file) ?? 0) + result.executed)
  }
  const mismatches: string[] = []
  for (const [file, info] of files) {
    const ran = executedByFile.get(file) ?? 0
    if (ran !== info.total) mismatches.push(`${file}: expected ${info.total}, executed ${ran}`)
  }
  // Per-unit reconciliation catches a split that silently selects the wrong tests
  // (for example a -g pattern matching a sibling test as well as its own).
  for (const result of results) {
    if (result.executed !== result.unit.expected) {
      mismatches.push(`${result.unit.label}: expected ${result.unit.expected}, executed ${result.executed}`)
    }
  }
  const failedUnits = results.filter(result => result.code !== 0 || result.failed > 0)
  for (const result of failedUnits) {
    process.stderr.write(`\n--- ${result.unit.label} failed; log: ${result.logFile} ---\n`)
    const lines = readFileSync(result.logFile, 'utf8').trimEnd().split('\n')
    process.stderr.write(`${lines.slice(-25).join('\n')}\n`)
  }

  const executedTotal = results.reduce((sum, result) => sum + result.executed, 0)
  process.stdout.write(`\ne2e-parallel: ${executedTotal}/${expectedTotal} tests in ${wall.toFixed(1)}s (${failedUnits.length} failing units)\n`)
  if (failedUnits.length > 0) return 1
  if (mismatches.length > 0) {
    process.stderr.write(`e2e-parallel: coverage mismatch, refusing to pass:\n  ${mismatches.join('\n  ')}\n`)
    return 1
  }
  return 0
}

try {
  process.exitCode = await main()
} catch (error) {
  process.stderr.write(`e2e-parallel: ${error instanceof Error ? error.message : String(error)}\n`)
  process.exitCode = 1
}
