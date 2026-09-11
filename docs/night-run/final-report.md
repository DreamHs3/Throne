# Night run final report — Stage 0 (PC-000 → PC-010 → PC-020 → PC-030)

Date: 2026-09-12 (night, autonomous). Branch `agent/night-foundation-2026-09-12`.

## 1. Общий статус

**PARTIAL.** Ни один пакет не FAIL; ограничения ночной среды (нет C++/Qt toolchain, нет Windows VM, запрещены UAC/TUN/изменения сети) не позволили исполнить C++ и GUI-части. Всё, что можно было исполнить безопасно, исполнено и зелёное. BLOCKED не засчитан как PASS.

## 2. Ветка и коммиты

- Branch: `agent/night-foundation-2026-09-12`, создана от audit baseline `21b8f680b95d1dfe7906b6b64f6c3c51c263ba40` («fix windows 11 theme»).
- Upstream `dev` HEAD на момент клона (`249121d0`) опережал baseline на 4 коммита — они в fork **не включены**.
- Commits (по одному на пакет, история не переписывалась):
  1. `b462ef16` — PC-000: lock upstream and document Windows baseline
  2. `9916bd9e` — PC-010: isolate ProxyCore identity and data
  3. `db26adf3` — PC-020: disable upstream update installation
  4. `6759182f` — PC-030: add compiler and lifecycle characterization tests
- Remote `upstream` → `https://github.com/throneproj/Throne.git` (read-only использование). **По обязательным ограничениям push/создание remote не выполнялись** — см. §15.

## 3. Статус пакетов

| Пакет | Статус | Суть |
| --- | --- | --- |
| PC-000 | PARTIAL | Lock-документация, workflow, fixtures — готово; Go core собран и протестирован (PASS); Qt GUI build/smoke — BLOCKED (нет toolchain); TUN — BLOCKED (VM) |
| PC-010 | PARTIAL | Изоляция идентичности/данных + copy-migration реализованы; coexistence/rollback тесты написаны, но не исполнялись (нет C++ toolchain) |
| PC-020 | PASS (по доступной evidence) | Updater-путь удалён из production-кода, сообщение добавлено, static guard 4/4; runtime-проверка GUI — по VM-процедуре |
| PC-030 | PARTIAL | Harness + characterization coverage написаны и подключены к CTest; исполнение BLOCKED (нет C++ toolchain) |

Gate 0 («PC-000…030 зелёные») **не закрыт** — нужен VM-прогон + компиляция/запуск тестов (см. §13).

## 4. Полный список изменённых/новых файлов (25)

Изменённые upstream (7): `CMakeLists.txt` (+тест-опция, +2 файла в PROJECT_SOURCES), `cmake/windows/windows.cmake` (PE-метаданные), `include/ui/mainwindow.h` (удалён `ExitReason::RunUpdater`), `include/ui/mainwindow.ui` (windowTitle), `src/main.cpp`, `src/ui/mainWindow/mainwindow_setup.cpp`, `src/ui/mainWindow/mainwindow_system.cpp`, `src/sys/windows/UrlScheme.cpp`, `script/windows_installer.iss`.

Новые (16): `docs/upstream-lock.md`, `docs/validation/baseline-windows.md`, `docs/night-run/{status,test-results,final-report,morning-review}.md`, `include/proxycore/storage/ThroneMigration.h`, `src/proxycore/storage/ThroneMigration.cpp`, `tests/fixtures/{README.md,ssh.json,shadowsocks.json,socks.json}`, `tests/proxycore/{CMakeLists.txt,sources_app_core.cmake,test_throne_migration.cpp,test_route_characterization.cpp,test_parser_fixtures.cpp,test_stubs.cpp,check_no_updater.sh}`.

Не тронуты: `core/server/**` (Go), `go.mod`/`go.sum`, `.github/workflows/**`, parsers/протоколы, `generate.cpp`/`RouteRule.cpp`/`RouteProfile.cpp` (production-код правил), Linux/macOS код, все зависимости.

## 5. Reused / wrapped / modified

- **Reused без изменений**: весь Go core (собран), parsers SSH/SS/SOCKS (covered тестами, не тронуты), RoutesRepo/RouteProfile/RouteRule (covered, не тронуты), subscriptions/geoip/dashboard загрузки, deploy/pack скрипты.
- **Modified (адресно)**: идентичность приложения (main.cpp, setup, .ui, PE metadata, installer), updater-путь (mainwindow_system.cpp, enum), single-instance/IPC префиксы, URL-scheme/ProgId, CMake (+option, +2 исходника).
- **New (own code, namespace `ProxyCore`)**: copy-migration + rollback с SHA-256 manifest; тестовые harness'ы за опцией `PROXYCORE_BUILD_TESTS=ON` (default OFF — upstream-сборка идентична).

## 6–7. Команды и результаты

Полный лог: `docs/night-run/test-results.md`. Ключевое: ThroneCore собран с точными CI-флагами (exit 0, SHA-256 `c9da2da5ebefb268…`); go test — 3 пакета ok, winipcfg 21 pass / 8 VM-blocked; smoke core — ожидаемые версии и выход; guard PC-020 — 4/4; секреты/бинарники в diff — отсутствуют.

## 8. Acceptance criteria — что доказано

- A01-фрагмент (build contract Go): зафиксирован toolchain, replacements, build log, artifact hash — **доказано**.
- Coexistence (PC-010 acceptance): статически — отдельные data dir (`AppData/Local/ProxyCore`), отдельные socket/pipe имена, отдельный installer AppId/директории; runtime-доказательство (две установки рядом) — **BLOCKED до VM**.
- PC-020 acceptance: ни один production action не скачивает/запускает Throne updater — доказано удалением кода + guard-чеком; ручной импорт профилей не затронут (изменения только в update-пути).
- PC-030: тесты фиксируют текущее поведение (включая quirks), production-код правил не изменялся — по коду; исполнение — BLOCKED.

