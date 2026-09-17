# REC-02 — жизненный цикл Start/Stop (R2/R3) — рабочий документ

Статус: **исправления implemented + Windows unit verified + SCM smoke VM
verified (2026-09-18).** Ветка `agent/rec02-lifecycle`
(`130bc51e` = R3, `3f63ef7c` = R2; база `59f28d3b` — закрытый REC-01).

Файлы: `core/server/server.go` (узкий diff: Start/Stop), `core/server/
service_windows.go` (граница остановки службы), `core/server/internal/boxbox/
api.go` (бюджет закрытия). Runtime (boxmain/box.go) не переписывался — по
условию задачи; legacy child-поведение за пределами дефектов не менялось.

## 1. Дефекты (REVIEW_RU.md)

- **R2 (P1)**: Stop не ограничен заявленным deadline. Три несвязанных
  ожидания: (а) `Start` держит `lifecycleMu` всё время `boxmain.Create`,
  `Stop` ждёт тот же mutex без срока; (б) `box.CloseWithTimeout` после
  истечения своего таймера ждал завершение close БЕЗ ОГРАНИЧЕНИЯ; (в)
  граница остановки службы (`Execute`) вызывала `globalServer.Stop`
  синхронно до bounded-ожидания хендлеров.
- **R3 (P1)**: повторный `Start` отказывал («instance already started»), но
  deferred error-cleanup этой попытки выполнялся и стирал ссылку на ЧУЖОЙ
  работающий runtime (`setBoxInstance(nil, nil)`) и сбрасывал
  `autoRedirectMark`: бокс продолжал работать, следующий Stop видел nil,
  Health лгал.

## 2. Исправления

### R3 (`130bc51e`)

Проверка `currentBox() != nil` перенесена ДО армирования deferred-cleanup:
отказанный дубликат не касается состояния. Cleanup выполняется только для
попытки, которая владеет состоянием (создала/ошиблась при создании).

### R2 (`3f63ef7c`)

1. **Lock wait**: `Stop` берёт `lifecycleMu` через bounded TryLock-цикл
   (бюджет 2с; после — ошибка «stop timed out: an in-flight start still
   holds the lifecycle lock», НИКОГДА не clean-stop). `Start` ждёт
   безусловно (единственный создатель). «Явное владение жизненным циклом»:
   владельцем является попытка Start; Stop не отнимает.
2. **Close budget**: `CloseWithTimeout` больше не ждёт после истечения
   таймера (ядро вынесено в `runCloseBounded`); зависший close продолжается
   в фоне и сам печатает время завершения. Close никогда не бросается на
   середине — прекращается только ОЖИДАНИЕ.
3. **Service boundary**: `Execute` (svc.Stop) ждёт `globalServer.Stop` в
   горутине с бюджетом 2с; после дедлайна teardown принадлежит F7-mark
   (shutdown mark поднимается до этого синхронно, O(1)): поздний Start
   разворачивает собственный runtime и отказывает — публикация runtime
   после Stopped невозможна, Stopped не является ложным clean-stop
   (обязательное условие ревью). Process fail-stop не потребовался.

## 3. Регрессии (red на базе `59f28d3b` / green на `3f63ef7c`) — проверено

| Тест | База (red) | Фикс (green) |
|---|---|---|
| `TestDuplicateStartPreservesRunningRuntime` | FAIL 0.01s: «duplicate Start erased the running runtime reference» | PASS: дубль-Start отказан, instance/mark целы (mark==7777), последующий Stop закрыл runtime; repeated Stop идемпотентен; реальная ошибка startup (кривой config) не публикует runtime |
| `TestServiceExecuteStopBoundedWithHeldLifecycleLock` | FAIL 10.0s: «Execute did not return after a Stop request while the lifecycle lock was held (unbounded stop)» | PASS 2.0s: Stopped достигнут при РЕАЛЬНО удержанном `lifecycleMu` (не mock-delegate), mark поднята, поздний `ServiceStart` отказан (`errServiceStopping`) |
| `TestRunCloseBoundedReturnsPastDeadline` (boxbox) | — (шов `runCloseBounded` новый) | PASS: возврат по дедлайну при зависшем close, warning залогирован, close дорабатывается в фоне |
| `TestServiceExecuteStopBoundedWithStartInsideCreation` (существующий, mock-delegate) | PASS | PASS (не регрессировал) |

