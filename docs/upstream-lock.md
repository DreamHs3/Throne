# upstream-lock — ProxyCore fork baseline

Created: 2026-09-12 (night run, branch `agent/night-foundation-2026-09-12`).
This file records the exact upstream state ProxyCore forked from and the exact
tool/dependency revisions needed to reproduce builds. Do not update
dependencies inside a feature package (PC-000 rule).

## 1. Upstream source

| Item | Value |
| --- | --- |
| Repository | https://github.com/throneproj/Throne.git |
| Baseline commit (audit baseline) | `21b8f680b95d1dfe7906b6b64f6c3c51c263ba40` |
| Commit subject / author / date | `fix windows 11 theme` / Nova / 2026-09-11 18:28:54 +0330 |
| Upstream default branch at fork time | `dev` @ `249121d0ea4b061d404ea2e3de4b6fd1fc46e739` (4 commits ahead of baseline; not part of this fork) |
| License | GNU GPL v3 (LICENSE in repo) |

The fork branch `agent/night-foundation-2026-09-12` was created **from the
baseline commit**, not from `dev` HEAD. Commits between baseline and dev HEAD
at fork time: `2ce157d7 add masque support`, `b59454a2 fix reserved bytes
corrupting amnezia packets`, `54c9529b fix macOS QTabBar`, `249121d0 update actions`.

## 2. Toolchain versions

### Required by the project (from sources/CI)

| Tool | Version | Source of truth |
| --- | --- | --- |
| CMake | ≥ 3.20 | `CMakeLists.txt` `cmake_minimum_required(VERSION 3.20)` |
| C++ standard | C++20 | `CMakeLists.txt` (`CMAKE_CXX_STANDARD 20`) |
| Qt (GUI) | 6.11.2 (primary), 6.2.13 (windowslegacy), 6.2.0 (Linux system Qt), 6.4.3/6.11.1 (legacy mac / linux-arm) | `.github/workflows/build.yml` matrix. **Qt Quick/QML is not used** by the build |
| Go (core) | declared `go 1.26.0` in `core/server/go.mod`; CI builds with `1.27.0` | go.mod + build.yml |
| protoc | 31.1 | build.yml (`arduino/setup-protoc@v3`) |
| protoc-gen-go | v1.36.12 | build.yml |
| protoc-gen-go-grpc | v1.6.2 | build.yml |
| C++ compiler (Windows) | MSVC via `ilammy/msvc-dev-cmd@v1` (x64, arm64, x86) | build.yml |
| Build generator | Ninja (`cmake -GNinja -DCMAKE_BUILD_TYPE=RelWithDebInfo`) | build.yml |
| Packaging (Windows) | Inno Setup (`script/windows_installer.iss`) + `script/deploy_windows.sh` | build.yml pack job |

### Available and used in this environment (night run, 2026-09-12)

| Tool | Version | Note |
| --- | --- | --- |
| OS | Windows 10.0.28000 x64 (user working machine; **not** a disposable VM) | |
| git | 2.55.0.windows.4 | |
| Go | 1.27.0 windows/amd64 (portable zip, not installed system-wide) | |
| protoc | libprotoc 31.1 (portable zip) | |
| protoc-gen-go / protoc-gen-go-grpc | v1.36.12 / v1.6.2 (via `go install`) | |
| CMake / Ninja / Qt / MSVC | **not available** | GUI build BLOCKED (see baseline-windows.md) |

## 3. Go module replacements (core/server/go.mod, pinned)

These replacements are load-bearing: upstream stock binaries are NOT
equivalent to ThroneCore. Do not remove or update independently.

