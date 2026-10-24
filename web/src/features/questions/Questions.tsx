import { useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ApiError, type Client } from '../../api/client'
import type { Question } from '../../api/types'
import { IClose, IQuestion, ISideChat, IStop, ISend } from '../../components/icons'
import { useDialogFocus } from '../../hooks/useDialogFocus'
import { useI18n } from '../../i18n'
import { clientRequestId } from '../../lib/client-request'
import { Markdown } from '../markdown/Markdown'
import { answerFromDraft, confirmedAnswerForm, orderedQuestions, questionDraftKey, readQuestionDraft, type QuestionDraft } from './model'
import { questionCopy } from './copy'
import { useSideConversation } from './useSideConversation'
import './questions.css'

export function QuestionsRail({ questions, onOpen }: { questions: Question[]; onOpen: (id: string) => void }) {
  const { lang } = useI18n()
  const c = questionCopy(lang)
  const [expanded, setExpanded] = useState(false)
  const [archive, setArchive] = useState(false)
  const [collapsed, setCollapsed] = useState(false)
  const active = orderedQuestions(questions)
  const history = orderedQuestions(questions, true)
  if (!active.length && !history.length) return null
  return <aside className={`questions-rail${expanded ? ' expanded' : ''}${collapsed ? ' collapsed' : ''}`} aria-label={c.questions} data-testid="questions-rail">
    <button className="questions-mobile-toggle" aria-expanded={expanded} onClick={() => { setExpanded(v => !v); setCollapsed(true) }}><IQuestion /><span>{c.questions}</span><b>{active.length}</b></button>
    <div className="questions-rail-content">
      <div className="questions-rail-heading"><IQuestion /><strong>{c.questions}</strong><span>{active.length}</span><button className="icon-btn" aria-label={c.hideQuestions} onClick={() => { setExpanded(false); setCollapsed(true) }}><IClose /></button></div>
      <div className="questions-cards">
        {(archive ? history : active).map((q, index) => <button type="button" key={q.questionItemId} className={`question-card ${q.status}`} data-testid="question-card" onClick={() => { onOpen(q.questionItemId); setExpanded(false) }}>
          <span className="question-card-index">{String(index + 1).padStart(2, '0')}</span>
          <strong>{q.title}</strong><span className="question-card-status">{c[q.status]}{q.sideSessionId ? <ISideChat /> : null}</span>
        </button>)}
      </div>
      {history.length || archive ? <button type="button" className="questions-history" aria-pressed={archive} onClick={() => setArchive(v => !v)}>{archive ? c.questions : c.archived} · {archive ? active.length : history.length}</button> : null}
    </div>
  </aside>
}

