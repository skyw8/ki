import type { Page } from '@playwright/test'
import type { Entry, LoopEvent, Question, SessionDetail, SessionInfo } from '../src/api/types'

type QuestionFixture = {
  question: Question
  sessions: SessionInfo[]
  details: Record<string, SessionDetail>
  prompts: { id: string; body: Record<string, unknown> }[]
  forks: number
  actions: string[]
  holdSide: boolean
  conflict: boolean
  requests: string[]
  holdList: boolean
  pendingLists: number
  failLists: boolean
  holdWorkspaces: boolean
  pendingWorkspaces: number
  failWorkspaces: boolean
  releaseWorkspaces: () => void
  releaseLists: () => void
  emit: (id: string, event: LoopEvent & { sessionId?: string }) => void
  finishSide: (text: string) => void
}
declare global { interface Window { questionFixture: QuestionFixture } }

export async function installQuestions(page: Page, lang = 'zh') {
  await page.addInitScript(({ lang }) => {
    localStorage.setItem('ki-lang', lang)
    localStorage.setItem('ki-message-view', 'detailed')
    const root: SessionInfo = { id: 'question-main', title: '发布策略与上线准备', cwd: '/workspace', provider: 'test', model: 'reasoner', workspaceId: 'question-workspace' }
    const source: Entry = { type: 'message', id: 'main-user', message: { role: 'user', content: [{ type: 'text', text: '请检查发布方案，并在需要决策时向我提问。' }] } }
    const assistant: Entry = { type: 'message', id: 'main-assistant', parentId: source.id, message: { role: 'assistant', content: [{ type: 'text', text: '已完成基础检查。我会继续核对兼容性，发布范围需要你确认。' }] } }
    const question: Question = JSON.parse(sessionStorage.getItem('question-fixture-reload') ?? 'null') ?? { questionItemId: '["request_user_input_async","question-call",0]', callId: 'question-call', index: 0, title: '这次发布应该覆盖哪些用户？', options: ['先灰度 10%，观察后再扩大', '直接向全部用户发布'], status: 'pending', revision: 1, createdAt: '2026-10-24T01:00:00Z', updatedAt: '2026-10-24T01:00:00Z', sourceEntryId: assistant.id, snapshotEntryId: source.id, sourceBranchValid: true }
    sessionStorage.removeItem('question-fixture-reload')
    const controllers = new Map<string, Set<ReadableStreamDefaultController<Uint8Array>>>()
    const heldLists: (() => void)[] = []
    const heldWorkspaces: (() => void)[] = []
    const state: QuestionFixture = window.questionFixture = {
      question, sessions: [root], details: { [root.id]: { ...root, entries: [source, assistant], questions: [question], running: false, leafId: assistant.id, oldestId: source.id, runtime: { ready: true } } },
      prompts: [], forks: 0, actions: [], holdSide: false, conflict: false, requests: [], holdList: false, pendingLists: 0,
      failLists: false, holdWorkspaces: false, pendingWorkspaces: 0, failWorkspaces: false,
      releaseWorkspaces() { state.holdWorkspaces = false; for (const release of heldWorkspaces.splice(0)) release(); state.pendingWorkspaces = 0 },
      releaseLists() { state.holdList = false; for (const release of heldLists.splice(0)) release(); state.pendingLists = 0 },
      emit(id, event) {
        for (const controller of controllers.get(id) ?? []) controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(event)}\n\n`))
      },
      finishSide(text) {
        const detail = state.details['question-side']
        const entry: Entry = { type: 'message', id: `side-answer-${detail.entries!.length}`, parentId: detail.leafId, message: { role: 'assistant', stopReason: 'stop', content: [{ type: 'text', text }] } }
        detail.entries!.push(entry); detail.leafId = entry.id; detail.running = false
        state.sessions.find(s => s.id === detail.id)!.running = false
        state.emit(detail.id, { type: 'message_end', entryId: entry.id, parentId: entry.parentId, message: entry.message })
        state.emit(detail.id, { type: 'agent_end' })
        for (const controller of controllers.get(detail.id) ?? []) controller.close()
        controllers.delete(detail.id)
      },
    }
    const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
    const update = (patch: Partial<Question>) => {
      state.question = { ...state.question, ...patch }
      state.details[root.id].questions = [state.question]
      state.emit('server', { type: 'question_updated', question: state.question, sessionId: root.id } as LoopEvent)
    }
    const original = window.fetch.bind(window)
    window.fetch = (async (input, init) => {
      const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, location.href)
      const path = url.pathname
      if (!path.startsWith('/v1/')) return original(input, init)
      state.requests.push(`${init?.method ?? 'GET'} ${path}${url.search}`)
      const signal = init?.signal
      if (path === '/v1/events' || path.endsWith('/events')) {
        const id = path === '/v1/events' ? 'server' : path.split('/')[3]
        let controller: ReadableStreamDefaultController<Uint8Array>
        const stream = new ReadableStream<Uint8Array>({
          start(value) { controller = value; const set = controllers.get(id) ?? new Set(); set.add(value); controllers.set(id, set) },
          cancel() { controllers.get(id)?.delete(controller) },
        })
        signal?.addEventListener('abort', () => { controllers.get(id)?.delete(controller); try { controller.close() } catch {} }, { once: true })
        if (id !== 'server' && !state.details[id]?.running) queueMicrotask(() => {
          for (const entry of state.details[id]?.entries ?? []) controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ type: 'message_end', entryId: entry.id, parentId: entry.parentId, message: entry.message })}\n\n`))
          controller.enqueue(new TextEncoder().encode('data: {"type":"agent_end"}\n\n')); controller.close(); controllers.get(id)?.delete(controller)
        })
        return new Response(stream, { headers: { 'Content-Type': 'text/event-stream' } })
      }
      if (path === '/v1/auth/status') return json({ authenticated: true, serverId: 'question-fixture', csrfCookieName: '' })
      if (path === '/v1/sessions') {
        const response = json(state.sessions.map(row => ({ ...row, questionLinks: (state.details[row.id]?.questions ?? []).map(q => ({ questionItemId: q.questionItemId, title: q.title, status: q.status, revision: q.revision, sideSessionId: q.sideSessionId })) })))
        if (state.holdList) { state.pendingLists++; await new Promise<void>(resolve => heldLists.push(resolve)) }
        return state.failLists ? json({ error: 'Session listing unavailable' }, 503) : response
      }
      if (path === '/v1/workspaces') {
        if (state.holdWorkspaces) { state.pendingWorkspaces++; await new Promise<void>(resolve => heldWorkspaces.push(resolve)) }
        return state.failWorkspaces ? json({ error: 'Workspace listing unavailable' }, 503)
          : json([{ id: root.workspaceId, title: '产品工作区', path: '/workspace', sessionIds: state.sessions.map(s => s.id) }])
      }
      if (path === '/v1/models') return json([{ provider: 'test', id: 'reasoner', name: 'Reasoner', spec: 'test/reasoner', contextWindow: 100000 }])
      if (path === '/v1/meta') return json({ home: '/workspace', provider: 'test', model: 'reasoner' })
      if (path === '/v1/message') return json({ busy: 'steer', contextWindow: 100000 })
      if (path === '/v1/push/config') return json({ enabled: false, publicKey: '' })
      const match = /^\/v1\/sessions\/([^/]+)(?:\/([^/]+))?$/.exec(path)
      if (match) {
        const [, id, action] = match
        if (!action) return json(state.details[id] ?? {}, state.details[id] ? 200 : 404)
        const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>
        if (action === 'fork') {
          state.forks++
          const side: SessionInfo = { ...root, id: 'question-side', purpose: 'question_side', sourceSessionId: root.id, sourceQuestionItemId: state.question.questionItemId, parentSessionId: root.id, sideBoundaryEntryId: 'boundary', title: '发布范围' }
          state.sessions.push(side); state.details[side.id] = { ...side, entries: [], running: false, runtime: { ready: true } }
          update({ sideSessionId: side.id, revision: state.question.revision + 1 })
          return json({ id: side.id, question: state.question })
        }
        if (action === 'question-action') {
          state.actions.push(String(body.action))
          if (body.action !== 'continue') update({ status: body.action === 'dismiss' ? 'dismissed' : 'pending', revision: state.question.revision + 1 })
          return json({ question: state.question, accepted: true, delivery: 'started' })
        }
        if (action === 'abort') {
          if (id === 'question-side') state.finishSide('旁聊已停止。')
          return json({ ok: true })
        }
        if (action === 'prompt') {
          state.prompts.push({ id, body })
          if (body.questionReply) {
            if (state.conflict) { update({ revision: state.question.revision + 1, status: 'answered', answer: '另一窗口的答案', answerBranchValid: true }); return json({ error: 'revision conflict' }, 409) }
            const reply = body.questionReply as { answer: string; submissionId: string; answerForm?: Question['answerForm'] }
            update({ status: 'answered', answer: reply.answer, answerForm: reply.answerForm, revision: state.question.revision + 1, submissionId: reply.submissionId, answerBranchValid: true })
            return json({ accepted: true, question: state.question })
          }
          const detail = state.details[id]
          const text = (body.content as { text: string }[])[0].text
          const entry: Entry = { type: 'message', id: `side-user-${detail.entries!.length}`, parentId: detail.leafId, message: { role: 'user', clientRequestId: body.clientRequestId as string, content: [{ type: 'text', text }] } }
          detail.entries!.push(entry); detail.leafId = entry.id; detail.running = true
          state.sessions.find(s => s.id === id)!.running = true
          if (!state.holdSide) {
            const answer: Entry = { type: 'message', id: `side-assistant-${detail.entries!.length}`, parentId: entry.id, message: { role: 'assistant', stopReason: 'stop', content: [{ type: 'text', text: text.includes('草稿') || text.includes('suggest an answer') ? '建议先灰度 10%，观察错误率和回滚指标后再扩大。' : '灰度发布能降低影响范围。可以先覆盖 10% 用户，再观察错误率与反馈。' }] } }
            detail.entries!.push(answer); detail.leafId = answer.id; detail.running = false; state.sessions.find(s => s.id === id)!.running = false
          }
          return json({ accepted: true })
        }
      }
      if (['/v1/commands', '/v1/skills', '/v1/extensions', '/v1/tools'].includes(path)) return json({ items: [] })
      return json({})
    }) as typeof fetch
  }, { lang })
}
