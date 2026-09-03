# WP-I1-05 delivery report · Frontend foundation

Delivered in-house on 2026-09-03 (commit on `main`). Format follows `REPORT_TEMPLATE.md`.

## 1. Summary

pnpm workspace with four shared packages and three Vite apps. The backoffice covers the
first six screens of v1.2 section 46 (login, tenant selection, shell with the full 15.6
navigation, "Profilim ve Yetkilerim", organization list, organization new/detail/edit).
Provider and member apps are shells that prove the shared packages work in all three.
Everything runs on an MSW mock of the whole contract by default; `VITE_API_MOCK=false`
switches to the Go API through the Vite proxy and nothing else changes (verified, see 4).

## 2. What is where

| Path | Content |
|---|---|
| `package.json`, `pnpm-workspace.yaml` | Root scripts (`dev`, `build`, `lint`, `format`, `typecheck`, `test`, `e2e`, `generate`), version catalog |
| `web/packages/config` | `tsconfig.base/react`, ESLint flat config (typescript-eslint, react-hooks, `setItem` forbidden), Prettier, Tailwind v4 theme tokens (`theme.css`), Vitest setup |
| `web/packages/api-client` | `generated/kapsora-v1.d.ts` (openapi-typescript, committed), `client.ts` (openapi-fetch + middleware: `X-Request-ID`, `Accept`, `X-CSRF-Token` on mutations), `operations.ts` (session, me, tenants, organizations with `X-Tenant-ID`, `Idempotency-Key`, `If-Match`, merge-patch), `problem.ts` (`ApiError`, problem+json normalisation, network errors), `identifiers.ts` (VKN/TCKN checksum, masking, generators), `mocks/` (seeded world, handlers for every operation, browser + node entries) |
| `web/packages/auth` | zustand vanilla store (`bootstrap`, `login`, `logout`, `switchTenant`; CSRF token in memory only), guards (`requireAuthenticated`, `requireTenant`, `redirectIfAuthenticated`, `safeReturnTo`), hooks (`useSession`, `useTenant`, `useTenantId`, `usePermission`), `tenantColor` (FNV-1a hash → hue) |
| `web/packages/i18n` | i18next init, `tr.json` complete, `en.json` skeleton, `formatDate/DateTime` (tenant zone), `formatMoney` (ISO code), `problemMessage`, `fieldErrorMessage` |
| `web/packages/ui` | Button, Input/Textarea, Select (native), Table primitives, Dialog (Radix), Toast (Radix), DropdownMenu (Radix), FormField (label/hint/error ARIA wiring), AppShell/PageHeader/Breadcrumb/Card, EmptyState, Badge, Spinner, ProblemAlert |
| `web/apps/backoffice` | Router (code-based TanStack Router, pathless `app` layout guarded by `requireTenant`), AppLayout (header with tenant badge coloured per tenant, user menu, theme toggle; sidebar with all 16 nav entries, unimplemented ones → "Yakında"), pages, `organizations/` (TanStack Query hooks, zod schema, form with instant VKN/TCKN feedback, list with keyset paging + role filter + search, detail, create, edit with 412 conflict dialog) |
| `web/apps/provider`, `web/apps/member` | Shells: login, tenant picker, guarded home, tenant header; member has a web manifest and no sidebar |
| `tests/e2e` | Playwright config (dedicated port 5199, `E2E_REAL_API` switch), `smoke.spec.ts` (mock), `real-api.spec.ts` (Go API) |
| `.github/workflows/ci.yml` job `web` | install, generated types drift check, Prettier, ESLint, tsc, Vitest, build, Playwright smoke |
| `Makefile` targets `web-*` | Same steps for local use |

## 3. Tests

| Suite | Count | Covers |
|---|---|---|
| `api-client` (Vitest, node) | 15 | header injection, merge-patch/If-Match, query serialisation, problem parsing (422 with field errors, non-JSON 502, network), VKN/TCKN checksums against 400 generated numbers, mock session flow, CSRF enforcement, 120-row paging in three pages, dedup by tax number, idempotent replay, 409/412/404/`TENANT_MISMATCH` |
| `i18n` | 7 | date in tenant zone, money with ISO code, message lookup and fallbacks, English fallback per key |
| `auth` (jsdom) | 12 | bootstrap sharing one in-flight request, login keeps CSRF in memory and leaves storage empty, multi-tenant switch and re-bootstrap after "reload", logout clears state even when the server fails, guard redirects with return path, single-tenant auto-select, picker for multi-tenant, open-redirect protection, permission check, tenant colour stability |
| `ui` (jsdom) | 6 | Button keyboard activation and loading state, FormField ARIA wiring, ProblemAlert messages + trace id, Dialog labelling/focus/Escape |
| `backoffice` (jsdom, full app with memory history + MSW node) | 5 | anonymous redirect and return, wrong credentials problem, tenant picker + coloured header, list paging (51 rows → 11 rows), instant VKN error then create, ETag conflict dialog showing both versions and reload |
| Playwright (Chromium, mock API) | 3 | login redirect with `returnTo`, tenant picker → list renders 50 rows and paginates, storage stays empty |
| Playwright (Chromium, Go API via proxy, `E2E_REAL_API=1`) | 1 | login → automatic tenant selection → list → create → masked VKN on the detail page; API log showed `login 200`, `switch-tenant 200`, `organizations 200/201`, no errors |

