// Admit one React parse per frame. Independent idle callbacks used to wake all
// newly mounted rows at once, moving the same long task one frame later.
const tasks = new Set<{ run: () => void; element: () => HTMLElement | null; priority: number; queued: number }>()
let frame = 0
let lastCost = 0
function schedule() {
  if (frame || !tasks.size) return
  frame = requestAnimationFrame(() => {
    frame = 0
    const distance = (task: { element: () => HTMLElement | null }) => {
      const rect = task.element()?.getBoundingClientRect()
      return rect ? Math.max(0, -rect.bottom, rect.top - window.innerHeight) : Infinity
    }
    // Read each rectangle once. Sorting with layout reads in the comparator
    // multiplied work during bursts of newly mounted history rows.
    const next = [...tasks].map(task => ({ task, distance: distance(task) }))
      .sort((a, b) => a.distance - b.distance || b.task.priority - a.task.priority || a.task.queued - b.task.queued)[0]?.task
    if (next) {
      tasks.delete(next)
      const started = performance.now()
      next.run()
      // React's commit may follow the callback. Observe the whole turn before
      // admitting another parse; expensive work gets a paint opportunity first.
      frame = requestAnimationFrame(() => {
        lastCost = performance.now() - started
        frame = 0
        if (lastCost > 32) frame = requestAnimationFrame(() => { frame = 0; schedule() })
        else schedule()
      })
    }
    schedule()
  })
}
export function queueMarkdown(run: () => void, element: () => HTMLElement | null, priority = 0): () => void {
  const task = { run, element, priority, queued: performance.now() }
  tasks.add(task)
  schedule()
  return () => {
    tasks.delete(task)
    if (!tasks.size) { cancelAnimationFrame(frame); frame = 0 }
  }
}
