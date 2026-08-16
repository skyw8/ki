/** Scroll a trajectory ledger to the tail when follow-tail is on. */
export function applyFollowTail(el: { scrollHeight: number; scrollTop: number } | null, follow: boolean): boolean {
  if (el == null || !follow) return false
  el.scrollTop = el.scrollHeight
  return true
}
