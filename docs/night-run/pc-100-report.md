# PC-100 report — prototype service-capable core runtime

Commit: see `git log` (PC-100: prototype service-capable core runtime; plus the
remediation commits `fix(PC-100): restrict privileged service RPC surface` and
`fix(PC-100): enforce service config filesystem policy and client identity
boundary`).
Branch: `agent/night-stage1-service-spike`. Status: **BLOCKED** — the spike
code is implemented and all available unit/build checks pass, but the roadmap
acceptance (SCM start/stop, non-elevated Windows-VM client evidence) has not
been executed. BLOCKED is not PASS; **Gate 1 is NOT closed**.

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

## Remediation round 2 — config filesystem policy (ADR-002 addendum #2)

Closes the "Why BLOCKED" item 1 **at the code level** (VM evidence still
outstanding). Start and CheckConfig now enforce a deny-by-default filesystem
policy before the privileged runtime sees any client JSON
(`core/server/service_config_policy.go`, new file):

- **Normalized (never client-controlled)**: `experimental.cache_file.path` is
  rewritten to `<THRONE_SERVICE_DATA_DIR>/cache.db` — fail-closed with a typed
  `ERR_CONFIG_POLICY` when cache_file is enabled without a data directory (the
  service working directory is System32; a working-directory-relative cache
  path would land there). When cache_file is disabled, a client-supplied path
  is silently dropped. `experimental.clash_api.external_ui*` keys are removed
  (the UI is not the core's business, and the values are directories the core
  would serve or download into).
