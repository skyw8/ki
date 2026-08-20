/** Leave follow when the user is this far from the tail. */
export const FOLLOW_RELEASE_PX = 80
/** Resume follow only once they are this close to the tail. */
export const FOLLOW_CATCH_PX = 16

/** Scroll a trajectory ledger to the tail when follow-tail is on. */
export function applyFollowTail(el: { scrollHeight: number; scrollTop: number } | null, follow: boolean): boolean {
  if (el == null || !follow) return false
  el.scrollTop = el.scrollHeight
  return true
}

/** Hysteresis so a tiny drag or a growing log does not yank follow back on. */
export function followFromGap(gap: number, following: boolean, release = FOLLOW_RELEASE_PX, catchUp = FOLLOW_CATCH_PX): boolean {
  if (gap > release) return false
  if (gap <= catchUp) return true
  return following
}
