# REC-02 — жизненный цикл Start/Stop (R2/R3/R2B) — рабочий документ

Статус: **исправления implemented + Windows unit verified + SCM smoke VM
verified + fault-сценарии VM verified (2026-09-18); REC-02C (гонка
регистрации teardown-record) исправлен, host-регрессия red/green (2026-09-18;
без новой VM-матрицы — VM-результаты §4 сохранены за исходными SHA).** Ветка
`agent/rec02-lifecycle` (`130bc51e` = R3, `3f63ef7c` = R2, `a8972e84` +
`af97fbb2` + `3308aa8b` = REC-02B, `8430036e` + `15c408aa` = REC-02C; база
`59f28d3b` — закрытый REC-01).

Файлы: `core/server/server.go` (Start/Stop), `core/server/
service_windows.go` (граница остановки службы), `core/server/internal/
boxbox/api.go` (бюджет закрытия, явный исход). Runtime (boxmain/box.go)
не переписывался — по условию задачи; legacy child-поведение за пределами
дефектов не менялось.

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
- **REC-02B (обзор 2026-09-18)**: bounded-ожидание закрытия ошибочно
  принималось за завершённый teardown. Stop возвращал пустой `ErrorResp`
  (успех) независимо от исхода close; повторный Stop видел только
  `currentBox()==nil`, пока close ещё работал в фоне; SCM-граница
  отбрасывала результат Stop (`_, _ = globalServer.Stop`) и записывала
  ЛЮБОЙ исход, включая неподтверждённый cleanup, как успешную остановку
  службы.

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
   разворачивает собственный runtime и отказывает. Process fail-stop не
   потребовался. (Поправлено REC-02B — см. §6: сама по себе F7-mark НЕ
   является гарантией no-survivor.)

### REC-02B (`a8972e84`, `af97fbb2`, `3308aa8b`)

1. **Явный исход закрытия** (`boxbox/api.go`): `CloseWithTimeout`
   возвращает `CloseCompleted` / `CloseFailed` / `CloseTimedOut` (с ошибкой
   close), плюс хук `onDone(error)`, который исполняется на горутине close
   ПОСЛЕ реального завершения — каким бы поздним оно ни было.
2. **Владение teardown'ом** (`server.go`): Stop снимает экземпляр с
   публикации до закрытия (`setBoxInstance(nil, nil)`) и регистрирует
   teardown-record ДО запуска закрытия (исходный порядок REC-02B —
   «регистрация при `CloseTimedOut`» — гонился с хуком; исправлен в
   REC-02C, `8430036e`, см. подраздел REC-02C в §2):
     - `Start` отказывает («teardown is still in progress») — второй
     runtime поверх закрывающегося не создаётся;
   - повторный Stop видит незавершённое закрытие: ждёт (bounded, 2с) и
     сообщает реальный исход вместо чистой остановки;
   - record снимается в хуке при реальном завершении close (только если
     это всё ещё та же запись).
   `CloseFailed` всплывает в ответе Stop («runtime close failed: ...») без
   регистрации записи (ничего не осталось работать в фоне).
3. **SCM-граница** (`service_windows.go`): результат остановки проверяется
   явно — подтверждение только при nil Go error И пустом `ErrorResp`.
   Ошибка Go, ошибка в `ErrorResp` или тайм-аут бюджета (2с) =
   НЕПОДТВЕРЖДЁННЫЙ cleanup: строка в журнале с префиксом `service stop:`
   и код `svcExitStopUnconfirmed` (2) как service-specific exit code
   (`sc query`: WIN32_EXIT_CODE 1066, SERVICE_EXIT_CODE 2), никогда не
   чистая остановка. Ограниченный аварийный путь — выход процесса после
   Stopped: `svc.Run` возвращается, `main` завершается, процесс умирает,
   уничтожая внутрипроцессный runtime вместе со всеми горутинами teardown.
   Именно ЭТОТ выход — гарантия no-survivor на границе (доказан в VM:
   исчезновение PID службы); F7-mark лишь отказывает новым запускам.
   Self-teardown в post-check `ServiceStart` логирует неподтверждённый
   исход тем же префиксом.
