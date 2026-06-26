# E2E Testing

NetsGo has two E2E layers:

- **System E2E**: Go tests drive real Docker Compose topologies. This is the source of truth for server, client, tunnel runtime, reverse proxy, and data-path behavior.
- **Playwright UI E2E**: browser tests verify user workflows. They run headed by default and must not be used as the primary proof of data-plane correctness.

## System E2E

System E2E lives in `test/e2e/system_e2e_test.go`.

Compose files define infrastructure only:

- `test/e2e/docker-compose.system.yml`
- `test/e2e/docker-compose.proxy.nginx.yml`
- `test/e2e/docker-compose.proxy.caddy.yml`

The Go test owns all business actions:

- admin login
- API key creation
- client readiness checks
- `/api/tunnels` mutation calls
- tunnel runtime polling
- HTTP and SOCKS5 traffic assertions
- restart and recovery assertions

For regular system E2E, shell scripts must not create tunnels or encode scenario logic. Compose services may use scripts only for process bootstrapping, such as waiting for a client key and starting `netsgo client`.

Run the full system E2E matrix. This runs every Go system test named `TestSystem*E2E`, including the main data-path suite, single-target server-expose compatibility suite, and clean-reject suite:

```bash
make test-system-e2e
```

Run one proxy variant:

```bash
make test-system-e2e-nginx
make test-system-e2e-caddy
```

Useful local stack commands:

```bash
NETSGO_ADMIN_PASS="$(openssl rand -base64 18)" make system-e2e-up
make system-e2e-logs
make system-e2e-down
make system-e2e-clean
```

## Topology Rules

System E2E must model real network boundaries:

- `server` and clients share the `control` network.
- target backends live on a target-side backend network.
- only `target-client` can reach target backends directly.
- `ingress-client` exposes client-side ingress ports to the host only when the scenario needs an external caller.
- `proxy` is an overlay service, not a business-test controller.

This keeps data-path assertions meaningful. For example, a client-to-client SOCKS5 test must prove that the ingress client accepts the SOCKS5 connection while the target client performs the backend dial.

## Current System Scenarios

`TestSystemE2E` covers:

- admin login rejection and admin API authorization.
- HTTP `server_expose` through nginx/caddy reverse proxy.
- HTTP `server_expose` through direct server upstream.
- HTTP Basic authentication on `server_expose` routes.
- TCP and UDP `server_expose` through server ingress.
- SOCKS5 `server_expose` CONNECT through server ingress.
- SOCKS5 `client_to_client` CONNECT through ingress-client and target-client.
- SOCKS5 username/password authentication, wrong-password rejection, and no-auth method rejection.
- SOCKS5 ingress source CIDR rejection.
- SOCKS5 target policy denial.
- TCP `client_to_client` data path.
- UDP `client_to_client` datagram echo path.
- multiple simultaneous TCP tunnels to distinct target backends, with isolation assertions.
- concurrent data streams through one TCP tunnel and one SOCKS5 tunnel.
- bounded latency sanity checks for fast TCP data-path requests.
- fast-tunnel responsiveness while another tunnel holds slow backend connections open.
- proxy restart recovery for HTTP.
- ingress-client restart recovery for TCP and UDP c2c.
- target-client restart recovery for SOCKS5 c2c.
- server restart recovery with persisted tunnel restoration and data-path revalidation.

Add new runtime scenarios here when the behavior depends on real process, network, reverse proxy, restart, or Docker topology.

## Cross-Version E2E

Cross-version tests live in `test/e2e/scripts/` because they must control image selection and service replacement steps directly.

Useful targets:

```bash
make docker-build-e2e-stable COMPAT_BASELINE=v0.1.8
make test-baseline-e2e COMPAT_BASELINE=v0.1.8
make test-compat-e2e COMPAT_BASELINE=v0.1.8
make test-upgrade-e2e COMPAT_BASELINE=v0.1.8
```

`test-baseline-e2e` is the first compatibility gate. It uses only the selected stable image for server, target-client, ingress-client, and NetsGo-based helper services such as the slow TCP and UDP helper containers. It intentionally does not build or reference the current image, even if `E2E_CURRENT_IMAGE` exists in the caller environment. Default `BASELINE_MODE=full` runs `TestSystem*E2E` against the stable-only stack; `BASELINE_MODE=smoke` only verifies stack startup, admin login, and client connectivity. Set `BASELINE_REBUILD_IMAGE=true` to rebuild the stable image even if a local tag already exists.

The `v0.1.8` stable-only baseline has been verified with:

```bash
make test-baseline-e2e COMPAT_BASELINE=v0.1.8 BASELINE_MODE=full
```

That run reused the local `netsgo-e2e:v0.1.8` image and passed `go test -tags=e2e ./test/e2e -run 'TestSystem.*E2E'` using only the stable image. It proves the stable baseline runtime, not the stable-image rebuild path. Run with `BASELINE_REBUILD_IMAGE=true` when the tag-to-image build path also needs proof.

`test-compat-e2e` starts fresh mixed-version stacks. Default `COMPAT_MODE=full` runs the main `TestSystemE2E` suite with prebuilt images and `--no-build` for each core server/target-client/ingress-client matrix row. It then runs focused rows for `TestSystemSingleTargetClientE2E` and `TestSystemClientToClientCleanRejectE2E`. It intentionally does not run every `TestSystem*E2E` test for every core matrix row; the focused cases are separate matrix rows to keep runtime bounded while still covering old-server/current-target server-expose and mixed-version clean-reject semantics. The single-target case includes unsupported target and unsupported server ingress clean-reject assertions: structured API error, no persisted tunnel, and no server listener on the rejected port. Set `COMPAT_MODE=smoke` only when you explicitly want startup/client-connectivity smoke coverage without tunnel data-path assertions. NetsGo-based helper services use the scenario's server image so the all-stable scenario does not depend on the current image.

