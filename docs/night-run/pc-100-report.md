# PC-100 report — prototype service-capable core runtime

Commit: see `git log` (PC-100: prototype service-capable core runtime; plus
the remediation commit `fix(PC-100): restrict privileged service RPC surface`).
Branch: `agent/night-stage1-service-spike`. Status: **BLOCKED** — the spike
code is implemented and all available unit/build checks pass, but the roadmap
acceptance (SCM start/stop, non-elevated Windows-VM client evidence) has not
been executed, and a **safe Start cannot be proven within PC-100 boundaries**
(see "Why BLOCKED" below). BLOCKED is not PASS; **Gate 1 is NOT closed**.

## Remediation (external review, this commit)

P0 confirmed and fixed: before the remediation, after the Hello handshake the
service serve loop called the **general `dispatch()`**, which exposed the
whole legacy handlers table to a service client — including SetSystemDNS,
InstallDashboard, CloseConnections and Start with an arbitrary
`extra_process_path`/`extra_process_args`/`extra_process_conf`. After a
per-user SID grant (PC-110/120) that would have allowed code execution and
file operations from SYSTEM. The earlier claim in this report that the wire
path already provided "only" the five spike methods was **wrong** and has been
removed.

Fix (`core/server/service_windows.go`):
- `serviceMethodAllowlist` — the service serve loop now resolves methods ONLY
  through an explicit five-method allowlist (Hello, Health, CheckConfig,
  Start, Stop). Everything else — known to the legacy table or not — gets the
  stable typed error `ERR_METHOD_NOT_ALLOWED`. No fallback to the legacy
  `handlers` map; the legacy map itself is untouched (GUI child mode keeps
  full behavior).
- `ServiceStart` guard — rejects `need_extra_process=true` and any non-empty
  `extra_process_path` / `extra_process_args` / `extra_process_conf` (and
  `extra_no_out`) with the typed `ERR_INVALID_REQUEST` error **before** any
  parsing or spawning; also rejects `need_xray=true` without `xray_config`.
- `normalizeLoadConfigReq` — upstream `server.go` dereferences proto2
  optional pointers directly (`server.go:399` `*in.NeedExtraProcess`, `:434`
  `*in.NeedXray`, `:474` `*in.CoreConfig`, `:579` `*in.CoreConfig`), so a
  request that omits fields would crash a handler (recovered upstream, but
  the request can never succeed). The GUI always sends every field; the
  service path now normalizes absent fields to their defaults instead. This
  is recorded as an **upstream robustness defect** and deliberately NOT
  patched in server.go (PC-100 boundary; belongs in the PC-110 typed
  contract / an upstreamable patch).
- Panic recover now logs the full stack (parity with `runDispatch`).
- New wire-path tests (all through `serveServiceConn` with real framing after
  Hello, never via direct `dispatch()` calls): allowed methods and the full
  safe Start→Health→Stop lifecycle; rejection of SetSystemDNS /
  InstallDashboard / CloseConnections / QueryStats / GenWgKeyPair / unknown
  methods with the connection staying usable; rejection of every
  extra-process field combination; omitted-field normalization; allowlist vs
  legacy table structure. Full suite: `go test -count=20` → **ok** (15 tests
  × 20 runs).

## Why BLOCKED (not PASS)

1. **Safe Start is not provable in PC-100 boundaries.** `Start` hands the
   client-supplied `core_config` (full sing-box JSON), optional
   `xray_config`/`xray_full_configs` (Xray JSON) and `tun_ipv4_cidr` to the
   privileged runtime. That JSON can direct the SYSTEM-privileged core to
   **write files** (sing-box `log.output`, `experimental.cache_file.path`;
   Xray `log.access`/`log.error`) and **read files** (TLS
   `certificate_path`/`key_path`, geoip/geosite paths). The extra-process
   guard removes the direct execution surface, but a config-content policy
   (deny/allowlist these fields, canonicalize paths) is required before a
   service Start can be called safe. That is exactly ADR-001's "Service не
   доверяет путям/JSON UI: canonicalization, validation и allowlisted
   operations выполняются на privileged side" and belongs to the PC-110
   typed contract. No unsafe temporary fallback was added: service-mode
   Start stays prototype-only until that contract exists.