4. **Exit code доходит до SCM** (`3308aa8b`, дефект из VM-evidence):
   Execute больше не шлёт `Stopped` в status-канал — этот посыл
   финализировал запись SCM с кодами 0/0 ДО того, как x/sys прикладывал
   код возврата (в VM-прогоне неиндексированный cleanup выглядел как
   успешная остановка). По контракту x/sys svc Stopped репортит сам
   рантайм после возврата Execute; первый возвращаемый параметр —
   `svcSpecificEC` (не «fire»).
5. **Диагностика** (`af97fbb2`): `THRONE_SERVICE_LOG` — необязательный
   путь файла, в который service-режим зеркалирует журнал (SCM не
   показывает ни stdout, ни stderr; без этого строки stop-пути
   ненаблюдаемы в реальной службе). По умолчанию выключен.

### REC-02C (`8430036e`) — гонка регистрации teardown-record

**Причина.** Запись регистрировалась только ПОСЛЕ возврата
`closePublishedRuntime` (ветка `CloseTimedOut`), а снимал её хук `onDone`
на горутине close. На границе бюджета эти два события гонятся: close
завершается в момент истечения бюджета — `runCloseBounded` возвращает
`CloseTimedOut`, а горутина close уже исполнила (или исполняет) хук. Хук
снимает запись до её регистрации (identity-проверка `clearTeardownRecord`
не находит свою запись — её ещё нет в `teardownActive`), после чего
`case CloseTimedOut: setTeardownRecord(rec)` регистрировал уже
ЗАВЕРШЁННУЮ запись.

**Последствие (точное).** `Start` отказывал («teardown is still in
progress») при фактически завершённом закрытии. Уточнение против
возможной трактовки «без пути восстановления»: такой путь на старом коде
БЫЛ — повторный Stop заходил в ветку `pendingTeardown`, канал `rec.done`
уже закрыт, запись снималась немедленно (и Stop сообщал реальный исход
завершившегося close). Дефект — ЛОЖНАЯ блокировка следующего Start до
дополнительного Stop, а не безвыходное состояние.

**Исправленный порядок событий:**

1. запись регистрируется ДО запуска `closePublishedRuntime` — хук всегда
   снимает СВОЮ запись (identity-проверка сохранена: чужую/новую запись
   поздний хук не сотрёт);
2. при `CloseTimedOut` повторная регистрация НЕ выполняется: запись либо
   уже снята гонким завершением (граница бюджета), либо остаётся
   владельцем закрывающегося runtime до реального завершения;
3. завершённые исходы (`CloseCompleted`, `CloseFailed`) дополнительно
   снимают запись в самом Stop — кто первый (хук или Stop), второй сработает
   как no-op: после возврата Stop запись зарегистрирована тогда и только
   тогда, когда close действительно ещё работает (узкое окно «Stop вернулся,
   хук ещё не исполнен» закрыто);
4. передача исхода вызывающему коду сохранена: `CloseFailed` →
   «runtime close failed: ...», `CloseTimedOut` → «stop timed out ...»
   (в том числе в сценарии гонки на границе — вызывающий видит тайм-аут
   ОЖИДАНИЯ, что соответствует контракту `CloseWithTimeout`),
   `CloseCompleted` → чистый Stop.

Наблюдаемые VM-сценарии §4 эта гонка не меняет: чистый close завершается
намного раньше бюджета (CloseCompleted), fault B не создаёт записи вовсе
(stop не смог выполниться из-за удержания lock). Поэтому новая VM-матрица
для REC-02C не запускалась; результаты §4 остаются за сборкой из `3308aa8b`
(бинарь sha256 `64ef4c91…`) с исходными SHA.

## 3. Регрессии (REC-02B — green на `3308aa8b`; red-обоснование на базе
`4644d4b0`) — проверено на хосте Windows

