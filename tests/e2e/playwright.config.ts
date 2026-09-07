import { defineConfig, devices } from '@playwright/test';

// Smoke tests run against the backoffice dev server with the mock API (no backend).
// A dedicated port so a developer's own dev server on 5173 does not collide.
const PORT = 5199;
const PROVIDER_PORT = 5198;
const MEMBER_PORT = 5197;
// E2E_REAL_API=1 turns the mock worker off and proxies /api to KAPSORA_API_URL.
const REAL = process.env['E2E_REAL_API'] === '1';
const API_URL = process.env['KAPSORA_API_URL'] ?? 'http://127.0.0.1:8080';

export default defineConfig({
  testDir: '.',
  timeout: 60_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  workers: 1,
  retries: process.env['CI'] ? 1 : 0,
  reporter: process.env['CI'] ? [['github'], ['list']] : 'list',
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: 'retain-on-failure',
    locale: 'tr-TR',
    timezoneId: 'Europe/Istanbul',
  },
  projects: [
    {
      name: 'chromium',
      testIgnore: /(provider[a-z-]*|member[a-z-]*|review-capture)\.spec\.ts$/,
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'provider',
      testMatch: /(provider[a-z-]*|review-capture)\.spec\.ts$/,
      use: { ...devices['Desktop Chrome'], baseURL: `http://127.0.0.1:${PROVIDER_PORT}` },
    },
    {
      // The member PWA is phone-first: its flows run at a phone viewport.
      name: 'member',
      testMatch: /member[a-z-]*\.spec\.ts$/,
      use: {
        ...devices['Desktop Chrome'],
        viewport: { width: 390, height: 844 },
        baseURL: `http://127.0.0.1:${MEMBER_PORT}`,
      },
    },
  ],
  webServer: [
    {
      command: `pnpm --filter @kapsora/backoffice exec vite --port ${PORT} --strictPort --host 127.0.0.1`,
      url: `http://127.0.0.1:${PORT}/auth/login`,
      reuseExistingServer: !process.env['CI'],
      timeout: 120_000,
      env: REAL
        ? { VITE_API_MOCK: 'false', VITE_API_BASE_URL: API_URL }
        : { VITE_API_MOCK: 'true' },
    },
    {
      command: `pnpm --filter @kapsora/provider exec vite --port ${PROVIDER_PORT} --strictPort --host 127.0.0.1`,
      url: `http://127.0.0.1:${PROVIDER_PORT}/auth/login`,
      reuseExistingServer: !process.env['CI'],
      timeout: 120_000,
      env: REAL
        ? { VITE_API_MOCK: 'false', VITE_API_BASE_URL: API_URL }
        : { VITE_API_MOCK: 'true' },
    },
    {
      command: `pnpm --filter @kapsora/member exec vite --port ${MEMBER_PORT} --strictPort --host 127.0.0.1`,
      url: `http://127.0.0.1:${MEMBER_PORT}/auth/login`,
      reuseExistingServer: !process.env['CI'],
      timeout: 120_000,
      env: REAL
        ? { VITE_API_MOCK: 'false', VITE_API_BASE_URL: API_URL }
        : { VITE_API_MOCK: 'true' },
    },
  ],
});
