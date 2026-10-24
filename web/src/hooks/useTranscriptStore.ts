import { useState, useSyncExternalStore } from 'react'
import { emptyView } from '../lib/model'
import { TranscriptStore } from '../lib/transcript-store'

export function useTranscriptStore() {
  const [store] = useState(() => new TranscriptStore(emptyView()))
  const view = useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot)
  return { store, view, setView: store.update }
}
