import { useState, useSyncExternalStore } from 'react'
import { emptyView } from '../lib/model'
import { TranscriptStore } from '../lib/transcript-store'

/** React renders committed snapshots; it never owns request acknowledgements. */
export function useTranscriptStore() {
  const [store] = useState(() => new TranscriptStore(emptyView()))
  const view = useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot)
  return { store, view, setView: store.update }
}
