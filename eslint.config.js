// Workspace-wide ESLint flat config; the rules live in @kapsora/config so every package
// and app lints the same way.
import kapsora from '@kapsora/config/eslint';

export default [
  {
    ignores: [
      '**/node_modules/**',
      '**/dist/**',
      '**/.vite/**',
      '**/generated/**',
      '**/public/mockServiceWorker.js',
      'playwright-report/**',
      'test-results/**',
    ],
  },
  ...kapsora,
  {
    files: ['tests/load/*.js'],
    languageOptions: { globals: { __ENV: 'readonly', open: 'readonly' } },
  },
];
