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
- [PC-000] → see sections below (appended as work progresses).
