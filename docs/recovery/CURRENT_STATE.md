# CURRENT_STATE — единая актуальная таблица состояния ProxyCore

Дата фиксации: 2026-09-16 (REC-00). Источник задачи: `docs/recovery/REVIEW_RU.md`
и `docs/recovery/NEXT_TASKS_RU.md` (ветка `codex/recovery-review` репозитория
`DreamHs3/GLM_project`). Этот файл — живой документ: каждая следующая задача
(REC-01 и далее) обязана обновлять его, а не переписывать исторические отчёты.
Исторические отчёты (`docs/night-run/*`, handoff-файлы) сохраняются как есть;
актуальность подтверждается здесь.

## 1. Источник (source of truth)

| Параметр | Значение |
|---|---|
| Репозиторий кода | `DreamHs3/Throne.git` (checkout `D:\GLM_project\ProxyCore`) |
| Ветка данной фиксации | `agent/rec00-current-state` (создана из `agent/pc130-enforcement`) |
| Base SHA | `93e897003a69fe34d86e4410d79b6fcc5bc8fbbe` (= gitlink `ProxyCore` в main внешнего репо `DreamHs3/GLM_project` @ `522216b`) |
| Рабочее дерево на момент фиксации | чистое (до создания файлов REC-00) |
| upstream remote | `throneproj/Throne` — не менять (правило recovery-пакета) |
| ТЗ и recovery-документы | внешний репо `DreamHs3/GLM_project`: main (ТЗ/handoff), `codex/recovery-review` (ревью + NEXT_TASKS) |

## 2. Тулчейн (фактические `--version`, хост Windows, 2026-09-16)

| Инструмент | Версия | Где | Статус |
|---|---|---|---|
| Go | go1.27.0 windows/amd64 | `D:\GLM_project\tools\go` (portable, не на PATH) | доступен |
| protoc | libprotoc 31.1 | `D:\GLM_project\tools\protoc-31.1` (portable) | доступен |
| Python | 3.12.10 | PATH хоста | доступен (для portable regression REC-01) |
| Git | 2.55.0.windows.4 | PATH хоста | доступен |
| VirtualBox | 7.2.16 r174877 | `C:\Program Files\Oracle\VirtualBox` | доступен |
| MSVC/cl, CMake, Ninja, Qt | ОТСУТСТВУЮТ на хосте | — | C++-сборка только в VM/CI |
| Inno Setup (ISCC) | ОТСУТСТВУЕТ на хосте | — | сборка Setup — только в VM (исторически Inno 6.7.3 в госте) |

**go.mod minimum ≠ протестированной версии Go:** `core/server/go.mod` требует
`go 1.26.0`, фактически собирали и тестировали только на **go1.27.0** (хост сейчас;
историческая VM — `vm-evidence/evidence-package/go_version.log`: тоже 1.27.0).
На Go 1.26.x сборка НЕ проверялась. Для воспроизводимости фиксировать 1.27.0.

Исторические host-артефакты (провенанс не перепроверялся в этой сессии):
`tools/thronecore-build/ThroneCore.exe`
sha256 `c9da2da5ebefb26894f891849bc05ebb5d8dd7c755d3d4f780e2522d6ccd2b7f`,
`ThroneCore-spike.exe`
sha256 `a3961cfe9db0d30fd404d7bd63f18db736f2075afedde27cb522b79f2cc24df9`.

## 3. Дефекты (из REVIEW_RU.md, статус не менялся)