| Тест | База 4644d4b0 | Фикс |
|---|---|---|
| `TestStopReportsTimeoutWhenCloseHangs` | — (швы новые) | PASS: зависший close → Stop сообщает timeout; Start при незавершённом закрытии отказан (второй runtime не создан); повторный Stop сообщает незавершённое закрытие; после завершения close record снят, Start и чистый Stop работают |
| `TestStopReportsCloseErrorNotClean` | — | PASS: ошибка close всплывает в ответе Stop, без teardown-record |
| `TestServiceExecuteStopResultAtBoundary` (4 кейса) | — | PASS: чистый Stop → код 0; Go error / ErrorResp / превышение бюджета → `svcExitStopUnconfirmed`, bounded |
| `TestServiceExecuteStartInsideCreationNoLatePublication` | — | PASS: Start внутри создания держит lifecycleMu как реальное создание; SCM-стоп против удержания → UNCONFIRMED код; Stopped при всё ещё припаркованном Start; затем создание возобновляется и РЕАЛЬНО публикует runtime уже ПОСЛЕ Stopped (краткая поздняя публикация — premise проверена), post-check сносит его: после сноса нет опубликованного runtime, нет record, нет Xray/extra process |
| `TestServiceExecuteStopBoundedWithHeldLifecycleLock` (R2) | PASS 2.0s c кодом 0 — **ложный clean-stop при удержанном lock (red по существу REC-02B)** | PASS 2.0s c кодом `svcExitStopUnconfirmed` |
| boxbox: `TestRunCloseBoundedReturnsPastDeadline` + 2 новых | — | PASS: (false,nil) по дедлайну; ошибка быстрого close — в ответе и хуке; позднее завершение — только через хук |
| `TestStopWithCompletedCloseTimedOutLeavesNoRecord` (REC-02C, `15c408aa`) | RED: завершённый close оставался зарегистрированным в teardown-record (проверено: фикс `8430036e` отложен — тест падает на «must not stay registered») | PASS: детерминированная подмена (хук исполнен inline → `CloseTimedOut`); запись снята гонким завершением; следующий Start работает БЕЗ дополнительного Stop; «stop timed out» по-прежнему доходит до вызывающего |
| `TestDuplicateStartPreservesRunningRuntime` (R3) | PASS | PASS (не регрессировал) |

Windows runtime tests: полный `go test ./...` в `core/server` — PASS
(`ThroneCore`, `internal/boxbox`, xray, xraydns); исключение —
`internal/boxdns/winipcfg` (8 тестов падают И на базе `59f28d3b`:
средозависимы — нужен интерфейс «Ethernet»; к REC-02 не относится).
`-race` недоступен на хосте (нет gcc/CGO) — как в предыдущих раундах.

## 4. VM-проверки (гость `PC130-Evidence`, 2026-09-18)

Бинарь собран на хосте из `3308aa8b` (prod-теги build_go.sh,
CGO_ENABLED=0; sha256 `64ef4c9161f303126ec5dd8329b785a43e364d59929d96686a
69d5bd0f0c9911`), подменён в установленную службу `ProxyCoreService`
(LocalSystem, demand): хэш-цепочка `90897198…` → `763d016d…` (промежуточная
сборка) → `64ef4c91…` → `90897198…` (восстановление подтверждено). Логи
службы — через `THRONE_SERVICE_LOG` (reg Environment), env восстановлен
исходным. Клиент pipe — утилита smokeclient (вне репо; Hello/Start/Health
по PC-110 envelope).

### 4.1 Обычный SCM smoke (финальный бинарь), 3 цикла net start/net stop

| Цикл | start | stop | PID службы | После stop |
|---|---|---|---|---|
| 1 (cold) | 2131 ms | 2531 ms | 2484 | STOPPED, 0/0, процесса нет |
| 2 | 2094 ms | 2537 ms | 8572 | STOPPED, 0/0, процесса нет |
| 3 | 2089 ms | ~2534 ms | 3556 | STOPPED, 0/0, процесса нет |

PID в каждом цикле новый (2484→8572→3556) — старый процесс завершается
всегда. Stop ~2.5s = 2s бюджет закрытия + SCM/cmd накладные.

### 4.2 Fault A — SCM stop с АКТИВНЫМ runtime