| Module | Replaced by (throneproj fork) | Revision |
| --- | --- | --- |
| `github.com/sagernet/sing-box` | `github.com/throneproj/sing-box` | `v1.11.16-0.20260910083023-087823102fa9` (commit `087823102fa97821862eedc3b40294c87ca6384a`, matches audit) |
| `github.com/sagernet/sing` | `github.com/throneproj/sing` | `v0.9.4-0.20260909013934-6ba2d76a691b` |
| `github.com/xtls/xray-core` | `github.com/throneproj/xray-core` | `v1.251015.1-0.20260909120523-7b26dbd842dc` |
| `github.com/sagernet/wireguard-go` | `github.com/throneproj/wireguard-go` | `v0.0.0-20260909014041-3c774d9c2177` |
| `github.com/sagernet/cronet-go/all` (+ per-platform `lib/*`) | `github.com/parhelia512/cronet-go` | `all: v0.0.0-20260809193224-6287f9c66f94`, libs `v0.0.0-20260809192447-ad5810f59b3c` |

Version resolution subtlety (verified with `go list -m`):
`go list -m -f '{{.Version}}' github.com/sagernet/sing-box` returns the
**require** line version `v1.14.1-0.20260908150512-6d1fc214c16b`, while the
actually compiled source comes from the replacement
`throneproj/sing-box @ 087823102fa9…`. `build_go.sh` injects the require-line
version into `-X constant.Version=…`, so the binary banner
(`sing-box: v1.14.1-0.2026…`) reflects the injected string, not the
replacement commit. The replacement table above is the source of truth for
compiled code. Binary banner also reports `Xray-core: 26.9.9`.

## 4. Go build contract (ThroneCore)

From `script/build_go.sh` (unchanged) for Windows amd64 non-legacy:

```bash
export GOOS=windows GOARCH=amd64
export CGO_ENABLED=0
TAGS="with_clash_api,with_gvisor,with_quic,with_wireguard,with_utls,with_dhcp,with_tailscale,with_openvpn,with_openconnect,badlinkname,tfogo_checklinkname0,with_purego,with_naive_outbound"
# protobuf regeneration first:
(cd core/server/gen && protoc -I . --go_out=. --go-grpc_out=. libcore.proto)
VERSION_SINGBOX=$(cd core/server && go list -m -f '{{.Version}}' github.com/sagernet/sing-box)
go build -v -o $DEST -trimpath \
  -ldflags "-w -s -X 'github.com/sagernet/sing-box/constant.Version=${VERSION_SINGBOX}' \
  -X 'internal/godebug.defaultGODEBUG=multipathtcp=0' -checklinkname=0" \
  -tags "$TAGS" \
  ./core/server
```

Notes:
- `-checklinkname=0` is required (the `tfo-go` dependency uses `//go:linkname`
  on `net.(*netFD).init`; without the flag, linking fails on Go 1.27 — this
  also affects `go test`, see §6).
- `go.sum` is committed and pins all module hashes; module downloads go
  through the default Go proxy (checksum-verified).

## 5. Build pipeline / external assets (as found upstream)

`build.yml` = 3 jobs: `build-go` (cross-compiles ThroneCore + downloads
release assets), `build-cpp` (MSVC/Qt GUI), `pack`/`publish` (Inno Setup /
Debian / RPM / macOS bundle).

**Unpinned external assets fetched during CI builds** (provenance risk —
recorded here, must be pinned before any release, see PC-610):

| Asset | Source | Pinned? |
| --- | --- | --- |
| Qt 6.11.2 / 6.2.13 (Windows) | `github.com/throneproj/buildqt` release `Qt_<ver>_<arch>.7z` | by version tag |
| OpenSSL (Windows legacy) | `github.com/throneproj/env_windows_legacy` release `latest` | **no** (`latest`) |
| Route rule-sets header | `raw.githubusercontent.com/throneproj/routeprofiles/rule-set/srslist.h` | branch ref, not SHA |
| Web dashboard | `SagerNet/sing-box-dashboard` @ `gh-pages` | branch ref, not SHA |
| updater binary | `github.com/throneproj/updater` release `latest/download` | **no** (`latest`) — this is the updater that PC-020 disables for ProxyCore |
| libcronet.dll (Windows) | `github.com/SagerNet/cronet-go` release `latest/download` | **no** (`latest`) |
| cronet-go toolchain (Linux build) | `parhelia512/cronet-go` @ `9ed95366ae2f9b4b994cf54f8f9de0d8bc350057` | by SHA |

