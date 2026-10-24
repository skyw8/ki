import { useEffect, useRef } from 'react'
import { clearTabFocus, publishTabFocus } from '../lib/tab-focus'

export function useTabFocus(serverId: string, sessionId: string | null): void {
  const tabId = useRef('')
  if (!tabId.current) tabId.current = Math.random().toString(36).slice(2)
  useEffect(() => {
    const update = () => publishTabFocus(serverId, tabId.current, sessionId)
    const clear = () => clearTabFocus(serverId, tabId.current)
    update()
    const heartbeat = window.setInterval(update, 10_000)
    window.addEventListener('focus', update)
    window.addEventListener('blur', update)
    window.addEventListener('pagehide', clear)
    window.addEventListener('pageshow', update)
    document.addEventListener('visibilitychange', update)
    return () => {
      window.removeEventListener('focus', update)
      window.removeEventListener('blur', update)
      window.removeEventListener('pagehide', clear)
      window.removeEventListener('pageshow', update)
      window.clearInterval(heartbeat)
      document.removeEventListener('visibilitychange', update)
      clear()
    }
  }, [serverId, sessionId])
}
