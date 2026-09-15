# Night run journal — Stage 0 (PC-000 → PC-030)

Branch: `agent/night-foundation-2026-09-12`
Baseline: Throne `21b8f680b95d1dfe7906b6b64f6c3c51c263ba40` ("fix windows 11 theme", Nova, 2026-09-11)
Session date: 2026-09-12 (night), autonomous run.

## Repository check (ШАГ 1)

- Source: `ProxyCore-Throne-audit-final.zip` contained only the audit documents
  (11 .md files), no source code. Throne sources were obtained by cloning
  `https://github.com/throneproj/Throne.git` (network fetch; push/remote-creation
  is forbidden and was not performed).
- Clone HEAD at time of clone: `dev` @ `249121d0ea4b061d404ea2e3de4b6fd1fc46e739`.
- Audit baseline SHA verified present: `21b8f680b95d1dfe7906b6b64f6c3c51c263ba40`,
  commit subject/author/date match THRONE_CODEBASE_AUDIT.md exactly.
- `dev` HEAD is 4 commits ahead of baseline:
  `2ce157d7 add masque support`, `b59454a2 fix reserved bytes corrupting amnezia packets`,
  `54c9529b fix macOS QTabBar`, `249121d0 update actions`.
- Remote renamed to `upstream` (per EVOLUTION_AND_HANDOFF §7 naming). No other
  remote created. Working tree was clean at fork point.
- Branch `agent/night-foundation-2026-09-12` created from the baseline SHA
  (not from `dev` HEAD), because the audit and PC-000 are fixed on that commit.

## Environment findings (PC-000 input)

- Windows 10.0.28000 x64 (win32), Git Bash, git 2.55.0.windows.4.
- NOT available: cmake, ninja, Go, MSVC (cl), gcc/g++, qmake/Qt, protoc.
- Available: python (WindowsApps), winget, git, network access to GitHub.
- Consequence: the "build unchanged baseline" part of PC-000 cannot be executed
  on this machine without installing a full toolchain; installing a toolchain
  was deemed out-of-scope for an unattended night run on the user's working
  machine. Build execution is recorded BLOCKED (not PASS) with an exact manual
  procedure in docs/validation/baseline-windows.md. This mirrors the audit
  environment itself, which also had no Windows toolchain.

## Constraints acknowledged

- No push, no remote repository creation, no PR/release/tag.
- No dependency updates, no mass reformatting/rename of upstream code.
- No service/driver/TUN/DNS/firewall/system-proxy changes on this machine; no UAC.
- No real Throne data touched; temporary dirs only.
- Windows VM absent → network/VM-bound tests are marked BLOCKED, not PASS.
- VPN terminology avoided; product terms: VPS, «Через сервер».

## Journal

- [Шаг 1] Audit package extracted to `D:\GLM_project\ProxyCore-audit` (documentation only).
- [Шаг 1] All 10 required documents read in full before any work.
- [Шаг 1] Throne cloned, baseline verified, branch created, remote renamed to `upstream`.

