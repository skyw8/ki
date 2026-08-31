import { defineConfig } from '@playwright/test'
import { createServer } from 'node:net'
import { join } from 'node:path'
import { baseURLForAddress, runID, storageStatePath } from './e2e/run-state.ts'

async function availablePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer()
    server.unref()
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      if (!address || typeof address === 'string') {
        server.close()
        reject(new Error('Playwright could not allocate a loopback port'))
        return
      }
      server.close(error => error ? reject(error) : resolve(address.port))
    })
  })
}

let baseURL = process.env.KI_BASE_URL?.trim()
if (!baseURL) {
  let address = process.env.KI_SERVE_ADDR?.trim()
  if (!address) {
    address = `127.0.0.1:${await availablePort()}`
    process.env.KI_SERVE_ADDR = address
  }
  baseURL = baseURLForAddress(address)
  process.env.KI_BASE_URL = baseURL
}

export default defineConfig({
  testDir: './e2e',
  outputDir: join('test-results', 'runs', runID),
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: 'list',
  use: {
    baseURL,
    browserName: 'chromium',
    locale: 'zh-CN',
    headless: true,
    trace: 'retain-on-failure',
    storageState: storageStatePath,
  },
  globalSetup: './e2e/global-setup.ts',
  globalTeardown: './e2e/global-teardown.ts',
  projects: [
    {
      name: 'fake',
      testMatch: '**/*.spec.ts',
      testIgnore: ['**/*.live.spec.ts', '**/shots.spec.ts'],
      timeout: 30_000,
      expect: { timeout: 15_000 },
    },
    {
      name: 'live',
      testMatch: '**/*.live.spec.ts',
      timeout: 180_000,
      expect: { timeout: 120_000 },
    },
    {
      name: 'shots',
      testMatch: '**/shots.spec.ts',
      timeout: 60_000,
    },
  ],
})
