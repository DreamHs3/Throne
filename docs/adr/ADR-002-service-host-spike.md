# ADR-002: Service host spike — вариант превращения ThroneCore в service-capable runtime

Статус: принято для PC-100 spike (prototype scope). Дата: 2026-09-12.
Основание: ADR-001, PC-100/PC-110 (IMPLEMENTATION_ROADMAP), изучение кода
`core/server/{main,server,dispatch}.go`, `parentcheck/*`, `ipc/ipc_windows.go`,
`src/api/RPC.cpp` на baseline `21b8f680`.

## Контекст

Сегодня ThroneCore — дочерний процесс GUI: он требует `THRONE_CORE_SOCKET`,
проверяет родителя (`parentcheck`: на Windows родитель обязан быть `Throne.exe`
в том же каталоге), сам подключается к QLocalServer GUI и **выходит через
`log.Fatal` при обрыве соединения** (dispatch.go: `runDispatch` deferred
`log.Fatal("IPC connection dropped, exiting")`). Служба так жить не может:
родитель — services.exe, отключение UI — норма, а не авария. ADR-001 требует
service-capable runtime с переиспользованием handlers и минимальной
привилегированной поверхностью.

## Вариант A — service mode внутри ThroneCore

Новый режим запуска того же бинарника: `ThroneCore.exe service` (без аргументов
— прежний child mode; иной аргумент — ошибка). Service mode: SCM-хендлер
(`golang.org/x/sys/windows/svc`, уже в go.sum через x/sys), named pipe СЕРВЕР
(`tailscale/go-winio`, уже в go.sum) с явным SDDL, тот же dispatch/handlers.

- Объём privileged code: ~1 новый файл (`service_windows.go`) + ~10 строк в
  `main.go`. SCM/pipe-код живёт в том же процессе, что и сетевой runtime —
  ровно один привилегированный бинарник, никакого отдельного supervisor.
- Затрагиваемые файлы: `main.go` (mode dispatch), новый `service_windows.go`
  (SCM handler + accept loop + per-connection serve + Health/Hello handlers,
  зарегистрированные в существующую карту `handlers` из собственного init()),
  новый `service_other.go` (compile-stub для Linux/macOS), additive-сообщения
  в `libcore.proto`. `server.go`, `dispatch.go`, `parentcheck/*`, `ipc/*`
  — **не изменяются**.
- Влияние на upstream merge: минимальное. Один маленький hunk в main.go;
  всё остальное — новые файлы. Ноль конфликтов в критичных файлах.
- Unit testing без SCM: `svc.Handler.Execute` вызывается напрямую с каналами
  ChangeRequest/Status; serve-loop тестируется in-process (net.Pipe и
  go-winio listener) без SCM и без сети; handlers переиспользуются
  (`globalServer`), так что CheckConfig/Start/Stop покрываются как есть.
- Lifecycle: SCM Start → StartPending → listener + Running; SCM Stop →
  StopPending → закрыть listener → `globalServer.Stop` (idempotent) → Stopped.
  Обрыв клиента — норма: соединение закрывается, служба живёт (в отличие от
  `runDispatch`).
- IPC ownership: инверсия относительно child mode — служба СЛУШАЕТ
  (`\\.\pipe\...`), UI/test-client подключается. Peer validation: child mode
  сохраняет parentcheck + PID peer check нетронутыми; service mode заменяет
  их на **ACL при создании pipe (SDDL по умолчанию: SYSTEM+Administrators,
  переопределяемый)** — то есть замена есть, а не удаление. Полная
  per-client SID validation, envelope, лимиты и deadlines — PC-110.
- Recovery: процесс службы постоянен; runtime падения изолированы recover'ом
  dispatch; рестарт runtime — существующие Start/Stop; watchdog памяти общий.
- Packaging: тот же ThroneCore.exe (аргумент `service`); SCM-регистрация,
  ACL-конкретика и per-user SID — PC-120 (installer), не в spike.
- Rollback: revert одного нового файла + hunk main.go; child mode не менялся
  вообще, поэтому откат тривиален и не влияет на существующих пользователей.

## Вариант B — отдельный минимальный service host вокруг reusable runtime

Отдельный маленький executable, который переиспользует runtime как библиотеку.

- Объём privileged code: меньше в host (SCM+pipe), НО требуется вынести
  handlers из `package main` (server.go ~1000 строк, dispatch.go, глобальное
  состояние) в импортируемый пакет (например `internal/corehost`) — это
  массовое перемещение самых критичных файлов.
- Затрагиваемые файлы: server.go, dispatch.go, main.go, go.mod (если хост —
  отдельный модуль), новые файлы host. Существенно больше и глубже.
- Влияние на upstream merge: высокий — перемещение `server.go`/`dispatch.go`
  гарантированно конфликтует с активной upstream-разработкой.
- Unit testing без SCM: такое же (handlers тестируемы), выигрыша нет.
- Lifecycle/IPC/recovery: эквивалентны варианту A.
- Packaging: два бинарника вместо одного (проще ACL-история для host, но
  релизная матрица build_go.sh усложняется).
- Rollback: revert большого рефакторинга — дороже и рискованнее.

## Решение

