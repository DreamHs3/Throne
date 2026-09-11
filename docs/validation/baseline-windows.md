# baseline-windows — validation record (PC-000)

Date: 2026-09-12 (night run). Machine: user Windows 10.0.28000 x64 working
machine — **not** a disposable VM. Baseline: Throne
`21b8f680b95d1dfe7906b6b64f6c3c51c263ba40`, unchanged.

Status legend: PASS (executed here, evidence below) / BLOCKED (cannot be
executed in this environment — VM/UAC/toolchain missing). BLOCKED is not PASS.

## 1. Executed here (PASS)

### 1.1 Go core build (ThroneCore), unchanged baseline

- Toolchain: Go 1.27.0 windows/amd64 (portable), protoc 31.1,
  protoc-gen-go v1.36.12, protoc-gen-go-grpc v1.6.2 — identical to CI versions.
- Exact command: see `docs/upstream-lock.md` §4/§6 (`CGO_ENABLED=0`, CI tags,
  `-checklinkname=0`).
- Result: exit 0. `ThroneCore.exe` 79 719 424 bytes,
  SHA-256 `c9da2da5ebefb26894f891849bc05ebb5d8dd7c755d3d4f780e2522d6ccd2b7f`.
  Artifact kept outside the repository.
- protobuf regenerated via protoc as CI does (generated files are
  git-ignored; nothing committed).

### 1.2 Existing Go tests (unchanged code)

`go test -ldflags="-checklinkname=0" -tags "<CI tags>" ./...` in
`core/server`:

| Package | Result |
| --- | --- |
| `ThroneCore` (egress_test.go) | ok (0.159s) |
| `ThroneCore/internal/xray` (gate_test.go) | ok (0.163s) |
| `ThroneCore/internal/xraydns` (resolver_test.go) | ok (0.128s) |
| `ThroneCore/internal/boxdns/winipcfg` | 21 tests ok; 8 **BLOCKED** (below) |

`go test` without `-checklinkname=0` fails to link (`tfo-go` linkname) —
this is a baseline build-contract fact, recorded in upstream-lock.md §4.

### 1.3 Core binary smoke (no network, no system changes)

Running `ThroneCore.exe` without `THRONE_CORE_SOCKET`:
- prints `sing-box: v1.14.1-0.20260908150512-6d1fc214c16b`, `Xray-core: 26.9.9`;
- exits with `THRONE_CORE_SOCKET not set` (expected without GUI parent);
- startup performed a read-only `restoreSystemDNS` check → `no action needed`.
  **No DNS, routes, firewall, system-proxy or TUN changes were made at any
  point of this session.**

## 2. BLOCKED in this environment (manual procedure in §4)

| Item | Why blocked |
| --- | --- |
| Qt GUI build (`Throne.exe`) | No MSVC, Qt 6, CMake, Ninja, protoc-C++ on this machine; installing them requires admin/UAC or large system-level setup — forbidden unattended |
| GUI smoke (launch, tray, theme) | Requires built GUI |
| Profile import via GUI + `CheckConfig` + core start/stop through RPC | Requires built GUI + core pair; must run in disposable VM |
| Any TUN test | Forbidden on the working machine; VM only |
| winipcfg machine-dependent Go tests (8) | Need a prepared test interface; some mutate interface/routes/DNS — VM only |
| C++ parser fixture execution | Fixture harness is C++/Qt (added for PC-030); same toolchain gap |

## 3. Parser fixtures (safe data, added for PC-000)

Location: `tests/fixtures/` — `ssh.json`, `shadowsocks.json`, `socks.json`.

All values are synthetic: `example.com` hosts, RFC-5737/1986 documentation
addresses, fake credentials (`example-user`, `example-password`), and clearly
marked synthetic key material. **No real credential, private key, or
subscription URL is present.** These fixtures are consumed by the
characterization harness (PC-030) and by manual checks.

