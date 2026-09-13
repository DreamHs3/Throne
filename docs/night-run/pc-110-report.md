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
go test real-pipe / in-process shutdown / SCM-      → ok  (200/200) — the
  contract simulation tests -count=50                       "SCM" tests drive
                                                            Execute directly
                                                            WITHOUT an SCM; a
                                                            real SCM run
                                                            stays BLOCKED
go test ./...                                       → ThroneCore, internal/xray,
                                                      internal/xraydns ok;
                                                      internal/boxdns/winipcfg:
                                                      the 8 pre-existing
                                                      environment failures
                                                      (no VM test adapter) —
                                                      baseline, not a regression
gofmt -l (changed files)                            → clean
git diff --check                                    → clean
tests/proxycore/check_no_updater.sh                 → BLOCKED as acceptance
                                                      evidence: only the
                                                      static source grep runs
                                                      (exit 0 in Git Bash);
                                                      what would verify the
                                                      PC-020 claim — a real
                                                      GUI build/package
                                                      without the updater —
                                                      cannot be executed here
                                                      (no C++ toolchain)
go test -race                                       → BLOCKED (unchanged):
                                                      CGO_ENABLED=0, no C
                                                      compiler on this machine
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

## Remediation — independent PC-110 review (2026-09-13)

An independent read-only review of commit `bfd79c58` confirmed three
findings; all three were fixed in the remediation described below
(remediation baseline `2e1a6384`, reviewed HEAD `bfd79c58`). A follow-up
review of the remediation itself found three further defects (a runtime that
could still be created after shutdown began, a window left open for the
first Hello, and a payload-budget leak on a failed handshake answer) — they
are fixed and documented in "Remediation round 2" below; until that round's
items, a "REQUEST CHANGES" verdict stands. SCM/VM acceptance is untouched
and stays **BLOCKED** — nothing below is VM evidence.

**Finding 1 (P1): the request deadline did not act during the handler-slot
waits.** The deadline was checked once before dispatch, both handler-limit
waits selected only on the service context, the request context was created
only after the waits, and nothing re-checked it before `op.call` — a request
whose deadline expired while queued for a global or per-connection slot was
still dispatched (proven chain: envelope → first deadline check passes →
blocks on a slot → deadline expires → slot frees → handler runs →
`ServiceStart` → runtime effects).

Fix (`service_windows.go`): the request context (`envelopeContext`) is now
created BEFORE the waits, so the client deadline participates in both waits
(`select` on the slot send vs `hctx.Done()`; shutdown still aborts them —
`hctx` derives from the serve context). Every refusal path releases exactly
what it acquired — the global slot, the per-connection slot, the admission
count, the frame's payload weight, and the context cancel — once. The
spawned handler goroutine re-checks `hctx.Err()` immediately before
`op.call` (the reliable last check): an expired request answers
`code=5` with the `ERR_DEADLINE_EXCEEDED` prefix and the original
`request_id`, runs nothing, and the connection stays usable. A freed slot
cannot resurrect an expired request. Documented dedup nuance: the deadline
refusal happens AFTER admission, so the id stays consumed and a retry needs
a fresh id (conservative — the handler never ran, so this is never unsafe);
ids of requests refused before admission remain retryable as before.

**Finding 2 (P1): no reliable shutdown/admission barrier.** `WaitGroup.Add`
could race the shutdown `Wait` (serve loop passing its ctx check and then
adding while `waitServiceHandlers` was already waiting on a zero counter),
a handler could observe a still-live context and call `ServiceStart` after
shutdown began, and the listener context was canceled by a watcher
goroutine — a window where shutdown had begun but serve loops had not been
interrupted.

