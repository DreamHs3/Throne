# PC-110 report — versioned typed IPC contract for the service path

Branch: `agent/night-stage1-service-spike`. Status: code-complete and
verified in-process; the SCM/VM acceptance of PC-100 is untouched and stays
**BLOCKED** (nothing here produces VM evidence, and PC-110 inherits every
VM-bound item of PC-100).

## What was implemented

- `core/server/service_envelope_windows.go` (new) — the PC-110 wire contract:
  framing `[u32 frameLen][RequestEnvelope]` / `[u32 frameLen][ResponseEnvelope]`,
  the envelope code constants (0..9, mirrored in the proto comment), the
  frame reader (length checked against `serviceMaxEnvelopeLen` = 32 MiB
  BEFORE allocation; global 64 MiB payload-budget semaphore held per frame
  until its handler completes), the response writer, the single operation
  registry `serviceOperations` (Hello, Health, CheckConfig, Start, Stop —
  with STRICT typed payload decoding: any unknown-field residue, i.e. another
  message type's or a newer client's fields, is refused), the bounded
  per-connection request-id dedup (window 4096), `envelopeContext` (client
  deadline → handler context) and the handler-error → code classifier.
- `core/server/service_windows.go` — the serve loop
  (`serveServiceConnContext`) rewritten to the envelope protocol with a fixed
  validation order: version → shutdown → handshake state → request_id →
  deadline → expected policy revision → operation registry → typed payload →
  dedup → spawn. The PC-100 spike's `serviceMethodAllowlist`, `dispatchService`,
  `readServiceRequest`, `writeServiceResponse` and the 4096/32 MiB method/
  payload guards are removed — superseded by the registry and
  `serviceMaxEnvelopeLen`. SDDL, SDDL guard, client-identity boundary, SCM
  handler, connection set, handler slots: untouched.
- `core/server/service_config_policy.go` — `configPolicyRevision = 1` (the
  policy contract version a client pins via `expected_policy_revision`);
  everything else unchanged.
- `core/server/gen/libcore.proto` — the owner's PC-110 WIP
  (`RequestEnvelope`/`ResponseEnvelope`, +23 lines) is now part of this
  package by explicit authorization of the handoff; the messages are
  additive, not in the LibcoreService block, so the generated server
  interface (and non-Windows builds) are unaffected. Generated bindings
  verified byte-identical to a fresh protoc (31.1) regeneration; no
  regeneration was needed.
- Untouched by construction: `dispatch.go`, `server.go`, `parentcheck/*`,
  `ipc/*`, `main.go`, `runmode.go` — the legacy GUI-child mode keeps its
  framing, its handlers table and its behavior byte-identical.

## Wire contract (service pipe only, protocol version 1)

Request: `[u32 frameLen LE][RequestEnvelope{protocol_version, request_id,
operation, deadline_unix_ms, expected_policy_revision, typed_payload}]`.
Response: `[u32 frameLen LE][ResponseEnvelope{request_id, code, message,
typed_payload, service_protocol_version}]`.

Codes: 0 OK; 1 incompatible/missing version (disconnect); 2 unauthorized
(reserved — identity refusal happens at accept, before any envelope, and
closes the connection silently as in PC-100); 3 invalid request (malformed
envelope/payload, unknown operation, deliberate handler refusals carrying an
`ERR_*` prefix); 4 frame too large (answered before the body is read, then
disconnect — the stream position is unknown); 5 deadline exceeded (expired
before dispatch, or hit inside the handler); 6 stale policy revision;
7 service unavailable (shutdown refuses all new work and starts no new
handlers); 8 runtime failure (handler error without a typed prefix, or
panic); 9 duplicate request_id.

Semantics the tests pin down:

- request_id is mandatory (0 = absence → code 3), echoed verbatim, and
  deduplicated per connection (in-flight included). Ids are recorded only
  when a request is accepted for execution, so a refused request can be
  retried with the same id. The window is bounded (4096, oldest evicted).
