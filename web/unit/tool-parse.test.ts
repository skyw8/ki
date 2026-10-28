import { describe, expect, test } from 'bun:test'
import { nestedCalls } from '../src/features/chat/tools/CodeModeResult'
import { agentListSummary, receiptOf } from '../src/features/chat/tools/Receipts'
import {
  codeModePreview,
  codeModeSnapshot,
  codeModeSource,
  diffFiles,
  diffStat,
  execSnapshot,
  execSnapshotFromProcess,
  execSnapshotFromResult,
  execSnapshotOf,
  globDetails,
  grepDetails,
  readDetails,
  spillOf,
  syntheticEdit,
  writeDetails,
  writeDiff,
} from '../src/features/chat/tools/parse'

const execResult = (details: unknown, text = '') => execSnapshotOf(details, text)

test('exec snapshot parses the persisted process shape', () => {
  const snapshot = execResult({
    revision: 5, run_id: 'run', tool_call_id: 'call', session_id: 42,
    cmd: 'go test ./...', workdir: '/repo', tty: false, status: 'exited', pid: 9,
    output: 'ok\n', exit_code: 0, total_bytes: 3, truncated: true,
    output_file: '/tmp/out.log', output_offset: 0,
    started_at: '2026-10-21T10:00:00Z', finished_at: '2026-10-21T10:00:01Z',
  }, '')
  expect(snapshot).toEqual({
    cmd: 'go test ./...', workdir: '/repo', tty: false, status: 'exited', exitCode: 0,
    output: 'ok\n', outputFile: '/tmp/out.log', truncated: true, totalBytes: 3,
    sessionID: 42, startedAt: '2026-10-21T10:00:00Z', finishedAt: '2026-10-21T10:00:01Z',
  })
})

test('exec snapshot unwraps live progress and falls back to result JSON', () => {
  const live = execSnapshot({ delta: 'k', process: { revision: 2, session_id: 7, status: 'running', cmd: 'sleep 1', output: 'ok' } }, '')
  expect(live?.status).toBe('running')
  expect(live?.sessionID).toBe(7)

  const legacy = execSnapshotFromResult('{"revision":1,"session_id":3,"status":"exited","output":"hi","cmd":"ls","tty":false}')
  expect(legacy?.output).toBe('hi')
  expect(legacy?.cmd).toBe('ls')

  expect(execResult(undefined, 'plain text')).toBeNull()
  expect(execSnapshot({ revision: 1 }, '')).toBeNull()
  expect(execSnapshotFromResult('{not json')).toBeNull()
})

test('live process snapshots map onto the same shape', () => {
  const snapshot = execSnapshotFromProcess({
    session_id: 11, status: 'running', tty: true, cmd: 'top', workdir: '/w',
    total_bytes: 20, started_at: '2026-10-21T10:00:00Z',
  })
  expect(snapshot).toMatchObject({ sessionID: 11, status: 'running', tty: true, cmd: 'top', totalBytes: 20 })
  expect(snapshot.exitCode).toBeUndefined()
})

test('diff details cover edit and multi-file apply_patch', () => {
  const edit = diffFiles({
    diff: '--- /repo/a.go\n+++ /repo/a.go\n@@ -1 +1,2 @@\n old\n+new\n',
    patch: 'same',
    first_changed_line: 1,
  })
  expect(edit).toEqual([{ path: '/repo/a.go', kind: 'update', diff: expect.stringContaining('+new') }])
  expect(diffStat(edit)).toEqual({ added: 1, removed: 0 })

  const patch = diffFiles({
    status: 'completed',
    changes: [
      { path: '/repo/a.go', kind: 'update', unified_diff: '--- a\n+++ b\n@@ -1 +1 @@\n-a\n+b\n' },
      { path: '/repo/b.go', kind: 'add', unified_diff: '--- a\n+++ b\n@@ -0,0 +1 @@\n+new\n' },
      { path: '/repo/c.go', kind: 'delete' },
    ],
  })
  expect(patch.map(f => [f.path, f.kind])).toEqual([['/repo/a.go', 'update'], ['/repo/b.go', 'add']])
  expect(diffStat(patch)).toEqual({ added: 2, removed: 1 })
  expect(diffFiles(null)).toEqual([])
})

test('grep, glob, write and read details parse with their paging fields', () => {
  const grep = grepDetails({
    mode: 'content', files: 2, matches: 9, truncated: true, offset: 0, limit: 250,
    hits: [{ path: 'a.go', line: 4, text: 'needle', truncated: true }, { path: 'b.go' }],
  })
  expect(grep?.hits).toEqual([{ path: 'a.go', line: 4, text: 'needle', truncated: true }])

  const files = grepDetails({ mode: 'files_with_matches', files: 1, matches: 0, truncated: false, paths: ['a.go'] })
  expect(files?.paths).toEqual(['a.go'])
  expect(grepDetails({ mode: 'content' })?.hits).toBeUndefined()
  expect(grepDetails({ hits: [] })).toBeNull()

  expect(globDetails({ paths: ['a.go'], root: '/repo', files: 1, limit: 100, truncated: true }))
    .toMatchObject({ files: 1, root: '/repo', truncated: true, paths: ['a.go'] })
  expect(globDetails({ root: '/repo' })).toBeNull()

  expect(writeDetails({ path: 'a.go', bytes: 4, lines: 2, created: true }))
    .toEqual({ path: 'a.go', bytes: 4, lines: 2, created: true })
  expect(writeDetails({ bytes: 4 })).toBeNull()

  expect(readDetails({ truncation: { truncated: true, total_lines: 900, output_lines: 800, next_offset: 801 } })?.truncation?.next_offset)
    .toBe(801)
  expect(readDetails({})).toBeNull()
})