Fix: a per-run `serviceHandlerGate` (service_windows.go) whose `admit()` and
`beginShutdown()` share one mutex, so the two are totally ordered: an admit
that wins its critical section has its `wg.Add(1)` counted before
`beginShutdown` runs; an admit that loses sees `closing` and adds nothing.
There is no third ordering — after `beginShutdown` returns, no `Add` can
happen and the Wait (started only after it) observes exactly the handlers
admitted before shutdown. `beginShutdown` closes connection admission
synchronously too (`admitConn` shares the mutex): a connection whose Accept
succeeds in the Accept/close race is closed by the accept loop and never
reaches a handshake. The Execute stop path is now one synchronous sequence:
close admission → cancel the serve context (no watcher goroutine) and close
stop channel/listener → drop established connections → stop the runtime
(idempotent) → wait, bounded (2 s), for the handlers admitted before
shutdown → report `Stopped`. Bounded shutdown is preserved; idle
established-connection semantics are unchanged until shutdown closes them;
client disconnect remains a normal event. One gate per service run (a field
of `proxyCoreServiceHandler`, created in `Execute`) so no run — and no test
— can pollute another run's WaitGroup. Residual, documented: a connection
admitted microseconds before `beginShutdown` may still complete its
handshake in the closeAll race — the handshake has no privileged effect and
its per-request gates refuse every operation; no handler can start.

**Finding 3 (P2): the first Hello bypassed the general envelope contract**
(no request_id requirement, no deadline, no expected_policy_revision, no
dedup, no shutdown gate) and the handshake built `HandshakeResp` manually
while the registry's `serviceOperations["Hello"]` (bound to
`globalServer.Hello`) was never called by the wire — two Hello semantics.

Fix: ONE validation pipeline in `serveServiceConnContext` now serves every
frame, the mandatory Hello included: version → shutdown gate (a new Hello
is refused after shutdown) → handshake state → request_id (0 refused on the
handshake too) → deadline (an expired Hello establishes no handshake) →
expected policy revision (the general stale-revision gate covers the
handshake — one rule for every `RequestEnvelope`, no per-operation
exceptions) → registry → strict typed payload → dedup → dispatch. The
handshake phase dispatches INLINE through the very registry entry
(`callServiceOperation`, panic-contained) — the manual `HandshakeResp`
construction is gone, so `serviceOperations["Hello"]` is the single Hello
semantics: the envelope's `protocol_version` is the single version
authority (mismatch/absence → `code=1` + disconnect before any dispatch)
and `HandshakeReq.protocol_version` is deliberately NOT consulted (kept for
wire compatibility with the PC-100 spike's handshake, documented as
ignored, pinned by test). Handshake refusals (bad id/deadline/revision/
payload) keep the connection open so the client can retry the Hello — with
the same id, since a refused handshake consumes nothing; a completed
handshake joins the connection's dedup window, so a later request reusing
the handshake's id gets `code=9`. The second Hello after a completed
handshake stays refused; malformed protobuf still cannot panic (the
registry decode and the framing layer both refuse typed).

### Security boundaries re-proven after the fix

- The serve loop resolves operations ONLY through `serviceOperations`
  (five entries; `CheckConfig`/`Start` bound to `ServiceCheckConfig`/
  `ServiceStart`); `grep` over the wire files shows no `dispatch(` call and
  no `handlers[` read — the only `handlers[...]` lines are the pre-existing
  legacy-table `init()` registrations, untouched.
- `dispatch.go`, `server.go`, `service_config_policy.go` are byte-identical
  (git diff empty): the config filesystem policy of `2e1a6384` (extra_process_*,
  `log.output`, `cache_file.path` normalization, certificate/key paths,
  OpenVPN/OpenConnect/SSH paths, local rule-set/hosts/netns paths, Xray
  `log.access`/`log.error`, TLS/REALITY `masterKeyLog` sentinel `none`,
  `geodata assets.file`, top-level Xray env, Hysteria masquerade dir, Xray
  full configs, absolute/traversal/UNC/device/pipe/reparse refusal) is
  unchanged, and the ordinary URL paths (sing-box WS/HTTP, DoH, Xray
  WS/XHTTP/HTTPUpgrade) remain allowed.
- The wire rejection matrix (`TestServiceWirePathRejectsPrivilegedMethods`)
  and the registry-vs-legacy divergence test still pass unchanged.

### Remediation tests (all through the real `serveServiceConnContext`, or
the real `proxyCoreServiceHandler.Execute`; new file
`service_lifecycle_windows_test.go`, 13 tests)

