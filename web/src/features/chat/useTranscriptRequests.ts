import { useLayoutEffect, useMemo, useRef, useSyncExternalStore } from 'react'
import type { Client } from '../../api/client'
import type { MessageView } from '../../lib/messageView'
import { TranscriptRequests } from '../../lib/transcript-requests'
import type { TranscriptStore } from '../../lib/transcript-store'

/** React owns only this controller's lifetime and snapshot subscription. */
export function useTranscriptRequests(
  api: Client,
  id: string | null,
  revision: number,
  store: TranscriptStore,
  presentation: MessageView,
  beforeCommit: (signal: AbortSignal) => Promise<void>,
  onRecoverNeeded?: () => Promise<void>,
) {
  const callbacks = useRef({ beforeCommit, onRecoverNeeded })
  callbacks.current = { beforeCommit, onRecoverNeeded }
  const canRecover = !!onRecoverNeeded
  const controller = useMemo(() => new TranscriptRequests({
    api, id, store, presentation,
    beforeCommit: signal => callbacks.current.beforeCommit(signal),
    onRecoverNeeded: canRecover ? () => callbacks.current.onRecoverNeeded?.() ?? Promise.resolve() : undefined,
  }), [api, id, revision, store, presentation.mode, presentation.keep, canRecover])
  useLayoutEffect(() => {
    controller.start()
    return () => controller.dispose()
  }, [controller])
  const status = useSyncExternalStore(controller.subscribe, controller.getSnapshot, controller.getSnapshot)
  return { ...status, cancel: controller.cancel, loadOlder: controller.loadOlder,
    requestHydrate: controller.requestHydrate, requestIndex: controller.requestIndex,
    refreshIndex: controller.refreshIndex, requestTurn: controller.requestTurn, protect: controller.protect }
}