- a refused request (unknown operation, wrong payload, stale revision,
  expired deadline, duplicate id, forbidden legacy RPC) NEVER breaks the
  connection; version incompatibility and oversized frames do.
- strict payload typing: protobuf silently parks wire-type mismatches in
  unknown fields, so the registry rejects any unknown-field residue —
  `Start` with a `HandshakeReq` payload is a typed refusal, not a dispatch.
- handler errors: `ERR_CONFIG_POLICY`/`ERR_INVALID_REQUEST`/... → code 3 with
  the prefix preserved in the message (the PC-100 wire assertions carry over
  unchanged in meaning); runtime failures and panics → code 8.

## Proof: the service path cannot reach the legacy dispatch

- Structural: the serve loop resolves operations ONLY through
  `serviceOperations` (service_envelope_windows.go), which is a package-level
  literal of five entries binding CheckConfig/Start to `ServiceCheckConfig`/
  `ServiceStart`. No code path from the serve loop references the legacy
  `handlers` map or `dispatch()`; dispatch.go is byte-identical (git diff
  shows no change to it).
- Behavioral (`TestServiceRegistryVsLegacyTable`): the registry's CheckConfig
  refuses the hostile document `{"log":{"output":...}}` with
  `ERR_CONFIG_POLICY`, while the legacy `dispatch("CheckConfig", ...)` accepts
  the very same payload (no policy in the GUI-child contract) — two tables,
  two behaviors, and the wire path only ever consults the registry.
- Wire (`TestServiceWirePathRejectsPrivilegedMethods`): SetSystemDNS,
  InstallDashboard, CloseConnections, QueryStats, GenWgKeyPair, WarpRegister
  and unknown names all get code 3 + `ERR_METHOD_NOT_ALLOWED` over the real
  serve loop, and Health still works on the same connection afterwards.

## Source-to-sink (Start and CheckConfig, re-proven through the envelope)

Chain: named-pipe client → `[u32][RequestEnvelope]` → `readServiceEnvelope`
(frame ≤ 32 MiB checked before allocation; 64 MiB aggregate budget; strict
envelope decode) → serve-loop gates (version, shutdown, dedup, deadline,
policy revision) → registry (only `ServiceCheckConfig`/`ServiceStart`) →
typed `LoadConfigReq` decode (strict) → normalize (proto2 optional nil-safety)
→ filesystem policy → runtime.

`ServiceCheckConfig` → policy on `core_config` (fail-closed strict JSON,
case-folded deny keys, cache_file normalization, external_ui removal) →
policy on `xray_config` → policy + `xray.CheckXrayConfig` on EVERY
`xray_full_configs` entry → `boxmain.Check` (parse/build only).
`ServiceStart` → extra-process surface refused before anything is parsed
(`need_extra_process`, `extra_process_path/args/conf`, `extra_no_out`) →
`need_xray` requires `xray_config` → the same policy → legacy `Start` →
`xray.CreateXrayInstance`/gates (policy-scanned documents only) →
`boxmain.Create([]byte(core_config))` → sing-box runtime.

Sink checklist (each name rejected by the deny list or normalized; wire-tested):

- extra_process_* — refused in the handler before any parsing (wire matrix);
- sing-box `log.output` — blanket-denied `output` key (wire: JSONC and
  case-variant spellings refused, sink file never created);
- `experimental.cache_file.path` — rewritten to
  `<THRONE_SERVICE_DATA_DIR>/cache.db`; hostile absolute/traversal/junction
  targets never appear (real-wire junction test: cache.db lands only in the
  data dir); enabled cache_file without a data dir fails closed;
- TLS/OpenVPN/OpenConnect certificate & key paths (incl. client/mTLS, CA,
  MCA, CRL, static keys, ECH config, wrapper scripts) — denied keys (41-case
  matrix);
- local rule sets (`rule_set` local `path`/`paths`), remote `initial_path`,
  DNS hosts `path`, `dhcp_lease_files` — denied;
