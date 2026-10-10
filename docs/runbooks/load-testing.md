# Load testing

This is the tooling slice of [WP-I10-02](../delegation/WP-I10-02-load-performance.md).
It runs against an operator-started Go API. It does not start application services.
Use synthetic accounts and a test database: cancelled requests/bookings, staged rows and
audit entries remain as evidence after a run.

## Local preparation

From the repository root, with the native demo already seeded and running:

```powershell
pnpm load:install
$env:KAPSORA_LOAD_BASE_URL = 'http://127.0.0.1:8090'
$env:KAPSORA_LOAD_PASSWORD = '<local synthetic demo password>'
pnpm load:prepare
pnpm load:test
```

The installer pins k6 **2.2.0**, verifies the official archive's SHA-256 and extracts it
under ignored `tools/k6`; Windows/Linux amd64 are supported. It adds no application
dependency. `KAPSORA_K6_BIN` can point to an existing binary of that exact version.

The preparer accepts only a local origin and the seeded `DEMO_A` tenant. It discovers
the configured demo hospital/sponsor, physiotherapy entitlement and a priced hotel room
for one night, fourteen days ahead. It writes only synthetic account names and resource
ids to ignored `test-results/load/fixture.json`; passwords and sessions stay in memory.
If inventory, eligibility or seed data is unsuitable, preparation stops with a clear error.

## Smoke workflows

The default `pnpm load:smoke` selects all five workflows. Each runs once with one virtual
user, staggered by three seconds, against the real API:

| Workflow | Action measured | Completion and cleanup |
| --- | --- | --- |
| read | Organization collection GET | A collection is returned |
| write | Create a service-request draft | Cancel with the response ETag; verify CANCELLED |
| eligibility | Check the configured person/service | A positive result and stored evaluation id |
| hold | Reserve one room/night | Release; verify CANCELLED and restore room/entitlement snapshot |
| import | Stage a small CSV_V1 file | Wait for validation, reconcile valid row count, cancel with a fresh ETag |

Import measures **staging and validation**, not apply or a customer HR/policy adapter.
It requires `import.execute` and a password step-up. PROGRAM_MANAGER now supplies that
permission; demo `admin.a` holds this role in addition to TENANT_ADMIN. Apply migration
000049 for existing system roles, or provision new tenants with the updated template.
The harness itself never changes grants. A PostgreSQL-backed HTTP integration test also
covers applying a staged member and worker redelivery; this is separate from k6 staging.

For a deliberately reduced four-workflow smoke:

```powershell
$env:KAPSORA_LOAD_WORKLOADS = 'read,write,eligibility,hold'
pnpm load:smoke
Remove-Item Env:KAPSORA_LOAD_WORKLOADS
```

The report then says `partial_functional_smoke` and lists `import` as omitted. An omitted
workflow never counts as passed. With the migrated demo and running native services,
leave KAPSORA_LOAD_WORKLOADS unset to run the default five-workflow smoke.
All configured accounts use the test password supplied in `KAPSORA_LOAD_PASSWORD`.

Setup logs in each distinct account context, checks its required grants and records the
baseline before workload mutations start. Cookies, CSRF, app context and idempotency keys
are real. Password step-up is renewed before expiry, rather than omitted for benchmarking.
Cleanup failure stops load generation. Teardown compares server balances without floating
point arithmetic and ignores object-key order and harmless decimal scale differences.

## Designated load environment

`pnpm load:run` refuses a fixture whose environment kind is not `dedicated-load` and refuses
to omit a workflow. Name the test target, hardware/resources, dataset size, account pools
and workload mix before execution. Full-load execution has not been authorized for the
current local environment and has not been performed.

The fixture's `profile` uses the shape of
[`tests/load/profile.example.json`](../../tests/load/profile.example.json). Each rate means
**workflows per second**, not HTTP requests: draft cleanup, hold release and import polling
add requests. The example is a starting mix, not a validated capacity profile. Each scenario
ramps for two minutes, then holds its rate for ten minutes. `maxVUs` bounds allocation;
dropped iterations fail the run instead of silently shrinking the requested workload.

Only requests started during the ten-minute measurement window count toward the 300 RPS
target (180,000 measured API calls). Successful primary-operation latency thresholds are
read p95 <300 ms, draft write <700 ms, eligibility <1.5 s and hold <1 s. Expected hold
contention refusals are counted separately; the ordinary profile also requires >95% hold
success. Refusals are not fast successful samples. Unexpected HTTP responses, failed
business assertions, incomplete workflows, cleanup or reconciliation failures fail the run.

Use enough **distinct seeded cases and accounts** for the intended contention model. One
member repeatedly trying to hold the same room is not evidence of 5,000 member sessions.
The report states the actual number of authenticated account contexts. It never declares
full-capacity acceptance, even when its thresholds pass.

## Evidence

Aggregate JSON goes to `test-results/load/<mode>-summary.json` by default. An alternative
`KAPSORA_LOAD_OUTPUT` must remain a JSON file beneath `test-results/load`. The runner removes
the previous artifact before a new run and stamps the result with the process exit code.
It records mode, included/omitted workflows, scenario configuration, account/case counts,
revision, dirty-tree state and hashes of the exact workload/fixture inputs. The artifact
contains aggregate timings/counts and fixed operation names; no request/response bodies,
headers, cookies, personal records or resource URLs. HTTP debug/raw external outputs and
environment overrides of the workload schedule are refused by the runner.

Keep raw evidence ignored. Put a concise conclusion and commands in the work-package/PR
report. A one-iteration smoke proves wiring and cleanup; its p95 is not performance evidence.

To close WP-I10-02, still provide:

- Full import staging smoke with the authorized role, and apply/adapter workload once I10-01
  supplies its source contract; measure the 100,000-row/day requirement explicitly.
- Verified 1,000,000-beneficiary /10,000,000-history data and the 5,000 member /200 staff
  session model, actual target sizing and a completed measured load run.
- Pool/lock/resource evidence, scan backlog, notification enqueue and outbox-age observations;
  the HTTP client does not prove those server-side SLOs.
- Baseline, measured bottlenecks, targeted corrections and final reconciliation/results.

## Sources

Targets come from the [frozen baseline](../baseline-v1.2/KAPSORA_Teknik_Proje_Dokumani_v1.2.md)
§§30.4 and 32.1. The runner follows the official k6 guidance for
[arrival-rate workloads](https://grafana.com/docs/k6/latest/using-k6/scenarios/concepts/arrival-rate-vu-allocation/),
[response classification](https://grafana.com/docs/k6/latest/javascript-api/k6-http/set-response-callback/)
and [aggregate summaries](https://grafana.com/docs/k6/latest/results-output/end-of-test/custom-summary/).