- `TestServiceHandlerGateAdmissionBarrier` — the gate contract: admission
  open before shutdown; `beginShutdown` closes handler AND connection
  admission synchronously (no `Add` after it); the Wait observes exactly
  the admitted handlers.
- `TestServiceListenerAdmissionClosesWithShutdown` — the Accept/shutdown
  race, staged deterministically with a stub listener: a connection
  accepted after the barrier closed is closed unread (no handshake answer,
  no handler admitted, accept loop exits).
- `TestServiceExecuteShutdownRefusesPendingStart` — the full coordinated
  shutdown through the real `Execute` on a real winio pipe: a client Start
  is held at the global-slot wait (admission counted, observed via the
  gate, runtime absent), SCM Stop begins, `Stopped` is reported, the
  pending Start never reaches the runtime sink even after every slot is
  freed, admission returns to zero and the payload budget is intact. No
  sleep-only synchronization: the test synchronizes on the admission
  counter, the status channel and the process exit.
- `TestEnvelopeDeadlineExpiresWaitingForGlobalSlot` — a request whose
  deadline expires while queued for a global slot: `code=5`,
  `ERR_DEADLINE_EXCEEDED`, request_id preserved, not dispatched (no
  runtime), a freed slot does not resurrect it, Health still works on the
  same connection, and nothing leaks (admission zero, all slots free,
  full payload budget acquirable).
- `TestEnvelopeDeadlineExpiresWaitingForPerConnSlot` — the same for the
  per-connection limit (eight in-flight handlers hold it via a blocking
  registry stub, restored on cleanup): the ninth request expires in the
  per-connection wait with the same typed refusal, the eight real handlers
  still complete (exactly eight OKs), the connection keeps serving, no
  leak.
- `TestEnvelopeShutdownRefusesPendingPerConnWait` — shutdown aborts a
  request parked in the per-connection wait (`code=7`), the serve loop
  finishes, the parked handlers wind down, nothing is executed, no leak.
- `TestEnvelopeHelloZeroRequestIDRefused` — Hello with `request_id=0` is a
  typed invalid request; the connection stays usable and the handshake can
  still be completed.
- `TestEnvelopeHelloExpiredDeadlineNoHandshake` — an expired Hello is
  `code=5` with the id preserved and establishes NO handshake (a following
  non-Hello request gets `ERR_NO_HANDSHAKE` and the close).
- `TestEnvelopeHelloExpiredDeadlineRetryAllowed` — the refused Hello
  consumed nothing: the same id completes the handshake afterwards.
- `TestEnvelopeHelloStalePolicyRevisionRefused` — the general
  stale-revision gate covers the handshake (`code=6`, id preserved); the
  retry with the current revision succeeds on the same connection.
- `TestEnvelopeHandshakeIDConsumedByDedup` — the documented dedup
  semantics of the handshake id: a later request reusing it gets `code=9`;
  a fresh id is served.
- `TestEnvelopeHelloPayloadVersionNotConsulted` — the envelope version is
  the single authority: a Hello whose payload carries a different
  `protocol_version` still completes the handshake (documented-ignored
  field); the mismatching ENVELOPE version case stays
  `code=1` + disconnect (existing test).
- `TestEnvelopeShutdownRefusesHandshake` — a Hello arriving after the serve
  context was canceled establishes no handshake and the connection closes
  (with the context already canceled, the frame's payload-budget gate
  aborts before the body is read — the documented no-answer path — so the
  deterministic assertions are no-handshake + close; the typed `code=7`
  refusal for an already-read frame exercises the same serve-loop gate and
  is covered by the pending-handler shutdown tests).

### Remediation check results (2026-09-13, Go 1.27.0, CI tags,
`-ldflags=-checklinkname=0`)

