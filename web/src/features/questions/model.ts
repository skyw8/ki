import type { Question } from '../../api/types'

export type QuestionDraft = { selected: string; text: string; sideText: string; submissionId?: string; submittedAnswer?: string }

export function questionDraftKey(serverId: string, sessionId: string, questionItemId: string): string {
  return `ki:question-draft:${JSON.stringify([serverId, sessionId, questionItemId])}`
}

export function readQuestionDraft(storage: Pick<Storage, 'getItem'>, key: string, question: Question): QuestionDraft {
  const fallback = { selected: question.options?.[0] ?? '', text: '', sideText: '' }
  try {
    const parsed = JSON.parse(storage.getItem(key) ?? 'null')
    if (!parsed || typeof parsed !== 'object') return fallback
    return {
      selected: typeof parsed.selected === 'string' && (parsed.selected === '' || question.options?.includes(parsed.selected)) ? parsed.selected : fallback.selected,
      text: typeof parsed.text === 'string' ? parsed.text : '',
      sideText: typeof parsed.sideText === 'string' ? parsed.sideText : '',
      submissionId: typeof parsed.submissionId === 'string' ? parsed.submissionId : undefined,
      submittedAnswer: typeof parsed.submittedAnswer === 'string' ? parsed.submittedAnswer : undefined,
    }
  } catch { return fallback }
}

export function answerFromDraft(draft: Pick<QuestionDraft, 'selected' | 'text'>): string {
  return [draft.selected.trim(), draft.text.trim()].filter(Boolean).join('\n\n')
}

export function orderedQuestions(questions: Question[], archived = false): Question[] {
  return questions.filter(q => q.sourceBranchValid && (archived ? q.status === 'answered' || q.status === 'dismissed' : q.status === 'pending' || q.status === 'answer_queued'))
    .sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.index - b.index)
}

export function mergeQuestion(questions: Question[], question: Question): Question[] {
  const old = questions.find(q => q.questionItemId === question.questionItemId)
  if (old && old.revision > question.revision) return questions
  return [...questions.filter(q => q.questionItemId !== question.questionItemId), question]
}

export function reconcileQuestions(current: Question[], snapshot: Question[], stale = false): Question[] {
  const old = new Map(current.map(q => [q.questionItemId, q]))
  const seen = new Set(snapshot.map(q => q.questionItemId))
  return [...snapshot.map(q => {
    const prior = old.get(q.questionItemId)
    return prior && prior.revision > q.revision ? { ...prior, ...(!stale ? { sourceBranchValid: q.sourceBranchValid } : {}) } : q
  }), ...(stale ? current.filter(q => !seen.has(q.questionItemId)) : [])]
}
