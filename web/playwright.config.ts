import { defineConfig } from '@playwright/test'

const live = process.env.KI_LIVE === '1'
const baseURL = process.env.KI_BASE_URL || (live ? 'http://127.0.0.1:19833' : 'http://127.0.0.1:19832')

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: 'list',
  use: {
    baseURL,
    browserName: 'chromium',
    headless: true,
    trace: 'retain-on-failure',
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