- **Rejected (typed `ERR_CONFIG_POLICY`)**: every other filesystem-bearing
  key, at any depth — log sinks, TLS/OpenVPN/OpenConnect certificate & key
  paths (client/mTLS, CA, MCA, CRL, static keys, wrapper scripts), SSH
  `private_key_path`, local/remote rule-set paths, tailscale/ACME/tor
  directories & executables, Xray `log.access`/`log.error` files (only
  `none`/empty accepted), Xray `certificateFile`/`keyFile` — plus
  `unix://`, `\\.\pipe`, `\\.\`, `\\?\` string literals anywhere in the
  document.
- **Deny list proven against the pinned sing-box**: the key list was built by
  enumerating every filesystem-bearing JSON key in the pinned sing-box
  `option` package. The enumeration surfaced **24 path-bearing keys beyond
  the review's original list** (`client_certificate_path`, `client_key_path`,
  `crl_path`, `static_key_path`, `certificate_directory_path`,
  `certificate_authority_path`, `mca_certificate_path`, `mca_key_path`,
  `secret_path`, `credential_path`, `usages_path`, `config_path`,
  `initial_path`, `cache_path`, `data_directory`, `state_directory`,
  `taildrop_directory`, `mesh_psk_file`, `pid_file`, `dhcp_lease_files`,
  `directory`, `executable_path`, `wrapper_path`, `protect_path`) — all added.
  Route matchers (`process_path`, `process_path_regex`) and the DERP `home`
  HTTP route are deliberately NOT denied: they match or route, the core never
  opens those values as files, and Throne's per-app proxy may legitimately
  use them.
- **One contract for Start and CheckConfig**: what one accepts, the other
  accepts; what one rejects, the other rejects (asserted over the wire, both
  directions). `ServiceStart` additionally still rejects the whole
  extra-process surface (round 1 guard).

## Remediation round 2 — Windows identity boundary (ADR-001)

The default DACL (SYSTEM + Administrators) alone does not identify a client:
any administrator process could connect, and PC-120 will grant a per-user SID
at the DACL. The pipe now also authorizes clients **by token identity at
accept time**, before the handshake:

- `serveServiceListener` → `authorizeServiceClient` (test hook; production
  `authorizeServiceClientReal`) → `clientTokenIdentity`:
  `GetNamedPipeClientProcessId` → `OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION)`
  → `OpenProcessToken(TOKEN_QUERY)` → `TokenUser` + `TokenGroups` SIDs. The
  identity is bound to the connecting process at accept time.
- `serviceClientAllowed` — the pure ADR-001 admission policy: the configured
  owner SID list (`THRONE_SERVICE_ALLOWED_SIDS`, set by the installer PC-120)
  plus the built-in Administrators group (`S-1-5-32-544`). Unresolvable or
  unknown identities are denied (fail closed); only the SID — never any
  payload — is logged.
- `safeServiceSDDL` protects the SDDL override from unsafe substitution: a
  usable DACL must be protected (`D:P`) and must not grant access to broad
  trustees (WD/AN/AU/BU); per-user SID grants remain fine. An unsafe override
  fails the listener start — never a silent fallback.

### Defects found by the round-2 tests (fixed)

The three defects below were present in the round-1 "ready" code and caught
only by the new test suite — vet/build alone had passed all of them:

1. **The policy rejected its own normalized configs.** The
   `experimental.cache_file.path` exemption compared the *parent* walk path
   against the *child's* full path, so the exemption never fired and every
   normalized Start was rejected with `ERR_CONFIG_POLICY`.
2. **The SDDL trustee ban was dead code.** After `strings.Split(sddl, "(A;")`
   each ACE body keeps a leading `;` (the split consumes `type` + one
   delimiter), and the trustee is the *last* field of six — the old
   `SplitN(..., 4)` + `fields[3]` check matched nothing, so a
   `D:P(A;;GA;;;WD)` override would have been accepted. Fixed: parse to the
   last semicolon-separated field of each allow ACE.
3. **`clientTokenIdentity` panicked on any real client.**
   `Tokengroups.Groups[:GroupCount]` slices a fixed-size `[1]SIDAndAttributes`
   array — any token with more than one group panicked the accept loop. The
   members are now re-sliced with `unsafe.Slice` (`tokenGroupSIDs`).

## Remediation round 3 — parser semantics (config-policy bypasses, review P0)

An independent read-only review of the round-2 code (2026-09-12) found the
config policy **bypassed end-to-end** and proved it by execution on the
development machine, not theory. The policy scanned the document with std
`encoding/json` while the privileged runtimes parse the same document with
different semantics. Three holes:

1. **JSONC comments.** On a std-json parse error both policy entry points
   returned the document unscanned with a nil error ("the runtime's own
   parser reports malformed JSON"). But sing's contextjson strips C-style
   comments before parsing (`stripJSONComments`), and the pinned xray-core
   serial loader is documented permissive (Java/Python comments): a config
   `{"log":{"output":"C:\\…\\pwn.txt"}} /* c */` passed the policy untouched
   and a real `ServiceStart` executed it — the privileged runtime created
   the file.
2. **Case-insensitive key binding.** The scan used exact map lookups; both
   runtimes bind JSON keys to config fields case-insensitively.
   `{"LOG":{"OUTPUT":"…"}}` passed the deny map, the re-marshal kept the
   client's spelling, and the runtime bound it to `log.output`.
3. **Xray branch.** `validateServiceXrayConfigPolicy` had the same
   pass-through on a parse error.

The deny-list CONTENT was re-verified correct (enumerated against the pinned
sing-box `option` package) — the failure was parsing semantics, so the fix
changes semantics, not the list (`core/server/service_config_policy.go`):

- **Fail-closed parsing**: a document the strict std parser cannot read
  (comments, trailing commas, trailing data, non-object roots) is rejected
  with typed `ERR_CONFIG_POLICY` in BOTH entry points instead of passed
  through. Genuinely malformed JSON is rejected by the runtime anyway — no
  legitimate configs are lost.
- **Case-folded key matching** (`strings.EqualFold`) in the deny scan, the
  Xray `log.access`/`error` sink check, and the normalization/exemption key
  lookups. Every case variant of a key the policy owns (`path`,
  `external_ui*`) is deleted before the service-owned value is written —
  merely adding a canonical key would have left the hostile spelling in the
  re-marshaled document.
- **Scan-what-you-run kept**: the runtime still sees exactly the scanned
  tree (re-marshal of the scanned map), with numbers preserved verbatim via
  `json.Decoder.UseNumber()` (a float64 round trip silently re-rounded large
  int64 values).
- **Value prefixes matched case-insensitively** (Windows pipe/device
  namespaces are case-insensitive: `\\.\PIPE\evil` names the same pipe as
  `\\.\pipe\evil`), and the bare gRPC form `unix:` added alongside
  `unix://`.
- **CheckConfig↔Start parity** (review P2): `ServiceCheckConfig` now
  validates `xray_full_configs` exactly like `ServiceStart` always did.
- **SDDL guard hardening** (`service_windows.go`, review P2): raw-SID
  trustees are now treated like abbreviations — `D:P(A;;GA;;;S-1-1-0)`
  (Everyone) and `D:P(A;;GA;;;S-1-5-32-545)` (Builtin Users) previously
  passed the guard; it now also refuses the raw SIDs of Everyone (`S-1-1-0`),
  Anonymous (`S-1-5-7`), Authenticated Users (`S-1-5-11`), Builtin Users
  (`S-1-5-32-545`) and Builtin Guests (`S-1-5-32-546`) at listener start.
  Defense-in-depth for an admin-controlled variable.

