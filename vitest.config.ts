import { defineConfig } from 'vitest/config';

// One Vitest run for the whole workspace. Packages with React components run in jsdom;
// pure TypeScript packages run in node. Each project keeps its own setup file.
export default defineConfig({
  test: {
    projects: [
      {
        test: {
          name: 'api-client',
          root: 'web/packages/api-client',
          environment: 'node',
          include: ['src/**/*.test.ts'],
        },
      },
      {
        test: {
          name: 'i18n',
          root: 'web/packages/i18n',
          environment: 'node',
          include: ['src/**/*.test.ts'],
        },
      },
      {
        test: {
          name: 'auth',
          root: 'web/packages/auth',
          environment: 'jsdom',
          include: ['src/**/*.test.{ts,tsx}'],
          setupFiles: ['../config/vitest.setup.ts'],
        },
      },
      {
        test: {
          name: 'ui',
          root: 'web/packages/ui',
          environment: 'jsdom',
          include: ['src/**/*.test.{ts,tsx}'],
          setupFiles: ['../config/vitest.setup.ts'],
        },
      },
      {
        test: {
          name: 'backoffice',
          root: 'web/apps/backoffice',
          environment: 'jsdom',
          include: ['src/**/*.test.{ts,tsx}'],
          setupFiles: ['../../packages/config/vitest.setup.ts'],
        },
      },
      {
        test: {
          name: 'provider',
          root: 'web/apps/provider',
          environment: 'jsdom',
          include: ['src/**/*.test.{ts,tsx}'],
          setupFiles: ['../../packages/config/vitest.setup.ts'],
        },
      },
    ],
  },
});