## 6. Baseline build executed in this environment (2026-09-12)

Unchanged baseline, exact commands and results (full log excerpts in
`docs/night-run/test-results.md`):

| Step | Command | Result |
| --- | --- | --- |
| protoc generation | `protoc -I . --go_out=. --go-grpc_out=. libcore.proto` (in `core/server/gen`) | OK; `libcore.pb.go`, `libcore_grpc.pb.go` generated (git-ignored, not committed) |
| ThroneCore build | `go build` with §4 flags/tags, `CGO_ENABLED=0` | **OK** (exit 0) |
| Artifact | `ThroneCore.exe`, 79 719 424 bytes, SHA-256 `c9da2da5ebefb26894f891849bc05ebb5d8dd7c755d3d4f780e2522d6ccd2b7f` | built outside repo (`D:\GLM_project\tools\thronecore-build`), not committed |
| Smoke run | `ThroneCore.exe` without env | prints `sing-box: v1.14.1-…6d1fc214c16b`, `Xray-core: 26.9.9`, then `THRONE_CORE_SOCKET not set` and exits — expected without GUI parent. Startup ran a read-only `restoreSystemDNS` check that reported `no action needed`; **no DNS/network change occurred** |
| `go test ./...` (CI tags) | `go test` | `ThroneCore` **ok**, `internal/xray` **ok**, `internal/xraydns` **ok** |
| `go test` root packages without `-checklinkname=0` | — | link failure in `tfo-go` (`net.(*netFD).init`) → tests must be run with `-ldflags="-checklinkname=0"` |
| `internal/boxdns/winipcfg` tests | `go test -vet=off -ldflags="-checklinkname=0"` | 21 **PASS** / 8 **BLOCKED-on-VM**: `TestIPInterface`, `TestIPChangeMetric`, `TestIPChangeMTU`, `TestGetIfRow`, `TestAddDeleteIPAddress`, `TestAddDeleteRoute`, `TestFlushDNS`, `TestSetDNS` require a machine-prepared test interface (`getTestInterface` → `ERROR_NOT_FOUND`) and some of them modify interface/routes/DNS — must run only on a disposable machine |
| `go vet ./...` | — | pre-existing vet errors in `internal/boxdns/winipcfg/winipcfg_test.go` (`%w` directive in `t.Errorf`) — upstream test-code debt, left untouched |

Deviations from CI: the release-packaging downloads (updater binary,
libcronet.dll) were **not** performed — they are packaging inputs, not build
requirements. GUI (`build-cpp`) job not executed (no MSVC/Qt on this machine).

## 7. Fixed identity facts (input for PC-010)

- GUI executable must remain `Throne.exe` (Windows) / `Throne` (Unix):
  `core/server/parentcheck/parentcheck.go` requires parent basename match.
- Core executable must remain `ThroneCore`:
  `src/ui/mainWindow/mainwindow_setup.cpp` builds `core_path` as
  `applicationDirPath() + "/ThroneCore"`.
- Core IPC socket name is generated per-run:
  `"throneIPC-" + QUuid::createUuid()` (mainwindow_setup.cpp), passed to the
  core via `THRONE_CORE_SOCKET` env; pipe peer PID verified on both sides.
- Single-instance socket: `"throne-" + md5(wd path)` (src/main.cpp,
  `LOCAL_SERVER_PREFIX`).
- Data location: `QApplication::setApplicationName("Throne")` drives
  `QStandardPaths::AppConfigLocation`; DB files `config/throne.db`,
  `throne_stats.db`.