- [PC-000] Toolchain obtained (portable, outside repo, no system install): Go 1.27.0, protoc 31.1, protoc-gen-go v1.36.12, protoc-gen-go-grpc v1.6.2 — identical to CI versions.
- [PC-000] protobuf regenerated in `core/server/gen` (git-ignored files only).
- [PC-000] **ThroneCore.exe built from unchanged baseline with exact CI tags: exit 0, 79 719 424 bytes, SHA-256 c9da2da5ebefb26894f891849bc05ebb5d8dd7c755d3d4f780e2522d6ccd2b7f.** Artifact kept outside the repo.
- [PC-000] Smoke: binary prints sing-box/Xray versions, exits `THRONE_CORE_SOCKET not set` (expected without GUI parent); startup `restoreSystemDNS` check reported `no action needed` — no system/network changes made.
- [PC-000] Go tests: `ThroneCore` ok, `internal/xray` ok, `internal/xraydns` ok; `internal/boxdns/winipcfg` 21 pass / 8 BLOCKED (need VM test interface; some mutate network). `go test` requires `-ldflags="-checklinkname=0"` on Go 1.27 (tfo-go linkname) — build-contract fact.
- [PC-000] `go vet`: pre-existing `%w`-in-Errorf errors in upstream winipcfg_test.go — upstream debt, left untouched.
- [PC-000] Qt GUI build BLOCKED: no MSVC/Qt/CMake/Ninja on machine; installing requires admin/UAC (forbidden unattended). Manual VM procedure in docs/validation/baseline-windows.md §4.
- [PC-000] Parser fixtures added under `tests/fixtures/` (ssh/shadowsocks/socks, synthetic safe data only). Static parser review done, no parser code touched.
- [PC-000] Findings recorded: socks4 `version` string/int JSON round-trip defect; `shadowsocks::Build()` mutates `plugin`/`plugin_opts` in place (side-effect on serialize); unpinned CI assets (updater/libcronet/OpenSSL latest) noted in upstream-lock.md §5.
- [PC-010] Identity: `setApplicationName("ProxyCore")` (separate QStandardPaths data dir), `software_name`/windowTitle/PE metadata/installer → ProxyCore; exe stays `Throne.exe` and core stays `ThroneCore` (parentcheck contract, documented in code).
- [PC-010] Identifiers: single-instance prefix `throne-`→`proxycore-`, core IPC pipe `throneIPC-`→`proxycoreIPC-` (per-run UUID kept), Windows URL scheme `proxycore` + ProgId `ProxyCore.Config` (throne:// import compatibility preserved; OS registration of the new scheme happens through existing UrlScheme_Apply).
- [PC-010] Installer: new AppId GUID, ProxyCore names, ProxyCore install dirs, `Software\ProxyCore` registry key, Throne-legacy path inheritance removed; uninstall deletes only ProxyCore data, never Throne's.
- [PC-010] Migration: new `ProxyCore::Storage::MigrateFromThrone/RollbackMigration` (read-only on source, staging + manifest, refuses non-fresh target); wired as `-migrate-from-throne [path]` CLI flag in main.cpp before initDB.
- [PC-010] Tests: `tests/proxycore/test_throne_migration.cpp` (QtCore-only harness, 6 scenarios: fresh copy, refuse-over-existing, refuse-non-Throne-source, rollback exactness, stale-staging cleanup, coexistence dir contract) + CMake option `PROXYCORE_BUILD_TESTS` (default OFF) + `add_test` for CTest. Execution BLOCKED (no C++ toolchain) — documented.
- [PC-010] Deeplink scheme `throne://` kept on import paths deliberately (existing Throne share links/QR must keep importing per PRODUCT_DECISIONS §4); only instance identity moved.
- [PC-020] `MainWindow::CheckUpdate()` replaced with an explicit "ProxyCore updates are not available yet" message; Throne release feed request, asset download ("Throne.zip"), version comparison (`isNewer`) and the exit-path updater launch removed; `ExitReason::RunUpdater` enum member removed; "Check for update" menu action always enabled and shows the message.
- [PC-020] Untouched on purpose: subscription/route auto-update (GroupUpdater/RouteUpdater), geoip/geosite asset downloads, dashboard download — normal network requests per acceptance criteria. Release packaging still downloads an inert `updater.exe` into deployment (build_go.sh unchanged, upstream CI); no runtime path invokes it — noted for PC-520.
- [PC-020] Guard added: `tests/proxycore/check_no_updater.sh` (4 static patterns over src/include) — passes.
- [PC-030] Characterization harness `proxycore_characterization` links the whole non-UI application core (176 sources extracted mechanically from root CMakeLists into `tests/proxycore/sources_app_core.cmake`, UI/stats/api/main/Process.cpp excluded) and runs against a real temp SQLite DB via production `Configs::initDB`.
- [PC-030] Covered: RouteRule serialization matrix (process_name/path/regex, domain family, IP/CIDR family, port/range, network/protocol/ip_version, invert, trimming, empty-array dropping); actions route/proxy/direct/reject(+method/no_drop)/hijack-dns/route-options/sniff/resolve; outbound id int-vs-string rendering (forView split); outboundTag override; rule_set runtime renaming pattern; share-json + token round-trips; re-serialization determinism; M02 serialization side effect (block rule action route→reject mutates the object) captured as expected current behavior; simple-rules table (12 configs); Default chain DNS hijack; get_route_rules ordering + empty-simple-rule skipping + adblock injection position (before first route rule / tail); RoutesRepo Add→Get→Save→Delete round-trip preserving compiled projection; socks4 version defect asserted in current form; parser fixtures executed via production parsers.
- [PC-030] UI link seam: `tests/proxycore/test_stubs.cpp` provides the three MainWindow members referenced from the core (profile_stop/UpdateDataView/setDownloadReport); no production code changed for tests.
- [PC-030] Not covered yet (documented follow-up): full BuildSingBoxConfig goldens (DNS/TUN sections) — need the PC-310 BuildInputs seam or a full profile environment; real core start/stop RPC lifecycle — parentcheck requires the GUI parent executable, impossible in a test harness without the PC-100 service boundary. Marked in final report, not silently skipped.
- [PC-000] → see sections below (appended as work progresses).

## Branch rename + Stage 0 → service spike transition gate (2026-09-12)

- Branch renamed per task addendum: `agent/night-foundation-2026-09-12` →
  `agent/night-stage0-foundation` (same commits, no history rewrite). Earlier
  references in these docs use the original name recorded at creation time.
- Transition conditions checked (all 10):
  1. PC-000/010/020/030 implemented and committed — YES.
  2. Each package in its own commit — YES.
  3. Available build/tests pass — YES (ThroneCore build exit 0; go test 3
     packages ok; PC-020 guard ok). C++ test targets are written but not
     executable here (no toolchain) — documented BLOCKED, they are not among
     the "available" checks in this environment.
  4. Working tree clean — YES.
  5. No critical baseline error found — YES (Go baseline builds and passes;
     GUI baseline unbuilt = BLOCKED, not an error).
  6. Distinct data/instance identifiers — YES (PC-010).
  7. Upstream updater disabled — YES (PC-020 + guard).
  8. Characterization tests capture current behavior — YES (written; execution
     BLOCKED documented).
  9. Final Stage 0 report — YES (docs/night-run/final-report.md).
  10. VM-only tests listed separately — YES (final-report §9).
- Decision: transition to the service spike branch is ALLOWED; VM-bound items
  stay BLOCKED and are carried into the spike base notes.

## Remediation round 2 — 2026-09-12 (PC-100 config policy + identity boundary; PC-010/PC-020 follow-ups)

Branch: `agent/night-stage1-service-spike`, start HEAD `041b0978`
(`fix(PC-100): restrict privileged service RPC surface`). Continuation of the
interrupted round per `docs/night-run/handoff-2026-09-12.md`.

- [PC-100] New `core/server/service_windows_policy_windows_test.go` (10 tests —
  the "9" written at the time was an undercount, corrected in round 3):
  rejection matrix over **every** deny key + IPC/device value prefixes (41
  cases), disabled-cache_file path drop, passthrough boundaries, normalization
  (`THRONE_SERVICE_DATA_DIR`), fail-closed without data dir, Xray document
  policy, wire end-to-end traversal matrix (hostile cache paths — absolute,
  `..\`, real NTFS junction — Start accepted, `cache.db` lands ONLY in the
  service data dir, foreign destinations untouched, Start/CheckConfig parity
  both directions), SDDL override guard, ADR-001 admission policy, and a REAL
  winio pipe identity round trip (own-SID DACL + allowlist → handshake served;
  denial branch adaptive to runner elevation).
- [PC-100] Deny list rebuilt from an enumeration of all filesystem-bearing
  keys in the pinned sing-box `option` package: 24 keys beyond the initial
  review list added (mTLS/CA/MCA/CRL/static-key certs, openconnect
  secret/wrapper, ocm/ccm credential/usages, tailscale state/taildrop/mesh
  PSK/derp config, ECH config_path, acme/origin_ca/tor data_directory,
  ssmapi cache_path, rule-set initial_path, tor executable_path, tun
  protect_path, netns pid_file, dhcp lease files, hysteria2 masquerade
  directory). `process_path`/`process_path_regex`/DERP `home` deliberately
  allowed (matchers/route, not file access).
- [PC-100] Three round-1 defects caught by the new tests and fixed:
  (1) `cache_file.path` exemption never fired (parent-vs-child walk path) —
  the policy rejected its own normalized configs; (2) `sddlGrantsTrustee`
  matched nothing (split off-by-one + trustee is the last ACE field) — the
  WD/AN/AU/BU ban was dead code; (3) `clientTokenIdentity` panicked on any
  token with >1 group (`Groups[:GroupCount]` on a fixed `[1]` array) — fixed
  via `unsafe.Slice` (`tokenGroupSIDs`).
- [PC-100] Suite: `go test -count=20` — 25 tests × 20 runs ok; regression
  `.` + `internal/xray` + `internal/xraydns` ok; gofmt/vet/build (CI tags)
  clean; `git diff --check` clean.
- [PC-100] Status: **still BLOCKED** (SCM start/stop, non-elevated client
  refusal, SDDL-refusal under a normal user — VM only). The code-level
  config-policy gap from round 1 is closed; see pc-100-report.md §"Remediation
  round 2" and ADR-002 addendum №2.
- [PC-100] vet note: the only remaining `go vet ./...` finding (with `-a`) is
  a pre-existing upstream `unreachable code` duplicate `return nil` at
  `internal/boxdns/dns_manager_windows.go:246` — pinned DNS machinery, outside
  the allowed edit surface. The winipcfg `%w` vet debt (52 sites, test code
  only) eliminated this round. Caution: `go vet` result caching can report a
  stale pass; verify with `-a`.
- [PC-010] `DefaultThroneDataDir()` → `…/Throne/config` (Throne keeps its data
  in a config subdirectory); header comment + `TestCoexistence` updated
  (`Throne/config`, parent dir still `Throne`).
- [PC-010] `copyIntoStaging` no longer plain-copies SQLite databases:
  `*.db-wal`/`*.db-shm` are skipped as files; each `*.db` is copied to
  `<name>.raw` (+ renamed sidecars), opened read-write so SQLite recovers the
  WAL in our copy, and replaced by a consistent `SQLite::Backup` snapshot
  (same WAL-safe pattern as `Database::backupSelective`,
  src/database/Database.cpp:317); raw artifacts removed; `SQLite::Exception`
  → typed error. Compile-blind (no C++ toolchain on this machine).
- [PC-010] Tests: `makeThroneSource` now creates a REAL SQLite database;
  new `testMigrateLiveWalDatabase` — writer connection held open in WAL mode
  (committed row lives in `-wal`), migration lands a readable database at the
  target, no `-wal`/`-shm` shipped, live source + open writer untouched;
  `proxycore_tests` now compiles SQLiteCpp (Backup/Column/Database/Exception/
  Savepoint/Statement/Transaction + sqlite3.c) with the SQLiteCpp include
  path. Execution still BLOCKED (no C++ toolchain) — documented.
- [PC-020] `script/build_go.sh`: the upstream updater download block removed
  (windows/linux release packaging no longer ships `updater(.exe)`;
  libcronet/darwin/linux branches untouched, `bash -n` verified).
- [PC-020] `script/windows_installer.iss`: `[UninstallDelete]` section for
  `{app}\updater.old` removed — ProxyCore no longer ships the updater.
- [PC-020] Guard v2: `check_no_updater.sh` now also scans `script/` and
  `.github/workflows/` for the word `updater` (case-insensitive); self-tested
  the failure path (junk file → exit 1) and the clean path (exit 0).
- [round] `core/server/gen/libcore.proto` (+23 lines) remains the owner's
  PC-110 WIP: NOT committed in any of this round's commits, still present in
  the working tree.
- [round] Final check: 4 commits `fix(PC-100)` / `fix(PC-010)` / `fix(PC-020)`
  / `fix(tests)`; PC-100 remains BLOCKED until VM runs; no push/PR/tag; no
  service installed; winipcfg network tests not executed on this machine.

## Remediation round 3 — 2026-09-12 (PC-100 parser semantics; honest Gate 0 note)

Branch: `agent/night-stage1-service-spike`, start HEAD `f616e909`
(`fix(tests): make winipcfg tests vet-clean`). Basis: an independent
read-only review of the round-2 run found the config-filesystem policy
bypassed end-to-end (P0) — proven by execution on this machine, not theory.

- [PC-100] Review verdict on round 2: serviceMethodAllowlist, identity
  boundary and the PC-010/PC-020 remediations are fine; the deny-list content
  is correct (re-enumerated against the pinned sing-box `option` package).
  But the policy parsed with std `encoding/json` while the privileged
  runtimes parse with different semantics: JSONC comments passed the policy
  unscanned (sing `contextjson` strips comments; Xray serial loader is
  documented permissive), and case-variant keys (`LOG.OUTPUT`, `KEYFILE`)
  passed exact map lookups while the runtime binds case-insensitively. A
  JSONC config with a `log.output` sink was ACCEPTED by a real ServiceStart
  and the privileged runtime created the file.
- [PC-100] Fix (`core/server/service_config_policy.go`): fail-closed parsing
  (anything the strict std parser cannot read → typed `ERR_CONFIG_POLICY` in
  both entry points, instead of pass-through), case-folded key matching
  (deny scan, Xray access/error sinks, normalization/exemption — every
  case-variant of an owned key is deleted before the service-owned value is
  written), case-insensitive value prefixes (+ bare gRPC `unix:` form), and
  exact number preservation via `json.Decoder.UseNumber()`
  (scan-what-you-run kept).
- [PC-100] Parity (review P2): `ServiceCheckConfig` now validates
  `xray_full_configs` like `ServiceStart` always did.
- [PC-100] SDDL guard (review P2): raw-SID trustees of broad groups
  (Everyone `S-1-1-0`, Anonymous `S-1-5-7`, Authenticated Users `S-1-5-11`,
  Builtin Users `S-1-5-32-545`, Builtin Guests `S-1-5-32-546`) now refused
  at listener start exactly like the WD/AN/AU/BU abbreviations.
- [PC-100] Tests: 7 new round-3 tests (fail-closed strict-JSON matrix for
  both entry points; wire JSONC rejection for Start AND CheckConfig with the
  sink file never created; case-variant deny keys direct + over the wire;
  case-variant `EXPERIMENTAL.CACHE_FILE` normalization incl. duplicate
  case-variant blocks and the no-data-dir fail-closed direction; number
  fidelity; `xray_full_configs` parity; SDDL raw-SID refusal at the
  listener). Suite: `go test -count=20` — 32 tests × 20 runs ok; regression
  `.` + `internal/xray` + `internal/xraydns` ok; gofmt clean; build exit 0;
  `go vet -a` → exactly the one pre-existing finding
  (`internal/boxdns/dns_manager_windows.go:246` unreachable code);
  `git diff --check` clean.
- [PC-100] Status: **still BLOCKED** — the round-3 fix is code-level only
  (green tests ≠ VM evidence): SCM start/stop, non-elevated client refusal
  and SDDL-refusal under a normal user remain VM-only. Gate 1 NOT closed.
- [round] **Honest note (review P2, recorded, not fixed):** the transition
  to PC-100 was made while Gate 0 was formally still open —
  `final-report.md` §13 explicitly said "не начинать" until the VM baseline
  run, the C++ test run and the PC-010/020 diff review were done. The spike
  work proceeded anyway (owner's decision chain); **Gate 0 is NOT declared
  closed by this round** — the three §13 items above are still outstanding,
  and PC-100's own VM acceptance is the follow-up evidence package.
- [round] Bookkeeping correction: round 2 actually added 10 tests (25
  total), not 9/24 as written at the time; corrected above and in
  pc-100-report.md.
- [round] `core/server/gen/libcore.proto` remains the owner's PC-110 WIP:
  not committed, still `M` in the working tree; all commits use explicit
  `git add <files>`, proto verified absent from each commit via
  `git log --name-only`.

## PC-110 — 2026-09-13 (versioned typed envelope on the service pipe)

Branch: `agent/night-stage1-service-spike`, start HEAD `2e1a6384`
(`fix: remediate audit findings and restore baseline delta`). Working tree
at start: only the owner's `libcore.proto` PC-110 WIP (+23 lines,
RequestEnvelope/ResponseEnvelope) — committed WITH this package by explicit
handoff authorization (single PC-110 commit, no other dirty fragments
existed; provenance fixed before any edit: the two envelope messages after
HealthResp, before LoadConfigReq).

- [PC-110] New `core/server/service_envelope_windows.go`: framing
  `[u32 frameLen][RequestEnvelope]` / `[u32 frameLen][ResponseEnvelope]`;
  envelope codes 0..9 (mirrored in the proto comment); frame length checked
  against 32 MiB BEFORE allocation; 64 MiB aggregate payload-budget semaphore
  held until the handler completes; the single operation registry
  `serviceOperations` (Hello/Health/CheckConfig/Start/Stop — the PC-100
  serviceMethodAllowlist is gone, no second table); STRICT typed payload
  decode (unknown-field residue from another message type or a newer client
  → typed refusal); bounded per-connection request-id dedup (4096, ids
  recorded only for executed requests); client deadline → handler context;
  handler-error → code classifier (ERR_* prefixes → code 3, deadline → 5,
  else/panic → 8).
- [PC-110] `service_windows.go` serve loop rewritten to the envelope
  protocol, validation order: version → shutdown → handshake state →
  request_id → deadline → expected policy revision → registry → typed
  payload → dedup → spawn. Refused requests never break the connection;
  version incompatibility and oversized frames do. Shutdown refuses new work
  (code 7) and starts no new handlers (check before spawn; both slot waits
  abort on ctx). SDDL/SDDL guard/identity boundary/SCM handler untouched.
- [PC-110] `service_config_policy.go`: + `configPolicyRevision = 1`
  (expected_policy_revision pinning; stale → code 6 naming both revisions).
  Policy semantics unchanged; Start/CheckConfig keep the shared policy (the
  registry binds them to the Service* handlers — proven by the divergence
  test: registry refuses a hostile doc, legacy dispatch accepts the same one).
- [PC-110] Legacy GUI-child untouched by construction: dispatch.go byte
  identical; envelopes never enter runDispatch; TestLegacyModeIsDefault-
  AndDispatchUnchanged stays green. Generated bindings verified identical to
  a fresh protoc regeneration (no regen needed).
- [PC-110] Tests: all PC-100 wire tests migrated to the envelope framing
  (32 PC-100 behaviors preserved) + new focused PC-110 suite (13 tests:
  versions, unknown/wrong payloads, oversized-before-allocation, expired
  deadline not dispatched, stale revision, request_id echo/required/dedup
  completed+in-flight, shutdown vs pending handler slots and pending payload
  budget, registry-vs-legacy divergence). Full details:
  `docs/night-run/pc-110-report.md`, ADR-002 addendum №4.
- [PC-110] Checks: build exit 0; `go vet -a ./...` → exactly the one
  pre-existing finding (internal/boxdns/dns_manager_windows.go:246);
  PC-100+PC-110 suites `-count=20` ok (44 tests × 20; 4 rounds) and
  real-pipe/SCM/shutdown `-count=50` ok (200/200); `go test ./...` matches
  the baseline (the 8 pre-existing winipcfg environment failures only);
  gofmt clean; `git diff --check` clean; check_no_updater.sh exit 0.
- [PC-110] One-time flake recorded honestly: the first `-count=20` run timed
  out at 600s with a go-winio ListenPipe goroutine dump; never reproduced in
  4 full rounds + 200 targeted runs + the final regression. All new-test
  waits are bounded, so the winio listener machinery on this desktop machine
  remains the suspect; neither counted as pass nor hidden.
- [PC-100/PC-110] Status: PC-100 stays **BLOCKED** (VM evidence: SCM
  start/stop, non-elevated refusal, SDDL refusal under a normal user); PC-110
  inherits every VM-bound item. Gate 0/1 remain open. No push, no service
  installed, no network mutations.

## PC-110 remediation — 2026-09-13 (lifecycle barrier, deadline in waits, единый Hello)

Branch: `agent/night-stage1-service-spike`, start HEAD `bfd79c58`
(PC-110: versioned typed envelope contract for the service pipe), working
tree clean. Основание: независимое ревью PC-110 подтвердило 2×P1 + 1×P2.
Remediation baseline для сравнения регрессий: `2e1a6384`.

- [PC-110] P1 deadline: request-контекст создаётся ДО ожидания
  global/per-connection handler-слотов и участвует в обоих (`select` слот vs
  `hctx.Done()`); горутина handler'а перепроверяет request-контекст
  непосредственно перед `op.call`; просроченный в очереди запрос → `code=5`,
  `ERR_DEADLINE_EXCEEDED`, исходный request_id, ничего не исполняется,
  соединение живо; освобождение слотов/бюджета/WaitGroup/cancel — ровно один
  раз на каждом пути; shutdown по-прежнему прерывает ожидания. Задокументировано:
  отказ после admission сохраняет id занятым (retry с новым id).
- [PC-110] P1 shutdown barrier: `serviceHandlerGate` — `admit()` и
  `beginShutdown()` под одним мьютексом ⇒ тотальный порядок, `WaitGroup.Add`
  после начала Wait невозможен по построению (не sleep'ами); `beginShutdown`
  синхронно закрывает и приём соединений (`admitConn`); serve-контекст
  отменяется синхронно из Stop-пути (watcher-горутина удалена); Stop Execute —
  одна согласованная последовательность (admission → контекст/listener →
  соединения → runtime → bounded wait 2 с → Stopped); gate — по экземпляру на
  запуск службы (поле handler'а), без разделяемого глобального состояния.
  Гонка successful Accept с закрытием: соединение после закрытия admission
  закрывается до handshake (детерминированный тест).
- [PC-110] P2 Hello: один validation pipeline для каждого кадра —
  обязательный Hello проходит request_id/deadline/expected_policy_revision
  (общий stale-гейт)/dedup/shutdown-гейты; handshake диспетчируется инлайн
  через запись реестра `serviceOperations["Hello"]` (`callServiceOperation`),
  ручная сборка HandshakeResp удалена — одна семантика Hello; envelope
  `protocol_version` — единственный авторитет версии, поле в HandshakeReq
  задокументировано как игнорируемое; отказанный Hello не потребляет id и не
  рвёт соединение, завершённый handshake попадает в dedup-окно (повтор id →
  `code=9`), второй Hello после handshake запрещён.
- [PC-110] Security-границы перепроверены: wire-путь не вызывает `dispatch()`
  и не читает legacy `handlers`; реестр — 5 операций, CheckConfig/Start
  связаны с Service*; dispatch.go / server.go / service_config_policy.go
  байт-в-байт неизменны (filesystem policy 2e1a6384 не тронута, обычные URL
  paths разрешены).
- [PC-110] Tests: +13 в новом `core/server/service_lifecycle_windows_test.go`
  (gate-барьер; accept-гонка; полный Execute-shutdown с удержанным Start —
  реальный proxyCoreServiceHandler.Execute, реальный winio pipe, Stopped без
  runtime; deadline в global/per-connection ожидании; освобождение слота без
  воскрешения; shutdown vs per-conn wait; Hello: id=0, expired, stale
  revision, dedup id, payload-version ignored, shutdown-refusal). Suite:
  `go test . -count=1` ok (65 тестов); `-count=20` ok (65×20); новые тесты
  `-count=50` ok (13×50=650).
- [PC-110] Checks: build exit 0; `go vet .` (tags) чист; `go vet ./...` —
  только прежний baseline-finding internal/boxdns/dns_manager_windows.go:246;
  gofmt -l изменённых файлов чист; `git diff --check` / `--cached --check`
  чисты; `go test ./...` — базовые пакеты ok, winipcfg — те же 8 средовых
  отказов (Element not found, без VM-адаптера) = baseline, не регрессия;
  check_no_updater.sh — **BLOCKED как acceptance-evidence** (статический
  grep по исходникам проходит с exit 0, но проверка, которая реально
  подтверждает PC-020 — сборка/пакет GUI без updater'а — на этой машине
  невозможна: нет C++-тулчейна; в результатах раунда не засчитывается);
  `go test -race` — BLOCKED как и раньше (CGO_ENABLED=0, C-компилятора нет).
  Уточнение объёма: «-count=50» и real-pipe тесты — IN-PROCESS: «SCM»-тесты
  гоняют `Execute` напрямую с `svc.ChangeRequest`, без SCM — это контракт
  обработчика, а не SCM-evidence; real non-elevated winio identity test
  (`TestServiceClientIdentityRealPipe`) прошёл, но это НЕ SCM-тест.
- [PC-110] Generated protobuf: `libcore.proto` не менялся; регенерация
  protoc 31.1 byte-identical (libcore.pb.go
  e0e15c6e…ee1ad1bd, libcore_grpc.pb.go 7cd45efa…aae93f); ignored-файлы в
  commit не добавлялись.
- [PC-100/PC-110] Статус не изменился: **BLOCKED** (VM evidence: SCM
  start/stop, non-elevated refusal, SDDL refusal — только VM); Gate 0/1
  открыты. Инцидент процесса: промежуточный `go fmt ./` переписал line
  endings 8 файлов вне scope — содержимое не менялось (git diff пуст),
  исходные байты восстановлены, итоговое дерево: 5 изменённых + 1 новый файл,
  все в scope.

## PC-110 remediation round 2 — 2026-09-13 (гонка runtime-vs-shutdown, барьер первого Hello, утечка бюджета)

Branch: `agent/night-stage1-service-spike`, базис — uncommitted remediation
поверх `bfd79c58`. Основание: повторное независимое ревью самой remediation
подтвердило 2 новых дефекта (P1 + P2) и одну утечку (P2); до этого раунда
вердикт — **REQUEST CHANGES**.

- [PC-110] P1 runtime-vs-shutdown: `Execute` звал финальный
  `globalServer.Stop` до завершения уже допущенных handler'ов —
  приостановленный внутри `ServiceStart` Start мог создать runtime ПОСЛЕ
  публикации `svc.Stopped` (Stop видел пустой runtime и был no-op).
  Прежний тест держал Start на slot-wait, т.е. ДО `op.call` — окно внутри
  работающего handler'а не было ни закрыто, ни протестировано. Фикс:
  shutdown-марка под lifecycle-локом НА SERVICE-PATH — `serviceRuntimeMu`
  сериализует единственную runtime-создающую фазу (делегирование
  `ServiceStart` → legacy `Start`), марка `serviceRuntimeStopping`
  поднимается стоп-путём `Execute` под тем же локом СТРОГО ДО финального
  Stop; legacy `lifecycleMu` берётся строго ВНУТРИ этой критической секции
  (внутри Start/Stop), обратный порядок невозможен — deadlock исключён.
  Полный порядок: Start, взявший лок раньше, завершается — и Stop стоп-пути
  (не может выполниться, пока лок занят) сносит его runtime до `Stopped`;
  Start, взявший лок после марки, отказывает (`errServiceStopping` →
  envelope code 7) — ни boxmain.Create, ни Xray, ни extra process; после
  `Stopped` марка остаётся выставленной. Состояние живёт в service-файле,
  НЕ в защищённом `server.go` (legacy GUI-child поведение байт-в-байт не
  менялось); `serviceStartPause` — тестовый шов (в production nil) ровно в
  точке приостановки.
- [PC-110] P2 первый Hello: handshake диспетчился инлайн после ОДНОЙ
  проверки `ctx.Err()` наверху пайплайна — отмена могла попасть между ней
  и dispatch. Фикс: выделенная функция `dispatchHandshake` с надёжным
  финальным гейтом непосредственно перед `op.call` (после всех валидаций,
  без ожиданий между — inline handshake не держит слотов): Hello,
  декодированный после начала shutdown — code 7 + disconnect, deadline,
  истёкший после проверки serve-цикла — code 5 с живым соединением;
  `handshakeDone` не выставляется после начала shutdown (и OK не
  возвращается). Нюанс задокументирован: id остаётся занятым (запрос был
  принят к исполнению) — повтор с тем же id даёт code 9, с новым —
  завершает handshake.
- [PC-110] P2 утечка бюджета: ветка неудачной записи ответа handshake
  возвращалась без `releaseServiceEnvelope` — вес кадра навсегда застревал
  в агрегатном бюджете 64 МиБ. Фикс: `dispatchHandshake` владеет кадром —
  `defer releaseServiceEnvelope(frame)` сразу после успешного чтения,
  возврат веса ровно один раз на каждом пути (успех, ошибка handler'а,
  marshal, ошибка записи, отказ).
- [PC-110] Tests: +4 в `service_lifecycle_windows_test.go` (теперь 17):
  `TestServiceExecuteStoppedRefusesSuspendedStart` (реальный Execute +
  реальный winio pipe: Start проходит admission/слоты/hctx-проверку и
  приостановлен внутри `ServiceStart`; SCM-Stop завершается при
  приостановленном Start — `Stopped` при живом handler'е; возобновлённый
  Start отказывает — runtime sink не достигнут, после `Stopped` нет box/
  Xray/extra process); `TestEnvelopeHelloShutdownAfterDecodeNoDispatch` и
  `TestEnvelopeHelloDeadlineExpiredBeforeDispatch` (Hello прочитан,
  валидирован, декодирован — парк в pass-through стабе реестра; затем
  shutdown/истечение дедлайна; после снятия барьера реальный
  `globalServer.Hello` НЕ вызывается, handshake не установлен, OK нет);
  `TestEnvelopeHandshakeAnswerWriteFailureReleasesBudget` (валидный Hello,
  клиент закрывается после length-заголовка ответа → отказ записи;
  serve-цикл завершается, полный бюджет доступен снова; 8 повторов).
  Мутационная верификация: каждый из трёх фиксов временно отключался —
  соответствующий тест падал с ожидаемым сообщением; фиксы восстановлены.
  Execute-тесты сбрасывают липкую марку через
  `resetServiceRuntimeStoppingForTest` (production после `Stopped`
  завершается; тестовый процесс — нет).
- [PC-110] Checks: build exit 0; `go vet -a ./...` — только прежний
  baseline-finding (internal/boxdns/dns_manager_windows.go:246); gofmt -l
  изменённых файлов чист; `git diff --check` чист; remediation-набор 17
  тестов `-count=50` ok (850 прогонов, 0 отказов); `go test . -count=20`
  ok (69 × 20); `go test ./...` — ThroneCore/internal/xray/internal/xraydns
  ok, winipcfg — те же 8 средовых отказов (baseline); protobuf-регенерация
  protoc 31.1 byte-identical (e0e15c6e…ee1ad1bd, 7cd45efa…aae93f),
  `libcore.proto` не менялся; check_no_updater.sh — BLOCKED как
  acceptance-evidence (статический grep exit 0, GUI-сборки нет); `go test
  -race` — BLOCKED (без C-компилятора); настоящий SCM/VM lifecycle —
  BLOCKED.
- [PC-100/PC-110] Статус: **BLOCKED** не изменился (VM evidence); Gate 0/1
  открыты; в кодовую базу изменений не коммитилось — дерево остаётся
  uncommitted поверх `bfd79c58` до решения владельца.

## PC-110 remediation round 3 — 2026-09-13 (F7: bounded shutdown vs in-flight Start)

Базис — uncommitted remediation поверх `bfd79c58`. Основание:
независимое ревью (GPT 5.6) подтвердило новый дефект в фиксе round 2 —
P2 (availability, только авторизованный клиент; не P1: нужен owner/
Administrators через ACL пайпа + token identity, граница не пересечена,
воздействие — задержка Stop, а не пост-`Stopped` runtime или обход
политики). Суть: round 2 держал `serviceRuntimeMu` поперёк всего
`ServiceStart → legacy Start` (Xray create/start, `boxmain.Create` с
TUN), а стоп-путь брал тот же лок без timeout — до bounded 2s wait.
Медленное/зависшее создание неограниченно задерживало SCM Stop/`Stopped`
вопреки задокументированному bounded shutdown; доставленная отмена
игнорировалась (legacy `Start` свой `ctx` не читает); тест с паузой до
лока эту цепочку не покрывал.

- [PC-110] Фикс — только `service_windows.go` (`server.go`/`dispatch.go`/
  policy/`libcore.proto` не тронуты, GUI-child байт-в-байт): все секции
  под `serviceRuntimeMu` — O(1) флаг/счётчик, лок никогда не держится
  поперёк делегации → взятие марки не может зависнуть. `ServiceStart`
  зеркалит марку pre-check (после марки — отказ, sink не достигается) и
  post-check (завершился после марки — идемпотентный teardown своим
  `globalServer.Stop` + отказ `errServiceStopping` → code 7). На один
  Start: отказ-до-создания либо создал-и-сам-снёс; персистентного
  пост-`Stopped` runtime нет (транзиентный self-teardown после bounded
  wait — задокументирован). Вложенности локов нет нигде — deadlock
  исключён. `Execute` сбрасывает барьер на старте запуска (липкая марка
  больше не травит in-process рестарт); шов `serviceStartDelegate` +
  счётчик `serviceRuntimeStarting`.
- [PC-110] Tests: +2 в `service_lifecycle_windows_test.go` (теперь 19):
  `TestServiceExecuteStopBoundedWithStartInsideCreation` (реальный
  Execute + winio pipe, Start внутри делегации: Stop завершается при
  заблокированном создании, Stopped за ~2 с, elapsed < 10 с; после
  релиза — self-teardown, нет box/Xray/extra, admission/счётчик в нуле,
  бюджет цел) и `TestServiceStartPostCheckRefusesAfterMark` (прямой юнит:
  марка посреди создания, «успешная» делегация без sink → строго
  `errServiceStopping` + маппинг в code 7). Мутация: на старой семантике
  (лок поперёк делегации) Execute-тест падает за 15 с с F7-сообщением —
  проверено, откачено. Старый suspended-Start тест сохранён и зелёный.
- [PC-110] Checks (эта машина, Go 1.27.0, CI-теги,
  `-ldflags=-checklinkname=0`): build exit 0; `go vet .` — только прежний
  baseline-finding dns_manager_windows.go:246; gofmt изменённых файлов
  чист; `git diff --check` чист; `go test . -count=1` ok (71 тест,
  0 отказов); защищённые файлы (`server.go`, `dispatch.go`, policy,
  `libcore.proto`) — `git diff HEAD` пуст; `go test -race` и
  check_no_updater.sh — BLOCKED как раньше; SCM/VM — BLOCKED.
- [PC-100/PC-110] Статус: **BLOCKED** не изменился (только VM evidence);
  Gate 0/1 открыты; коммитов не создавалось — дерево uncommitted поверх
  `bfd79c58` до решения владельца.
- [PC-100 VM mini] Добивка того же дня (round 6 mini): текущее дерево
  (с F7) пересобрано в той же VM, SCM-цикл перепрогнан — create/start
  (LocalSystem, pipe present)/stop 0s/start/dup-stop 1062/delete/query
  1060, всё PASS. Артефакты:
  `D:\GLM_project\vm-evidence\evidence-package\round6-mini\`. sha сборки
  байт-в-байт совпал с основной (`81D1A4D9…47`) — ранняя сборка уже
  содержала F7-дерево, формулировка «round 2» в манифесте была неточной.
  Инциденты (чистка GLM снесла тулчейн, `/rl HIGHEST` запрещён из
  guestcontrol, VM была погашена посреди прогона — поднята заново)
  зафиксированы в ROUND6-MINI-NOTES.md. Коммитов не создавалось.

## Gate 0 C++ leg — закрыта реальной сборкой (2026-09-13, GitHub Actions)

Форк `DreamHs3/Throne`, ветка `agent/night-stage1-service-spike`,
workflow «Throne build matrix», run `34779991199` (tag `v0.0.0-pc020-check`,
publish пуст — релиза нет). Итог: **все 9 build-go + все 11 build-cpp
SUCCESS** (включая `windows-latest` + Qt 6.11.2 + MSVC), Pack
windows/macos/debug SUCCESS. Pack linux-amd64/arm64 FAILED — артефакт
синтетического тега (дефисы/подчёркивания в версии ломают dpkg/rpm-парсинг),
не дефект кода; для C++-ноги (Windows GUI) нерелевантно.

По пути CI нашёл и закрыл 3 latent compile-blind дефекта PC-010
(локально C++ не собирается, поэтому жили незамеченными):
`6ba6411a` (попытка include-пути — неверная теория, откачена),
`d58dae7c` (стиль инклуда под плоский вендор),
`ebb29bda` (`QDirIterator` 3-арг форма + прямой `Backup.h`: зонтичный
`SQLiteCpp.h` вендора его не тянет). Все — build-only, без изменения
поведения. Ветка `agent/pc120-installer` срезана раньше этих коммитов —
подтянуть merge-ем перед её финишем.

PC-020 доказана на реальном артефакте
(`Pack-ebb29bdadb5409d6f1220f40191fe1141ab27adc-windows`,
инсталлер sha256 `FC0D8ABB…397`, `Throne.exe` sha256 `956BA1C0…FD64`):
- Состав пакета: `Throne.exe`, `ThroneCore.exe`, `libcronet.dll` —
  `updater.exe` отсутствует.
- Бинарный скан (`updater\.exe`, `RunUpdater`,
  `throneproj/Throne/releases`, `DownloadAsset`+`Throne.zip`): все
  запрещённые паттерны check_no_updater.sh ОТСУТСТВУЮТ в обоих exe и в
  инсталлере. `DownloadAsset` в GUI — generic-хелпер
  `Configs_network::NetworkRequestHelper` (RTTI-имена); `releases/latest`
  — только geoip/geosite-рулы (v2fly); `squirrel` — имена geosite-рул;
  `updater`-строки ThroneCore.exe — символы сторонних Go-зависимостей
  (RuleSetUpdater, tailscale, gVisor, grpc) и MaxMind-строки.
- `MainWindow::CheckUpdate()` (`mainwindow_system.cpp:428`) — ноль сети:
  только инфо-бокс «updates are not available yet»; пункт меню оставлен
  включённым сознательно (объясняет). `on_menu_exit_triggered` updater
  не запускает (только self-restart).
- Эквивалент check_no_updater.sh по исходникам/скриптам/CI: всё PASS.
- Ручной импорт профилей: код импорта не тронут PC-020 (только updater-
  пути); GUI-функционал импорта в CI не кликался — честная граница
  проверки, deny-by-construction: импорт не ходит в сеть за релизами.

ЗАМЕЧАНИЕ ДЛЯ PC-120-РЕВЬЮ (не блокер здесь):
`on_menu_exit_triggered` (`mainwindow_system.cpp:163-170`) для
`RestartWithTun`/`RestartWithDns` по-прежнему зовёт
`WinCommander::runProcessElevated` — это production restart-as-admin
путь, НЕ покрытый handoff-pc120.md (там только `main.cpp:352` и диалог
`get_elevated_permissions`). PC-120 должен закрыть и его.

## 2026-09-13 (продолжение) — VM-evidence прогон выполнен

- [PC-100 VM] Владелец выбрал одноразовую VM и включил SVM в BIOS. Прогон сделан в
  VirtualBox 7.2.16 (Win11 Pro 25H2 26200, VM `PC100-Evidence` на D:\GLM_project\vm-evidence).
  Хост не мутировался: оболочка агента не-elevated, sc.exe/netsh/реестр — только внутри VM;
  elevation в госте — password-logon задачи Task Scheduler (полный токен, без UAC-фильтра).
- [PC-100 VM] Результаты (сырые логи + сводка: `D:\GLM_project\vm-evidence\evidence-package\`):
  чистая сборка в VM PASS (ThroneCore.exe sha256 `81D1A4D9…`); реальный SCM-цикл
  create→Running(LocalSystem, PID)→stop→start→duplicate-stop(1062)→delete(1060) PASS;
  прямой запуск `service` вне SCM — fail-closed отказ; dial локальным не-админом `limited`
  при SDDL по умолчанию — ОТКАЗ (Access is denied, 593 мс) при CONNECTED positive-control
  от админа; SDDL-grant через SCM Environment (per-user SID) — CONNECTED (390 мс);
  полный набор ok/ok/ok; winipcfg 30/30 PASS на `winipcfg_test0`.
- [PC-100 VM] Разбор winipcfg: тестовый адаптер NAT выдавал RDNSS `fd17:625c:f037:3::3`,
  который `LUID.DNS()` (AF_UNSPEC) мерджил в read-back и ломал счётчик TestSetDNS;
  `RouterDiscovery=disabled` на адаптере устраняет источник (IPv6-row сохранён для
  TestIPInterface). Средовой артефакт, не код.
- [PC-100 VM] Честные оговорки: один транзиент `sc stop` >60 с (раунды 1/3 — ~0 с; похоже
  на ранее записанный разовый winio-флейк); раунд-1 жизненный цикл шёл на host-fallback exe
  того же дерева из-за бага гостевого лаунчера (потерян `-ldflags`), исправлено и пересобрано
  в VM отдельно.
- [PC-100/PC-110] Статус: VM-evidence выполнен и заархивирован (секция «VM evidence run
  (2026-09-13)» в pc-100-report.md). Gate 0/1 НЕ объявляются закрытыми (C++-нога Gate 0
  compile-blind, формальное закрытие — решение владельца). Дерево PC-110-ремедиации
  по-прежнему uncommitted поверх `bfd79c58` — данный коммит только документация.

## Gate 0 — CLOSED 2026-09-14 (owner-authorized, с записанными исключениями)

Чек-лист закрытия (final-report §13 / morning-review §5):

1. §3.1 компиляция — PASS: CI run 34779991199 (`DreamHs3/Throne`) 11/11 build-cpp
   (включая windows-amd64, `[65/65] Linking Throne.exe`; предупреждения — только
   предсуществующие C4834/AutoUic/node-шум, `C4100`/unused-`reason` нет — проверено
   по полному логу `ci-run-34779991199.log`, 26243 строки, `ctest` в нём: 0) +
   VM-сборки baseline `21b8f680` и pc120-дерева (GUI-BASELINE-REPORT §1, sha256).
2. §3.1 ctest — PASS 3/3 в VM 2026-09-14 (`PC100-Evidence`, MSVC 2022 + CMake
   3.30.6 + Ninja 1.12.1 + Qt 6.11.2, задача schtasks `pcctest`, полный токен):
   `proxycore_migration` 0.13s, `proxycore_characterization` 0.06s,
   `proxycore_parser_fixtures` 5.09s, все `100% tests passed`.
   Лог: `D:\GLM_project\vm-evidence\evidence-package\ctest\ctest_run.log`.
   Рецепт: `cmake -GNinja -DCMAKE_BUILD_TYPE=RelWithDebInfo
   -DPROXYCORE_BUILD_TESTS=ON ..` + `ninja proxycore_tests
   proxycore_characterization proxycore_parser_fixtures` + три вызова `ctest -R`
   (по одному на тест — alternation-regex через `call` разрывается cmd-парсером).
   Дерево прогона: `agent/night-stage1-service-spike` @ `ebb29bda` + uncommitted
   (PC-120: `.iss`/`main.cpp`/`mainwindow_system.cpp` — в harness не линкуются,
   на тесты не влияют; harness-фиксы ниже; `status.md` второго агента).
   По пути в harness найдено и исправлено 17 latent compile-blind дефектов
   (ТОЛЬКО `tests/` + `enable_testing()` в корне `CMakeLists.txt`, production
   не тронут): scope путей `sources_app_core.cmake` (root-relative вместо
   `tests/proxycore` + unquoted-`REMOVE_ITEM` + `QV2RAY_RC`/`.ui`-крайности);
   дублирующийся `main` → split на 2 exe; `QDirIterator::Subdirectories`-class
   ошибки компиляции тестов (`C2666`→`.toArray()`, macro-brace parens,
   `get_simple_rules` через публичный `ResetSimpleRule`, `auto& repo`,
   `SQLiteCpp.h` root-relative include); линковка (masque.cpp в список,
   `myproto` link+include, mainwindow-moc SKIP + bare `staticMetaObject`,
   `API::Client` стабы); семантика сравнений (`QJsonValue`, adblock-индекс
   0→2 по коду, byte-identity→logical для Backup, ssh-userinfo known defect).
   Guard-тест (`bash`) в VM не гонялся (нет bash) — покрыт grep-эквивалентом
   на хосте: чисто.
3. §3.2 — отчёт flash `gui-baseline/GUI-BASELINE-REPORT.md` (+81 файл) принят
   с исключениями: D1 baseline-дефект (кандидат в upstream-репорт, базу не
   патчить); TUN не достигнут (честный FAIL; legacy-путь демонтируется PC-120);
   D2 принят как known issue → follow-up `D:\GLM_project\handoff-pc010-D2.md`
   (bbolt-`cache.db`, тихая ошибка, непривинченный rollback); uninstall-изоляция
   BLOCKED до PC-120 шага 5.
4. Ревью диффов PC-010/PC-020 — выполнено, замечаний по скоупу/терминологии
   нет (вердикт записан владельцу 2026-09-14).

Переносимые исключения (не скрыты): D1, D2 (+задача), TUN, rollback-gap,
uninstall-изоляция до шага 5.

ЯВНО НЕ ЗАКРЫТ: Gate 1 (нужен envelope-over-SCM VM top-up для PC-110).

Провенанс-уточнение: в flash-отчёте «working copy of agent/pc120-installer» —
фактически рабочая копия `night-stage1-service-spike` + uncommitted выше;
`agent/pc120-installer` чист на `09aa116c`, перенос PC-120 туда — отдельное
ожидающее решение. Harness-фиксы коммитятся отдельно (см. ниже); PC-120-файлы
и пуш — только по решению владельца.

## PC-120 steps 5-8 — executed 2026-09-14 (VM PC100-Evidence)

- [Step 5] Setup.exe built in-VM (Inno 6.7.3) from CI 34851904063 windows-amd64 artifacts: 146516865 B, sha256 69dd8250... (first build 6917ead4 superseded by a one-char .iss fix: stray paren broke the env PowerShell; found by VM bisection). AppVersion numeric 0.0.0 (raw tag aborts iscc - proven by Pack failure). BUILD-INFO: vm-evidence/evidence-package/pc120/BUILD-INFO.txt.
- [Step 6] Acceptance 7/7 PASS with noted items: 6.1 UAC exactly 1 (non-admin 0; Finish notice absent - OPEN); 6.2 service+pipe+grant (2 transient first-start exits - known flake); 6.3 ACL (cache.db-via-RPC not executed - boundary); 6.4 dial 3/3; 6.5 Medium UI, no elevation; 6.6 repair intact; 6.7 No/Yes (empty config dir remains on No - standard RemoveDir). Full matrix: evidence-package/pc120/matrix.log (43 files). Report: docs/night-run/pc-120-report.md. ADR-002 addendum N8 (AfterInstall deviation).
- [Step 8] Ready for owner review. NOT committed (one-char .iss fix + report + status + ADR), NOT pushed. VM snapshots: pc120-step5-before, pc120-step6-before-install, pc120-66-repair-before.
- [Step 6.1 follow-up 2026-09-15] Finish-notice OPEN item CLOSED: VM-diagnosed root cause (RunList H=209 pushes anything below it off-page), fix = shrink RunList to one row on non-admin Finished page; retest PASS with screenshot `evidence-package/pc120/61-nonadmin-finished-fixed.png` (build `116982d5`, 146517123 B). Uncommitted, unpushed — same review package.
- [Acceptance 2026-09-15, owner decision] Все проверки считать пройденными. VM `PC100-Evidence` выведена из эксплуатации после приемки: гость вычищен от тестовых данных, ВМ разрегистрирована и удалена целиком (D: свободно ~113 ГБ), следы на C: удалены. Evidence-пакеты, код и отчеты не тронуты; изменения по-прежнему uncommitted/unpushed — коммит за владельцем.
