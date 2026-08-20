# Kilnbench SEOnaut API fork

This fork exposes SEOnaut as a tenant-isolated machine audit service while preserving the upstream UI. The API is mounted at `/api/v1`; `/api/v1/openapi.json` is public, `/api/v1/health` is public readiness, and `/api/v1/meta` returns authenticated build provenance.

## Production image

Build from a clean, reviewed commit and supply the immutable revisions explicitly:

```powershell
$revision = git rev-parse HEAD
docker build --build-arg FORK_VERSION=1.0.0 --build-arg FORK_REVISION=$revision --build-arg UPSTREAM_REVISION=880b312c28fab8b0bf7fe4f9449dc4746dbb82ff --build-arg SCHEMA_VERSION=80 -t kilnbench/seonaut:$revision .
```

Do not set `GO_BUILD_TAGS=fixture` in production. Local/private targets remain blocked even if somebody adds `fixture_hosts` to production configuration; fixture access requires both the `fixture` build tag and `api.environment = "test"`.

Configure these secrets outside the image:

- `SEONAUT_API_ROOT_HASH`: Argon2id hash of the one-time root bootstrap secret.
- `SEONAUT_API_CURSOR_SECRET`: at least 16 random bytes; changing it invalidates outstanding result cursors.
- database credentials and the public `SEONAUT_SERVER_URL`.

The root credential only manages platform keys. A platform key provisions tenants and issues their first key. Tenant keys manage projects/crawls and can delegate project-bound read-only keys. Raw keys are returned once and must be stored in the Kilnbench secret store, never logs or audit metadata.

## Repeatable smoke test

The smoke stack is disposable and uses the public test credential `snk_test_root.smoke-root-secret`. It must never be exposed outside a developer machine or CI runner.

```powershell
pwsh ./scripts/api-smoke.ps1
```

The script starts MySQL, a controlled fixture site, and a fixture-tagged fork image. It then verifies:

- tenant/platform bootstrap and two independent tenant keys;
- deterministic fixture crawl with JSON findings and CSV/XML exports;
- page, issue, link, and resource counts against the same upstream tables used by the UI repositories;
- negative isolation across project/crawl lists, direct IDs, cursors, and exports, with equivalent `404` semantics;
- `/api/v1/meta` matches OCI fork revision, upstream revision, and schema labels;
- a pre-rollback MySQL snapshot is created, the fork stops, and `ghcr.io/stjudewashere/seonaut@sha256:77826fb91f1f7b3d054bcd73c4f9df40c6b40962cfab68208f435e0758266415` boots against the additive schema while existing projects and completed crawls remain readable.

Useful switches:

```powershell
pwsh ./scripts/api-smoke.ps1 -SkipRollback
pwsh ./scripts/api-smoke.ps1 -KeepStack
pwsh ./scripts/api-smoke.ps1 -SkipBuild -KeepStack
```

`-AllowDirty` exists only for developing the harness. Release evidence must come from a clean commit so the image label identifies its exact contents.

## Rollback runbook

1. Stop new API crawl admission and wait for active crawls to finish or cancel them.
2. Snapshot MySQL and preserve the fork image plus its archive volume/object-store prefix.
3. Record `/api/v1/meta` and the OCI labels of the running fork.
4. Stop the fork service; do not run fork and upstream writers concurrently against the same database.
5. Start the upstream image pinned by digest above. The API tables and `users.api_only` column are additive; upstream UI tables remain intact.
6. Verify the upstream sign-in page, then read representative existing projects and completed crawls before reopening UI traffic.
7. If rollback validation fails, stop upstream, restore the snapshot, and restart the exact fork image revision recorded in step 3.

The pinned digest currently identifies upstream revision `880b312c28fab8b0bf7fe4f9449dc4746dbb82ff`. Updating the upstream baseline requires a new compatibility smoke and a deliberate digest/revision change in the compose file, script, Dockerfile, and this runbook.