2. **SCM evidence missing** — no disposable Windows VM; `sc create/start/stop`
   and the non-elevated client procedure below have never been executed.
3. SDDL/peer-SID behavior on a real VM (refusal of a non-elevated client
   against the default DACL) is unverified.

## Decision (ADR-002)

Variant **A — service mode inside ThroneCore** (docs/adr/ADR-002-service-host-spike.md):
`ThroneCore.exe service` is an explicit entry point; no arguments = untouched
legacy GUI-child mode. Chosen for the smallest privileged surface (one new
Windows file + a stub + a hunk in main.go + additive proto messages), zero
edits to server.go/dispatch.go/parentcheck/ipc, and unit-testability without
SCM. Re-evaluation trigger: growth beyond ~500 lines or any need to touch
server.go → extract to internal/ package.

## What was implemented

- `core/server/runmode.go` — explicit run-mode selection; unknown modes
  rejected (`bogus`, `--service`, `service extra` → fatal, never a silent
  mode guess).
- `core/server/main.go` — 13-line mode dispatch; legacy path byte-identical.
- `core/server/service_windows.go` — SCM handler (x/sys/windows/svc):
  StartPending → pipe listener (go-winio, fixed name `\\.\pipe\ProxyCoreService`,
  explicit SDDL, default `D:P(A;;GA;;;SY)(A;;GA;;;BA)`, override via
  `THRONE_SERVICE_SDDL`/`THRONE_SERVICE_PIPE` for tests) → Running →
  Stop/Shutdown → listener closed → clients closed → `globalServer.Stop`
  (idempotent) → bounded handler drain → Stopped. Mandatory versioned
  `Hello` handshake before any other method (typed `ERR_NO_HANDSHAKE` /
  `ERR_PROTOCOL_VERSION`); incompatible clients disconnected. Client
  disconnect is a normal event (vs. legacy `runDispatch` log.Fatal).
  Bounded per-connection handler concurrency (8) + global WaitGroup drain;
  wire guards methodLen≤4096, payloadLen≤32 MiB (spike-level; formal limits
  are PC-110). Service mode forces `debug=false` (no raw config dumps).
  `Hello`/`Health` handlers registered into the existing `handlers` map from
  this file's init(); proto service block unchanged.
- `core/server/service_other.go` — non-Windows compile stub.
- `core/server/gen/libcore.proto` — additive messages only:
  `HandshakeReq`, `HandshakeResp`, `HealthResp` (fields 1..3, no renumbering);
  generated bindings regenerated via protoc (git-ignored artifacts, as upstream).

