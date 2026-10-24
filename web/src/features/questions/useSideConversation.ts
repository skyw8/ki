import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import type { Client } from '../../api/client'
import type { Question } from '../../api/types'
import { useTranscriptStore } from '../../hooks/useTranscriptStore'
import { useServerEvents } from '../../hooks/useServerEvents'
import { emptyView } from '../../lib/model'
import { SessionSyncController } from '../../lib/session-sync'
import { clientRequestId } from '../../lib/client-request'

export function useSideConversation(api: Client, parentId: string, question: Question, onQuestion: (q: Question) => void, onCreated: () => void) {
  const [id, setId] = useState(question.sideSessionId ?? '')
  const { store, view } = useTranscriptStore()
  const [error, setError] = useState('')
  const [sending, setSending] = useState(false)
  const [loadingOlder, setLoadingOlder] = useState(false)
  const pendingSend = useRef<{ text: string; requestId: string } | null>(null)
  const sendingRef = useRef(false)
  const preparedSide = useRef<string | null>(null)
  const mounted = useRef(true)
  const sync = useMemo(() => new SessionSyncController(api, store, {
    transcriptOptions: () => ({}),
    onError: e => setError(e instanceof Error ? e.message : String(e)),
  }), [api, store])
  const status = useSyncExternalStore(sync.subscribe, sync.getSnapshot, sync.getSnapshot)
  useEffect(() => {
    mounted.current = true
    sync.activate()
    return () => { mounted.current = false; sync.dispose() }
  }, [sync])
  useEffect(() => {
    if (question.sideSessionId) setId(question.sideSessionId)
  }, [question.sideSessionId])
  useEffect(() => {
    if (preparedSide.current === id) { preparedSide.current = null; return }
    if (store.sessionId !== (id || null)) store.reset(id || null, emptyView())
    sync.select(id || null)
    if (id) void sync.recover()
  }, [id, store, sync])
  useEffect(() => {
    const wake = () => { if (document.visibilityState === 'visible' && id) void sync.resume() }
    const hide = () => sync.suspend()
    window.addEventListener('online', wake)
    window.addEventListener('pageshow', wake)
    window.addEventListener('pagehide', hide)
    document.addEventListener('visibilitychange', wake)
    return () => {
      window.removeEventListener('online', wake)
      window.removeEventListener('pageshow', wake)
      window.removeEventListener('pagehide', hide)
      document.removeEventListener('visibilitychange', wake)
    }
  }, [id, sync])
  useServerEvents(api, ev => {
    if (!id) return
    if (ev.type === 'ready' || (ev.type === 'invalidate' && ev.scope === 'sessions') || (ev.sessionId === id && ['agent_start', 'agent_end', 'run_aborted'].includes(ev.type))) void sync.recover()
  })
  const send = useCallback(async (text: string): Promise<boolean> => {
    if (sendingRef.current || view.busy || !text.trim()) return false
    sendingRef.current = true
    setSending(true)
    setError('')
    try {
      let target = id
      if (!target) {
        const fork = await api.questionSide(parentId, question.questionItemId)
        target = fork.sessionId || fork.id || ''
        if (!target) throw new Error('Side conversation was not created')
        onCreated()
        if (!mounted.current) return false
        if (fork.question) onQuestion(fork.question)
        preparedSide.current = target
        setId(target)
        store.reset(target, emptyView())
        sync.select(target)
        await sync.recover()
      }
      if (!mounted.current) return false
      const requestId = pendingSend.current?.text === text ? pendingSend.current.requestId : clientRequestId()
      pendingSend.current = { text, requestId }
      await api.prompt(target, [{ type: 'text', text }], undefined, undefined, 'steer', undefined, requestId)
      pendingSend.current = null
      if (!mounted.current) return true
      store.update(v => ({ ...v, busy: true }))
      void sync.listen(target)
      return true
    } catch (e) {
      if (mounted.current) {
        setError(e instanceof Error ? e.message : String(e))
      }
      return false
    } finally { sendingRef.current = false; if (mounted.current) setSending(false) }
  }, [api, id, onCreated, onQuestion, parentId, question.questionItemId, sending, store, sync, view.busy])

  const loadOlder = async () => {
    if (!id || loadingOlder || !view.oldestId) return
    setLoadingOlder(true)
    const ticket = store.capture('page')
    try { store.commit(ticket, await api.get(id, { before: view.oldestId })) }
    catch (e) { if (mounted.current) setError(String(e)) }
    finally { if (mounted.current) setLoadingOlder(false) }
  }
  const stop = async () => {
    if (!id) return
    try { await api.abort(id); void sync.recover(true) }
    catch (e) { setError(String(e)) }
  }
  return { id, view, status, sending, error, send, stop, loadOlder, loadingOlder, recover: () => sync.recover(true) }
}
