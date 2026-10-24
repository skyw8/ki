export const FOLLOW_RELEASE_PX = 80
export const FOLLOW_CATCH_PX = 16

export function applyFollowTail(el: { scrollHeight: number; scrollTop: number } | null, follow: boolean): boolean {
  if (el == null || !follow) return false
  el.scrollTop = el.scrollHeight
  return true
}

export function followFromGap(gap: number, following: boolean, release = FOLLOW_RELEASE_PX, catchUp = FOLLOW_CATCH_PX): boolean {
  if (gap > release) return false
  if (gap <= catchUp) return true
  return following
}