| ID | Приоритет | Суть | Где | Статус |
|---|---|---|---|---|
| R1 | P1 | Незакрытый `)` в SDDL ACE в конкатенации `THRONE_SERVICE_SDDL` | `script/windows_installer.iss`, `SetupServiceEnv` | Закрывающая скобка возвращена recovery-патчем ветки `codex/recovery-review` + portable regression; Inno build/SDDL parse/read-back в VM НЕ выполнены → закрывает REC-01 |
| R2 | P1 | `Stop` не ограничен 2-секундным deadline: синхронный Stop до select; Start держит `lifecycleMu` в `boxmain.Create` | `core/server/service_windows.go`, `core/server/server.go` | Открыт → REC-02 |
| R3 | P1 | Повторный `Start` отложенным cleanup стирает ссылку на работающий runtime (`setBoxInstance(nil,nil)`, сброс mark) | `core/server/server.go` | Открыт → REC-02 |
| R4 | P1 | Installer не проверяет безопасность цели: `sc`/`icacls` без checked exit codes, repair без ownership-проверки, нет отдельных кавычек вокруг exe в ImagePath, нет reparse-отказа/read-back DACL | `script/windows_installer.iss` | Открыт → REC-01 |
| R5 | P1 (план) | PC-130 требует UI→service RPC, которых нет: UI поднимает child-core + legacy frame, несовместимый с envelope | `src/sys/Process.cpp`, `src/main.cpp`, `src/api/RPC.cpp` | Открыт → REC-03 (узкий service transport до PC-130) |

## 4. Какие PASS только исторические (VM PC100-Evidence УДАЛЕНА)

