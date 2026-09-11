# Morning review — что проверить владельцу

Дата: 2026-09-12 (после ночной сессии). Репозиторий: `D:\GLM_project\ProxyCore`, ветка `agent/night-foundation-2026-09-12`.

## 1. Быстрая проверка результата (5 минут)

```bash
cd /d/GLM_project/ProxyCore

# 1) Какие коммиты сделаны ночью (должно быть 4, история не переписана):
git log --oneline 21b8f680..HEAD

# 2) Чистое рабочее дерево:
git status --short

# 3) Что изменилось относительно baseline (25 файлов, только идентичность/
#    updater/документация/тесты; core/server не тронут):
git diff 21b8f680..HEAD --stat
git diff 21b8f680..HEAD --stat -- core/server   # пусто

# 4) Guard updater-пути (PC-020):
bash tests/proxycore/check_no_updater.sh

# 5) Прочитать отчёты:
docs/night-run/final-report.md
docs/night-run/test-results.md
docs/night-run/status.md
```

## 2. Проверка собранного ночью Go core

Toolchain лежит портативно (вне репозитория): `D:\GLM_project\tools` (Go 1.27.0, protoc 31.1).
Артефакт: `D:\GLM_project\tools\thronecore-build\ThroneCore.exe` (SHA-256 `c9da2da5ebefb268…`, см. docs/upstream-lock.md §6).

```bash
export PATH=/d/GLM_project/tools/go/bin:$PATH
cd /d/GLM_project/ProxyCore/core/server
go test -ldflags="-checklinkname=0" \
  -tags "with_clash_api,with_gvisor,with_quic,with_wireguard,with_utls,with_dhcp,with_tailscale,with_openvpn,with_openconnect,badlinkname,tfogo_checklinkname0,with_purego" \
  . ./internal/xray/ ./internal/xraydns/
```
Ожидание: `ok` по трём пакетам. winipcfg — не запускать на рабочей машине (там есть тесты, меняющие сеть/DNS).

## 3. Закрыть BLOCKED части (нужно для Gate 0)

### 3.1 C++ тесты (локально достаточно без VM)

1. Установить MSVC Build Tools (Desktop C++), CMake ≥3.20, Ninja, Qt 6.11.2 x64
   (upstream prebuilt: `throneproj/buildqt`), для GUI-сборки также OpenSSL.
2. ```bash
   export CMAKE_PREFIX_PATH="<путь>/Qt/lib/cmake"
   cmake -GNinja -DCMAKE_BUILD_TYPE=RelWithDebInfo -DPROXYCORE_BUILD_TESTS=ON -S . -B build
   cmake --build build --target proxycore_tests proxycore_characterization
   cd build && ctest -R proxycore --output-on-failure
   ```
3. Ожидание: `proxycore_migration` и `proxycore_characterization` зелёные; если
   есть ошибки компоновки — это ожидаемый первый пункт доработки (harness
   написан вслепую: нет C++ компилятора в ночной сессии), чинить в tests/, не
   в production-коде.

### 3.2 Windows 11 VM (GUI smoke + coexistence + TUN)

Пошагово: `docs/validation/baseline-windows.md` §4. Дополнительно к ней —
проверка coexistence PC-010: установить Throne (baseline) и ProxyCore рядом,
убедиться:
- у каждого свой каталог данных (`AppData/Local/Throne` и `AppData/Local/ProxyCore`), настройки не смешиваются;
- у каждого свой single-instance socket/pipe (`proxycore-…` / `proxycoreIPC-…`);
- миграция `-migrate-from-throne` копирует данные Throne, источник не меняется, rollback удаляет ровно скопированное;
- uninstall ProxyCore не удаляет данные Throne.

## 4. Публикация на GitHub (действие владельца — по вашим ограничениям ночью не выполнялось)

```bash
cd /d/GLM_project/ProxyCore
# создать приватный репозиторий ProxyCore в своём аккаунте (github.com/new, private)
git remote add origin https://github.com/<ваш-аккаунт>/ProxyCore.git
git push -u origin agent/night-foundation-2026-09-12
```
Ветка НЕ опубликована до этого шага; тегов/релизов нет.

## 5. Решение по PC-100

Gate 0 закрывается только после §3.1 + §3.2. До этого PC-100 не начинать
(нулевой контракт требует PASS PC-000). Если VM нет — ограничься §3.1 и
review-командой из AGENT_HANDOFF_COMMANDS.md («Проведи read-only review…»).

## 6. Заметки

- Updater: в release-упаковке CI по-прежнему скачивается инертный `updater.exe`
  (build_go.sh не менялся — upstream CI); ни один production-путь его не
  запускает (guard). Полная замена на подписанный transactional updater — PC-520.
- Синтетические данные: все fixture/тестовые креды — `example-*` / `PC-TEST`;
  реальных секретов, подписок, ключей в репозитории нет (проверено сканом).
- Инсталлятор ProxyCore: иконка `res/Throne.ico` и общая запись
  `Applications\Throne.exe` — известные ограничения (см. final-report §10.7).