Старт службы (PID 6440) → smokeclient Start (реальный mixed-инбаунд на
127.0.0.1:17890) → `netstat`: порт 17890 LISTENING у PID **6440** (внутри
процесса службы) → `net stop`: 2545 ms → STOPPED 0/0; PID 6440 больше нет
(«service process exit proven»); порт 17890 освобождён («no listener
remains»). Лог службы: `[Info] sing-box closed in 0 ms` — путь
ограниченного закрытия подтверждён журналом, чистый код 0 подтверждает
cleanup. (Промежуточный прогон на сборке `763d016d`: close 55 ms, тот же
итог.)

### 4.3 Fault B — SCM stop во время СОЗДАНИЯ runtime (unconfirmed)

Старт службы (PID 4256) → smokeclient `start-nowait` с конфигом
~29.6 МБ (450k route-правил; frame < 32 МБ лимита) — создание в полёте →
`net stop` через 3с: 5096 ms (bounded: 2s бюджет Stop + 2s ожидание
хендлеров + накладные) → STOPPED, **WIN32_EXIT_CODE 1066
(ERROR_SERVICE_SPECIFIC_ERROR), SERVICE_EXIT_CODE 2
(svcExitStopUnconfirmed)**; PID 4256 исчез. Лог службы:
`service stop: the runtime stop did not finish within 2s; cleanup is
UNCONFIRMED - after Stopped this process exits, which destroys the
in-process runtime`.

Контраст с 4.1/4.2 (чистый стоп = 0/0) и журнальные строки
«budget»-пути фиксируют: длительность/код остановки объяснены
журналом, а не предположением. История вопроса: первый fault-прогон
зафиксировал 0/0 при UNCONFIRMED-логе — дефект доставки exit code,
исправлен в `3308aa8b` и перепроверен этим прогоном.

Артефакты: `vm-evidence/recovery/rec02b/` — `logs/` (smoke-results.txt,
fault-runtime.txt, fault-unconfirmed*.txt, state-before*.txt, restore*.txt,
скриншоты консоли гостя), `artifacts/` (ThroneCore.exe, smokeclient.exe,
конфиги), `guest/` (сценарии), `rec02b-run.sh`, `rec02b-type.sh`.

## 5. Тест выполнен / требование выполнено (разделение)

| Пункт NEXT_TASKS | Тест выполнен | Требование выполнено |
|---|---|---|
| R2/R3 (см. предыдущую редакцию §5) | да | **да** |
| 1. Исход закрытия явный (completed/error/timeout); Stop не отвечает успехом при продолжающемся закрытии | да (boxbox-тесты, `TestStopReports*`) | **да** |
| 2. Владение ресурсами закрывающегося runtime; блокировка нового runtime; повторный Stop видит незавершённое закрытие | да (teardown-record: Start-отказ, повторный Stop — в `TestStopReportsTimeoutWhenCloseHangs`) | **да** |
| 3. SCM-граница: ErrorResp + Go error + timeout; аварийный путь; доказательство выхода процесса; не один F7-флаг | да (`TestServiceExecuteStopResultAtBoundary`, fault A/B на VM: PID исчезает, порт освобождён, SERVICE_EXIT_CODE 2) | **да** (no-survivor = выход процесса, доказан; F7-mark — только запрет запусков) |
| 4. Уже начатый Start (прошёл admission/barrier, остановлен в создании, SCM-стоп упёрся в бюджет): возобновление создания; краткая публикация до post-check возможна — гарантируется последующее закрытие и отсутствие остатков (точная формулировка — под таблицей) | да (`TestServiceExecuteStartInsideCreationNoLatePublication`: создание реально публикует late runtime — premise проверена — и пост-чек сносит) | **да**, в точной формулировке: краткая поздняя публикация НЕ отсутствует — она закрывается post-check'ом; не заменяется отказом нового Start |

### Точная формулировка гарантии (уточнение 2026-09-18)

