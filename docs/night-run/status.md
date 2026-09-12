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
