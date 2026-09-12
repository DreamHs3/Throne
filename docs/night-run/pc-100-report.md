# PC-100 report — prototype service-capable core runtime

Commit: see `git log` (PC-100: prototype service-capable core runtime).
Branch: `agent/night-stage1-service-spike`. Status: **PASS (spike scope)**
with SCM acceptance **BLOCKED** (no disposable Windows VM — by rule, not by
limitation of the code).

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
| Handshake/Health/CheckConfig/Start/Stop only | PASS | service path gates on Hello; no policy mutation, no WFP, no command execution (none exist in the wire path) |
| Real SCM test | **BLOCKED** | requires disposable Windows VM + service registration; procedure below |

## Commands and results

```
go build (CI tags)                 → exit 0
go test -run <PC-100 tests> -v .   → 10/10 PASS (0.19s)
go test . ./internal/xray/ ./internal/xraydns/ → all ok
ThroneCore.exe service             → refuses outside SCM (expected)
ThroneCore.exe bogus-mode          → "unknown run mode" fatal (expected)
```

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

- SDDL default (SYSTEM+Administrators) intentionally excludes the normal user
  until PC-110/PC-120 define per-user SID grants — the UI cannot talk to the
  service yet; that is the next package's contract, not a gap in this spike.
- Legacy client framing retained for the spike; PC-110 replaces it with the
  versioned envelope (request ID, deadline, expected revision, typed errors)
  and the formal limits/bounded concurrency.
- If upstream refactors main.go's RunCore, the small mode-dispatch hunk may
  conflict — kept to ~13 lines for exactly that reason.