```
go build .                                          → exit 0
go vet . (main package, tags)                       → clean (the only
                                                      `go vet ./...` finding
                                                      remains the pre-existing
                                                      internal/boxdns/
                                                      dns_manager_windows.go:246
                                                      unreachable code)
gofmt -l (six changed Go files)                     → clean
git diff --check / --cached --check                 → clean
go test . -count=1                                  → ok (65 tests)
go test . -count=20                                 → ok (65 × 20)
new remediation tests -count=50                     → ok (13 × 50 = 650 runs)
go test ./...                                       → ThroneCore, internal/xray,
                                                      internal/xraydns ok;
                                                      winipcfg: the same 8
                                                      pre-existing environment
                                                      failures (Element not
                                                      found, no VM adapter) —
                                                      baseline, not a regression
tests/proxycore/check_no_updater.sh                 → BLOCKED as acceptance
                                                      evidence (the static
                                                      source grep alone runs
                                                      exit 0; a real GUI
                                                      build/package check
                                                      needs a C++ toolchain
                                                      this machine does not
                                                      have) — not a
                                                      verification result of
                                                      this remediation
go test -race                                       → BLOCKED (unchanged):
                                                      CGO_ENABLED=0, no C
                                                      compiler on this machine
generated protobuf vs fresh protoc 31.1 regen       → byte-identical
                                                      (libcore.proto unchanged;
                                                      SHA-256 below)
```

Scope of the "-count=50" and real-pipe lines above: they are IN-PROCESS
tests — the "SCM" tests drive `proxyCoreServiceHandler.Execute` directly
with `svc.ChangeRequest` values, without an SCM. What they prove is the
handler's stop-sequence contract; they are NOT SCM evidence. Likewise
`TestServiceClientIdentityRealPipe` exercises the real winio pipe and token
identity under a non-elevated runner and PASSED — but it is not an SCM test:
SCM start/stop, the service-context checks and the refusals under a normal
user remain VM-only (**BLOCKED**, Gate 0/1 open).

Generated bindings (fresh regeneration == working files, both
`e0e15c6e79f72a971e72de262adad326ce7a23af92c1eee085054412ee1ad1bd` for
`libcore.pb.go` and
`7cd45efaf65fbab93553de8b1f9dbe8a5b7cacb9b3c44f32fc9636f364aae93f` for
`libcore_grpc.pb.go`); still git-ignored, not added to any commit.

## Remediation round 2 — follow-up review of the remediation (2026-09-13)

A second independent review examined the remediation itself (uncommitted
tree on top of `bfd79c58`) and confirmed three further defects. All three
are fixed in this round; until this round the verdict stayed
**REQUEST CHANGES**. SCM/VM acceptance remains **BLOCKED**.

**Finding 4 (P1): a runtime could still be created after shutdown began.**
`Execute` called `globalServer.Stop` before the already-admitted handlers
had finished: a `ServiceStart` handler admitted before shutdown could be
inside its policy parsing while the stop path ran, the stop's `Stop` found
an empty runtime (no-op), `svc.Stopped` was published — and the suspended
`ServiceStart` then continued and created the runtime (box, Xray, extra
process) after `Stopped`. The previous "pending Start" coverage held the
request at the slot wait, i.e. BEFORE `op.call` — the window inside the
running handler was untested and unguarded.

Fix (`service_windows.go`): a shutdown mark tied to a lifecycle lock, on the
service path. `serviceRuntimeMu` serializes the service path's one
runtime-creating phase — `ServiceStart`'s delegation to the legacy Start —
and `serviceRuntimeStopping` is the mark both sides share under it. The
Execute stop path raises the mark (`beginServiceRuntimeShutdown`, under
`serviceRuntimeMu`) strictly BEFORE its final `globalServer.Stop`; the
legacy `lifecycleMu` is taken strictly INSIDE that critical section (inside
Start/Stop), never the reverse, so the nesting cannot deadlock. Total order:
a Start that takes the lock first completes — and the stop path's Stop,
which cannot run until the lock is released, tears its runtime down before
`Stopped`; a Start that takes the lock after the mark refuses
(`errServiceStopping`, classified as envelope code 7) — no boxmain.Create,
no Xray instance, no extra process. After `Stopped` the mark is still set,
so no late handler can create a runtime. Design note: the state lives in
the service file, not in the legacy `server.go` lifecycle — `server.go` is a
protected legacy file and the GUI-child `Start` must not observe a service
shutdown mark; the same-lock re-check the review demanded is satisfied by
`serviceRuntimeMu` (the mark, the check and the final Stop share it).
`serviceStartPause` is a test seam (production leaves it nil) at the exact
suspension point — inside `ServiceStart`, after every validation, before the
critical section.