export function QuestionDialog({ api, sessionId, question, mainBusy, onClose, onQuestion, onCreated, onRefresh, onLocate }: {
  api: Client
  sessionId: string
  question: Question
  mainBusy: boolean
  onClose: () => void
  onQuestion: (q: Question) => void
  onCreated: () => void
  onRefresh: () => void
  onLocate?: (id: string) => void
}) {
  const { lang } = useI18n()
  const c = questionCopy(lang)
  const titleId = useId()
  const hintId = useId()
  const optionName = useId()
  const initialFocus = useRef<HTMLButtonElement>(null)
  const dialog = useDialogFocus({ open: true, onEscape: onClose, initialFocusRef: initialFocus })
  const key = questionDraftKey(api.serverId, sessionId, question.questionItemId)
  const [draft, setDraft] = useState<QuestionDraft>(() => readQuestionDraft(sessionStorage, key, question))
  const [tab, setTab] = useState<'answer' | 'side'>('answer')
  const [sending, setSending] = useState(false)
  const sendingRef = useRef(false)
  const [acting, setActing] = useState(false)
  const [error, setError] = useState('')
  const side = useSideConversation(api, sessionId, question, onQuestion, onCreated)
  const transcript = useRef<HTMLDivElement>(null)
  const follow = useRef(true)
  const editable = question.status === 'pending' && question.sourceBranchValid
  const answer = answerFromDraft(draft)
  const confirmed = question.status === 'answered' || question.status === 'answer_queued'
  const form = confirmed ? confirmedAnswerForm(question, draft) : draft
  useEffect(() => {
    try { sessionStorage.setItem(key, JSON.stringify(draft)) } catch {}
  }, [key, draft])
  useEffect(() => {
    if (question.status === 'answered' || question.status === 'answer_queued') {
      setDraft(v => ({ ...v, submissionId: undefined, submittedAnswer: undefined }))
    }
  }, [question.status])
  useEffect(() => {
    if (follow.current && transcript.current) transcript.current.scrollTop = transcript.current.scrollHeight
  }, [side.view.nodes, tab])
  const patch = (change: Partial<QuestionDraft>) => setDraft(v => ({ ...v, ...change }))
  const submit = async () => {
    if (!editable || !answer || sendingRef.current) return
    sendingRef.current = true
    setSending(true)
    setError('')
    const submissionId = draft.submissionId && draft.submittedAnswer === answer ? draft.submissionId : clientRequestId()
    const submittedDraft = { ...draft, submissionId, submittedAnswer: answer }
    setDraft(submittedDraft)
    try { sessionStorage.setItem(key, JSON.stringify(submittedDraft)) } catch {}
    try {
      const receipt = await api.questionReply(sessionId, { questionItemId: question.questionItemId, revision: question.revision, submissionId, answer, answerForm: { selected: draft.selected, text: draft.text } })
      onQuestion(receipt.question)
      onRefresh()
    } catch (e) {
      setError(e instanceof ApiError && e.status === 409 ? c.conflict : e instanceof Error ? e.message : String(e))
      onRefresh()
    } finally { sendingRef.current = false; setSending(false) }
  }
  const action = async (action: 'dismiss' | 'restore' | 'continue') => {
    if (acting) return
    setActing(true)
    setError('')
    try {
      onQuestion((await api.questionAction(sessionId, question.questionItemId, question.revision, action)).question)
      if (action === 'continue') onRefresh()
    }
    catch (e) { setError(e instanceof Error ? e.message : String(e)); onRefresh() }
    finally { setActing(false) }
  }
  const sendSide = async () => {
    const text = draft.sideText
    if (await side.send(text)) setDraft(v => ({ ...v, sideText: v.sideText === text ? '' : v.sideText }))
  }
  return createPortal(<div className="question-overlay" onMouseDown={e => { if (e.target === e.currentTarget) onClose() }}>
    <section ref={dialog} className={`question-dialog question-tab-${tab}`} role="dialog" aria-modal="true" aria-labelledby={titleId} aria-describedby={hintId} data-testid="question-dialog">
      <header className="question-dialog-header">
        <div className="question-dialog-symbol"><IQuestion /></div>
        <div className="question-dialog-heading"><span>{c.questions} · {c[question.status]}</span><h2 id={titleId}>{question.title}</h2></div>
        <button ref={initialFocus} type="button" className="icon-btn" aria-label={c.close} onClick={onClose}><IClose /></button>
      </header>
      <p className="question-boundary" id={hintId}>{c.snapshot} · {new Date(question.createdAt).toLocaleString()}</p>
      <div className="question-mobile-tabs" role="tablist" aria-label={c.questions} onKeyDown={e => {
        if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) return
        e.preventDefault()
        const next = e.key === 'Home' ? 'answer' : e.key === 'End' ? 'side' : tab === 'answer' ? 'side' : 'answer'
        setTab(next)
        document.getElementById(`${titleId}-${next}-tab`)?.focus()
      }}>
        <button role="tab" tabIndex={tab === 'answer' ? 0 : -1} id={`${titleId}-answer-tab`} aria-selected={tab === 'answer'} aria-controls={`${titleId}-answer`} onClick={() => setTab('answer')}>{c.answer}</button>
        <button role="tab" tabIndex={tab === 'side' ? 0 : -1} id={`${titleId}-side-tab`} aria-selected={tab === 'side'} aria-controls={`${titleId}-side`} onClick={() => setTab('side')}><ISideChat />{c.side}<span>{c.readonly}</span></button>
      </div>
      <div className="question-dialog-body">
        <section className="question-answer-pane" id={`${titleId}-answer`} aria-label={c.answer}>
          <div className="question-answer-scroll">
            <h3>{c.answer}</h3>
            {!question.sourceBranchValid ? <p className="question-warning" role="status">{c.branch}</p> : null}
            {question.status === 'answered' && question.answerBranchValid === false ? <p className="question-warning" role="status">{c.answerOtherBranch}</p> : null}
            <div data-testid={confirmed ? "question-receipt" : undefined}>
              {confirmed ? <p className="question-confirmed" role="status">{question.status === 'answer_queued' ? c.queued : c.receipt}</p> : null}
              {question.options?.length ? <fieldset className="question-options" disabled={!editable || sending}>
                <legend className="sr-only">{c.answer}</legend>
                {question.options.map((option, i) => <label className={form.selected === option ? 'selected' : ''} key={`${i}-${option}`}>
                  <input type="radio" name={optionName} value={option} checked={form.selected === option} onChange={() => patch({ selected: option })} />
                  <span><strong>{option}</strong>{i === 0 && !confirmed ? <small>{c.default}</small> : null}</span>
                </label>)}
                <label className={!form.selected ? 'selected' : ''}><input type="radio" name={optionName} value="" checked={!form.selected} onChange={() => patch({ selected: '' })} /><span>{c.other}</span></label>
              </fieldset> : null}
              <textarea className="question-answer-input" aria-label={c.answerHint} placeholder={c.answerHint} value={form.text} readOnly={confirmed} disabled={!confirmed && (!editable || sending)} onChange={e => patch({ text: e.target.value })} />
            </div>
            {error ? <p role="alert" className="question-error">{error}</p> : null}
            {onLocate ? <button type="button" className="question-locate" onClick={() => { onLocate(question.sourceEntryId); onClose() }}>{c.locate}</button> : null}
          </div>
          <footer className="question-answer-footer">
            {question.status === 'pending' ? <><small>{mainBusy ? c.steerMain : c.continueMain}</small><button type="button" className="primary-btn" data-testid="question-submit" disabled={!editable || !answer || sending} onClick={() => void submit()}>{sending ? c.loading : c.send}</button><button type="button" disabled={acting || sending} onClick={() => void action('dismiss')}>{c.dismiss}</button></> : question.status === 'dismissed' ? <button type="button" disabled={acting || !question.sourceBranchValid} onClick={() => void action('restore')}>{c.restore}</button> : <><small>{c[question.status]}</small>{question.status === 'answer_queued' && !mainBusy ? <button type="button" className="primary-btn" disabled={acting || !question.sourceBranchValid} onClick={() => void action('continue')}>{acting ? c.continuing : c.continue}</button> : null}</>}
          </footer>
        </section>
        <section className="question-side-pane" id={`${titleId}-side`} aria-label={c.side}>
          <header className="question-side-header"><ISideChat /><h3>{c.side}</h3><span className="question-readonly">{c.readonly}</span><span className="question-side-model">{side.view.model}</span>{side.view.busy ? <span className="question-side-running" role="status">{c.running}</span> : null}</header>
          <p className="question-isolation">{c.isolation}</p>
          <div ref={transcript} className="question-side-transcript" data-testid="question-side-transcript" onScroll={() => { const el = transcript.current; if (el) follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < 60 }}>
            {side.view.hasMore ? <button type="button" disabled={side.loadingOlder} onClick={() => void side.loadOlder()}>{side.loadingOlder ? c.loading : c.older}</button> : null}
            {!side.view.nodes.length ? <div className="question-side-empty"><ISideChat /><p>{side.status.phase === 'recovering' ? c.loading : c.noSide}</p></div> : null}
            {side.view.nodes.map(n => n.kind === 'user' ? <div className="question-side-user" key={n.id}>{n.text}</div> : n.kind === 'assistant' ? <div className="question-side-assistant" key={n.id}><Markdown text={n.text} streaming={n.streaming} />{n.error ? <p className="question-error">{n.error}</p> : null}</div> : n.kind === 'tool' ? <details className="question-side-tool" key={n.id}><summary>{n.name} · {n.running ? c.running : n.isError ? c.error : c.readonly}</summary><pre>{n.result || JSON.stringify(n.args, null, 2)}</pre></details> : null)}
            {side.error ? <div role="alert" className="question-error">{side.error}<button type="button" onClick={() => void side.recover()}>{c.retry}</button></div> : null}
          </div>
          <div className="question-side-composer">
            <div className="composer" data-testid="side-composer">
            <textarea aria-label={c.sideHint} placeholder={c.sideHint} value={draft.sideText} onChange={e => patch({ sideText: e.target.value })} rows={1} onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing && !side.view.busy) { e.preventDefault(); void sendSide() } }} />
            <div className="composer-row"><span className="grow" />{side.view.busy ? <button type="button" className="send stop" aria-label={c.stop} title={c.stop} onClick={() => void side.stop()}><IStop /></button> : <button type="button" className="send" disabled={side.sending || !draft.sideText.trim()} aria-label={c.sendSide} title={c.sendSide} onClick={() => void sendSide()}><ISend /></button>}</div>
            </div>
          </div>
        </section>
      </div>
    </section>
  </div>, document.body)
}