test('spill references are read without shadowing a tool output field', () => {
  const spill = spillOf({
    output: 'cell log',
    spill: { path: '/tmp/x', bytes: 10, totalBytes: 40, lines: 2, truncated: true },
  })
  expect(spill).toMatchObject({ path: '/tmp/x', totalBytes: 40, truncated: true })
  expect(spillOf({ output: 'cell log' })).toBeNull()
  expect(spillOf({ spill: { bytes: 1 } })).toBeNull()
})

test('a legacy edit and a fresh write both produce a renderable diff', () => {
  const legacy = syntheticEdit({ file_path: 'a.go', first_changed_line: 12 }, 'old line', 'new line')
  expect(legacy).toEqual({
    path: 'a.go', kind: 'update',
    diff: '--- a.go\n+++ a.go\n@@ -12,1 +12,1 @@\n-old line\n+new line\n',
  })
  const written = writeDiff({ path: 'b.go', bytes: 8, lines: 2, created: true }, 'one\ntwo\n')
  expect(written.kind).toBe('add')
  expect(written.diff).toContain('@@ -0,0 +1,3 @@')
  expect(diffStat([legacy, written])).toEqual({ added: 4, removed: 1 })
})

test('Code Mode results split concatenated envelopes, braces inside strings included', () => {
  const result = 'Script completed' + JSON.stringify({ content: [{ type: 'text', text: '{"a":1}' }], isError: false })
    + JSON.stringify({ content: [{ type: 'text', text: 'boom' }], isError: true })
  const calls = nestedCalls(result)
  expect(calls).toEqual([{ text: '{"a":1}', isError: false }, { text: 'boom', isError: true }])
  expect(nestedCalls('plain text')).toBeNull()
  expect(nestedCalls('Script completed{"content":[')).toBeNull()
})

test('Code Mode output keeps every text block and separates it from the header', () => {
  const snapshot = codeModeSnapshot('ignored', [
    { type: 'text', text: 'Script completed' },
    { type: 'text', text: '== ki.toml 行数: 1' },
    { type: 'text', text: '== files\nNo files found' },
    { type: 'image', data: 'aGVsbG8=', mimeType: 'image/png' },
  ], { cell_id: 'cell-1', status: 'completed', truncated: true })
  expect(snapshot).toMatchObject({
    status: 'completed', cellId: 'cell-1', truncated: true,
    body: '== ki.toml 行数: 1\n== files\nNo files found',
    calls: null,
  })
  expect(snapshot.images).toEqual([{ data: 'aGVsbG8=', mimeType: 'image/png' }])
  expect(codeModePreview(snapshot, '')).toEqual({ text: '== ki.toml 行数: 1' })
})

test('Code Mode falls back to the concatenated result and reads running cell IDs', () => {
  const completed = codeModeSnapshot('Script completedmodule ki', undefined, undefined)
  expect(completed).toMatchObject({ status: 'completed', body: 'module ki', calls: null })
  const running = codeModeSnapshot('Script running with cell ID cell-9', undefined, undefined)
  expect(running).toMatchObject({ status: 'running', cellId: 'cell-9', body: '' })
  expect(codeModePreview(running, '')).toEqual({ text: '#cell-9', status: 'running' })
  const failed = codeModeSnapshot('Script failedout\nScript error:\nboom', undefined, { status: 'failed' })
  expect(failed).toMatchObject({ status: 'failed', body: 'out', error: 'boom' })
  expect(codeModePreview(failed, '')).toEqual({ text: 'boom' })
})

test('Code Mode source and preview understand function and freeform exec arguments', () => {
  expect(codeModeSource({ code: 'const a = 1' })).toBe('const a = 1')
  expect(codeModeSource({ input: 'await tools.read({})' })).toBe('await tools.read({})')
  expect(codeModeSource('raw source')).toBe('raw source')
  expect(codeModeSource({})).toBe('')
  const empty = codeModeSnapshot('Script completed', [{ type: 'text', text: 'Script completed' }], { status: 'completed' })
  expect(codeModePreview(empty, 'const a = 1')).toEqual({ text: 'const a = 1' })
})

test('coordination receipts and agent lists read the fields the tools publish', () => {
  expect(receiptOf({ agent_id: 'a-1', task_name: '/root/audit', status: 'queued', message: 'ok', pending_tasks: 2 }))
    .toEqual([
      { label: 'task', value: '/root/audit' },
      { label: 'status', value: 'queued' },
      { label: 'agent', value: 'a-1' },
      { label: 'message', value: 'ok' },
    ])
  expect(receiptOf({})).toBeNull()
  expect(receiptOf('nope')).toBeNull()
  expect(agentListSummary({ agents: [{ status: 'running' }, { status: 'completed' }], total: 9 }))
    .toEqual({ total: 9, active: 1 })
  expect(agentListSummary({ total: 1 })).toBeNull()
})
