import { createContext, useCallback, useContext, useLayoutEffect, useRef, type DependencyList, type ReactNode } from 'react'

const MeasureRow = createContext<() => void>(() => {})

/** Local expansions must join the same pre-paint measurement as body updates. */
export function useTranscriptRowMeasurement(changes: DependencyList) {
  const measure = useContext(MeasureRow)
  useLayoutEffect(measure, [measure, ...changes])
}

export function TranscriptRow({ id, index, start, measure, children }: {
  id: string
  index: number
  start: number
  measure: (element: HTMLDivElement | null) => void
  children: ReactNode
}) {
  const element = useRef<HTMLDivElement | null>(null)
  const measureCurrent = useCallback(() => {
    if (element.current) measure(element.current)
  }, [measure])
  const attach = useCallback((el: HTMLDivElement | null) => {
    element.current = el
    measure(el)
  }, [measure])
  return <MeasureRow.Provider value={measureCurrent}>
    <div ref={attach} data-index={index} data-item-key={id} className="chat-virtual-item" style={{ transform: `translateY(${start}px)` }}>{children}</div>
  </MeasureRow.Provider>
}
