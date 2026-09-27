// Admit one React parse per frame. Independent idle callbacks used to wake all
// newly mounted rows at once, moving the same long task one frame later.
const tasks = new Set<{ run: () => void; element: () => HTMLElement | null }>()
let frame = 0
function schedule() {
  if (frame || !tasks.size) return
  frame = requestAnimationFrame(() => {
    frame = 0
    const distance = (task: { element: () => HTMLElement | null }) => {
      const rect = task.element()?.getBoundingClientRect()
      return rect ? Math.max(0, -rect.bottom, rect.top - window.innerHeight) : Infinity
    }
    const next = [...tasks].sort((a, b) => distance(a) - distance(b))[0]
    if (next) { tasks.delete(next); next.run() }
    schedule()
  })
}
export function queueMarkdown(run: () => void, element: () => HTMLElement | null): () => void {
  const task = { run, element }
  tasks.add(task)
  schedule()
  return () => {
    tasks.delete(task)
    if (!tasks.size) { cancelAnimationFrame(frame); frame = 0 }
  }
}
