import { expect, test } from '@playwright/test'
import { openStream } from './stream-fixture'

for (const profile of [{ name: 'desktop', history: 0, cpu: 1 }, { name: 'long history', history: 800, cpu: 1 }, { name: 'mobile CPU 4x', history: 0, cpu: 4 }]) {
  test(`visible stream deadlines and stable completion: ${profile.name}`, async ({ page }, info) => {
    if (profile.cpu > 1) {
      await page.setViewportSize({ width: 390, height: 844 })
      const cdp = await page.context().newCDPSession(page)
      await cdp.send('Emulation.setCPUThrottlingRate', { rate: profile.cpu })
    }
    await openStream(page, profile.history)
    const stats = await page.evaluate(async () => {
      const send = (window as unknown as { streamSend: (value: unknown) => void }).streamSend
      const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms))
      let text = '', seq = 0, shown = 0, stopped = false, maxPending = 0, maxFrame = 0
      let previousFrame = performance.now()
      const times = new Map<number, number>(), lags: number[] = []
      const errors: string[] = []
      const frame = (now: number) => {
        if (stopped) return
        maxFrame = Math.max(maxFrame, now - previousFrame)
        previousFrame = now
        const element = document.querySelector('[data-stream-seq]') as HTMLElement | null
        const next = Number(element?.dataset.streamSeq ?? 0)
        if (next > shown) {
          if (!element?.textContent?.includes(`tick${next.toString().padStart(4, '0')}`)) errors.push(`revision ${next} committed without its text`)
          lags.push(now - (times.get(next) ?? now))
          shown = next
        }
        if (seq > shown) maxPending = Math.max(maxPending, now - (times.get(shown + 1) ?? now))
        if (!stopped) requestAnimationFrame(frame)
      }
      requestAnimationFrame(frame)
      const chunk = () => {
        seq++
        const delta = `tick${seq.toString().padStart(4, '0')}: 中文🙂 **visible** ${'body '.repeat(35)}\n\n`
        text += delta
        times.set(seq, performance.now())
        send(seq === 1 ? { type: 'message_update', runId: 'budget', seq, messageStream: 1, message: { role: 'assistant', content: [{ type: 'text', text }] } }
          : { type: 'message_update', runId: 'budget', seq, messageStream: 1, messagePatch: { baseSeq: seq - 1, changes: [{ path: ['content', '0', 'text'], op: 'append', value: delta }] } })
      }
      // Dense input, a pause and a replay burst exercise different scheduling paths.
      for (let i = 0; i < 180; i++) { chunk(); await sleep(5) }
      await sleep(150)
      for (let i = 0; i < 240; i++) chunk()
      for (let i = 0; i < 20; i++) { chunk(); await sleep(50) }
      await sleep(150)
      const before = document.querySelector('[data-stream-seq]')
      send({ type: 'message_end', runId: 'budget', seq: seq + 1, entryId: 'final', message: { role: 'assistant', content: [{ type: 'text', text }] } })
      // The terminal marker is not another text delta; stop the delta probe first.
      stopped = true
      await sleep(100)
      const sameRoot = before === document.querySelector('[data-stream-seq]')
      const finalText = document.querySelector('[data-stream-seq]')?.textContent ?? ''
      lags.sort((a, b) => a - b)
      return { p95: lags[Math.floor(lags.length * .95)] ?? Infinity, maxPending, maxFrame, commits: lags.length, sameRoot, errors, markers: finalText.match(/tick\d{4}:/g)?.length ?? 0, expected: seq }
    })
    console.log(`${profile.name}: ${JSON.stringify(stats)}`)
    expect(stats.errors).toEqual([])
    expect(stats.commits).toBeGreaterThan(20)
    expect(stats.p95).toBeLessThanOrEqual(profile.cpu > 1 ? 120 : 50)
    expect(stats.maxPending).toBeLessThanOrEqual(profile.cpu > 1 ? 300 : 200)
    expect(stats.maxFrame).toBeLessThanOrEqual(profile.cpu > 1 ? 300 : 200)
    expect(stats.sameRoot, 'message_end keeps the mounted Markdown root').toBe(true)
    expect(stats.markers).toBe(stats.expected)
    await page.screenshot({ path: info.outputPath('stream-completed.png') })
  })
}