**Вариант A** — service mode внутри ThroneCore. Критерии ADR-001 (меньше
привилегированного кода, тестирование handlers без SCM, переиспользование
server handlers) выполняются; upstream merge cost минимальный (2 новых файла +
stub + hunk main.go + additive proto). Отказ от варианта B зафиксирован с
re-evaluation триггером: если service-mode код вырастет заметно (~500+ строк
или потребует изменений в server.go), вынести его в `internal/` пакет того же
бинарника — это остаётся совместимо с вариантом A (пути отката сохраняются).

Security-контракт spike (PC-100): parentcheck в child mode не тронут; в
service mode его место занимает явный SDDL на pipe + обязательный versioned
handshake перед любым методом; неизвестные режимы запуска отклоняются;
service mode игнорирует `THRONE_CORE_DEBUG` (raw config никогда не логируется)
— формальная redaction остаётся за PC-510. Доверенная команда — только
существующие typed RPC-методы (Health/Hello/CheckConfig/Start/Stop); никаких
путей к исполняемым файлам, shell-команд и raw privileged операций служба не
принимает.

## Addendum (PC-100 remediation, external review P0)

Первоначальная реализация после handshake вызывала общий `dispatch()` —
service-клиент получал доступ ко всей legacy таблице handlers
(SetSystemDNS, InstallDashboard, Start с произвольным extra_process_path).
Исправлено: service serve loop резолвит методы ТОЛЬКО через явный
пятиметодный allowlist (`serviceMethodAllowlist`), всё остальное получает
стабильную typed ошибку `ERR_METHOD_NOT_ALLOWED`; legacy dispatch и
parentcheck не изменены; SDDL не ослаблен. `ServiceStart` дополнительно
отклоняет все extra-process поля (typed `ERR_INVALID_REQUEST`) до какого-либо
парсинга/запуска. Оставшийся риск зафиксирован: безопасный Start в границах
PC-100 недоказуем — config JSON может направить privileged runtime на
запись/чтение произвольных путей (sing-box `log.output`/`cache_file.path`,
TLS cert/key пути, Xray log пути); typed contract политики конфигурации —
обязательная часть PC-110 (ADR-001: privileged side не доверяет путям/JSON
UI). До него service-mode Start — prototype-only, статус PC-100 — BLOCKED.

## Addendum №2 (PC-100 remediation round 2 — filesystem policy + identity boundary)

Закрывает в коде главный оставшийся риск предыдущего аддендума (evidence на
VM остаётся за BLOCKED). Два механизма, оба fail-closed:

**Config filesystem policy** (`core/server/service_config_policy.go`, новый
файл; единый контракт для `ServiceStart` и `ServiceCheckConfig` — «что
принимает один, принимает другой»):

- *Нормализация* (клиент не контролирует): `experimental.cache_file.path`
  переписывается в `<THRONE_SERVICE_DATA_DIR>/cache.db`; включённый cache_file
  без data dir — typed `ERR_CONFIG_POLICY` (fail-closed: рабочая директория
  службы — System32); при выключенном cache_file клиентский path удаляется;
  `clash_api.external_ui*` удаляются всегда.
- *Deny-by-default*: каждое filesystem-bearing поле на любой глубине —
  логи, TLS/OpenVPN/OpenConnect сертификаты и ключи (включая client/mTLS, CA,
  MCA, CRL, static keys, wrapper scripts), SSH `private_key_path`,
  локальные/удалённые rule-set пути, каталоги/бинарники tailscale/ACME/tor,
  Xray `log.access`/`error` (только `none`/пусто) и Xray
  `certificateFile`/`keyFile` — плюс литералы `unix://`, `\\.\pipe`, `\\.\`,
  `\\?\` в любом поле. Список ключей построен перечислением всех
  filesystem-bearing JSON-ключей в `option`-пакете закреплённого sing-box
  (24 ключа сверх исходного списка ревью добавлены). Route-матчеры
  `process_path`/`process_path_regex` и DERP `home` НЕ запрещены намеренно:
  ядро не открывает эти значения как файлы, а per-app proxy Throne их
  использует.
- Решение осознанно interim: PC-110 заменяет string-контракт typed
  параметрами; до тех пор неизвестные upstream path-поля всплывут как
  отказ (fail-closed направление).

**Windows identity boundary** (ADR-001) — DACL идентифицирует только группу;
клиент авторизуется по токену процесса в момент accept, до handshake:
`GetNamedPipeClientProcessId` → `OpenProcess` → `OpenProcessToken` →
`TokenUser`/`TokenGroups` → политика допуска `serviceClientAllowed`
(`THRONE_SERVICE_ALLOWED_SIDS` от инсталлятора PC-120 + Administrators
`S-1-5-32-544`). Неразрешимая личность — отказ; в лог пишется только SID.
`safeServiceSDDL` не даёт ослабить переопределение: обязателен `D:P`,
запрещены trustee WD/AN/AU/BU, небезопасное переопределение валит старт
listener'а (никакого тихого fallback). Ограничение (не баг): admin-процесс
проходит по группе — это принятая модель ADR-001; ужесточение до
explicit-разрешений — PC-110/120.

Три дефекта round-1 кода найдены тестами этого раунда и исправлены: не
срабатывавшая exemption `cache_file.path` (сравнение пути родителя с путём
ребёнка), мёртвая проверка trustee в `sddlGrantsTrustee` (off-by-one после
split по `(A;` + trustee — последнее поле ACE), и паника
`Tokengroups.Groups[:GroupCount]` на любом реальном токене с >1 группой
(фиксированный массив `[1]`; переписано через `unsafe.Slice`).
