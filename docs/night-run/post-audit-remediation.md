# Post-audit remediation (2026-09-13)

This note supersedes implementation claims in the historical night-run and
PC-100 reports where they conflict with the current working tree.

- The functional delta from `21b8f680..249121d0` is restored in the working
  tree. Git ancestry is unchanged; rewriting it requires a separately
  authorized rebase/cherry-pick.
- The Linux release installer fails closed and the updater guard scans build,
  package, installer, workflow, CMake, and README paths.
- SQLite migration uses the SQLite Online Backup API instead of copying a live
  database and its WAL/SHM files independently.
- Service RPC dispatch remains an explicit five-method allowlist. SDDL is
  parsed and canonicalized by Windows before broad trustees are rejected, and
  deny-only token groups do not authorize a client.
- Service connections, handlers, frame duration, writes, and aggregate payload
  memory are bounded.
- `Start` and `CheckConfig` share the filesystem policy. Xray permits ordinary
  transport URL `path` fields but rejects known privileged sinks including
  log files, TLS/REALITY master-key logs, geodata asset files, top-level
  environment overrides, and Hysteria file-masquerade directories.
- `CheckConfig` also schema-validates every `xray_full_configs` item.

Real SCM start/stop, non-elevated-client refusal, installation/coexistence,
and Qt/C++ execution still require the disposable Windows VM/toolchain. They
remain **BLOCKED**, not PASS. Gate 0 and Gate 1 therefore remain open.
