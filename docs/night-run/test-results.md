# Night run — command & test results log

Branch: `agent/night-foundation-2026-09-12` @ `6759182f` (baseline `21b8f680` + 4 commits).
Machine: Windows 10.0.28000 x64, user working machine (no VM, no UAC, no network changes).

## Commands executed (chronological)

| # | Command | Result |
| --- | --- | --- |
| 1 | `git clone https://github.com/throneproj/Throne.git ProxyCore` | OK |
| 2 | `git cat-file -t 21b8f680b95d1dfe7906b6b64f6c3c51c263ba40` / `git log -1 <sha>` | baseline exists; "fix windows 11 theme", Nova, 2026-09-11 |
| 3 | `git rev-list --count <baseline>..dev-HEAD` | 4 commits ahead (not taken into the fork branch) |
| 4 | `git checkout -b agent/night-foundation-2026-09-12 <baseline>` | OK; tree clean |
| 5 | Go 1.27.0 + protoc 31.1 portable download/extract (outside repo, `D:\GLM_project\tools`) | `go version go1.27.0 windows/amd64`; `libprotoc 31.1` |
| 6 | `go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12` and `protoc-gen-go-grpc@v1.6.2` | OK |
| 7 | `protoc -I . --go_out=. --go-grpc_out=. libcore.proto` (core/server/gen) | OK; generated files are git-ignored, not committed |
| 8 | `CGO_ENABLED=0 go build -v -o … -trimpath -ldflags "-w -s -X 'github.com/sagernet/sing-box/constant.Version=…' -X 'internal/godebug.defaultGODEBUG=multipathtcp=0' -checklinkname=0" -tags "with_clash_api,with_gvisor,with_quic,with_wireguard,with_utls,with_dhcp,with_tailscale,with_openvpn,with_openconnect,badlinkname,tfogo_checklinkname0,with_purego,with_naive_outbound"` | **exit 0**; ThroneCore.exe 79 719 424 bytes, SHA-256 `c9da2da5ebefb26894f891849bc05ebb5d8dd7c755d3d4f780e2522d6ccd2b7f` (kept outside repo) |
| 9 | `ThroneCore.exe` (no env) | prints `sing-box: v1.14.1-0.20260908150512-6d1fc214c16b`, `Xray-core: 26.9.9`, exits `THRONE_CORE_SOCKET not set`; startup `restoreSystemDNS` check: `no action needed` (read-only, no change) |
| 10 | `go test -tags "<CI tags>" ./...` (без ldflags) | link FAIL: `tfo-go` → `net.(*netFD).init` linkname (Go 1.27) — build-contract fact |
| 11 | `go test -ldflags="-checklinkname=0" -tags "<CI tags>" ./...` | `ThroneCore` **ok** (0.159s); `internal/xray` **ok** (0.163s); `internal/xraydns` **ok** (0.128s); `internal/boxdns/winipcfg` build FAIL |
| 12 | `go test -vet=off -ldflags="-checklinkname=0" … ./internal/boxdns/winipcfg/` | 21 **PASS** / 8 **BLOCKED-VM** (`TestIPInterface`, `TestIPChangeMetric`, `TestIPChangeMTU`, `TestGetIfRow`, `TestAddDeleteIPAddress`, `TestAddDeleteRoute`, `TestFlushDNS`, `TestSetDNS` — need prepared test interface, some mutate network) |
| 13 | `go vet ./...` | pre-existing vet errors in upstream `winipcfg_test.go` (`%w` in `t.Errorf`) — upstream debt, untouched |
| 14 | `bash tests/proxycore/check_no_updater.sh` (after PC-020) | 4/4 ok, exit 0 |
| 15 | `go build` (HEAD, final) with CI flags | **exit 0** ("FINAL BUILD OK") |
| 16 | `go test -ldflags="-checklinkname=0" … . ./internal/xray/ ./internal/xraydns/` (final HEAD) | all **ok** (cached — code identical to baseline: `git diff 21b8f680..HEAD -- core/server` is empty) |
| 17 | Secret scan over full branch diff (`PRIVATE KEY|AKIA|ghp_|xox|ss://\|vmess://\|trojan://\|vless://…`) | no hits (fixtures contain only synthetic data) |
| 18 | Binary/artifact scan over diff (`*.exe\|*.dll\|*.db\|*.zip\|*.pdb\|…`) | no hits |
| 19 | `git status --short` at end | clean |

## Not executed (BLOCKED) and why

| Item | Reason |
| --- | --- |
| Qt GUI build (`Throne.exe`), GUI smoke, profile import, GUI core start/stop | No MSVC/Qt/CMake/Ninja on machine; installing them requires admin/UAC — forbidden unattended. Exact VM procedure: `docs/validation/baseline-windows.md` §4 |
| C++ test targets (`proxycore_tests`, `proxycore_characterization`, parser fixtures harness) | Same toolchain gap — code is written and wired into CMake (`-DPROXYCORE_BUILD_TESTS=ON`), execution pending |
| TUN tests | Forbidden on the working machine; VM only |
| winipcfg machine-dependent tests | Require prepared test interface; some mutate interface/routes/DNS |
