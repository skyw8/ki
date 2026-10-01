import { expect, test } from 'bun:test'
import type { Entry, ViewState } from '../src/api/types'
import { conversationTarget, conversationTargetTurn } from '../src/features/chat/conversationTarget'
import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { useTranscriptScroll, type TranscriptIntent } from '../src/features/chat/useTranscriptScroll'

const message = (id: string, parentId?: string): Entry => ({
  id, parentId, type: 'message', message: { role: 'user', content: [] },
})
const view = (allEntries: Entry[], leafId = allEntries.at(-1)?.id): Pick<ViewState, 'allEntries' | 'nodes' | 'leafId'> => ({
  allEntries, leafId, nodes: [],
})

test('remote and local checkpoints keep their exact persisted row identity', () => {
  const entries: Entry[] = [
    message('u'), { id: 'start', parentId: 'u', type: 'compaction_start' },
    { id: 'remote', parentId: 'start', type: 'compaction', remoteContext: true },
    { id: 'end', parentId: 'remote', type: 'compaction_end' },
    { id: 'local', parentId: 'end', type: 'compaction', summary: 'summary' },
  ]
  expect(conversationTarget(view(entries), 'remote')).toBe('remote')
  expect(conversationTarget(view(entries), 'local')).toBe('local')
})

test('tool result entries resolve to paired call rows, not their entry IDs', () => {
  const result: Entry = { id: 'result', parentId: 'u', type: 'message', message: { role: 'toolResult', toolCallId: 'call', content: [] } }
  expect(conversationTarget(view([message('u'), result]), 'result')).toBe('call')
})

test('non-rendered event metadata locates its preceding branch row', () => {
  const entries: Entry[] = [
    message('u'), { id: 'header', parentId: 'u', type: 'request_header' },
    { id: 'model', parentId: 'header', type: 'model_change' }, message('tail', 'model'),
  ]
  expect(conversationTarget(view(entries), 'model')).toBe('u')
  expect(conversationTarget(view(entries), 'header')).toBe('u')
})

test('initial model selection finds the first following row only on the active branch', () => {
  const entries: Entry[] = [
    { id: 'model', type: 'model_change' }, message('sibling', 'model'),
    { id: 'header', parentId: 'model', type: 'request_header' }, message('selected', 'header'), message('tail', 'selected'),
  ]
  expect(conversationTarget(view(entries, 'tail'), 'model')).toBe('selected')
})

test('missing parents, unknown metadata and cycles never silently locate the tail', () => {
  expect(conversationTarget(view([{ id: 'event', parentId: 'missing', type: 'model_change' }, message('tail', 'event')]), 'event')).toBeNull()
  expect(conversationTarget(view([{ id: 'event', type: 'context_usage' }, message('tail', 'event')]), 'event')).toBeNull()
  expect(conversationTarget(view([{ id: 'event', parentId: 'event', type: 'model_change' }]), 'event')).toBeNull()
})

test('actual chat node identity wins, while unknown sparse row identity remains pageable', () => {
  const known = { ...view([]), nodes: [{ id: 'call', kind: 'tool' as const, name: 'read' }] }
  expect(conversationTarget(known, 'call')).toBe('call')
  expect(conversationTarget(view([]), 'older-user')).toBe('older-user')
})

test('compact hidden targets load their exact owning turn, including paired tool rows', () => {
  const entries: Entry[] = [
    message('u1'),
    { id: 'header', parentId: 'u1', type: 'request_header' },
    { id: 'agent', parentId: 'header', type: 'message', message: { role: 'user', origin: 'agent:worker', content: [] } },
    { id: 'result', parentId: 'agent', type: 'message', message: { role: 'toolResult', toolCallId: 'call', content: [] } },
    message('u2', 'result'), message('last', 'u2'),
  ]
  // Only IDs matter to the ownership lookup; bodies/stats stay on the store.
  const compact = { allEntries: entries, compactTurns: [{ id: 'u1' }, { id: 'u2' }] } as Pick<ViewState, 'allEntries' | 'compactTurns'>
  expect(conversationTargetTurn(compact, 'agent')).toBe('u1')
  expect(conversationTargetTurn(compact, 'call')).toBe('u1')
  expect(conversationTargetTurn(compact, 'last')).toBe('u2')
  expect(conversationTargetTurn(compact, 'unknown')).toBeNull()
})

test('compact ownership does not cross missing parents or select a sibling turn', () => {
  const entries: Entry[] = [
    message('selected'), { ...message('hidden', 'missing'), previousId: 'selected' }, message('sibling'),
  ]
  const compact = { allEntries: entries, compactTurns: [{ id: 'selected' }] } as Pick<ViewState, 'allEntries' | 'compactTurns'>
  expect(conversationTargetTurn(compact, 'hidden')).toBeNull()
  expect(conversationTargetTurn(compact, 'sibling')).toBeNull()
  const siblingChild = { ...compact, leafId: 'selected', allEntries: [...entries, message('sibling-child', 'selected')] }
  expect(conversationTargetTurn(siblingChild, 'sibling-child')).toBeNull()
})

test('ordinary chat mounts follow, but pending navigation starts without end pinning', () => {
  // Rendering without a DOM exercises the hook's initial ownership only.
  // Browser coverage checks the later layout/seek effects and paging gate.
  function Initial({ initialIntent }: { initialIntent?: TranscriptIntent }) {
    const navigation = useTranscriptScroll({ scrollRef: { current: null }, initialIntent })
    return createElement('span', {
      'data-follow': String(navigation.isFollowing),
      'data-ref-follow': String(navigation.following.current),
    }, navigation.scrollIntent)
  }
  expect(renderToString(createElement(Initial))).toBe('<span data-follow="true" data-ref-follow="true">following</span>')
  expect(renderToString(createElement(Initial, { initialIntent: 'reading' }))).toBe('<span data-follow="false" data-ref-follow="false">reading</span>')
})
