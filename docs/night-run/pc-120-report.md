# PC-120 report: installer + per-user SID + non-elevated UI proof

Date: 2026-09-14. Branch: `agent/pc120-installer` (base `09aa116c` + merge
`041a82b0` + steps 1–3 commit `9f60ba67` + ONE uncommitted one-char fix, see §5).
Executed in VM `PC100-Evidence` (Win11 Pro 25H2). Handoff: `handoff-pc120.md`
(steps 0–8 + amendments of 2026-09-14: artifact provenance, numeric AppVersion,
BUILD-INFO location, dead-path elevation check).

## What changed (code)

1. `script/windows_installer.iss` — `sc create ProxyCoreService` (demand,
   admin mode only), `SetupServiceEnv` via `AfterInstall` (per-user SID →
   `THRONE_SERVICE_SDDL`/`ALLOWED_SIDS`/`DATA_DIR` in the service
   `Environment`, fail-closed abort), service data dir + SYSTEM/Admin-only
   `icacls`, `sc stop/delete` on uninstall, merged data-folder prompt,
   non-admin Finish notice.
2. `src/main.cpp` — Windows admin auto-relaunch block removed (11 lines).
3. `src/ui/mainWindow/mainwindow_system.cpp` — Windows
   `get_elevated_permissions()` informs + returns `false`.
4. One-char fix (uncommitted): stray `)` in the `THRONE_SERVICE_*` `-Value`
   array broke the env PowerShell with "string missing terminator" (found by
   VM bisection + byte audit; two reviews missed it).

Static checks (step 4): `runProcessElevated` absent from `main.cpp`,
"Please run Throne as admin" absent everywhere, SDDL SY+BA+installer-SID
only, `git diff --check` clean, exactly 3 code files (+1-char fix).

## Matrix 7/7 (logs + screenshots: `vm-evidence/evidence-package/pc120/`, 43 files)

- **6.1 UAC: PASS** — exactly 1 `consent.exe` per admin attempt (4 attempts,
  PIDs logged, secure-desktop black captured); 0 for non-admin. Non-admin
  install verified (no service/key/datadir, files in user dir).
  SUB-ITEM CLOSED 2026-09-15: the non-admin Finish notice now renders
  (`61-nonadmin-finished-fixed.png`). Root cause (VM-diagnosed, not guessed):
  the RunList checklist is full-height (`H=209` at 768p, `notice-diag.log`)
  with a single item, so anything anchored below it landed off-page
  (`Top=385`, client bottom `~=365`) — that killed BOTH the original
  FinishedLabel append AND the first dedicated-label attempt. Fix in
  `script/windows_installer.iss` (uncommitted): shrink RunList to one row
  (`Height := ScaleY(30)`) on the non-admin Finished page, notice below it;
  ASCII hyphen (the .iss is UTF-8 without BOM). Retest: fresh non-admin
  install of `ProxyCoreSetup-notice.exe` (sha256
  `116982d5…04a6e3480`, 146517123 B, iscc 6.7.3 from the fixed .iss),
  notice visible, post-state correct (5 user-dir files, service 1060 absent,
  no ProgramData dir), then uninstalled clean.
- **6.2 service: PASS** — LocalSystem/demand/correct binPath; RUNNING + pipe;
  env grant byte-verified (per-user evidence SID). Note: 2 transient
  first-start exits (code 0, no trace), then stable incl. identical config —
  recorded as known startup flake (PC-110 family).
- **6.3 ACL: PASS** — dir lists only SYSTEM+Administrators; SYSTEM writes,
  limited refused.
- **6.4 dial: PASS** — admin CONNECTED; in-grant limited CONNECTED;
  stranger REFUSED; installer-grant specificity proven (limited REFUSED under
  evidence-only grant, by design).
- **6.5 UI: PASS** — Medium launch, titled window, zero UAC, no
  elevate-restart, no admin window.
- **6.6 repair: PASS** — reinstall exit 0; service+SDDL+data+ACL intact.
- **6.7 uninstall: PASS/PASS** — No: service gone, data intact (empty
  runtime `config\` remains — standard Inno RemoveDir semantics, noted);
  Yes: everything gone incl. env key.

Setup.exe: 146516865 B, sha256
`69dd8250e37977ad5ff28fec6e901d32b2c7e917a6c9588caa81e53b784997ca`
(see `BUILD-INFO.txt`; first build `6917ead4` superseded by the paren fix).
Built with Inno Setup 6.7.3 in-VM from CI artifacts of run 34851904063
(`windows-amd64` + legacy flavors), AppVersion 0.0.0 numeric.

## Honest boundaries (what was NOT proven)

- cache.db write via CheckConfig/Start RPC not executed (needs envelope RPC
  client); covered by service-identity-equivalence probe (SYSTEM write OK).
  Owner-invited option, still open.
- Non-admin Finish notice missing (see 6.1) — needs a fix + retest cycle.
- Interactive UAC-accept via injected keys does not work in this VM
  (bisected with a stock Microsoft binary — environment, not product);
  methodology adapted without rigor loss (PID observation + elevated launch
  with zero-consent polling).
- Installer grants the INSTALLING (elevated) user; a non-admin can never
  self-grant (fail-closed by design). User management beyond that is
  PC-200 territory.
- `on_menu_exit_triggered` RestartWithTun/Dns elevated branch is dead code
  after step 3 (no writer of those reasons remains) — verified by grep +
  zero elevations in-session; removal proposed for PC-200.
- VM snapshots taken: `pc120-step5-before`, `pc120-step6-before-install`,
  `pc120-66-repair-before` (+ pre-existing). Test users `limited/limited2`
  left registered (documented). D: pressure relieved (ISO→C:, tgz/pdb
  cleaned); guest shots cleaned after archival.
- Nothing committed, nothing pushed (step 8: owner decision). The one-char
  `.iss` fix MUST be committed before any rebuild/CI of this branch.