`test-upgrade-e2e` starts from a stable running stack, creates real tunnels, verifies data paths, replaces server/client images, and revalidates the same tunnels. It includes server-only, target-client-only, ingress-client-only, clients-only, server rollback, current-write rollback, all-components upgrade, client-first rolling upgrade, and full cold upgrade cases. Server-only and target-only cover server-expose HTTP/TCP/UDP/SOCKS5. The full rolling/cold/current-write-rollback cases cover server-expose HTTP/TCP/UDP/SOCKS5 and client-to-client TCP/UDP/SOCKS5. The narrower ingress-only and clients-only cases currently cover client-to-client TCP/SOCKS5 plus HTTP where relevant. The harness also asserts empty tunnel issues plus listener counts for server-expose TCP/UDP/SOCKS5. The upgrade baseline uses the stable image for NetsGo-based helper services so the pre-upgrade stack is not coupled to the current image. The default reconnect/re-provision recovery window is `UPGRADE_RECOVERY_TIMEOUT_SECONDS=120`, forwarded by `make test-upgrade-e2e`; use the same variable when intentionally changing the upgrade wait budget.

GitHub Actions also provides a manual `Cross-Version E2E` workflow. It is intentionally `workflow_dispatch` only. The workflow first runs the stable-only baseline, then builds the current E2E image, then optionally runs `COMPAT_MODE=full` and the upgrade/rollback matrix. Use it before merging or releasing the payload split implementation when local Docker time is expensive or when an auditable CI run is needed. Each phase tears down its Compose project so the default host ports can be reused by the next matrix.

Legacy flat provision compatibility fixtures live under `internal/client/testdata/`:

- `legacy_v0.1.8_proxy_provision_tcp.json`
- `legacy_v0.1.8_proxy_provision_udp.json`
- `legacy_v0.1.8_proxy_provision_http.json`
- `legacy_v0.1.8_proxy_provision_http_full.json`
- `legacy_v0.1.8_proxy_provision_tcp_bound.json`
- `legacy_v0.1.8_proxy_provision_tcp_unknown_field.json`
- `legacy_v0.1.8_proxy_provision_udp_relay.json`
- `legacy_v0.1.8_proxy_close.json`

They are hand-crafted from the `v0.1.8` `ProxyNewRequest` schema and dual-dispatch code. They intentionally contain no `tunnel_id`, so current clients must route them through the legacy flat fallback. Do not add flat SOCKS5 or close-by-id fixtures for `v0.1.8`; those were not real flat legacy wire shapes.

CI syntax-checks these scripts and runs a cross-version smoke gate: stable-only baseline smoke followed by mixed-version compatibility smoke. Full cross-version execution is still intended for release or manual validation because it builds stable/current images and runs multiple Docker Compose stacks. PR CI should not be treated as proof of full cross-version data-path compatibility unless the manual `Cross-Version E2E` workflow or equivalent local commands have also passed.

System E2E and cross-version harnesses never set `NETSGO_TDD_RED`. The expected-red TDD guard applies only to `internal/client` and `internal/server` unit tests through `make test-tdd-red-client` and `make test-tdd-red-server`.

## Playwright UI E2E

Playwright tests live under `web/e2e`.

They run against `test/e2e/docker-compose.playwright.yml` and focus on UI behavior:

- login
- form behavior
- create/edit/stop/resume/delete workflows
- field validation and visible feedback

Playwright is configured with `headless: false` in `web/e2e/playwright.config.ts`.

For local visual runs on macOS, start Chrome with CDP enabled first:

```bash
devtools
```

Then run the smoke suite through that visible Chrome:

```bash
make test-playwright-e2e-cdp-smoke
```

The CDP target defaults to `http://127.0.0.1:9222` and can be overridden:

```bash
LOCAL_CHROME_CDP_ENDPOINT=http://127.0.0.1:9223 make test-playwright-e2e-cdp-smoke
```

The local CDP target opens a visible tab in the Chrome profile launched by `devtools`.
It also uses a small default delay between browser actions and keeps the final tab open so the run is easy to inspect.
Override these when needed:

```bash
LOCAL_CHROME_CDP_SLOW_MO_MS=500 LOCAL_CHROME_CDP_FINISH_DELAY_MS=15000 LOCAL_CHROME_CDP_KEEP_TAB=0 make test-playwright-e2e-cdp-smoke
```

CI does not use the local CDP profile. It launches Playwright's Chromium under a virtual Linux display via `xvfb-run -a`.

Run the smoke suite:

```bash
make test-playwright-e2e-smoke
```

The smoke suite is intentionally short. It creates TCP and UDP `client_to_client` tunnels from the UI, then verifies host-to-ingress traffic reaches the Docker backend services.
Use the full suite for UI lifecycle, validation, and conflict scenarios:

Run all Playwright E2E:

```bash
make test-playwright-e2e-full
```

## Design Rules

- Do not add new system scenarios to shell scripts.
- Do not create tunnels through legacy APIs in system E2E.
- Prefer `/api/tunnels` for all system E2E tunnel mutations.
- Keep Compose files declarative: services, ports, networks, volumes.
- Keep assertions in Go or Playwright tests, not in container startup scripts.
- Browser E2E must stay headed unless a specific CI environment requires a virtual display.