Verification performed here (static, no rewrite): the three parsers/builders
(`src/configs/outbounds/{ssh,shadowsocks,socks}.cpp`) were reviewed against
the fixtures; expected parse values in the fixtures are derived from that
code. Findings recorded in `docs/night-run/final-report.md` §upstream defects
(socks4 `version` string/int round-trip; `shadowsocks::Build()` mutates
`plugin`/`plugin_opts` in place).

## 4. Manual procedure — disposable Windows 11 x64 VM (required for full PC-000 acceptance)

Snapshot the VM first. Do **not** use a machine with real Throne data.

1. Install toolchain (admin/UAC allowed **on the VM**, once):
   - MSVC Build Tools (Desktop C++), CMake ≥3.20, Ninja, Go 1.27.0, protoc 31.1,
     `protoc-gen-go@v1.36.12`, `protoc-gen-go-grpc@v1.6.2`.
   - Qt 6.11.2 x64 (upstream prebuilt: `throneproj/buildqt` release
     `Qt_6.11.2_x64.7z` extracted to `tools\Qt`), OpenSSL from
     `throneproj/env_windows_legacy` (record exact downloaded versions).
2. Fetch GUI build inputs:
   ```bash
   curl -fLs -o build/srslist.h https://raw.githubusercontent.com/throneproj/routeprofiles/rule-set/srslist.h
   # record srslist.h SHA-256 here: ______
   ```
3. Build core exactly as `docs/upstream-lock.md` §4; copy `ThroneCore.exe`
   next to the expected GUI location (build dir).
4. Build GUI (matches CI):
   ```bash
   export CMAKE_PREFIX_PATH="$PWD/tools/Qt/lib/cmake"
   export OPENSSL_ROOT_DIR="$PWD/tools/openssl"
   cmake -GNinja -DCMAKE_BUILD_TYPE=RelWithDebInfo -S . -B build
   cmake --build build
   ```
5. Record artifact SHA-256 for `Throne.exe` and `ThroneCore.exe` in this file.
6. GUI smoke (per EVOLUTION PC-BASE-001):
   - launch GUI (normal user, no UAC prompt expected for plain launch);
   - main window opens, tray icon present; close-to-tray works;
   - import one synthetic test profile of each type from
     `tests/fixtures/*.json` (manual import, not subscription);
   - core start/stop via GUI with the fixture profiles (TUN **off**):
     core connects over IPC, CheckConfig passes, Start/Stop succeed,
     GUI shows core running/stopped;
   - observe `QueryConnections` returns empty-but-live data; stop core;
   - exit app; verify `config/throne.db` created inside the app working dir;
   - cleanup: exit, delete the app working dir, restore VM snapshot.
   Record pass/fail per item with screenshots/log excerpts (no secrets).
7. Optional (only after snapshot, only in VM): enable TUN on one fixture
   profile, confirm core starts and traffic flows, then stop, disable,
   restore snapshot. Any TUN result stays VM-only evidence.

## 5. Acceptance mapping (PC-000)

| Roadmap item | Status |
| --- | --- |
| Reproducible build log (Go core) | PASS (this machine, logs archived) |
| Reproducible build log (Qt GUI) | BLOCKED → §4 procedure |
| Toolchain/dependency versions recorded | PASS (`docs/upstream-lock.md`) |
| Replacement SHAs recorded | PASS (`docs/upstream-lock.md` §3) |
| Smoke: GUI, profile import, core start/stop | BLOCKED → §4 procedure |
| TUN smoke in disposable VM | BLOCKED (forbidden here; §4 step 7) |
| SSH/Shadowsocks/SOCKS5 parser fixtures | PASS (files added; execution BLOCKED until PC-030 harness has toolchain) |
| Cleanup returns network | N/A here (nothing was changed); VM step records it |

Baseline roll-forward note: if any GUI smoke item fails on the VM, file the
exact failure as an upstream defect report and stop Stage-0 UI work — do not
patch baseline UI (PC-000 constraint).