- Xray `log.access`/`log.error` — only `none`/empty accepted (case-folded);
- Xray/REALITY `masterKeyLog` — only `none`/empty; `geodata.file`,
  top-level `env`, `masquerade.dir`, `certificateFile`/`keyFile` — denied;
- `unix:`/`unix://`, `\\.\pipe`, `\\.\`, `\\?\` anywhere — denied
  (case-insensitive prefixes);
- absolute paths, `..\` traversal, device/pipe paths — moot by
  deny-by-default construction: the value is never evaluated by the runtime;
  route matchers `process_path`/`process_path_regex` and DERP `home` remain
  deliberately allowed (they route, they do not open files).

The envelope layer adds no new sink: operation names only index the registry,
request_id/deadline/revision are pure in-memory values, responses carry
handler output only.

## Tests

PC-100 wire tests migrated to the envelope framing (same behaviors, the
status→code mapping documented above); PC-110 adds a focused suite
(`service_envelope_windows_test.go`): missing version, version change
mid-connection, Hello after handshake, unknown/wrong payload types (strict
unknown-field refusal), malformed envelope keeping the connection usable,
oversized envelope answered before its body is read (direct reader and
end-to-end), expired deadline not dispatched (a would-be `ERR_INVALID_REQUEST`
Start produces code 5 instead) with the connection staying usable,
`envelopeContext` deadline binding, stale policy revision (naming both
revisions; the refused request's id may be retried), request_id echo of the
full uint64 range, zero request_id refused, duplicate id completed and
in-flight (exactly one OK + one duplicate), shutdown refusing a pending
handler (slot-saturated; a freed slot never resurrects it) and a pending
payload-budget acquire, and the registry-vs-legacy-table divergence.

Commands and results (CI tags
`with_gvisor,with_quic,with_wireguard,with_utls,with_clash_api,with_grpc`,
`-ldflags="-checklinkname=0"`, Go 1.27.0):

```
go build .                                          → exit 0
go vet -a ./...                                     → exactly one pre-existing
                                                      finding (unreachable code,
                                                      internal/boxdns/dns_manager_windows.go:246)
go test (PC-100+PC-110 service suites) -count=1     → ok
go test (PC-100+PC-110 service suites) -count=20    → ok  (44 tests × 20 runs;
                                                      4 full rounds green)
go test real-pipe/shutdown/SCM tests -count=50      → ok  (200/200)
go test ./...                                       → ThroneCore, internal/xray,
                                                      internal/xraydns ok;
                                                      internal/boxdns/winipcfg:
                                                      the 8 pre-existing
                                                      environment failures
                                                      (no VM test adapter) —
                                                      baseline, not a regression
gofmt -l (changed files)                            → clean
git diff --check                                    → clean
tests/proxycore/check_no_updater.sh                 → exit 0
```

One-time flake, recorded honestly: the FIRST `-count=20` run (immediately
after a `-count=1` run) timed out at the 600s harness limit with a goroutine
dump showing go-winio `ListenPipe` accept goroutines. It did not reproduce in
four subsequent full `-count=20` rounds, a `-count=50` round over the
real-pipe/identity/SCM/shutdown tests, or the final regression run. Every
wait in the new tests is bounded (5s) with cleanup guards, so a >600s hang
cannot come from the new suite logic; the winio listener machinery on this
desktop machine remains the environment-sensitive suspect. Not counted as a
pass or a failure — recorded as an unresolved single observation.

## Still BLOCKED (not changed by PC-110)

- Real SCM start/stop, non-elevated-client refusal and SDDL-refusal under a
  normal user on a disposable Windows VM (PC-100 Gate 0/1 evidence).
- The service code has still never executed under SCM anywhere.
- The string-JSON config contract inside `LoadConfigReq.typed_payload`
  remains interim (the deny-list policy is unchanged); typed config
  parameters where the service builds the config itself remain future work.
- PC-120 (installer: per-user SID grants, `THRONE_SERVICE_*` provisioning)
  and PC-130+ are not started.
