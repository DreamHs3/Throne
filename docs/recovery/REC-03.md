# REC-03 — минимальный клиент протокола службы и runtime-управление из существующего GUI

Статус: **в работе** (ветка `agent/rec03-service-client`, база `7546c237`).
Отчёт заполняется по ходу; итоговая матрица — в §6.

Задача (NEXT_TASKS R5, узкая версия до PC-130): подключить существующий GUI
(Throne/ProxyCore, Qt) к Windows-службе `ProxyCoreService` через действующий
PC-110 envelope-протокол: обязательный Hello, Health, CheckConfig, Start,
Stop. GUI в service-mode не поднимает параллельный child-core и не
переключается на него молча. Ремонт cold-start, PC-130/140 и enforcement в
этап не входят.

---

## 1. Фактический контракт службы (зафиксирован по коду и отчётам PC-110/120)

Источники: `core/server/service_envelope_windows.go`,
`core/server/service_windows.go`, `core/server/service_config_policy.go`,
`core/server/gen/libcore.proto`, отчёты `docs/night-run/pc-110-report.md` и
`pc-120-report.md`.

### 1.1 Транспорт

| Параметр | Значение |
|---|---|
| Канал | Windows named pipe `\\.\pipe\ProxyCoreService` (константа `defaultServicePipeName`; переопределение только через env `THRONE_SERVICE_PIPE` — это серверная тестовая возможность, GUI-клиент имя не переопределяет) |
| Доступ | SDDL из env службы `THRONE_SERVICE_SDDL`; по умолчанию `D:P(A;;GA;;;SY)(A;;GA;;;BA)` (только SYSTEM/Administrators); установщик (PC-120) добавляет SID пользователя-установщика `(A;;GA;;;SID)` — не-elevated GUI того же пользователя подключается (Medium IL), посторонние — отказ на accept-границе ещё до чтения envelope |
| Формат кадра | запрос `[u32 frameLen LE][RequestEnvelope (protobuf)]`, ответ `[u32 frameLen LE][ResponseEnvelope (protobuf)]` |
| Лимит кадра | `serviceMaxEnvelopeLen = 32 MiB`, проверяется по объявленной длине ДО выделения памяти; превышение → код 4 и разрыв (позиция потока неизвестна) |
| Тайм-ауты сервера | чтение одного кадра / idle после рукопожатия (`serviceReadTimeout` = 30 с на кадр; после установления рукопожатия первый байт следующего кадра ждёт без ограничения), запись 30 с |
| Соединения | до 16 (`serviceMaxConnections`); обрыв — штатное событие, процесс службы не страдает |

### 1.2 Envelope и версии

`RequestEnvelope{protocol_version=1, request_id, operation,
deadline_unix_ms, expected_policy_revision, typed_payload}`;
`ResponseEnvelope{request_id, code, message, typed_payload,
service_protocol_version}` (proto2, `gen/libcore.proto`).

- `serviceProtocolVersion = 1`. Несовпадение/отсутствие → код 1 + разрыв;
  проверяется с первого кадра, до всякой диспетчеризации.
- `request_id` обязателен (0 = отсутствие → код 3), эхоится в ответе,
  дедуплицируется на соединении (окно 4096, вытеснение старейшего; код 9 на
  повтор). Id записывается только при ПРИНЯТИИ запроса на исполнение —
  отказанный запрос можно повторить с тем же id.
- Первый запрос на соединении обязан быть `Hello`; второй `Hello` на
  установленном соединении → код 3.
- `deadline_unix_ms` (абсолютное время): истёкший → код 5 до диспетчеризации;
  истёкший в очереди → код 5, id остаётся израсходованным.
- `expected_policy_revision`: клиент закрепляет `configPolicyRevision = 1`
  (0 = не проверять); несовпадение → код 6 (`ERR_STALE_POLICY_REVISION`).

### 1.3 Реестр операций и коды

Единственная таблица — `serviceOperations`: ровно `Hello`, `Health`,
`CheckConfig`, `Start`, `Stop`. Привилегированный legacy-RPC
(SetSystemDNS, InstallDashboard, CloseConnections, QueryStats, Test, ...)
недостижим; CheckConfig/Start связаны с `ServiceCheckConfig`/`ServiceStart`
(с файловой политикой), никогда с raw-обработчиками.

Коды `ResponseEnvelope.code`: 0 OK; 1 несовместимая версия (разрыв);
2 unauthorized (резерв — отказ идентичности на accept-границе до envelope);
3 invalid request (механический брак, неизвестная операция, отказы с
префиксом `ERR_*`); 4 frame too large (ответ до чтения тела, затем разрыв);
5 deadline exceeded; 6 stale policy revision; 7 service unavailable
(остановка службы отказывает всей новой работе); 8 runtime failure; 9
duplicate request_id. Отказанные запросы (3/5/6/9) соединение НЕ рвут.

### 1.4 Типизированные полезные нагрузки

