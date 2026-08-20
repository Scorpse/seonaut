# Campanix SEOnaut API Fork Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve SEOnaut's UI and crawler while adding a tenant-safe `/api/v1` for Kilnbench provisioning, crawl execution, findings, and exports.

**Architecture:** Mount an independent standard-library HTTP handler beside the existing HTML routes. The API layer receives immutable principals from dedicated authentication middleware and calls owner-scoped repository interfaces; handlers never accept a tenant or upstream user ID as authority. Fork-owned MySQL migrations add opaque external IDs, keys, idempotency, crawl state, export jobs, and audit records without altering upstream tables.

**Tech Stack:** Go 1.25, `net/http`, MySQL, `golang-migrate`, Argon2id, existing SEOnaut repositories/services, Docker BuildKit.

**Spec:** `N:/wrk/campanix/docs/seonaut-api-fork-design.md`

## Global Constraints

- Root credentials manage platform keys only and receive `403 scope_forbidden` from every tenant-data route.
- Platform credentials provision tenants and tenant keys but cannot read tenant projects, crawls, findings, or exports.
- Every tenant-data query applies the authenticated tenant owner predicate in SQL; foreign IDs return `404`.
- Mutating provisioning and crawl-start requests require `Idempotency-Key`; payload reuse with different normalized data returns `409`.
- API keys are revealed once, stored only as Argon2id hashes, and never logged.
- Existing HTML routes and upstream tests remain green.
- The fork image records exact fork and upstream revisions and retains a pinned upstream rollback image.

---

### Task 1: API server, health, metadata, and provenance

**Files:**
- Create: `internal/api/server.go`
- Create: `internal/api/server_test.go`
- Create: `internal/buildinfo/buildinfo.go`
- Modify: `internal/routes/app.go`
- Modify: `internal/services/container.go`
- Modify: `Dockerfile`

**Interfaces:**
- Produces: `api.NewHandler(Dependencies) http.Handler`, `buildinfo.Info`, `/api/v1/health`, `/api/v1/meta`.

- [x] Write an `httptest` table proving health returns readiness only, metadata requires a non-root `meta:read` principal, and all responses echo/create `X-Request-ID`.
- [x] Run `go test ./internal/api` and verify failure because `api.NewHandler` does not exist.
- [x] Implement the minimal handler, JSON error envelope, request-ID middleware, and build-time provenance variables.
- [x] Mount the handler on the existing mux without changing HTML route behavior; add OCI labels and `-ldflags` build arguments.
- [x] Run `go test ./internal/api ./internal/routes ./...`, `go vet ./...`, and commit `MKT-281 add API health and provenance boundary`.

### Task 2: Key schema, hashing, principals, and root/platform routes

**Files:**
- Create: `migrations/0076_campanix_api_keys.up.sql`
- Create: `migrations/0076_campanix_api_keys.down.sql`
- Create: `internal/api/auth.go`
- Create: `internal/api/auth_test.go`
- Create: `internal/repository/api_keys.go`
- Create: `internal/repository/api_keys_test.go`

**Interfaces:**
- Produces: `api.Principal{Kind, KeyID, TenantID, ProjectID, Scopes}`, `Authenticate(context.Context,string)`, one-time key issuance and rotation/revocation.

- [x] Write table tests for malformed, expired, revoked, wrong-hash, root, platform, tenant, and project-bound read-only keys; explicitly prove root cannot satisfy tenant middleware.
- [x] Run the focused tests and verify missing-symbol failures.
- [x] Add `api_keys` and `api_audit_log` migrations with public ID, Argon2id hash, class, bindings, scopes, expiry, revocation, and rotation metadata.
- [x] Implement key parsing/generation/hash verification and repository methods with parameterized SQL.
- [x] Add root-only create/list/rotate/revoke platform-key routes and scope middleware.
- [x] Run focused tests, full `go test -race ./...`, and commit `MKT-281 add scoped API credential lifecycle`.

### Task 3: Tenant provisioning and tenant/read-only key lifecycle

**Files:**
- Create: `migrations/0077_campanix_api_tenants.up.sql`
- Create: `migrations/0077_campanix_api_tenants.down.sql`
- Create: `internal/api/tenants.go`
- Create: `internal/api/tenants_test.go`
- Create: `internal/repository/api_tenants.go`

**Interfaces:**
- Consumes: platform and tenant principals from Task 2.
- Produces: idempotent `PUT /api/v1/tenants/{external_tenant_id}` and tenant/read-only key issue/list/rotate/revoke.

- [x] Write tests proving repeated provisioning returns one binding, generated service users cannot log into the UI, platform keys cannot list tenant data, and tenant issuers cannot cross tenant/project bounds.
- [x] Verify red tests.
- [x] Add tenant-to-upstream-user binding persistence and an internal service-user creation path reusing upstream password hashing.
- [x] Implement handlers and repository methods; never accept owner IDs in request bodies.
- [x] Run focused and race tests, then commit `MKT-281 add tenant provisioning and delegated keys`.

### Task 4: Owner-scoped project API and idempotency

**Files:**
- Create: `migrations/0078_campanix_api_projects_idempotency.up.sql`
- Create: `migrations/0078_campanix_api_projects_idempotency.down.sql`
- Create: `internal/api/projects.go`
- Create: `internal/api/projects_test.go`
- Create: `internal/repository/api_projects.go`
- Create: `internal/repository/api_idempotency.go`

