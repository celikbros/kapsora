# WP-I1-04 · Platform services: audit recorder, outbox dispatcher, idempotency middleware, rate limit, scheduler jobs, keygen

| Field | Value |
|---|---|
| Milestone | M1 (plan increment I1) |
| Size | L |
| Depends on | Ports in `main`: `internal/audit`, `internal/identity`, `internal/platform/{db,httpx,crypto}` |
| Runs in parallel with | WP-I1-01, 02, 03, 05, 06 |
| Migration numbers assigned | `000011_system_rate_limit.up.sql` (and `000012` if you need a second one; say so in the report) |
| OpenAPI operations owned | none (you own the `TooManyRequests` and idempotency error semantics) |
| Read first | Handbook; v1.2 sections 11.13, 14.6, 17.5, 21.1-21.3, 23.1-23.3; ADR-009, ADR-015; migration 000007 |

## 1. Goal

The cross-cutting machinery every module relies on: audit rows written inside business
transactions, reliable at-least-once event dispatch from the transactional outbox,
idempotent command handling, rate limiting, the scheduler job registry, and a key
generator for local setups.

## 2. Scope

### 2.1 Audit recorder (`internal/audit/postgres`)

- Implement `audit.Recorder`: `Record` inserts into `audit.event`, `RecordAccess` into
  `audit.access_event`, both through the caller's `pgx.Tx`.
- Fill `request_id`, `trace_id`, `source_ip`, `user_agent_hash` (sha256 of the UA string)
  from context/request. Add a small helper `audit.WithRequestMeta(ctx, meta)` +
  `audit.RequestMetaFrom(ctx)` in the `audit` package and a chi middleware in
  `internal/audit/transport/http` that sets it from the request.
- Detail allow-list: keep only keys matching `^[a-z][a-z0-9_]*$` whose values are
  strings up to 200 chars, numbers, booleans or UUIDs; drop everything else; never nest.
  Keys `tckn`, `vkn`, `identifier`, `name`, `email`, `token`, `secret`, `password`
  (case-insensitive) are always dropped. Test this.
- Audit writes must never abort the business transaction because of validation of the
  detail map (sanitize instead); a database failure does abort it (audit is mandatory).

### 2.2 Outbox dispatcher (`internal/platform/outbox`)

- `Publisher.Publish(ctx, tx, event)` inserts into `system.outbox_event` inside the
  business transaction (`event_type` pattern `module.aggregate.action`, schema version,
  payload JSON, optional deduplication key).
- `Dispatcher` for `cmd/worker`: loop with `SELECT ... FOR UPDATE SKIP LOCKED LIMIT 100`
  on `PENDING`/`FAILED` where `available_at <= now()`, ordered by `occurred_at`; marks
  `PROCESSING` with `locked_by` = worker instance id; calls the registered handler; on
  success `SUCCEEDED` + `processed_at`; on failure classify (`Transient`, `RateLimited`,
  `Permanent`, `Security`) via a returned error type, exponential backoff with jitter
  (base 5s, cap 1h), max 10 attempts then `DEAD_LETTER`; `Permanent` and `Security` go to
  dead-letter immediately. Handlers are idempotent by contract; pass the event id.
- Handler registry: `dispatcher.Handle("audit.export.requested", fn)`; unknown event types
  stay `PENDING` and are logged once per type per hour (not every tick).
- Metrics hooks: expose counts through an interface so OpenTelemetry can be attached in
  a later WP; for now log a backlog summary every minute (replace the placeholder in
  `cmd/worker/main.go`).
- Stale lock recovery: rows `PROCESSING` older than 10 minutes go back to `PENDING`.

### 2.3 Idempotency middleware (`internal/platform/httpx/idempotency.go`)

- Applied by the integrator to command routes; constructor
  `Idempotent(pool, commandCode string, opts)`.
- Requires `Idempotency-Key` (16-128 chars) unless `opts.Optional`; missing →
  `400 IDEMPOTENCY_KEY_REQUIRED`.
