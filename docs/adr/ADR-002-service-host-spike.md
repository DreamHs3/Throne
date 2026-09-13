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

## Addendum №3 (PC-100 remediation round 3 — семантика парсера)

Независимое ревью round-2 кода (2026-09-12) нашло и **доказало исполнением**
обход interim-политики: политика сканировала документ средствами std
`encoding/json`, а privileged runtime парсит тот же документ парсером с
другой семантикой. Три дыры: (1) JSONC-комментарии — при ошибке std-json оба
входа возвращали документ неотсканированным с nil-ошибкой, а sing
`contextjson` срезает C-комментарии до разбора и Xray serial loader
документированно permissive (Java/Python-комментарии) — конфиг
`{"log":{"output":"…"}} /* c */` прошёл политику нетронутым и был исполнен
(файл создан реальным ServiceStart); (2) регистронезависимый биндинг ключей в
обоих рантаймах — `{"LOG":{"OUTPUT":"…"}}` проходил точный lookup deny-карты
и исполнялся как `log.output`; (3) Xray-ветка имела тот же pass-through.

Состав deny-списка перепроверен и признан корректным — дыры были в семантике
парсинга, поэтому фикс меняет семантику, а не список
(`core/server/service_config_policy.go`):

- **Fail-closed парсинг**: документ, который строгий std-парсер не может
  прочесть (комментарии, висячая запятая, хвостовые данные, не-объект),
  отклоняется typed `ERR_CONFIG_POLICY` в обоих входах, а не пропускается.
  Genuinely-битый JSON рантайм всё равно отклонит — легитимных потерь нет.
  Инвариант: любой документ «std-json не может, рантайм может» — это по
  построению неотсканированный вектор обхода.
- **Case-folded матчинг** (`strings.EqualFold`) в deny-скане, в проверке
  Xray-синков `log.access`/`error` и в нормализации/exemption. Каждый
  case-вариант принадлежащего политике ключа (`path`, `external_ui*`)
  удаляется ДО записи сервисного значения — простое добавление канонического
  ключа оставило бы хостильное написание в re-marshaled документе.
- **Scan-what-you-run сохранён**: рантайм видит ровно отсканированное дерево;
  числа — через `json.Decoder.UseNumber()` (float64-раундтрип молча портил
  большие int64).
- **Префиксы значений сравниваются без учёта регистра** (пространства имён
  pipe/device в Windows регистронезависимы: `\\.\PIPE\evil` ≡
  `\\.\pipe\evil`); добавлена голая gRPC-форма `unix:` рядом с `unix://`.
- **Паритет CheckConfig↔Start**: `ServiceCheckConfig` теперь валидирует
  `xray_full_configs` так же, как всегда делал `ServiceStart`.
- **SDDL-guard** (`service_windows.go`): raw-SID trustee теперь трактуется
  как аббревиатура — `D:P(A;;GA;;;S-1-1-0)` (Everyone) и
  `D:P(A;;GA;;;S-1-5-32-545)` (Builtin Users) проходили guard; теперь по
  raw-SID также отклоняются Everyone (`S-1-1-0`), Anonymous (`S-1-5-7`),
  Authenticated Users (`S-1-5-11`), Builtin Users (`S-1-5-32-545`) и Builtin
  Guests (`S-1-5-32-546`) — refuse на старте listener'а. Defense-in-depth
  для admin-контролируемой переменной.

После фикса interim-политика снова fail-closed по построению: она сканирует
ровно тот язык, который строгий парсер способен прочесть, и отклоняет всё,
что он прочесть не может, — «умнее» рантайм-парсера она быть больше не
пытается. Статус PC-100 не меняется: **BLOCKED** (VM-evidence). Замена
string-контракта typed-параметрами остаётся за PC-110.

## Addendum №4 (PC-110 — versioned typed envelope на service pipe)