Deterministic test `TestServiceExecuteStoppedRefusesSuspendedStart`
(real `Execute`, real winio pipe): a client Start passes admission, both
slot waits and the request-context re-check and is SUSPENDED inside
`ServiceStart` before the runtime-creation lock; SCM-style Stop completes
while it is suspended (`Stopped` is published with the handler still in
flight — observed), the mark is raised, the final `Stop` finds no runtime;
the resumed Start then refuses — the runtime sink is never reached, and
after `Stopped`: `currentBox() == nil`, no Xray instance, no extra process.
Mutation-checked: with the mark check disabled the test fails with "the
runtime sink was reached". `TestServiceExecuteShutdownRefusesPendingStart`
(slot-wait staging) is retained; both Execute-driving tests now reset the
sticky mark (production exits after `Stopped`; a test process keeps
running).

**Finding 5 (P2): the first Hello was not behind the strict
shutdown/deadline barrier.** The handshake dispatched inline after ONE
`ctx.Err()` check at the top of the frame pipeline; cancellation could land
between that check and the dispatch, so a decoded Hello could still call
the registry entry after shutdown began; a deadline expiring between the
serve loop's deadline check and the dispatch was equally unguarded, and
`handshakeDone` was set unconditionally after a successful call.

Fix: the handshake dispatch moved into a dedicated `dispatchHandshake`
function with a reliable final gate run immediately before `op.call` —
after every validation step, with nothing waited on in between (the inline
handshake holds no slots, so no wait can defer the check): a Hello decoded
after shutdown began is refused code 7 and disconnects; a Hello whose
deadline expired since the serve loop's check is refused code 5 and keeps
the connection (a corrected retry can complete the handshake — with a fresh
id: the request was already accepted, the same conservative rule as a
request refused after admission, now documented). A shutdown that begins
DURING the call still does not establish the handshake: `handshakeDone` is
never set after shutdown began (no OK is sent for it either).

Deterministic tests (real `serveServiceConnContext`, the Hello staged by a
pass-through registry stub whose decode parks — the recorded `called`
reports whether the real `globalServer.Hello` behind the stub executed):
`TestEnvelopeHelloShutdownAfterDecodeNoDispatch` — Hello read, validated,
decoded; shutdown begins; barrier lifted → code 7 with the preserved id,
`called == false`, no OK, connection closed, budget intact;
`TestEnvelopeHelloDeadlineExpiredBeforeDispatch` — the deadline expires
while the decoded Hello waits (deterministic expiry against the envelope's
own deadline value), barrier lifted → code 5 with the preserved id,
`called == false`, same-id retry gets code 9 (id consumed by the
acceptance), fresh-id retry completes the handshake, Health works, budget
intact. Mutation-checked: with the final gate disabled, the shutdown test
fails with "the registry Hello must not be called after shutdown began".

**Finding 6 (P2): the handshake leaked payload-budget weight on a failed
answer.** In the inline handshake branch, a failed `respond` returned
WITHOUT `releaseServiceEnvelope(&frame)` — the frame's weight stayed
stranded in the aggregate 64 MiB budget forever (a handful of connections
could compound this into exhaustion).

Fix: `dispatchHandshake` OWNS the frame — `defer
releaseServiceEnvelope(frame)` right after the successful read, so the
weight is returned exactly once on every path (success, handler error,
marshal error, write failure, refusal). The per-connection handler path's
weight transfer is unchanged.

