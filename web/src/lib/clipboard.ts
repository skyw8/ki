// Copy text to the clipboard, working in both secure and insecure contexts.
//
// The async Clipboard API (`navigator.clipboard`) is only exposed in a secure
// context: HTTPS, or plain HTTP on `localhost` / `127.0.0.1`. The WebUI is
// routinely reached over plain HTTP through a host name (a Tailscale/LAN
// address or a forwarded host), where `navigator.clipboard` is undefined and
// every copy button silently no-ops. Fall back to the legacy
// `document.execCommand('copy')` path, which works in any context.
//
// Returns whether the copy actually happened, so callers can avoid claiming
// success when it did not.
export async function copyText(text: string): Promise<boolean> {
  if (!text) return false
  if (window.isSecureContext && navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // Permission denied or a lost user gesture: fall through to the legacy path.
    }
  }
  return execCommandCopy(text)
}

// execCommandCopy is the fallback for insecure contexts and for a rejected
// async write. It never throws: callers use `void copyText(...)`, so an
// exception here would surface as an unhandled rejection.
function execCommandCopy(text: string): boolean {
  if (!document.body) return false
  const active = document.activeElement instanceof HTMLElement ? document.activeElement : null
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  // Keep the scratch element out of the layout so selecting it cannot scroll.
  ta.style.position = 'fixed'
  ta.style.top = '0'
  ta.style.left = '0'
  ta.style.width = '1px'
  ta.style.height = '1px'
  ta.style.padding = '0'
  ta.style.border = 'none'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  const selection = document.getSelection()
  const previous = selection && selection.rangeCount > 0 ? selection.getRangeAt(0) : null
  let ok = false
  try {
    // iOS Safari only applies the copy command while the element has focus.
    ta.focus()
    ta.select()
    ta.setSelectionRange(0, text.length)
    ok = document.execCommand('copy')
  } catch {
    ok = false
  } finally {
    ta.remove()
    // Focusing the scratch element (and removing it) moved focus to the body;
    // put it back so keyboard users keep their place.
    active?.focus()
    if (previous && selection) {
      try {
        selection.removeAllRanges()
        selection.addRange(previous)
      } catch {
        // The previous range can belong to a detached node; restoring is best-effort.
      }
    }
  }
  return ok
}
