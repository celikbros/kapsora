/**
 * Browser entry for the mocks. Apps call `startMockApi()` before rendering when
 * `VITE_API_MOCK` is not "false". The worker script lives in each app's public folder
 * (`pnpm exec msw init <app>/public`).
 */
import { setupWorker } from 'msw/browser';
import { createHandlers, MockApi, type MockOptions } from './handlers';

export async function startMockApi(options: MockOptions = {}): Promise<MockApi> {
  const api = new MockApi({ delayMs: 120, ...options });
  const worker = setupWorker(...createHandlers(api));
  await worker.start({
    onUnhandledRequest: 'bypass',
    quiet: true,
    serviceWorker: { url: '/mockServiceWorker.js' },
  });
  return api;
}