| Операция | Запрос (typed_payload) | Ответ при code=0 | Ошибка внутри ответа |
|---|---|---|---|
| `Hello` | `HandshakeReq{}` (поле игнорируется — версия несёт envelope) | `HandshakeResp{protocol_version, service_version}` | — |
| `Health` | `EmptyReq{}` | `HealthResp{runtime_running, protocol_version}` | — |
| `CheckConfig` | `LoadConfigReq` | `ErrorResp{error}` | `error != ""` |
| `Start` | `LoadConfigReq` | `ErrorResp{error}` | `error != ""` |
| `Stop` | `EmptyReq` | `ErrorResp{error}` | `error != ""` |

Три независимых уровня отказа, которые клиент обязан различать:
**(а) транспортная ошибка** (трубы нет/доступ запрещён/обрыв ввода-вывода —
ответа нет и быть не может); **(б) envelope-код** (код ≠ 0, `message` от
службы); **(в) ошибка внутри typed payload** (code=0, `ErrorResp.error`).
Отказ в payload — это выполненная операция с бизнес-ошибкой; обрыв во время
Start/Stop — операция с НЕИЗВЕСТНЫМ исходом.

### 1.5 Политика конфигурации (service policy, revision 1)

`ServiceStart`/`ServiceCheckConfig`: строгий std-JSON парсинг; ключи без
учёта регистра против deny-list путей; нормализация
`experimental.cache_file.path → <THRONE_SERVICE_DATA_DIR>/cache.db` и удаление
`clash_api.external_ui*`; отказ `\\.\pipe`/`\\.\`/`\\?\`/`unix:` строковых
значений; `ServiceStart` дополнительно отвергает всё extra-process
исполнение (`need_extra_process`, `extra_process_*`). Политику НЕ ослаблять
ради клиента: конфиг для smoke обязан проходить действующую политику
(например, mixed-inbound профиль без файловых полей).

### 1.6 Гарантии REC-02, существенные для клиента

- Stop ограничен бюджетами (2 с lock, 2 с close, 2 с граница SCM); исход
  закрытия явный (completed/failed/timeout), «stop timed out» доходит до
  вызывающего как `ErrorResp`.
- Повторный Start при незавершённом teardown → отказ
  («teardown is still in progress»), не порча состояния.
- После shutdown служба отказывает новой работе (код 7); на границе SCM
  процесс службы завершается (no-survivor).
- Дедуп request_id действует на соединении: клиент после разрыва получает
  новое соединение (и новое окно id).

## 2. Точка подключения в GUI (фактическая архитектура до REC-03)

- GUI поднимает child-core сам: `MainWindow` создаёт `QLocalServer` с
  перезапуско-уникальным именем `proxycoreIPC-<uuid>` и процесс
  `Configs_sys::CoreProcess` (`src/ui/mainWindow/mainwindow_setup.cpp`);
  ядро-ребёнок само подключается к этому серверу; соединение
  верифицируется по PID (`verify_core_pid`), затем `setup_rpc()` →
  `API::defaultClient->Reconnect(socket)`.
- `API::Client` (`src/api/RPC.cpp`) — legacy-клиент: фреймы
  `[u32 id][u16 nameLen][name][u32 len][payload]` в собственном потоке;
  это НЕ envelope-протокол, службой не обслуживается (реестр операций
  службы отклонил бы такие кадры уже на чтении длины).
- Start/Stop GUI: `profile_start()`/`profile_stop()`
  (`src/ui/mainWindow/mainwindow_profile_lifecycle.cpp`) →
  `defaultClient->Start/Stop` с `libcore::LoadConfigReq` из
  `Configs::BuildSingBoxConfig`.
- Конфигурация службы: служба устанавливается PC-120-установщиком
  (LocalSystem, demand); pipe-SDDL несёт grant SID пользователя-установщика
  → не-elevated GUI того же пользователя имеет доступ.

Вывод: для service-mode нужен ОТДЕЛЬНЫЙ envelope-клиент на
`\\.\pipe\ProxyCoreService`; существующие protobuf-модели C++
(`core/server/gen/libcore.pb.h`, генерация `cmake/myproto.cmake` из того же
`libcore.proto`) переиспользуются; второй протокол не вводится; allowlist
службы не расширяется.

## 3. Реализация

### 3.1 Клиент службы — `API::ServiceClient` (`include/api/ServiceClient.h`, `src/api/ServiceClient.cpp`)

- Транспорт: `QLocalSocket` к `\\.\pipe\ProxyCoreService`, владелец —
  выделенный `QThread` (как у legacy-канала); вызовы из рабочих потоков GUI
  блокируют только поток вызывающего (mutex+cv) — UI не блокируется.
- Фрейминг: `read_buf` копит частичные чтения; кадр собирается только
  целиком; объявленная длина проверяется против лимита 32 MiB ДО выделения
  буфера (зеркало серверного контракта); частичная запись — Qt-буферизация
  + flush, ошибка записи = транспортная.
- Рукопожатие: каждое НОВОЕ соединение открывается `Hello`
  (protocol_version=1) и ждёт `HandshakeResp`; без успешного Hello
  операция не отправляется.
- Корреляция: монотонный `request_id` (u64, с 1, не переиспользуется даже
  после переподключения — строже серверного окна); ответ с неизвестным id
  = клиентское нарушение протокола → соединение сбрасывается (защита от
  ложной атрибуции позднего ответа).
- Дедлайны: `deadline_unix_ms = now + timeout` на каждый запрос; локальное
  ожидание ограничено тем же бюджетом (плюс бюджет connect+Hello);
  локальный тайм-аут для Start/Stop = исход НЕИЗВЕСТЕН.
- Раздельная классификация исхода (§1.4): `Ok` / `PayloadError` /
  `EnvelopeError(code,message)` / `AccessDenied` / `ConnectFailed`
  (служба не отвечает) / `TransportError` / `Timeout` / `ProtocolError`.
- Переподключение: разрыв/ошибка помечает соединение мёртвым; следующий
  вызов сам создаёт соединение и Hello. Start/Stop АВТОМАТИЧЕСКИ не
  повторяются никогда: один вызов = одна отправка; после разрыва/тайм-аута
  результат помечается `outcomeKnown()==false`, и состояние сначала
  выясняется через Health (см. 3.2).
- Версии/ревизия: клиент закрепляет protocol_version=1 и
  expected_policy_revision=1 (константы продублированы с сервера с
  комментарием о синхронизации; коды 1/6 всплывают явными сообщениями).

### 3.2 Service-mode в GUI (минимальный diff, дизайн не переписывается)

- Флаг `service_mode` в `SettingsRepo` (персистентный; выключен по
  умолчанию) + чекбокс-действие в меню Program (программная вставка, без
  правки .ui); применяется при следующем запуске GUI.
- При старте GUI в service-mode: child-core НЕ создаётся
  (`core_process == nullptr`, QLocalServer для ребёнка не поднимается);
  точки, безусловно разыменовывающие `core_process` (prepare_exit,
  StopVPNProcess, RestartCore), получают null-guard: GUI никогда не убивает
  и не останавливает саму Windows-службу.
- `profile_start` в service-mode: сборка конфига как обычно →
  `ServiceClient::CheckConfig` → `ServiceClient::Start`; ошибки
  классифицируются из §3.1 и показываются явно (лог + MessageBox);
  ветка «core не слушает RPC → перезапустить ребёнка» не выполняется;
  тихого фолбэка на child-core нет.
- `profile_stop` в service-mode: `ServiceClient::Stop`; при неизвестном
  исходе — Health и явное сообщение.
- Отображение состояния: Health-монитор (2 с, рабочий поток, пропуск
  такта при занятом клиенте) сверяет `runtime_running` с GUI-состоянием и
  логирует расхождения (служба сообщила останов runtime / служба
  недоступна / связь восстановлена). Статистика/тесты (legacy-RPC) в
  service-mode недоступны — они не вызываются монитором и дают явный отказ
  при прямом запуске пользователем.

## 4. Проверки (методика)

1. Host (Windows): Go-регрессии службы не трогаем (этап клиента); красно-
   зелёная база REC-02C — коммит `23328f56`.
2. Клиентские C++-тесты (`tests/proxycore/test_service_client.cpp`, harness
   `proxycore_service_client`, ctest): in-process фейковый pipe-сервер
   (QLocalServer, тот же envelope-фрейминг) — случаи:Happy Hello+Health;
   framing (частичные чтения, склейка кадров, крупный payload);
   сопоставление request_id (в т.ч. чужой/поздний ответ); envelope-коды
   (3/6/7/9 → различимы); payload-ошибка Start; разрыв соединения
   (до ответа → неизвестный исход Start, восстановление на следующем
   вызове с новым Hello); отказ доступа.
3. Сборка GUI и прогон тестов: CI DreamHs3/Throne (workflow_dispatch на
   ветке, windows-amd64, Qt 6.11.2) — хост MSVC/Qt не имеет (CURRENT_STATE
   §2). Артефакт — Throne.exe+ThroneCore.exe; для Setup — pack-джоба.
4. VM smoke (`PC130-Evidence`, канал REC01R2Runner как в REC-01/02):
   Medium-токен GUI → Hello/Health → CheckConfig → Start (конфиг,
   допустимый политикой) → подтверждённый активный runtime службы
   (Health.runtime_running + слушающий порт у PID службы) → Stop →
   подтверждённая остановка; служба недоступна (net stop) → явный отказ
   GUI; восстановление после перезапуска службы; различение Stop-runtime
   через IPC и останова самой службы; подтверждение отсутствия второго
   child-core (процесс ThroneCore не порождается GUI).

## 5. SHA и хэши проверенных артефактов

Заполняется по итогам (§6).

## 6. Матрица PASS/FAIL/NOT RUN

Заполняется по итогам.

## 7. Остатки / не входит

- Полный набор GUI-возможностей через службу (статистика, тесты, VPN-челленджи,
  dashboard) — за пределами этапа; в service-mode недоступны явно.
- PC-130/140 (enforcement, harness), ремонт cold-start — отдельные этапы.
- Переключение service-mode требует перезапуска GUI (child-core
  порождается на старте) — осознанное ограничение минимального diff.