PC-110 заменяет ad-hoc framing service-пути на версионированный typed
envelope. Legacy GUI-child (`runDispatch`, dispatch.go, parentcheck, ipc/*)
не изменён вообще: envelope живёт только на service pipe, общего кода у двух
протоколов нет (кадры читает `service_envelope_windows.go`, dispatch.go не
тронут), поэтому дрейф одного в другой исключён по построению.

**Wire contract** (только service pipe, протокол версии 1):

- запрос: `[u32 frameLen LE][RequestEnvelope]`, ответ:
  `[u32 frameLen LE][ResponseEnvelope]` (оба сообщения — аддитивные,
  protobuf; `frameLen` проверяется против `serviceMaxEnvelopeLen` (32 МиБ)
  ДО выделения буфера; глобальный бюджет payload-памяти (64 МиБ semaphore)
  берётся на размер кадра и держится до завершения handler'а);
- `RequestEnvelope.protocol_version` обязателен и проверяется ДО всего
  dispatch'а (отсутствие/несовпадение → typed `code=1` + disconnect);
- `request_id` (uint64) обязателен (0 = отсутствие → `code=3`), дословно
  возвращается в ответе и дедуплицируется в рамках соединения (окно 4096 id;
  повтор → `code=9`; id записывается только для исполненных запросов —
  отклонённый запрос можно повторить с тем же id);
- `deadline_unix_ms`: истёкший → `code=5` до dispatch; живой ограничивает
  контекст handler'а (deadline внутри handler'а → `code=5`);
- `expected_policy_revision` ≠ 0 и ≠ текущей ревизии config-политики →
  `code=6` (stale) с указанием текущей ревизии, без dispatch (`configPolicyRevision`
  в service_config_policy.go = 1; бампится при изменении семантики политики);
- операция резолвится ТОЛЬКО через единый реестр `serviceOperations`
  (Hello, Health, CheckConfig, Start, Stop) — PC-100's serviceMethodAllowlist
  упразднён, второй таблицы нет; вне реестра → `code=3` с префиксом
  `ERR_METHOD_NOT_ALLOWED`, соединение сохраняется;
- `typed_payload` декодируется в конкретный тип операции СТРОГО: protobuf
  молча уводит чужие/новые поля в unknown — такой остаток отклоняется
  (`code=3`), т.е. операция принимает только объявленные ею поля;
- shutdown отказывает всем новым запросам `code=7` и не запускает новые
  handlers (проверка до spawn, оба ожидания слотов прерываемы по ctx);
- handler-ошибки классифицируются: намеренные typed-отказы (`ERR_*` префиксы)
  → `code=3`, deadline → `code=5`, остальное/panic → `code=8`; конкретный
  префикс всегда в `message`.

`code=2` (unauthorized) зарезервирован: отказ по identity происходит на
accept (до любого envelope) — соединение закрывается без ответа, как и в
PC-100. SDDL/SID-проверка не ослаблены. Disconnect клиента — норма; запрет
запроса соединение не рвёт.

Типизация payload'ов сделала невозможным и обход реестра «чужим типом»:
Start с payload HandshakeReq отклоняется как unknown-field, а не диспетчеризуется.
Start/CheckConfig сохраняют filesystem policy (реестр связывает их с
`ServiceStart`/`ServiceCheckConfig`, не с legacy Start/CheckConfig —
доказано тестом: реестр отклоняет hostile-документ `ERR_CONFIG_POLICY`,
legacy-таблица принимает тот же документ). PC-100 wire-семантика сохранена:
все прежние typed-префиксы (`ERR_CONFIG_POLICY`, `ERR_INVALID_REQUEST`,
`ERR_METHOD_NOT_ALLOWED`, `ERR_NO_HANDSHAKE`) остались в `message`.

Ограничение (осознанное, документированное): string-JSON контракт конфига
(`core_config`/`xray_config` внутри `LoadConfigReq.typed_payload`) сохранён —
typed-параметры конфигурации (сервис строит конфиг сам) остаются за PC-110+/
PC-120; interim policy из аддендумов №2-3 действует без изменений. VM-статус
PC-100 не меняется: **BLOCKED**.
