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
| R1 | P1 | Незакрытый `)` в SDDL ACE в конкатенации `THRONE_SERVICE_SDDL` | `script/windows_installer.iss`, `SetupServiceEnv` | **VM verified** (2026-09-16/17): portable regression зелёный; in-guest парс (RawSecurityDescriptor/ConvertFrom-SddlString) PASS; read-back реестра содержит полную SDDL. Логи: `vm-evidence/recovery/rec01/` |
| R2 | P1 | `Stop` не ограничен 2-секундным deadline: синхронный Stop до select; Start держит `lifecycleMu` в `boxmain.Create` | `core/server/service_windows.go`, `core/server/server.go` | Открыт → REC-02 |
| R3 | P1 | Повторный `Start` отложенным cleanup стирает ссылку на работающий runtime (`setBoxInstance(nil,nil)`, сброс mark) | `core/server/server.go` | Открыт → REC-02 |
| R4 | P1 | Installer не проверяет безопасность цели: `sc`/`icacls` без checked exit codes, repair без ownership-проверки, нет отдельных кавычек вокруг exe в ImagePath, нет reparse-отказа/read-back DACL | `script/windows_installer.iss` | **Implemented + VM verified** (07685be8/cdd40710, Setup `ba03f60f…`): quoted ImagePath, ownership guard в PrepareToInstall (foreign — EAbort до изменений), read-back Environment/DACL, reparse-отказ. NOT RUN в VM: reparse-кейс. Silent exit codes недостоверны (см. REC-01.md §6) |
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
| Состояние | RUNNING (boot #4, 2026-09-16 21:43 +03), Win11 Pro 25H2 build 10.0.26200.8037, EFI, 4 vCPU, 8192 MB, VDI 120 GB dynamic |
| Snapshot'ы | `REC00-CLEAN-BASE-OS` `525f8012-…`; `REC00B-TOOLCHAIN-READY` `a071da87-…` (содержит ПОЛНУЮ сессию #1: recagent v1, тулчейн, BUILD_OK); `REC00B-TOOLCHAIN-READY-V2` `31ef4968-…` (актуальный: тулчейн + recagent v2 + RecVBoxSvc-механика) — НЕ удалять до конца PC-140 |
| Guest Additions | 7.2.16 r174877 (VBoxService в госте — см. блокер ниже) |
| ISO (легальный, официальный MS) | `E:\Win11_25H2_English_x64.iso`, 8 471 603 200 B, sha256 `768984706b90…65e3` (пересчитан 2026-09-16, совпал) |
| Диски | D: свободно ~103 ГБ; E: свободно ~240 ГБ |

### 6.1 Итог REC-00B (2026-09-16, вечер)

- **Warm-session полный цикл — PASS** (run7): guest command (stdout/exit) ✓,
  файл в обе стороны со сверкой SHA-256 ✓, доставка portable Go 1.27.0 +
  module cache одним архивом (450 MB) ✓, compile smoke на source
  `facebecf(+gen)` с `BUILD_OK` ✓, артефакт `ThroneCore-rec00b.exe`
  sha256 `b03bc93cccc8d731858a8f1ec16ed2d6729235c36f8f837d2303c78c48ad555d`
  — **побайтово идентичен** в двух независимых сборках (run4 = run7).
- **Cold-start auto-ready — BLOCKED**: после чистого poweroff→start
  VBoxService не стартует сам (AUTO_START, но STATE STOPPED, exit 1077 — SCM
  не пытался); гостевой канал недоступен до ручного `net start VBoxService`.
  Временная задача `RecVBoxSvc` (SYSTEM, onstart, `cmd /c net start
  VBoxService`) сработала на boot#3, но исчезла после внешних вмешательств
  (см. 6.2). Это workaround, не фикс; кандидат-фикс — repair/reinstall GA из
  локального ISO + контролируемая boot-матрица.
- **6.2 Внешние вмешательства в стенд (важно, вопрос владельцу).** Подтверждённые
  события (каждое — с источником доказательств): (а) два жёстких reset —
  источник: VBox.log сессии, открытой 21:43:43 +03
  (`E:\VMs\PC130-Evidence\Logs\VBox.log`), `RUNNING→RESETTING` в VM-времени
  00:08:08 и 00:11:47 = 21:51:51 и 21:55:30 +03; (б) restore к
  `REC00-CLEAN-BASE-OS` между 20:50 и 20:52 +03 — источник: хост-сторона,
  цепочка дисков (`{76551b5e}.vdi` заморожен 20:41, активная запись в свежем
  diff `{fd9a4b10}.vdi` с 20:51, lastStateChange 20:51:40); (в) агент reset/
  restore не выполнял — источник: журналы команд агента
  (`vm-evidence/recovery/rec00b/run*`, glogs). Инициатор: **UNKNOWN** — не
  установлен; «VirtualBox GUI» — неподтверждённая гипотеза, вопрос владельцу
  открыт. Подтверждённые последствия restore (б): пропала созданная в сессии
  #1 среда — recagent v1, `C:\rec00` (созданы 20:30–20:41 на ветке дисков,
  замороженной restore'ом). Пропажа задачи `RecVBoxSvc` (создана 21:12,
  существовала и сработала на boot#3 — консольный вывод 21:32; отсутствует на
  boot#4 — консольный вывод 22:19) — это ОТДЕЛЬНОЕ событие, его причина —
  гипотеза, не факт: голый reset гостевой диск не откатывает, поэтому
  объяснение требует либо дополнительного host-side restore между 21:39 и
  22:19 (прямых следов в захваченных журналах нет), либо иного механизма;
  причина не установлена. Просьба владельцу: установить источник вмешательств
  и остановить их на время сетевых матриц. Дополнительное подтверждение
  (23:11–23:32 +03, boot#4): отсутствуют recagent v2 (создан 21:36–21:38,
  проба PASS 21:38; консоль-скриншот `rec01/console/r09-recagent-check.png`),
  RecVBoxSvc и тулчейн `C:\rec00\go` (`rec01/logs/20260916T202335Z-go-version.log`)
  — то есть всё, что было создано ПОСЛЕ restore 20:51; это усиливает гипотезу
  о повторном host-side restore между 21:39 и 22:19 (см. DIAG §6); инициатор
  по-прежнему UNKNOWN.
- **Каналы управления:** guestcontrol — рабочий при запущенном VBoxService
  (учетка `recagent`, пароль в guest property `REC00B_CRED`, хранится на
  хосте в .vbox и переживает reboot); интерактивная консоль + offline OCR —
  надёжный fallback (использовалась для всей диагностики). Synthetic
  keyboard: избегать мульти-сканкодовых аккордов одной посылкой; при залипании
  модификаторов лечится двойным Shift.
- **Раздельные вердикты REC-00B:** guest execution service ✓ (после ручного
  старта сервиса); credentials ✓ (recagent v2); controlled elevation в госте ✓
  (UAC Alt+Y, High integrity подтверждён `whoami /groups` = S-1-16-12288);
  передача файлов ✓; compile smoke ✓ (warm); холодная авто-готовность ✗
  BLOCKED.
- Полное расследование: `D:\GLM_project\vm-evidence\recovery\rec00b\`
  (`DIAG-guestcontrol-outage.md`, glogs/, console/, run1–run9, transfer/).

**Блокер стенда (один):** автоматический старт VBoxService на холодной
загрузке. До его устранения каждая холодная сессия требует ручного `net start
VBoxService` в госте (консоль) — после этого полный цикл воспроизводится
скриптом без сети (`-Offline`) при живом тулчейне на диске.

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

- Выполнено: REC-00 (baseline-документы, 2026-09-16 днём) и REC-00B
  (восстановление управления стендом, вечер): warm-цикл PASS, cold-start
  auto-ready BLOCKED (см. §6.1), production-код не менялся. REC-00 НЕ
  закрывается: по определению задачи PASS требует холодной последовательности
  «cold-start VM → guest command → сборка → выгрузка логов» без ручных
  вмешательств — сейчас возможна только с ручным `net start VBoxService`.
- REC-01 (статическая часть, ветка `agent/rec01-installer-static`):
  подтверждено, что база `93e8970` НЕ содержит recovery-коммит
  `010f5f50c9f63c8665fa1cf86465aeaf432d2b46` из `codex/recovery-review`;
  portable regression `tests/proxycore/test_installer_sddl_contract.py`
  красный на базе (2 SID-кейса), после переноса одно-символьного SDDL-фикса —
  зелёный. Inno build + SDDL parse/read-back в госте — по-прежнему
  требуются (REC-01 не завершён). Installer-hardening (R4) — draft в
  `docs/recovery/REC-01.md`.
- **REC-01 VM-часть — ВЫПОЛНЕНА (ночь 2026-09-16/17)**: ветка rebased на итог
  REC-00 (`a854a45f`), добавлены 28950f23 (silent override), 07685be8
  (hardening R4; итог `cdd40710`). Setup `ba03f60f…` собран в госте (ISCC
  6.7.3), verified: admin silent install, quoted ImagePath, Environment
  read-back, service RUNNING, pipe allow(evidence)/deny(recagent/recdeny),
  foreign-service EAbort до изменений, repair-кейс, fail-closed на sc-ошибке
  и DACL mismatch. NOT RUN: reparse-кейс. Матрица и находки: `REC-01.md` §5–6;
  артефакты `vm-evidence/recovery/rec01/`.
- Далее: REC-02.
- Правило веток: следующую задачу ветвить от фактического проверенного
  результата предыдущей.

## 8.1 Пометка для REC-01 (owner instruction, 2026-09-16)

База `93e8970` не включает recovery-коммит `010f5f50` (fix + regression + docs
в `codex/recovery-review`). До переноса в нашу ветку считалось: «SDDL fix не
применён». Теперь: перенос выполнен статически на
`agent/rec01-installer-static` (фикс + тест, regression зелёный), но это
`static checked`, НЕ `VM verified` — Inno build, Windows SDDL parser и
read-back Environment остаются обязательными шагами REC-01.

## 9. Классификатор статусов (не смешивать)

`implemented` — код написан; `static checked` — проверено разбором/portable
тестом без Windows-исполнения; `VM verified` — исполнено в disposable VM с
сохранёнными логами; `accepted` — принято владельцем. Исторические PASS §4 —
не `VM verified` повторно, пока стенд §6 не даст свежие логи.
