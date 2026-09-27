import { chromium } from '@playwright/test'

// Run the Playwright WebSocket server under the same Node runtime as its CLI.
// Bun's server transport can stall remote clients during fixture setup.
const stopped = new Promise(resolve => {
  process.stdin.once('end', resolve)
  process.once('SIGTERM', resolve)
  process.once('SIGINT', resolve)
})
process.stdin.resume()
const browser = await chromium.launchServer({ host: '127.0.0.1', headless: true })
try {
  process.stdout.write(`${browser.wsEndpoint()}\n`)
  // Closing the parent's pipe also cleans up after an interrupted runner.
  await stopped
} finally {
  await browser.close()
}