Deterministic test `TestEnvelopeHandshakeAnswerWriteFailureReleasesBudget`:
a valid Hello is sent, the client reads ONLY the answer's length header
(proving the handshake dispatched and the answer write is in flight) and
closes the connection — the server's pending write fails on the synchronous
pipe; the serve loop must finish and the FULL `servicePayloadBudget` must
be acquirable again; repeated 8× because a per-connection leak is exactly
an accumulating one (the full-budget acquire fails on ANY stranded weight).
Note: a strictly-valid Hello cannot be padded — the strict decode refuses
unknown-field residue — so the frame is the one the handshake actually
allocates; the accounting is what the test pins. Mutation-checked: with the
defer removed and the leak restored, the test fails on the first iteration.

### Round 2 check results (2026-09-13, Go 1.27.0, CI tags,
`-ldflags=-checklinkname=0`)

```
go build .                                          → exit 0
go vet -a ./...                                     → exactly the one
                                                      pre-existing finding
                                                      (unreachable code,
                                                      internal/boxdns/
                                                      dns_manager_windows.go:246)
gofmt -l (changed files)                            → clean
git diff --check                                    → clean
remediation suite (17 tests: 13 round-1 + 4 new)    → ok (17 × 50 = 850 runs,
  -count=50                                           0 failures)
go test . -count=20                                 → ok (69 tests × 20)
go test ./...                                       → ThroneCore, internal/xray,
                                                      internal/xraydns ok;
                                                      winipcfg: the same 8
                                                      pre-existing environment
                                                      failures (no VM adapter)
                                                      — baseline, not a
                                                      regression
go test -race                                       → BLOCKED (unchanged):
                                                      CGO_ENABLED=0, no C
                                                      compiler on this machine
generated protobuf vs fresh protoc 31.1 regen       → byte-identical
                                                      (libcore.pb.go
                                                      e0e15c6e…ee1ad1bd,
                                                      libcore_grpc.pb.go
                                                      7cd45efa…aae93f);
                                                      libcore.proto itself
                                                      unchanged
tests/proxycore/check_no_updater.sh                 → BLOCKED as acceptance
                                                      evidence (static grep
                                                      only, exit 0; no C++
                                                      toolchain for a real
                                                      GUI build check)
real SCM/VM service lifecycle                       → BLOCKED (unchanged):
                                                      the service code has
                                                      never run under SCM;
                                                      the in-process
                                                      Execute/SCM-contract
                                                      tests and the real-pipe
                                                      non-elevated identity
                                                      test do not substitute
                                                      for VM evidence
```

## Still BLOCKED (not changed by PC-110)

- Real SCM start/stop, non-elevated-client refusal and SDDL-refusal under a
  normal user on a disposable Windows VM (PC-100 Gate 0/1 evidence).
- The service code has still never executed under SCM anywhere.
- The string-JSON config contract inside `LoadConfigReq.typed_payload`
  remains interim (the deny-list policy is unchanged); typed config
  parameters where the service builds the config itself remain future work.
- PC-120 (installer: per-user SID grants, `THRONE_SERVICE_*` provisioning)
  and PC-130+ are not started.

## Remediation round 3 — F7: bounded shutdown vs in-flight Start (2026-09-13)

An independent review (GPT 5.6) confirmed one further defect in the round-2
fix. Severity P2 (availability, authorized client only — NOT P1: the vector
needs an owner/Administrator identity past the pipe ACL + token check, so no
privilege boundary is crossed; the impact is a stalled Stop, not a
post-Stopped runtime or a policy bypass).

**Finding 7 (P2): the stop path could wait unboundedly for an in-flight
Start.** Round 2 held `serviceRuntimeMu` across the whole
`ServiceStart → globalServer.Start` delegation (Xray create/start,
`boxmain.Create` with TUN setup), while `Execute`'s
`beginServiceRuntimeShutdown()` took the same lock with NO timeout — ahead
of the bounded 2 s handlers wait. A slow or hanging creation stalled SCM
Stop/`Stopped` without bound, contradicting the documented bounded
shutdown. The cancel signal was already delivered (the stop path cancels
the serve context before raising the mark, and the request context derives
from it) but the legacy `Start` never reads its `ctx`. The existing
suspended-Start test parks at `serviceStartPause` BEFORE the lock, so this
chain was untested.