Round-3 acceptance tests (32 PC-100 tests total, all green at `-count=20`):
strict-JSON fail-closed matrix (JSONC spellings, trailing comma, trailing
data, non-object roots) for both entry points; JSONC in `core_config` and
`xray_config` rejected over the wire by Start AND CheckConfig with the sink
file never created; case-variant deny keys (`LOG.OUTPUT`, `CERTIFICATE_PATH`,
Xray `KEYFILE`, `UNIX://`, `\\.\PIPE`) asserted directly and over the wire;
case-variant normalization (`EXPERIMENTAL.CACHE_FILE.ENABLED` rewritten to
the service-owned path, duplicate case-variant blocks, fail-closed without a
data dir); number fidelity; `xray_full_configs` parity through both methods;
SDDL raw-SID refusal at the listener; the round-2 lowercase matrix stays
green. **PC-100 remains BLOCKED** — nothing here substitutes the VM evidence
(SCM start/stop, non-elevated client refusal, SDDL refusal under a normal
user).

Round-2 count correction: the round-2 suite added **10** tests (not 9) for
**25** total (not 24); the command blocks below are corrected.

## Why BLOCKED (not PASS)

1. **Config-content policy — implemented in rounds 2-3, VM evidence outstanding.**
   `Start` hands the client-supplied `core_config` (full sing-box JSON),
   optional `xray_config`/`xray_full_configs` (Xray JSON) and `tun_ipv4_cidr`
   to the privileged runtime. That JSON can direct the SYSTEM-privileged core
   to **write files** (sing-box `log.output`, `experimental.cache_file.path`;
   Xray `log.access`/`log.error`) and **read files** (TLS
   `certificate_path`/`key_path`, geoip/geosite paths). Round 2 closed this
   surface in code (normalization + deny-by-default rejection of every
   filesystem-bearing field, proven by a wire-level traversal matrix —
   absolute, `..\` and junction cache paths land only in the service data
   directory). Round 3 closed the parser-semantics bypasses of that policy an
   independent review proved end-to-end (JSONC comments, case-variant keys —
   see the round-3 section). What remains BLOCKED is the VM-side acceptance:
   SCM start/stop with the policy active, and the non-elevated client
   procedure below.
2. **SCM evidence missing** — no disposable Windows VM; `sc create/start/stop`
   and the non-elevated client procedure below have never been executed.
3. SDDL/peer-SID behavior on a real VM (refusal of a non-elevated client
   against the default DACL) is unverified. The identity boundary itself
   (client token resolution over a real pipe, admission policy, DACL-vs-token
   interplay) is unit-tested in-process, including a real winio
   listen/dial round trip with an own-SID DACL — but a non-elevated runner on
   a VM is still required for the denial direction.

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
| Handshake/Health/CheckConfig/Start/Stop only | **BLOCKED** | Pre-remediation this row claimed PASS — that claim was **wrong**: the serve loop called the general `dispatch()` and exposed the whole legacy handlers table (review P0). Post-remediation the service path uses an explicit five-method allowlist with typed `ERR_METHOD_NOT_ALLOWED` for everything else; proven by wire-path tests. The config-content policy gap (review P0 #2) is closed in code in round 2 (normalization + deny-by-default with a wire-level traversal matrix) and its parser-semantics bypasses (JSONC comments, case-variant keys) in round 3; full safe-Start proof on a VM is still outstanding |
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

Round 2 (config policy + identity boundary):

```
gofmt -l (changed files)                           → clean
go vet (CI tags, root package)                     → exit 0
go build (CI tags)                                 → exit 0
go test -count=20 (25 PC-100 tests: 15 round-1 +
  10 round-2, CI tags, -ldflags="-checklinkname=0") → ok  (25 × 20 runs)
go test . ./internal/xray/ ./internal/xraydns/     → all ok (regression)
git diff --check                                   → clean
```

Round 3 (parser semantics — fail-closed parsing, case-folded keys, SDDL
raw-SID trustees):

```
gofmt -l (changed files)                           → clean
go vet -a (CI tags, ./...)                         → exactly one pre-existing
                                                     finding (unreachable code,
                                                     internal/boxdns/dns_manager_windows.go:246)
go build (CI tags)                                 → exit 0
go test -count=20 (32 PC-100 tests: 15 round-1 +
  10 round-2 + 7 round-3, CI tags,
  -ldflags="-checklinkname=0")                     → ok  (32 × 20 runs)
go test . ./internal/xray/ ./internal/xraydns/     → all ok (regression)
git diff --check                                   → clean
```

Round 2 vet note: `go vet ./...` (any tags, with `-a`) has exactly one
remaining finding — a pre-existing upstream `unreachable code` duplicate
`return nil` at `internal/boxdns/dns_manager_windows.go:246` (pinned DNS
machinery, outside the allowed edit surface). The winipcfg `%w`-in-`t.Errorf`
vet debt (52 sites) was eliminated this round; beware that `go vet` caches
results per build config — a cached pass can hide a fresh finding unless run
with `-a`.

Pre-remediation suite (10 tests) passed once; the external review then found
the P0 dispatch-table exposure — fixed as described above. Round 2 added the
config-policy and identity suites (10 tests — the original "9 tests / 24
total" bookkeeping was off by one and corrected in round 3 — including the
wire traversal matrix and a real named-pipe identity round trip); round 3
added the parser-semantics suite (7 tests). Everything re-verified at
-count=20.

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

- **PC-110 typed contract supersedes the round-2 interim policy.** The
  deny-by-default JSON policy closes the known filesystem surface of the
  string-config contract, but it is still a string contract: PC-110 must
  replace it with typed parameters (the service builds its own configuration
  end-to-end) plus the versioned envelope, deadlines and formal limits. The
  interim policy is deliberately conservative — unknown path-bearing fields
  added upstream will surface as rejections until the deny list is revised
  (fail-closed direction).
- **Identity boundary scaling**: `THRONE_SERVICE_ALLOWED_SIDS` +
  Administrators is the ADR-001 admission policy; per-user SID grants at the
  DACL arrive with the installer (PC-120). VM evidence for the denial
  direction (non-elevated client refused) is still the BLOCKED item.
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

## VM evidence run (2026-09-13) — the VM-only items are executed

Disposable VirtualBox 7.2.16 VM `PC100-Evidence` (Windows 11 Pro 25H2 26200, EFI+TPM,
2nd NIC as expendable test adapter; host: Windows 11 Home, non-elevated agent shell,
no host mutations — all sc.exe/netsh/registry work inside the VM). Evidence target:
HEAD `bfd79c58` + the uncommitted PC-110 remediation tree (fingerprint in the package
manifest). Package: `D:\GLM_project\vm-evidence\evidence-package\` (raw logs + summary).

- **SCM lifecycle**: `sc create ProxyCoreService binPath="C:\spike\ThroneCore.exe service"`
  → Running as LocalSystem (PID observed, pipe `\\.\pipe\ProxyCoreService` present) →
  `sc stop` → STOPPED (process exit) → `sc start` again → duplicate stop rc 1062 →
  `sc delete` (query 1060). Done twice with two builds of the same tree.
- **Non-SCM direct run**: `ThroneCore.exe service` outside SCM fails closed
  ("service mode requested outside the Windows SCM"), nonzero exit.
- **Non-elevated client refusal**: local non-admin user `limited` (own SID, batch-logon
  granted) dials the pipe under default SDDL `D:P(A;;GA;;;SY)(A;;GA;;;BA)` → refused
  (`UnauthorizedAccessException: Access is denied`, 593 ms).
- **SDDL refusal**: same run as above — the refusal is the SDDL denial, not a service fault
  (admin dial CONNECTED as positive control before and after).
- **SDDL grant direction**: `THRONE_SERVICE_SDDL=D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;<limited-SID>)`
  via SCM Environment → limited dial CONNECTED (390 ms), admin dial still CONNECTED.
- **Full suite baseline in the clean VM**: `go test -count=1` ThroneCore + internal/xray +
  internal/xraydns with exact CI tags → ok/ok/ok (9.8s/3.5s/4.1s).
- **winipcfg suite**: 30/30 PASS on `winipcfg_test0` (elevated; adapter: static IPv4,
  DNS none on both families, IPv6 bound, RouterDiscovery=disabled to suppress VBox-NAT
  RDNSS — probe showed `fd17:625c:f037:3::3` from RA merging into `LUID.DNS()` AF_UNSPEC
  read-back and tripping TestSetDNS's count; environment artifact, not code).
- **Clean build in VM**: exact CI tags, CGO_ENABLED=0, Go 1.27.0 → exit 0,
  ThroneCore.exe sha256 `81D1A4D982323220D81B4F88819AFE2121599FC8E37AE0718816D6F272F4AB47`.

Caveats recorded honestly: one transient `sc stop` >60 s in round 2 (stops ~0 s in rounds
1/3; consistent with the earlier one-time winio ListenPipe flake); round-1 lifecycle ran on
the host-built fallback exe (same tree) after a launcher-script bug (missing `-ldflags`)
broke the first in-VM build; fixed and re-run — in-VM build PASS captured separately.

Status: the PC-100 "BLOCKED (VM evidence)" items are executed and archived. Gate 0/1 are
NOT declared closed by this note — formal closure stays the owner's call (C++ leg of Gate 0
remains compile-blind on this machine; PC-110 remediation tree is still uncommitted).
