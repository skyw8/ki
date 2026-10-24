export async function copyText(text: string): Promise<boolean> {
  if (!text) return false
  if (window.isSecureContext && navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
    }
  }
  return execCommandCopy(text)
}

function execCommandCopy(text: string): boolean {
  if (!document.body) return false
  const active = document.activeElement instanceof HTMLElement ? document.activeElement : null
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
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
    ta.focus()
    ta.select()
    ta.setSelectionRange(0, text.length)
    ok = document.execCommand('copy')
  } catch {
    ok = false
  } finally {
    ta.remove()
    active?.focus()
    if (previous && selection) {
      try {
        selection.removeAllRanges()
        selection.addRange(previous)
      } catch {
      }
    }
  }
  return ok
}