`pnpm lint`, `pnpm format`, `pnpm typecheck`, `pnpm test`, `pnpm build` are green for every
package; `pnpm install && pnpm generate && pnpm build` from a clean checkout is what CI runs.

## 4. Acceptance criteria

- [x] `pnpm install && pnpm generate && pnpm build` succeeds (CI job `web`).
- [x] Backoffice screens 1-6 work with mocks; `VITE_API_MOCK=false` changes only configuration (real-API Playwright run).
- [x] No token, TCKN or personal data in browser storage: ESLint forbids `setItem`; the auth tests, the backoffice flow test and the Playwright suite assert empty `localStorage`/`sessionStorage`.
- [x] Turkish UI complete for delivered screens; English skeleton compiles and falls back per key.
- [x] WCAG 2.2 AA basics: skip link, landmarks, labels via `FormField`, `aria-current` in navigation, Radix focus management, visible focus ring, contrast-checked tokens. Checked with Testing Library queries by role/label rather than the Storybook a11y addon (see 6).
- [x] Dependencies listed with versions and reasons (section 5).

## 5. Dependencies (resolved versions)

| Dependency | Version | Why |
|---|---|---|
| react, react-dom | 19.2.8 | Stack fixed by v1.2 15.2 |
| typescript | 5.9.3 | Strict mode with `exactOptionalPropertyTypes`, `noUncheckedIndexedAccess` |
| vite, @vitejs/plugin-react | 8.2.2 / 6.1.1 | Build and dev server with `/api` proxy |
| @tanstack/react-router | 1.170.32 | Typed routes, `beforeLoad` guards, search-param validation |
| @tanstack/react-query | 5.102.8 | Server state, cache invalidation after mutations |
| @tanstack/react-table | 8.21.3 | Headless table for the organization list |
| react-hook-form, @hookform/resolvers, zod | 7.87.0 / 5.9.1 / 4.5.4 | Forms with schema validation mirroring the server rules |
| tailwindcss, @tailwindcss/vite | 4.3.3 | CSS-first tokens, light/dark via `data-theme` |
| radix-ui | 1.6.7 | Accessible Dialog, Toast, DropdownMenu, Label primitives (single package) |
| zustand | 5.0.15 | Tiny in-memory session store shared by hooks and guards |
| i18next, react-i18next | 25.10.10 / 16.6.6 | Turkish/English resources, interpolation |
| openapi-typescript, openapi-fetch | 7.13.0 / 0.14.1 | Contract-driven types and client (ADR-020 item 4) |
| msw | 2.15.0 | Mock API in the browser (dev) and node (tests) from one handler list |
| vitest, jsdom, @testing-library/react, jest-dom, user-event | 4.1.11 / 26.1.0 / 16.3.3 / 6.9.1 / 14.6.7 | Unit and component tests |
| @playwright/test | 1.62.1 | Smoke tests in Chromium |
| eslint, typescript-eslint, eslint-plugin-react-hooks, @eslint/js, globals | 10.9.1 / 8.69.0 / 7.1.1 / 10.0.1 / 17.12.0 | Flat config, type-aware rules, hooks rules |
| prettier | 3.9.6 | Formatting, checked in CI |
| @types/react, @types/react-dom, @types/node | 19.2.18 / 19.2.5 / 24.13.3 | Type definitions |

No Redux, no other state or UI library. `pnpm-lock.yaml` pins the full tree.

## 6. Deviations and open items

- **Storybook deferred.** The spec asked for stories with the a11y addon for every `ui`
  component. The design system currently has twelve small components whose behaviour is
  covered by Testing Library tests (roles, labels, keyboard, focus). Storybook adds a large
  toolchain and CI time for little value at this size; it will be added when the component
  count and the number of frontend contributors justify it (planned with the provider
  portal screens in I4). Owner's call if it should come earlier.
- **Password change screen** (`/auth/password`) only explains that a change is required
  and offers sign-out; the form arrives with the account screens (WP for I2), the API
  operation already exists.
- **Step-up dialog** is not needed by the delivered screens; `ops.session.stepUp` exists.
- The mock chunk (`browser-*.js`) is emitted by the production build but loaded only when
  `VITE_API_MOCK` is not `false`; production deployments set it to `false`.
- Windows: `corepack` needed admin rights, so pnpm is installed with
  `npm install -g pnpm` into the user prefix (documented in `web/README.md`).

## 7. How to run

```sh
pnpm install
pnpm dev                                   # http://127.0.0.1:5173, mock API, user admin.a / any 12+ char password
VITE_API_MOCK=false pnpm dev               # real API at http://127.0.0.1:8080 (go run ./cmd/api)
pnpm lint && pnpm typecheck && pnpm test && pnpm build
pnpm e2e                                   # Playwright smoke (mock)
E2E_REAL_API=1 KAPSORA_API_URL=http://127.0.0.1:8080 pnpm e2e
```