VM `PC100-Evidence` полностью удалена владельцем 2026-09-15 после приёмки
PC-120 (status.md, последняя запись). Всё ниже — утверждения отчётов с
артефактами в `D:\GLM_project\vm-evidence\`, НЕ независимо воспроизводимые,
пока новый стенд (§6) не пройдёт smoke:

- Gate 0 закрыт 2026-09-14 (решение владельца; CI-лог и harness-фиксы сохранены).
- ctest 3/3 в VM (2026-09-14, MSVC 2022 + CMake 3.30.6 + Ninja 1.12.1 + Qt 6.11.2):
  `proxycore_migration`, `proxycore_characterization`, `proxycore_parser_fixtures`.
- PC-110 round6-mini: SCM lifecycle в VM (start/RUNNING/LocalSystem/pipe, STOPPED,
  second start, duplicate stop 1062, delete) — PASS в удалённой VM.
- PC-120 installer 7/7 + Finish-notice retest — принят владельцем 2026-09-15
  с оговорками (см. §5); Setup SHA тех прогонов к новому дереву НЕ переносится.
- CI build-cpp 11/11 (run 34779991199, DreamHs3/Throne) — для старых деревьев.
- PC-010 D2: fix УЖЕ в base SHA `93e8970`; отдельная регрессия D2 в этой
  сессии NOT RUN (подтвердить затронутые тести при ближайшей сборке).

## 5. Принятые исключения (не скрыты, не переоткрывать без причины)

- D1 baseline-дефект GUI (кандидат в upstream-репорт, базу не патчить).
- TUN не достигнут в GUI-baseline (честный FAIL; legacy-путь демонтирован PC-120).
- PC-120 оговорки приёмки: cache.db write через CheckConfig/Start RPC не
  исполнялся (boundary); 2 транзиентных first-start exit (known flake); пустой
  config dir при No (стандартный RemoveDir); ADR-002 addendum N8 (AfterInstall
  deviation); AppVersion numeric 0.0.0 (raw tag ломает iscc).
- Gate 1 ОТКРЫТ: envelope-over-SCM VM top-up для PC-110 не выполнен.
- ПК-130 entry gate (по 00-PREFLIGHT): BLOCKED — нет закрытия Gate 1, нет
  pipe top-up Health/CheckConfig/Start/Stop, нет стенда.

## 6. Стенд (VM) — восстановлен частично, smoke BLOCKED

| Параметр | Значение |
|---|---|
| VM | `PC130-Evidence`, UUID `2a5bd38d-46bd-47a8-8e83-c1fe1aa2138b`, E:\VMs |
| Состояние | RUNNING с 2026-09-16 17:44:55 (+03); desktop, вход выполнен пользователем `evidence` (admin) — скриншот `vm-evidence/recovery/rec00-vm-state2.png` |
| Гость | Windows 11 Pro 25H2, build 10.0.26200.8037, en-US, EFI, 4 vCPU, 8192 MB, VDI 120 GB dynamic |
| Guest Additions | 7.2.16 r174877, RunLevel 3 |
| ISO (легальный, официальный MS) | `E:\Win11_25H2_English_x64.iso`, 8 471 603 200 B, sha256 `768984706b909479417b2368438909440f2967ff05c6a9195ed2667254e465e3` (пересчитан 2026-09-16, совпал с 01-VM-PROVISION.txt) |
| Snapshot | `REC00-CLEAN-BASE-OS` UUID `525f8012-18d5-4c69-9414-8f5838255c3f` (2026-09-16, live): чистая ОС, БЕЗ тулчейна/служб/TUN/firewall — НЕ удалять до конца PC-140 |
| Диски | D: свободно ~103 ГБ; E: свободно ~240 ГБ |
| Часы | `rtcuseutc=off` (гость в локальном времени; UTC фиксировать командой в госте — пока BLOCKED) |

**BLOCKED (единственный отсутствующий prerequisite стендовой части):**
`VBoxManage guestcontrol` завершается `VBOX_E_IPRT_ERROR: The guest execution
service is not ready (yet)` — 2 попытки с паузой 30 с, 2026-09-16 ~19:5x–20:0x.
Следствие: guest execution, передача файлов, controlled elevation, снятие
stdout/stderr/exit code, compile smoke в госте — NOT RUN. Для разблокировки
нужно (владелец/интерактивно в консоли VM): проверить/переустановить guest
control-компонент Guest Additions и передать агенту пароль пользователя
`evidence` (пароль НЕ архивирован — только чат владельца; агенту не сообщать
в логах/файлах). Скрипт-оркестратор: `tests/proxycore/windows/recovery/run-vm-smoke.ps1`
(см. README там же); он сам останавливается с кодом 2 при этой ошибке.

## 7. Инвентарь PC-130 попыток (локально, ничего не удалено)

- `vm-evidence/evidence-package/pc130/`: `00-PREFLIGHT.txt` (entry gate BLOCKED,
  SHA base = 93e8970 подтверждён), `01-VM-PROVISION.txt` (успешный unattended
  install 2026-09-16 после правки RAM/cpus/TPM), `STAGE2-VM-INSTALL.txt`
  (3 неудачные попытки 2026-09-15, закрыто), `02-VM-STATE-2026-09-16.txt`
  (аддендум REC-00: состояние, скриншоты, snapshot, guest-control блокировка).
- Скрипты оркестрации исторических прогонов: `vm-evidence/evidence-package/run-scripts/`
  (bootstrap1–8.cmd, build_vm.ps1, cleanup_vm.ps1, dial.ps1, probe*.ps1,
  round2–6*.ps1, run_master.ps1) и `vm-evidence/share/gui/` (эпоха PC-120).
  Кода PC-130-harness в share/нет — сохранять diff из гостя нечего (гость
  недоступен, проверка в госте BLOCKED).
- `vm-evidence/tools/Fido.ps1` — легальный загрузчик ISO с серверов MS.
- ADR: в дереве только `docs/adr/ADR-002-service-host-spike.md`.
  **ADR-003, enforcement adapter, pc-130-report, windows/pc130 harness —
  ОТСУТСТВУЮТ в Git** (попыток кода в дереве нет).

## 8. Текущая задача и порядок

- Выполнено: REC-00 (этот файл + README/скрипт оркестрации + аддендум в pc130;
  production-код не менялся).
- Далее: REC-01 (SDDL fix проверка + installer hardening; статическую часть
  можно делать без VM), затем REC-02 (lifecycle Start/Stop; обязательны
  Windows runtime тесты). VM-приёмка REC-01/02 — после разблокировки §6.
- Правило веток: следующую задачу ветвить от фактического проверенного
  результата предыдущей (сейчас: `agent/rec00-current-state` после коммита
  файлов REC-00).

## 9. Классификатор статусов (не смешивать)

`implemented` — код написан; `static checked` — проверено разбором/portable
тестом без Windows-исполнения; `VM verified` — исполнено в disposable VM с
сохранёнными логами; `accepted` — принято владельцем. Исторические PASS §4 —
не `VM verified` повторно, пока стенд §6 не даст свежие логи.
