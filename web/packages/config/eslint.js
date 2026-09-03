// Shared ESLint rules: typescript-eslint recommended (type-aware where a tsconfig is
// present) plus the React hooks rules. Kept deliberately small; the compiler in strict
// mode does most of the work.
import js from '@eslint/js';
import reactHooks from 'eslint-plugin-react-hooks';
import globals from 'globals';
import tseslint from 'typescript-eslint';

export default tseslint.config(
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ['**/*.{ts,tsx}'],
    plugins: { 'react-hooks': reactHooks },
    languageOptions: {
      globals: { ...globals.browser, ...globals.node },
    },
    rules: {
      ...reactHooks.configs['recommended-latest'].rules,
      // TanStack Table's useReactTable returns non-memoisable functions by design.
      'react-hooks/incompatible-library': 'off',
      '@typescript-eslint/consistent-type-imports': ['error', { prefer: 'type-imports' }],
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_', caughtErrorsIgnorePattern: '^_' },
      ],
      '@typescript-eslint/no-explicit-any': 'error',
      // Personal data must never reach browser storage (WP-I1-05 section 4.7).
      'no-restricted-properties': [
        'error',
        {
          object: 'localStorage',
          property: 'setItem',
          message: 'Do not persist state in localStorage; PII and tokens stay in memory.',
        },
        {
          object: 'sessionStorage',
          property: 'setItem',
          message: 'Do not persist state in sessionStorage; PII and tokens stay in memory.',
        },
      ],
      'no-console': ['error', { allow: ['warn', 'error'] }],
    },
  },
  {
    files: ['**/*.js', '**/*.mjs', '**/*.cjs'],
    languageOptions: { globals: { ...globals.node } },
  },
);
