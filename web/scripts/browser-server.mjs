import { chromium } from '@playwright/test'

const stopped = new Promise(resolve => {
  process.stdin.once('end', resolve)
  process.once('SIGTERM', resolve)
  process.once('SIGINT', resolve)
})
process.stdin.resume()
const browser = await chromium.launchServer({ host: '127.0.0.1', headless: true })
try {
  process.stdout.write(`${browser.wsEndpoint()}\n`)
  await stopped
} finally {
  await browser.close()
}