Уже выполняющийся Start, прошедший admission и барьер, допускает КРАТКУЮ
публикацию: если SCM-стоп упёрся в бюджет, а создание возобновляется,
runtime публикуется УЖЕ ПОСЛЕ Stopped — до срабатывания post-check.
Поэтому гарантия НЕ является «отсутствием поздней публикации», и так её
называть нельзя. Гарантия состоит в другом: post-check обнаруживает такой
поздний runtime и закрывает его; после сноса не остаётся ни опубликованного
runtime, ни teardown-record, ни Xray-инстансов, ни дополнительного процесса.
Отказ нового Start (F7-mark) это не заменяет.

Отдельно — что подтверждено проверками (юнит-тест + VM-прогон, каждый факт
со своим источником):

- **последующее закрытие** — юнит-тест
  `TestServiceExecuteStartInsideCreationNoLatePublication`: возобновлённое
  создание публикует runtime, post-check его сносит (`currentBox() == nil`,
  teardown-record снят, Xray-инстансов и extra process нет, счётчик барьера
  возвращён в 0);
- **аварийный выход процесса** — ограниченный аварийный путь (§2.3),
  подтверждён на VM (fault B, §4.3): после Stopped процесс службы
  завершается и уносит внутрипроцессный runtime вместе с горутинами
  teardown;
- **отсутствие PID и ресурсов после выхода** — VM (§4.2–4.3): PID службы
  исчезает, порт освобождён, `sc query` → STOPPED (0/0 в чистом цикле,
  1066/2 в unconfirmed-сценарии).
| 5. Регрессии: hung Close→timeout; Start при незавершённом Close; восстановление после Close; ошибка Stop не теряется на SCM-границе; Start не публикует после shutdown | да (§3) | **да** |
| 6. SCM smoke + fault-сценарий с активным runtime; STOPPED + PID/ресурсы; «бюджет» подтверждён логами | да (§4.1–4.3) | **да** |
| DoD: минимальный diff, регрессии, Windows-проверки, push | да | **да** |

## 6. Исправления документа (REC-02B)

Убрано неподтверждённое утверждение прежней редакции: «…поздний Start
разворачивает собственный runtime и отказывает — публикация runtime после
Stopped невозможна, Stopped не является ложным clean-stop (обязательное
условие ревью). Process fail-stop не потребовался» и «reason: F7-mark уже
даёт no-survivor гарантию». F7-mark блокирует ЗАПУСКИ — survivor-гарантию
на границе даёт только выход процесса службы; теперь он явный (аварийный
путь §2.3) и доказанный (§4.2–4.3). Также уточнено: bounded-ожидание
закрытия не подтверждает teardown (§1, REC-02B).

Дополнено этой редакцией: формулировка гарантии для уже начатого Start
уточнена — краткая публикация до post-check не называется «отсутствием
поздней публикации» (§5, «Точная формулировка гарантии»); отдельно
перечислено, что подтверждено проверками, с источником каждого факта.

Дополнено REC-02C (`8430036e`/`15c408aa`): порядок регистрации
teardown-record исправлен и описан как есть (подраздел REC-02C в §2,
строка в §3); явная поправка к возможной трактовке дефекта — утверждение
«без пути восстановления» было бы НЕВЕРНЫМ: на старом коде повторный Stop
снимал зависшую запись (ветка `pendingTeardown` видела уже закрытый
`rec.done`), так что последствием была ложная блокировка следующего Start
до дополнительного Stop, а не безвыходное состояние.

## 7. Остатки / не входит

- Legacy Start не принимает ctx в boxmain.Create (структурно; обходится
  bounded-границей службы). Полная отмена создания — только вместе с
  переделкой создания (вне узкого diff REC-02).
- Teardown-record не переживает аварийное завершение процесса — и не
  должна: процесс уносит runtime с собой (§2.3). Внутрипроцессный
  «переживший» close невозможен после выхода.
- Полный GUI-пакет из дерева в CI не собирался (Go-часть проверена
  юнитами + SCM smoke на хост-сборке бинаря; Setup .iss не менялся).
- REC-03 (service envelope client + UI smoke) — следующая задача,
  зависимости REC-01/02 закрыты.
