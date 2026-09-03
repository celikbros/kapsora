# KAPSORA web workspace

pnpm workspace (WP-I1-05). Packages under `packages/`, apps under `apps/`.

| Package / app         | Purpose                                                                                                                                          |
| --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| `@kapsora/config`     | Shared tsconfig, ESLint rules, Prettier, Tailwind v4 theme tokens, Vitest setup                                                                  |
| `@kapsora/api-client` | Types generated from `api/openapi/kapsora-v1.yaml`, openapi-fetch client with KAPSORA headers, problem+json parsing, VKN/TCKN helpers, MSW mocks |
| `@kapsora/auth`       | In-memory session store (CSRF token never touches storage), route guards, tenant colour                                                          |
| `@kapsora/i18n`       | i18next (tr complete, en skeleton), date/money formatting                                                                                        |
| `@kapsora/ui`         | Design system on Radix primitives + Tailwind tokens                                                                                              |
| `@kapsora/backoffice` | Desktop-first backoffice (port 5173)                                                                                                             |
| `@kapsora/provider`   | Provider portal shell (port 5174)                                                                                                                |
| `@kapsora/member`     | Member PWA shell, mobile-first (port 5175)                                                                                                       |

## Commands (repository root)

```sh
pnpm install            # Node 24, pnpm 10 (npm install -g pnpm if corepack needs admin)
pnpm generate           # regenerate TypeScript types from the contract (committed; CI checks drift)
pnpm dev                # backoffice with the mock API
VITE_API_MOCK=false VITE_API_BASE_URL=http://127.0.0.1:8080 pnpm dev   # real Go API via proxy
pnpm lint && pnpm typecheck && pnpm test && pnpm build
pnpm e2e                # Playwright smoke on the mock API
E2E_REAL_API=1 KAPSORA_API_URL=http://127.0.0.1:8080 pnpm e2e         # same screens on the real API
```

Mock users: `admin.a`, `reviewer.a`, `admin.b`, `both.ab` with any password of 12+ characters.
Real API demo users come from `go run ./cmd/seed demo` (see the repository README).

## Rules

- No token, tax number or personal data in `localStorage`/`sessionStorage` (ESLint forbids
  `setItem`; the flows test and the Playwright suite assert empty storage).
- Every problem+json is rendered by `ProblemAlert`; messages come from `problems.*` keys.
- Forms validate on the client for instant feedback; the server's 422 field errors are
  mapped back onto the same field paths and win.
- ETag/If-Match on every update; a 412 shows both versions and never overwrites silently.
