import { defineConfig } from '@playwright/test';

/** API-only, opt-in natural-calendar check. It never starts a web or API server. */
export default defineConfig({
  testDir: '.',
  testMatch: 'real-pc05-calendar.spec.ts',
  workers: 1,
  retries: 0,
  projects: [{ name: 'chromium' }],
  reporter: 'list',
  use: { trace: 'off', screenshot: 'off', video: 'off' },
});