- Request hash: sha256 of method + path + canonical JSON body (sorted keys) + `X-Tenant-ID`.
- Flow inside `db.WithTenantTx` using the `RequestContext` tenant/actor: insert
  `system.idempotency_record` IN_PROGRESS; on unique violation read the row: same hash and
  SUCCEEDED/FAILED → replay stored status/body/headers (`ETag`, `Location`) with header
  `Idempotent-Replayed: true`; same hash and IN_PROGRESS → `409 IDEMPOTENCY_IN_PROGRESS`
  with `Retry-After: 2`; different hash → `409 IDEMPOTENCY_KEY_REUSED`.
- After the handler runs, store status, body (JSON only, max 256 KB; larger bodies store
  resource id only and replay a `303` to it), completed_at.
- Purge job deletes rows with `expires_at < now()`.

### 2.4 Rate limiting (`internal/platform/ratelimit`)

- Interface `Limiter.Allow(ctx, key string, policy Policy) (Decision, error)` with token
  bucket semantics (`Rate` per minute, `Burst`).
- Two implementations: in-memory (single process, tests) and PostgreSQL
  (`system.rate_limit_bucket`, migration 000011: `key text PK, tokens numeric, updated_at
  timestamptz`) updated atomically in one statement; the DB implementation is what
  production uses (ADR-021: no Valkey).
- Middleware `httpx.RateLimit(limiter, keyFn, policy)`: key = tenant + actor (or client
  IP when unauthenticated) + route pattern; on deny `429` problem `RATE_LIMITED` with
  `Retry-After`. Default browser policy 120/min burst 60 (v1.2 17.5); make policies
  configurable per route by the integrator.
- Sensitive identifiers must never be part of a key; add a test that the key function
  ignores query strings and bodies.

### 2.5 Scheduler job registry (`internal/platform/scheduler`)

- `Registry.Register(Job{Code, Every, Run func(ctx) error})`; the leader loop in
  `cmd/scheduler` runs due jobs sequentially, records `system.job_run` (migration 000011
  may add this table: `id, job_code, scheduled_for, started_at, finished_at, status,
  error_code, metrics_json`, unique `(job_code, scheduled_for)`), and skips a job whose
  previous run is still running.
- Jobs delivered in this WP: `session.cleanup` (calls WP-I1-01's `DeleteExpired`; until
  it lands, implement against the `identity.SessionStore` interface with a new method
  `DeleteExpired(ctx, before time.Time) (int64, error)` — add it to the interface and
  tell the integrator), `idempotency.purge`, `audit.ensure_partitions` (calls
  `audit.ensure_month_partition` for next month for both audit tables, daily),
  `outbox.recover_stale`, `ratelimit.purge` (buckets idle > 1 day).

### 2.6 Key generator (`cmd/keygen`)

Prints fresh hex keys for `KAPSORA_LOCAL_MASTER_KEY` and `KAPSORA_COOKIE_SIGNING_KEY`
(`localkey.GenerateMasterKey`), nothing else.

## 3. Tests required

- Audit: allow-list sanitization; rows written in the same transaction roll back with it;
  `dbtest` insert as the application role.
- Outbox: `dbtest` with two concurrent dispatchers (goroutines) processing 500 events
  exactly once; backoff schedule; dead-letter after max attempts; stale lock recovery;
  unknown type left pending.
- Idempotency: replay returns identical body and headers; concurrent duplicate returns
  409 IN_PROGRESS; different payload 409 REUSED; hash ignores key order in JSON.
- Rate limit: bucket math with fake clock; DB implementation under 50 concurrent callers
  never exceeds burst; middleware 429 headers.
- Scheduler: job registry runs due jobs, records `job_run`, no overlapping runs.

## 4. Acceptance criteria

- [ ] `cmd/worker` dispatches a test event end to end (publish in a transaction, worker
      picks it up, handler runs once) in a `dbtest`-based test.
- [ ] Idempotency middleware satisfies v1.2 11.13 and 14.6 word for word.
- [ ] Rate limiting works with only PostgreSQL available.
- [ ] Scheduler creates next month's audit partitions when run.
- [ ] `make ci` and `make test-db` green; no new dependency beyond the standard library
      and what is already in `go.mod` (justify anything else).