Not touched: parentcheck/*, ipc/*, dispatch.go, server.go, TUN/DNS/routing,
sing-box/Xray logic. No service was installed or started on this machine
(smoke outside SCM correctly refuses: "service mode requested outside the
Windows SCM").

## Requirement checklist (from the spike task)

| Requirement | Status | Evidence |
| --- | --- | --- |
| Core server runs without mandatory GUI parent | PASS | net.Pipe tests: parent is `go test`, no parentcheck involved; `TestServiceSurvivesClientDisconnectWithoutGUI` |
| Legacy/dev child mode not broken | PASS | `TestRunModeFromArgs` + `TestLegacyModeIsDefaultAndDispatchUnchanged` (dispatch serves CheckConfig without handshake, as before); main() legacy path unchanged |
| Service mode has an explicit entry point | PASS | `service` argument only; `ThroneCore.exe service` outside SCM refuses to run |
| Unknown mode rejected | PASS | test + binary smoke (`bogus-mode` → fatal) |
| Service shutdown stops runtime correctly | PASS | `TestServiceHandlerExecuteLifecycleWithoutSCM` (Stop → runtime gone, Stopped reported) |
| Duplicate start/stop idempotent | PASS | `TestStartStopLifecycleIdempotent`, `TestStopIdempotentWithoutRuntime` (2nd Start rejected, 2nd Stop clean) |
| Incompatible client gets typed error | PASS | `TestServiceHandshakeVersionMismatchTypedError` (`ERR_PROTOCOL_VERSION`, disconnect) |
| No GUI → no service crash | PASS | disconnect/reconnect keeps serving (Health OK after reconnect) |
| Secrets/full config not in logs | PASS | `TestServiceModeNeverLogsConfigSecrets` (marker not logged even with THRONE_CORE_DEBUG=1; child-mode debug dump stays a documented upstream debt) |
| Handshake/Health/CheckConfig/Start/Stop only | **BLOCKED** | Pre-remediation this row claimed PASS — that claim was **wrong**: the serve loop called the general `dispatch()` and exposed the whole legacy handlers table (review P0). Post-remediation the service path uses an explicit five-method allowlist with typed `ERR_METHOD_NOT_ALLOWED` for everything else; proven by wire-path tests. Full safe-Start proof still blocked by the config-content policy gap (see "Why BLOCKED") |
| Real SCM test | **BLOCKED** | requires disposable Windows VM + service registration; procedure below |

## Commands and results (post-remediation)

```
gofmt -l -w (changed files)                        → clean
go vet (project build tags, root package)          → exit 0
go build (CI tags)                                 → exit 0
go test -count=20 (15 PC-100 tests, CI tags,
  -ldflags="-checklinkname=0")                     → ok  (15 × 20 runs)
go test . ./internal/xray/ ./internal/xraydns/     → all ok (regression)
git diff --check                                   → clean
ThroneCore.exe service                             → refuses outside SCM (expected)
ThroneCore.exe bogus-mode                          → "unknown run mode" fatal (expected)
```

Pre-remediation suite (10 tests) passed once; the external review then found
the P0 dispatch-table exposure — fixed as described above, with the suite
grown to 15 tests and re-verified at -count=20.

## Manual SCM procedure (VM only, BLOCKED here)

1. Snapshot the VM. Copy `ThroneCore.exe` (spike build) into e.g. `C:\spike\`.
2. Elevated (VM only): `sc.exe create ProxyCoreService binPath= "C:\spike\ThroneCore.exe service" start= demand`
   (allowed here because it is the disposable VM, never the working machine).
3. `sc.exe start ProxyCoreService` → service enters Running; `sc.exe query` shows it.
4. From a normal (non-elevated) shell, a test client dialing
   `\\.\pipe\ProxyCoreService` should be REFUSED by the SDDL (only
   SYSTEM/Administrators in the default DACL) — this refusal is the expected
   result and must be recorded, not "fixed" locally.
5. Elevated test client: Hello → Health → CheckConfig (valid + invalid JSON) →
   Start (loopback config, TUN off) → Health (running=true) → Stop →
   Health (running=false); kill the client mid-call and verify the service
   survives; `sc.exe stop`/`sc.exe start` cycles; duplicate Start/Stop.
6. Verify Windows Event Log / stdout has no full config dumps.
7. Cleanup: `sc.exe delete ProxyCoreService`; restore snapshot.

## Risks / follow-ups

- **PC-110 typed contract must include a privileged-side config policy** for
  Start (deny/allowlist `log.output`, `cache_file.path`, Xray log file paths,
  TLS cert/key path handling; explicit TUN config policy) — without it a
  service client could still direct the SYSTEM runtime to arbitrary file
  writes/reads via the config JSON. This is the main reason PC-100 stays
  BLOCKED.
- **Upstream defect (recorded, not fixed here)**: `server.go` dereferences
  proto2 optional pointers directly (`:399`, `:434`, `:474`, `:579`); any RPC
  client omitting those fields crashes the handler (recovered, but the
  request can never succeed). The GUI always sends every field. Candidates
  for an upstreamable one-line getter patch outside PC-100 scope.
- SDDL default (SYSTEM+Administrators) intentionally excludes the normal user
  until PC-110/PC-120 define per-user SID grants — the UI cannot talk to the
  service yet; that is the next package's contract, not a gap in this spike.
- Legacy client framing retained for the spike; PC-110 replaces it with the
  versioned envelope (request ID, deadline, expected revision, typed errors)
  and the formal limits/bounded concurrency.
- If upstream refactors main.go's RunCore, the small mode-dispatch hunk may
  conflict — kept to ~13 lines for exactly that reason.
