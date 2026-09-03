// Shared jsdom test setup: DOM matchers and a clean document between tests.
import '@testing-library/jest-dom/vitest';
import { afterEach } from 'vitest';
import { cleanup } from '@testing-library/react';

// jsdom has no layout; the router's scroll restoration calls this on navigation.
window.scrollTo = () => undefined;

afterEach(() => {
  cleanup();
});