Fix (`service_windows.go` only — `server.go`, `dispatch.go`,
`service_config_policy.go`, `libcore.proto` untouched, GUI-child behavior
unchanged): every `serviceRuntimeMu` critical section is O(1) flag/counter
work; the lock is never held across the delegation, so the mark
acquisition cannot stall. `ServiceStart` mirrors the mark in two O(1)
steps around a lock-free delegation (new `serviceStartDelegate` seam,
production = legacy `Start`): pre-check (past the mark → refuse, sink
never reached) and post-check (finished past the mark → idempotent
teardown via `globalServer.Stop`, refuse `errServiceStopping` → envelope
code 7). Per Start: refused-before-creation or created-then-torn-down; no
persisting post-Stopped runtime. Documented residual: a creation
outliving the bounded handlers wait finishes its self-teardown after
`Stopped` is published — transient by construction, never persisting. No
lock is ever held across another (barrier lock alone for flag/counter,
`lifecycleMu` alone inside delegate/teardown) — no deadlock. `Execute`
resets the barrier state at run start (the sticky global mark can no
longer poison an in-process restart); `serviceRuntimeStarting` counter
added for tests/diagnostics.

Tests (`service_lifecycle_windows_test.go`, now 19):
`TestServiceExecuteStopBoundedWithStartInsideCreation` (real `Execute` +
real winio pipe, Start parked by a delegate stub INSIDE creation, past the
pre-check): Stop completes while creation is blocked (`Stopped` via the
bounded wait, elapsed asserted < 10 s); after release — self-teardown, no
box/Xray/extra process, admission and barrier counters zero, budget
intact. Mutation-checked: under the old lock-across-delegation semantics
the test fails in 15 s with the F7 message (verified, then reverted).
`TestServiceStartPostCheckRefusesAfterMark` (direct, no pipe): mark lands
mid-creation, delegation "succeeds" without touching the sink → return is
exactly `errServiceStopping`, mapped to code 7. The round-2 suspended-Start
test is retained and green.

Round 3 check results (this machine, Go 1.27.0, CI tags,
`-ldflags=-checklinkname=0`):

```
go build .                                          → exit 0
go vet . (main package, tags)                       → clean except the one
                                                      pre-existing finding
                                                      (internal/boxdns/
                                                      dns_manager_windows.go:246
                                                      unreachable code)
gofmt -l (changed files)                            → clean
git diff --check                                    → clean
go test . -count=1                                  → ok (71 tests, 0 failures)
go test -race                                       → BLOCKED (unchanged):
                                                      CGO_ENABLED=0, no C
                                                      compiler on this machine
protected files (server.go, dispatch.go,              → byte-identical
  service_config_policy.go, libcore.proto)            (git diff HEAD empty)
real SCM/VM service lifecycle                       → BLOCKED (unchanged)
```

Status unchanged: PC-100/PC-110 stay **BLOCKED** (VM evidence only);
Gate 0/1 open. The tree remains uncommitted on top of `bfd79c58` pending
the owner's decision — no commit was created by this round.

### Mini VM re-run (round 6, same day)

To close the "VM ran older code" wording gap (the main package says
"round 2" while the tree had moved on), the CURRENT tree was rebuilt in
the same disposable VM and the SCM lifecycle re-run: clean slate (1060),
build PASS, start/RUNNING/LocalSystem/pipe-present, STOPPED in 0 s,
second start, duplicate stop 1062, delete + query 1060 — all PASS.
Artifacts: `D:\GLM_project\vm-evidence\evidence-package\round6-mini\`
(status, tree fingerprint, master-log excerpt, notes). Notable:
the mini-build sha256 is byte-identical to the earlier VM build
(`81D1A4D9…47`), which — same toolchain/flags/trimpath, `.git` excluded —
implies the earlier build already compiled the same `.go` tree (the F7
fix included); the "round 2" manifest wording was imprecise. Incidents
(post-cleanup missing toolchain, `/rl HIGHEST` denied from guestcontrol,
VM found powered off mid-run and restarted) are logged honestly in
`round6-mini/ROUND6-MINI-NOTES.md`.