Windows runtime tests: полный `go test ./...` в `core/server` — PASS
(`ThroneCore`, `internal/boxbox`, xray, xraydns); исключение —
`internal/boxdns/winipcfg` (8 тестов падают И на базе `59f28d3b`:
средозависимы — нужен интерфейс «Ethernet»; на хосте его нет; к REC-02 не
относится, проверено stash-прогоном базы).

## 4. SCM smoke (VM `PC130-Evidence`, 2026-09-18)

Бинарь `ThroneCore.exe` собран на хосте из `3f63ef7c`
(prod-теги build_go.sh, CGO_ENABLED=0; sha256
`17e9020bbae489f309ed8766899661be30ccdf9c0e81cf84f604854f88118556`),
подменён в установленную службу `ProxyCoreService` (LocalSystem, demand):
подмена/восстановление подтверждены хэшами
(`90897198…` → `17e9020b…` → `90897198…`).

Actual elapsed (net start / net stop, 3 цикла):

| Цикл | start | stop | Состояния |
|---|---|---|---|
| 1 (cold) | 7438 ms | 2553 ms | RUNNING → STOPPED |
| 2 | 2077 ms | 2540 ms | RUNNING → STOPPED |
| 3 | 2095 ms | 2531 ms | RUNNING → STOPPED |

Stop стабильно ограничен (~2.5s = 2s бюджет закрытия + SCM/cmd накладные),
без зависаний. Артефакты: `vm-evidence/recovery/rec02/`
(`rec02-steps.txt`, `REPORT.md`), бинарь
`vm-evidence/recovery/rec01/artifacts/rec02/ThroneCore.exe`.

## 5. Тест выполнен / требование выполнено (разделение)

| Пункт NEXT_TASKS | Тест выполнен | Требование выполнено |
|---|---|---|
| 1. Регрессия repeated Start | да (red/green, §3) | **да** |
| 2. Разделение cleanup попытки и существующего runtime | да (тест: instance/mark сохраняются) | **да** (deferred cleanup только для своей попытки) |
| 3. SCM Stop при реальном удержании lifecycleMu | да (реальный lock, не mock; red 10s/green 2s) | **да** |
| 4. Bounded shutdown, один владелец, запрет поздней публикации, без ложного clean-stop | да (mark-отказ в тесте; `beginServiceRuntimeShutdown` один на run через stopOnce) | **да** (process fail-stop не понадобился — reason: F7-mark уже даёт no-survivor гарантию, bounded-ожидание на границе достаточно) |
| 5. Start deadline/cancel; concurrent Start/Stop; repeated Stop; stalled Create; реальная ошибка startup; legacy child поведение; без второго runtime manager | частично: repeated Stop + startup-error — автотесты; stalled Create — через реальный hold lock (unit) и ограничение lock-wait/close (код); Start ctx/cancel в legacy Start НЕ передаётся в boxmain.Create (как и было) — не менялось, граница службы ограничивает по-другому (mark + bounded waits) | **да с задокументированным остатком**: legacy Start по-прежнему не отменяется по ctx (за пределами узкого diff; создание не публикуется до возврата, служебная граница ограничена) |
| DoD: red-on-base, unit + SCM smoke, actual elapsed Stop, Windows runtime tests, отчёт | да (§3–4) | **да** |

## 6. Остатки / не входит

- Legacy Start не принимает ctx в boxmain.Create (структурно; обходится
  bounded-границей службы). Полная отмена создания — только вместе с
  переделкой создания (вне узкого diff REC-02).
- Полный GUI-пакет из дерева `3f63ef7c` в CI не собирался (Go-часть
  проверена юнитами + SCM smoke на хост-сборке бинаря; Setup .iss не менялся).
- REC-03 (service envelope client + UI smoke) — следующая задача, зависимости
  REC-01/02 закрыты.