## 9. BLOCKED из-за среды

1. Qt GUI build + GUI smoke (нет MSVC/Qt/CMake; UAC запрещён) — процедура: `docs/validation/baseline-windows.md` §4.
2. Исполнение C++ тестов (proxycore_tests, proxycore_characterization, parser fixtures) — тот же toolchain.
3. TUN и все network-инъекции — VM only (запрещены на рабочей машине).
4. winipcfg Go-тесты, мутирующие сеть (8 шт) — disposable machine only.
5. Coexistence двух установок (Throne + ProxyCore) runtime-проверка — VM.

## 10. Upstream defects / technical debt (найдено, не чинилось)

1. **socks4 `version` string/int round-trip**: `socks::ExportToJson/Build()` пишут `"version": "4"` строкой, `ParseFromJson` читает `.toInt()` → 0. Зафиксировано fixture-тестом как текущее поведение (фикс должен осознанно перевернуть проверку).
2. **Сериализация с побочным эффектом (M02 подтверждена)**: `RouteRule::get_rule_json()` мутирует `action` (route+blockID → reject). Теперь зафиксировано characterization-тестом.
3. `shadowsocks::Build()` перезаписывает `plugin`/`plugin_opts` при каждом вызове (не-const сериализация).
4. `winipcfg_test.go`: vet-ошибки (`%w` в `t.Errorf`) + тесты требуют подготовленный тестовый интерфейс и часть мутирует сеть.
5. `go test` требует `-ldflags="-checklinkname=0"` на Go 1.27 (tfo-go linkname) — build contract, не задокументирован upstream.
6. Unpinned release-активы CI (updater/libcronet/OpenSSL через `latest`, srslist.h по ветке) — provenance risk для будущего релиза (PC-610).
7. Инсталлятор ProxyCore всё ещё использует `res/Throne.ico` и регистрирует общий ключ `Applications\Throne.exe` (ограничение неизменяемого exe-имени) — известные ограничения coexistence.

## 11. Regression и licensing risks

- **Regression**: C++-изменения не компилировались (нет toolchain) — возможные compile-ошибки в новых файлах/правках; митIGATION — минимальный diff, upstream production-логика не менялась (кроме updater-удаления), тесты готовы.
- **Regression**: удаление `ExitReason::RunUpdater` — enum не сериализуется, использующие файлы обновлены; risk низкий.
- **Regression**: `setApplicationName("ProxyCore")` меняет QStandardPaths путь — новый пользователь получит пустую базу (ожидаемо); миграция `throne.db`-внутри-нашего-каталога не нужна. Throne-данные не затрагиваются.
- **Licensing**: fork остаётся GPLv3; добавлен только собственный GPL-совместимый код и документация; тесты/фикстуры — синтетические данные, без сторонних активов. Notices не менялись (это PC-600). Внутреннее использование/частный fork — ок по LICENSING.md; распространение — только после legal review.

## 12. Rollback procedure (точные команды)

Ветка не опубликована; история линейная. Откат в обратном порядке (каждый `git revert` — отдельный коммит, история не переписывается):

```bash
cd /d/GLM_project/ProxyCore
# Откатить PC-030 (тесты):
git revert 6759182f
# Откатить PC-020 (updater):
git revert db26adf3
# Откатить PC-010 (идентичность/данные):
git revert 9916bd9e
# Откатить PC-000 (документы):
git revert b462ef16
# Полный возврат к baseline (сохраняя историю):
git revert --mainline 1 <merge-or-all-four-above>
```

Данные: миграция PC-010 пишет только в data dir ProxyCore и никогда не трогает Throne; откат данных — `RollbackMigration(configDir)` (удаляет ровно файлы из `migration-manifest.json`) или просто удалить каталог `AppData/Local/ProxyCore`. Инсталлятор ProxyCore при uninstall с «удалить данные» трогает только собственные каталоги.
Артефакт ThroneCore.exe лежит вне репозитория (`D:\GLM_project\tools\thronecore-build`) — удалить каталог при желании.

## 13. Рекомендация: можно ли начинать PC-100

**Нет, не пока Gate 0 не закрыт.** PC-100 (service-capable runtime) требует PASS PC-000 по нулевому контракту. Перед PC-100 нужно (оценка: полдня):
1. Windows 11 VM: выполнить §4 `docs/validation/baseline-windows.md` (GUI build + smoke + import + core start/stop, без TUN), записать evidence.
2. Скомпилировать и запустить тесты: `cmake -GNinja -DPROXYCORE_BUILD_TESTS=ON …` → `ctest` (см. morning-review §команды).
3. Человекочитаемый review диффов PC-010/PC-020 (scope/терминология).
Если VM недоступна — PC-100 начинать нельзя; безопасная параллельная работа без Gate 0: подготовка fixtures/документации, ничего из Stage 1.

## 14. Команды утренней самопроверки

См. `docs/night-run/morning-review.md` — там же команды публикации на GitHub (по ограничениям ночью не выполнялись).

## 15. Отступление от первой строки запроса (GitHub)

В начале задачи сказано «Репозиторий создай на гитхабе я авторизую», но в обязательных ограничениях: «Не push, не создавай remote repository, PR, release или tag». Ограничения собраны: **remote не создавался, push не выполнялся**. Публикация — утреннее действие владельца (команды в morning-review.md).
