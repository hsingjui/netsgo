# Proxy Provision Payload Split Follow-up

This document is a handoff for the next agent or developer. It records what is
still planned after the test-plan hardening commit. It is intentionally separate
from `docs/proxy-provision-payload-split-plan.md` so the implementation plan can
stay stable while remaining work is tracked explicitly.

## Current scope

Do not start the production payload-split implementation unless the user
explicitly changes the scope. The immediate scope is:

- finish the remaining test-plan hardening;
- run or delegate the expensive cross-version tests;
- update the plan documents with exact evidence after those tests pass or fail.

The current work is test-driven. Expected-red tests are allowed only behind
`NETSGO_TDD_RED=1` and the `make test-tdd-red-*` targets. Default CI and default
`go test ./...` must not require expected-red tests to pass before the production
implementation exists.

## What has landed

The branch already contains the main compatibility-test scaffolding:

- protocol/client/server unit and guard tests for legacy flat payloads,
  unified payload round-trip, legacy fallback, unprovision revision guards,
  server-expose revision/header behavior, clean reject, and reconcile registry
  behavior;
- `internal/client/testdata/legacy_v0.1.8_*` fixtures for real v0.1.8 flat
  provision and close payload shapes;
- `test/e2e/scripts/test-baseline.sh`, `test-compat.sh`, and
  `test-upgrade.sh`;
- manual `Cross-Version E2E` GitHub workflow;
- PR CI smoke coverage for baseline and mixed-version compatibility;
- `docs/proxy-provision-payload-split-plan.md` section `6.0.5` coverage matrix.

The latest verified stable baseline is:

```bash
make test-baseline-e2e COMPAT_BASELINE=v0.1.8 BASELINE_MODE=full
```

That run reused an existing local `netsgo-e2e:v0.1.8` image. It proves the
v0.1.8 runtime baseline, not the tag-to-image rebuild path.

## Remaining work before calling the test plan complete

### 1. Strengthen rollback "old server continues service" coverage

Current state:

- `server-rollback` and `current-write-rollback` revalidate existing
  HTTP/TCP/UDP/SOCKS5 server-expose tunnels after stable server rollback;
- they also create and verify a new HTTP server-expose tunnel after rollback.

Gap:

- the post-rollback "new tunnel can still be created" proof is HTTP-only.

Required next step:

- extend post-rollback creation to a full server-expose suite:
  HTTP, TCP, UDP, and SOCKS5;
- verify each new tunnel reaches `active`;
- verify each new tunnel has empty `issues`;
- verify HTTP/TCP/UDP/SOCKS5 data paths;
- verify server listener counts for the new TCP/UDP/SOCKS5 ports.

Likely implementation shape:

- add dedicated server alt port variables, for example:
  `E2E_SERVER_TCP_ALT_PORT`, `E2E_SERVER_UDP_ALT_PORT`,
  `E2E_SERVER_SOCKS5_ALT_PORT`;
- pass them from `Makefile` to `test-upgrade.sh`;
- add the corresponding server port mappings to
  `test/e2e/docker-compose.system.yml`;
- add an `assert_new_server_expose_suite_works` helper in
  `test/e2e/scripts/test-upgrade.sh`;
- call that helper from `case_server_rollback` and
  `case_current_write_rollback`;
- update `docs/e2e-testing.md` and
  `docs/proxy-provision-payload-split-plan.md`.

Do not reuse `C2C_*` host ports for this. Those ports are mapped on the
`ingress-client` service, not the `server` service, so they are the wrong proof
surface for server-expose rollback creation.

### 2. Run a final external review

Ask qoder to review the remaining test strategy after the rollback suite is
strengthened.

Suggested command:

```bash
qodercli --yolo "请严肃审查 NetsGo 的 proxy provision payload split 测试规划。重点看 docs/proxy-provision-payload-split-plan.md、docs/proxy-provision-payload-split-followup.md、test/e2e/scripts/test-upgrade.sh、test/e2e/scripts/test-compat.sh、Makefile、.github/workflows/cross-version-e2e.yml。请只评价测试规划和兼容验收，不要实现生产代码。请明确指出 blocker、非 blocker、以及是否还存在 old server/current client、old client/current server、server rollback/current-write rollback 的覆盖缺口。"
```

The user previously requested long-running qoder calls to be polled about every
three minutes instead of being interrupted or narrowed prematurely.

### 3. Run full cross-version tests

Minimum commands before the test plan is considered closed:

```bash
make test-compat-e2e COMPAT_BASELINE=v0.1.8 COMPAT_MODE=full COMPAT_ABORT_ON_FAILURE=true
make test-upgrade-e2e COMPAT_BASELINE=v0.1.8
```

Optional but useful when validating the baseline image path:

```bash
make test-baseline-e2e COMPAT_BASELINE=v0.1.8 BASELINE_MODE=full BASELINE_REBUILD_IMAGE=true
```

If local Docker runtime is too expensive, use the manual `Cross-Version E2E`
GitHub workflow. Do not treat the PR CI smoke job as proof that full compat or
upgrade/rollback passed.

### 4. Update status labels after evidence exists

After the full test runs:

- update the coverage matrix status and evidence in
  `docs/proxy-provision-payload-split-plan.md`;
- keep production-dependent rows as `[RED]`, `[PARTIAL]`, or `[PENDING]` until
  the production implementation exists;
- do not mark expected-red rows complete just because they are present;
- if a red guard becomes green during implementation, remove `requireTDDRed(t)`
  or replace the guard with a stronger normal regression test.

## Production implementation is still separate

After the test plan is closed, the implementation PR still has to do the actual
payload split work. Expected implementation themes are:

- fixed TCP/UDP/HTTP target runtime must stop relying on legacy `c.proxies`;
- SOCKS5 target runtime must remain endpoint-specific;
- server-expose runtime/reconcile must use `TunnelSpec` endpoint data, not
  `StoredTunnel.ProxyNewRequest`, as the unified runtime source;
- HTTP host dispatch must use the ingress endpoint domain;
- stale provision ACKs must not activate old revisions;
- rejected provision must leave no listener, runtime, or ack waiter;
- reconcile registry dirty rerun/coalescing must be fixed;
- shutdown and in-flight reconcile cleanup need runtime-store-level tests;
- capability-loss / reconcile-stage clean reject needs an implementation-time
  test hook or production capability path before Docker E2E can fully cover it.

Those implementation tasks should be done only after the user asks to proceed
from test planning into production code.
