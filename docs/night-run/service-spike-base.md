# Service spike base (PC-100/PC-110 scope notes)

Created 2026-09-12, branch `agent/night-stage1-service-spike`.

## Source

- Forked from branch: `agent/night-stage0-foundation` (Stage 0, unmodified —
  no merge, no rebase).
- Fork point commit: `7001e5e14a99527396458ab09f10c53875b147cf`
  ("docs: stage0 branch rename and transition gate record").

## Stage 0 commits carried in (in order)

| Commit | Subject |
| --- | --- |
| `b462ef16` | PC-000: lock upstream and document Windows baseline |
| `9916bd9e` | PC-010: isolate ProxyCore identity and data |
| `db26adf3` | PC-020: disable upstream update installation |
| `6759182f` | PC-030: add compiler and lifecycle characterization tests |
| `7001e5e1` | docs: stage0 branch rename and transition gate record |

Upstream baseline: Throne `21b8f680b95d1dfe7906b6b64f6c3c51c263ba40`.

## Test results carried in (available checks, from docs/night-run/test-results.md)

- ThroneCore build with exact CI tags: **exit 0**
  (ThroneCore.exe SHA-256 `c9da2da5ebefb26894f891849bc05ebb5d8dd7c755d3d4f780e2522d6ccd2b7f`).
- `go test -ldflags="-checklinkname=0" <CI tags>`: `ThroneCore` ok,
  `internal/xray` ok, `internal/xraydns` ok.
- `internal/boxdns/winipcfg`: 21 pass / 8 VM-BLOCKED (machine interface tests,
  some mutate network — never run on a working machine).
- PC-020 guard (`tests/proxycore/check_no_updater.sh`): 4/4 ok.
- Core smoke: version banner + expected `THRONE_CORE_SOCKET not set` exit;
  read-only `restoreSystemDNS` check only, no system changes.
- Secret/binary scans over the Stage 0 diff: clean; tree clean.

## Known blockers carried in (unchanged by the spike branch)

- No Windows 11 VM → SCM install/start/stop, real named-pipe ACL/SID tests,
  GUI smoke, TUN and network-injection tests are BLOCKED; procedures documented
  in docs/validation/baseline-windows.md §4.
- No C++ toolchain (MSVC/Qt/CMake/Ninja) → C++ test targets
  (proxycore_tests / proxycore_characterization / parser fixture harness) are
  written but not executed; Go toolchain 1.27.0 + protoc 31.1 are available
  portably in D:\GLM_project\tools.
- Working machine constraints: no service install, no UAC, no sc.exe/netsh/
  registry changes, no DNS/firewall/route modifications.

## Spike scope discipline

- Allowed here: PC-100, then (only if its conditions hold) PC-110 — code,
  mocks, unit tests, building the service executable, additive protobuf.
- Forbidden here: PC-120/130/140, new UI, routing assignments, WFP/firewall/
  DNS/routes, service installation on this machine, World/Everyone pipe access,
  command-execution RPC, non-additive protobuf changes, force ops, push.
- After the final report of the spike: stop.