**Interfaces:**
- Produces: `PUT/GET/PATCH /api/v1/projects`, normalized request hashing, tenant-scoped opaque project IDs.

- [x] Write two-tenant tests for lists, direct IDs, project-bound read-only keys, idempotent replay, conflicting payload reuse, unknown fields, and forbidden schemes.
- [x] Verify red tests.
- [x] Add bindings and idempotency tables, then owner-predicate repository queries.
- [x] Reuse `ProjectService` validation through a transport-neutral save method that returns errors instead of logging them away.
- [x] Run focused, repository integration, and full race tests; commit `MKT-281 add tenant-scoped project provisioning`.

### Task 5: Durable asynchronous crawl lifecycle

**Files:**
- Create: `migrations/0079_campanix_api_crawls.up.sql`
- Create: `migrations/0079_campanix_api_crawls.down.sql`
- Create: `internal/api/crawls.go`
- Create: `internal/api/crawls_test.go`
- Modify: `internal/services/crawler.go`
- Modify: `internal/repository/crawl.go`

**Interfaces:**
- Produces: start/list/status/cancel routes and durable `queued|running|succeeded|failed|canceled` records.

- [x] Write tests for one active crawl, replay by idempotency key, cancel idempotency, foreign IDs as `404`, terminal-state monotonicity, and startup conversion to `worker_restarted` without deleting partial crawl data.
- [x] Verify red tests.
- [x] Extract a crawler-start result that exposes the upstream crawl ID while preserving the HTML caller.
- [x] Persist API crawl state around the existing crawler lifecycle and remove fork crawls from upstream destructive startup cleanup.
- [x] Run focused and race tests; commit `MKT-281 add durable API crawl lifecycle`.

### Task 6: Findings, pages, links, resources, and exports

**Files:**
- Create: `internal/api/findings.go`
- Create: `internal/api/findings_test.go`
- Create: `internal/repository/api_findings.go`
- Create: `internal/api/exports.go`
- Create: `internal/api/exports_test.go`
- Create: `migrations/0080_campanix_api_exports.up.sql`
- Create: `migrations/0080_campanix_api_exports.down.sql`

**Interfaces:**
- Produces: cursor-paginated issue/page/link/resource endpoints plus CSV, sitemap, and asynchronous WACZ exports.
- Pagination exception: upstream finding rows have no creation timestamp, so finding cursors use their immutable numeric row ID rather than the general `(created_at, id)` convention. The signed cursor still binds the route, project, crawl, and filters.

- [x] Write controlled-fixture tests with literal counts/codes, tampered cursors, max limits, project-bound reads, foreign-resource `404`, and CSV column equivalence with existing exporters.
- [x] Verify red tests.
- [x] Implement signed opaque cursors and owner-joined repository queries.
- [x] Adapt existing exporter/archive services without duplicating SEO calculations.
- [x] Run fixture, race, and full upstream tests; commit `MKT-281 expose scoped findings and exports`.

### Task 7: SSRF controls, quotas, audit, and OpenAPI

**Files:**
- Create: `internal/api/targetpolicy.go`
- Create: `internal/api/targetpolicy_test.go`
- Create: `internal/api/ratelimit.go`
- Create: `internal/api/openapi.go`
- Create: `internal/api/openapi_test.go`
- Modify: `internal/crawler/basic_client.go`

**Interfaces:**
- Produces: production target policy covering first resolution and redirects, per-key read quotas, per-tenant crawl/export quotas, and `/api/v1/openapi.json`.

- [x] Write tests for loopback/private/link-local/multicast/unspecified/metadata IPv4 and IPv6, redirect re-resolution, DNS rebinding, test-only fixture allowlist, rate headers, and redacted audit rows.
- [x] Verify red tests.
- [x] Inject target validation into the crawler client connection/redirect path and enforce page/byte/time/concurrency budgets.
- [x] Add rate-limit/audit middleware and OpenAPI generated from handler request/response schemas.
- [x] Run security, race, and full tests; commit `MKT-281 enforce crawl security and publish API contract`.

### Task 8: Isolation fixture, image provenance, and rollback

**Files:**
- Create: `internal/api/testdata/fixture-site/`
- Create: `scripts/api-smoke.ps1`
- Create: `docker-compose.campanix.yml`
- Create: `README.campanix-api.md`
- Modify: `Dockerfile`

**Interfaces:**
- Produces: repeatable two-tenant isolation smoke, controlled crawl accuracy check, labeled fork image, and pinned upstream rollback service.

- [ ] Build the image with explicit fork/upstream revisions and verify `/meta` plus OCI labels agree.
- [ ] Run the fixture crawl and compare JSON issue URLs/codes and CSV/page/link/resource counts with upstream UI repositories.
- [ ] Run two-tenant negative probes across lists, IDs, cursors, exports, and timing-equivalent `404` responses.
- [ ] Snapshot MySQL, start the upstream image pinned by digest, and verify existing projects/completed crawls remain readable.
- [ ] Run `go test -race ./...`, `go vet ./...`, Docker smoke, and commit `MKT-281 verify isolation provenance and rollback`.

## Self-review

- Spec coverage: credentials, key classes, tenancy, idempotency, projects, crawls, findings, exports, SSRF, rate limits, provenance, isolation, fixture accuracy, and rollback each map to a task.
- Placeholder scan: no deferred implementation steps or unspecified tests remain in the plan.
- Type consistency: all handlers consume `api.Principal`; all tenant-data repositories take the principal boundary rather than body-provided owner IDs.
