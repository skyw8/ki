import { describe, expect, test } from 'bun:test'
import type { Question } from '../src/api/types'
import { answerFromDraft, mergeQuestion, orderedQuestions, questionDraftKey, readQuestionDraft, reconcileQuestions } from '../src/features/questions/model'
import { applyEvent, applyRuntimeCatalog, applyTail, emptyView, loadHistory } from '../src/lib/model'

const question = (change: Partial<Question> = {}): Question => ({
  questionItemId: '["request_user_input_async","call",0]', callId: 'call', index: 0, title: 'Release scope?',
  options: ['Staged', 'Full'], status: 'pending', revision: 1, createdAt: '2026-10-24T01:00:00Z', updatedAt: '2026-10-24T01:00:00Z',
  sourceEntryId: 'call-entry', snapshotEntryId: 'safe', sourceBranchValid: true, ...change,
})

describe('async question model', () => {
  test('draft identity isolates servers, sessions and individual questions', () => {
    const keys = [questionDraftKey('server', 'main', 'one'), questionDraftKey('other', 'main', 'one'), questionDraftKey('server', 'side', 'one'), questionDraftKey('server', 'main', 'two')]
    expect(new Set(keys).size).toBe(4)
  })
  test('initial option is only a draft; custom answers and corrupt storage recover safely', () => {
    expect(readQuestionDraft({ getItem: () => null }, 'key', question())).toEqual({ selected: 'Staged', text: '', sideText: '' })
    expect(readQuestionDraft({ getItem: () => '{' }, 'key', question()).selected).toBe('Staged')
    expect(readQuestionDraft({ getItem: () => '{"selected":"removed","text":7,"sideText":"ask"}' }, 'key', question())).toEqual({ selected: 'Staged', text: '', sideText: 'ask', submissionId: undefined, submittedAnswer: undefined })
    expect(answerFromDraft({ selected: 'Staged', text: '  10% first  ' })).toBe('Staged\n\n10% first')
    expect(answerFromDraft({ selected: '', text: 'my answer' })).toBe('my answer')
  })
  test('rail separates archived records and does not submit to another branch', () => {
    const items = [question(), question({ questionItemId: 'answered', status: 'answered' }), question({ questionItemId: 'old-branch', sourceBranchValid: false })]
    expect(orderedQuestions(items).map(q => q.questionItemId)).toEqual([question().questionItemId])
    expect(orderedQuestions(items, true).map(q => q.questionItemId)).toEqual(['answered'])
  })
  test('repeated and out of order events do not regress answers or duplicate questions', () => {
    const answered = question({ revision: 3, status: 'answered', answer: 'Staged' })
    expect(mergeQuestion([answered], question())).toEqual([answered])
    expect(mergeQuestion([answered], answered)).toHaveLength(1)
    expect(reconcileQuestions([answered], [question()], true)[0].status).toBe('answered')
    expect(reconcileQuestions([answered], [question({ sourceBranchValid: false })])[0].sourceBranchValid).toBe(false)
  })
  test('GET restores all questions independently of paginated transcript and stale SSE recovery', () => {
    const initial = loadHistory({ id: 'main', entries: [], questions: [question()] })
    const updated = applyEvent(initial, { type: 'question_updated', question: question({ revision: 2, status: 'answer_queued' }) })
    const restored = applyTail(updated, { id: 'main', entries: [], questions: [question()] }, initial.liveRevision)
    expect(restored.questions?.[0].status).toBe('answer_queued')
    expect(applyEvent(emptyView(), { type: 'question_updated', question: question() }).questions).toHaveLength(1)
  })
  test('runtime and tail snapshots from another session cannot retain prior questions', () => {
    const first = loadHistory({ id: 'first', questions: [question({ revision: 9 })] })
    const runtime = applyRuntimeCatalog(first, { id: 'second', questions: [] })
    expect(runtime.questionsSessionId).toBe('second')
    expect(runtime.questions).toEqual([])
    const tail = applyTail(first, { id: 'second', questions: [question({ title: 'Second session only' })] })
    expect(tail.questionsSessionId).toBe('second')
    expect(tail.questions?.[0].title).toBe('Second session only')
  })
  test('typed answer renders human content, not the provider envelope', () => {
    const view = loadHistory({ id: 'main', entries: [{ type: 'message', id: 'answer', message: { role: 'user', content: [{ type: 'text', text: '<send_user_message_question_reply>secret envelope</send_user_message_question_reply>' }], questionReply: { questionItemId: question().questionItemId, title: question().title, answer: 'Staged rollout' } } }] })
    expect(view.nodes[0].kind).toBe('user')
    expect(view.nodes[0].kind === 'user' && view.nodes[0].text).toBe('Staged rollout')
    expect(view.nodes[0].kind === 'user' && view.nodes[0].questionReply?.title).toBe('Release scope?')
  })
})
