# WP-I1-05 · Frontend foundation: pnpm workspace, three app shells, generated client, first screens

| Field | Value |
|---|---|
| Milestone | M1 (plan increment I1) |
| Size | L |
| Depends on | `api/openapi/kapsora-v1.yaml` only (mock the backend) |
| Runs in parallel with | all M1 packages |
| Migration numbers assigned | none |
| OpenAPI operations owned | none (report any contract problem you hit; do not edit the spec) |
| Read first | Handbook; v1.2 sections 15.1-15.6, 46; ADR-020 item 4 |

## 1. Goal

A pnpm monorepo with three React applications sharing a design system, generated API
client, auth and i18n packages, plus the first backoffice screens working against mocked
API responses. When WP-I1-01..03 are merged, the same screens must work against the real
API with only the mock switched off.

## 2. Stack (fixed)

React 19.2, TypeScript 5.9 strict, Vite 8, pnpm workspace, TanStack Router, TanStack
Query, TanStack Table, React Hook Form + Zod, Tailwind CSS + Radix primitives, Zustand
only for tiny global UI state, i18next (Turkish complete, English skeleton), Storybook
for the `ui` package, Vitest + Testing Library, Playwright smoke, MSW for mocks,
`openapi-typescript` + `openapi-fetch` for the client. No Redux, no other state or UI
libraries. Node 24, pnpm 10 (install pnpm with `npm install -g pnpm` into a user prefix
if `corepack` needs admin rights).

## 3. Layout

```text
pnpm-workspace.yaml, package.json (scripts: dev, build, lint, typecheck, test, e2e, generate)
web/
  packages/
    config/        shared tsconfig, eslint, prettier, tailwind preset
    ui/            design system components + Storybook (Button, Input, Select, Table, Dialog, Toast, Form fields, PageShell, EmptyState, ProblemAlert)
    api-client/    generated types (openapi-typescript) + openapi-fetch wrapper adding X-Tenant-ID, X-CSRF-Token, X-Request-ID, Idempotency-Key, problem+json parsing
    auth/          session hooks (useSession, useTenant), route guards, login redirect to /auth/login, tenant switch
    i18n/          tr + en resources, formatting helpers (dates in tenant time zone, money with ISO code)
  apps/
    backoffice/    desktop-first
    provider/      desktop-first, fast entry
    member/        mobile-first PWA shell
tests/e2e/         Playwright smoke (login redirect, tenant select, organization list)
```

## 4. Behaviour to deliver

1. **Session bootstrap:** on load call `GET /api/v1/session`; 401 → redirect to
   `/auth/login?returnTo=<current path>`; otherwise store CSRF token in memory (never
   localStorage) and load `GET /api/v1/me`.
2. **Tenant selection:** if the actor has more than one tenant and no active one, show the
   tenant picker; `POST /api/v1/session/switch-tenant`; the active tenant name and code
   are always visible in the header with a distinct color per tenant (hash of code) to
   reduce cross-tenant mistakes (v1.2 15.4).
3. **Backoffice shell:** navigation from v1.2 15.6 (all entries present; unimplemented ones
   route to an "Yakında" page), breadcrumb, user menu with logout (`POST
   /api/v1/session/logout`, then navigate to `/auth/logout`).
4. **"Profilim ve Yetkilerim"** page: actor, tenants, permissions and scopes from `/me`.
5. **Organizations:** list with server-side cursor pagination, filter by role, search;
   detail; create (form with identifiers, VKN/TCKN client-side checksum for instant
   feedback, server remains the authority); edit with ETag: on `412` show both versions
   and let the user reload, never overwrite silently (v1.2 15.5).
6. **Errors:** every problem+json is rendered by `ProblemAlert` using `code` for the
   Turkish message lookup and `traceId` for support; field errors map to form fields.
7. **Security defaults:** `Cache-Control: no-store` meta for authenticated pages, no PII in
   localStorage, CSP-compatible (no inline scripts), strict TypeScript.
8. **Provider and member apps:** shells only (routing, session bootstrap, tenant header,
   placeholder home) proving the shared packages work in all three.

## 5. Mocks

MSW handlers under `web/packages/api-client/src/mocks` covering all operations in the
contract with realistic Turkish synthetic data (no real names, tax numbers generated with a
valid checksum from random prefixes). `pnpm dev --filter backoffice` runs with mocks by
default; `VITE_API_MOCK=false` switches to the real API at `VITE_API_BASE_URL`.

## 6. Tests required

- Vitest: api-client header injection and problem parsing; auth guard redirects; tenant
  color hashing; organization form validation (VKN/TCKN); ETag conflict flow.
- Storybook stories for every `ui` component with light and dark themes.
- Playwright smoke against the mocked app: login redirect, tenant picker, organization
  list renders 50 rows and paginates.
- `pnpm typecheck`, `pnpm lint`, `pnpm test`, `pnpm build` all green for all packages.

## 7. Acceptance criteria

- [ ] `pnpm install && pnpm generate && pnpm build` from a clean checkout succeeds.
- [ ] Backoffice screens 1-6 work with mocks; switching `VITE_API_MOCK=false` changes only
      configuration.
- [ ] No token, TCKN or personal data stored in browser storage; verified by a test that
      inspects `localStorage`/`sessionStorage` after the flows.
- [ ] Turkish UI complete for delivered screens; English resource skeleton compiles.
- [ ] WCAG 2.2 AA basics: keyboard navigation, focus order, labels, contrast checked in
      Storybook a11y addon.
- [ ] Report lists every dependency with its version and why.
