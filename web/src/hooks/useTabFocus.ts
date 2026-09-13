import { useEffect, useRef } from 'react'
import { clearTabFocus, publishTabFocus } from '../lib/tab-focus'

// Keeps the shared "focused session" marker in sync with this tab so other tabs
// can tell whether the user is looking at a session that just completed. The
// marker must follow focus, visibility, and navigation, and be cleared on
// unload; otherwise a stale entry would suppress a real notification.
export function useTabFocus(sessionId: string | null): void {
  const tabId = useRef('')
  if (!tabId.current) tabId.current = Math.random().toString(36).slice(2)
  useEffect(() => {
    const update = () => publishTabFocus(tabId.current, sessionId)
    const clear = () => clearTabFocus(tabId.current)
    update()
    window.addEventListener('focus', update)
    window.addEventListener('blur', update)
    window.addEventListener('pagehide', clear)
    document.addEventListener('visibilitychange', update)
    return () => {
      window.removeEventListener('focus', update)
      window.removeEventListener('blur', update)
      window.removeEventListener('pagehide', clear)
      document.removeEventListener('visibilitychange', update)
      clear()
    }
  }, [sessionId])
}
