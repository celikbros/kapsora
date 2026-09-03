/** Node entry for the mocks: Vitest and Playwright fixtures use it. */
import { setupServer } from 'msw/node';
import { createHandlers, MockApi, type MockOptions } from './handlers';

export function createMockServer(options: MockOptions = {}) {
  const api = new MockApi(options);
  const server = setupServer(...createHandlers(api));
  return { api, server };
}
