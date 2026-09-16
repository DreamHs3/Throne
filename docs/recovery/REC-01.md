# REC-01 — installer SDDL + hardening (рабочий документ)

Статус: **SDDL fix и installer hardening — VM verified (2026-09-16/17 ночь);
reparse VM-кейс реализован, но в VM не прогонялся (NOT RUN); холодные кейсы —
вне REC-01.** Ветка: `agent/rec01-installer-static`
(a854a45f → 28950f23 → 07685be8; итог `cdd4071010f627bc199c6a3f3891005f88d033a9`).

## 5. Результаты VM-верификации (стенд PC130-Evidence, snapshots сохранены)

Артефакты: `D:\GLM_project\vm-evidence\recovery\rec01\` (logs/, console/,
artifacts/, journal.txt — все power/snapshot операции через оркестратор).

| # | Кейс | Setup | Результат | Артефакт |
|---|---|---|---|---|
| 1 | Сборка Setup в VM (ISCC 6.7.3, source a854a45f) | `861dd6ad…` | PASS (exit 0) | logs/20260916T203604Z-iscc2-tail.log |
| 2 | SDDL Windows-парсер (гость, RawSecurityDescriptor + ConvertFrom-SddlString) | — | PASS (3 ACE) | logs/20260916T203954Z-sddl-parse.log |
| 3 | Silent install из elevated-консоли | 861dd6ad | FAIL→находка: per-user mode, служба пропущена | logs/20260916T204209Z-setup-log.log |
| 4 | Фикс: `PrivilegesRequiredOverridesAllowed=dialog commandline` (28950f23) | 861dd6ad | PASS (admin mode, HKLM) | console/r16 |
| 5 | Read-back Environment (3×REG_MULTI_SZ, закрывающая скобка SDDL, SID, DATA_DIR) | 861dd6ad | PASS | logs/20260916T204707Z-env-v2.log |
| 6 | Service start + pipe allow (evidence medium → SID ACE) | 861dd6ad | PASS (RUNNING; CONNECT_OK) | console/r17, logs/20260916T205117Z |
| 7 | Pipe deny recagent (админ-группа, medium) | 861dd6ad | PASS (denied) | logs/20260916T205131Z |
| 8 | Pipe deny recdeny (не-админ) | 861dd6ad | PASS (denied) | logs/20260916T205319Z |
| 9 | Hardening: quoted ImagePath `"<exe>" service` в sc qc | `c20c3858…` | PASS | logs/20260916T212125Z-v6-svc-qc.log |
| 10 | Ownership guard: foreign same-name service (notepad.exe) | `ba03f60f…` | PASS: EAbort ДО изменений, служба не тронута | logs/20260916T213105Z-v8-foreign-log.log |
| 11 | Repair/upgrade поверх своей службы | ba03f60f | PASS (exit 0, guard пропустил, env/ACL intact) | logs/20260916T213353Z-v9b-*.log |
| 12 | Fail-closed: sc exit 1639 → rollback+abort; DACL mismatch → rollback+abort | промежуточные | PASS (поведение как задумано) | setup-rec01-v3/v5.log |
| 13 | Reparse-point отказ | ba03f60f | NOT RUN в VM (код в SetupServiceEnv/NextButtonClick) | — |

## 6. Находки (новые дефекты/ограничения, зафиксированы)

1. **Silent + PrivilegesRequiredOverridesAllowed=dialog ставил per-user** даже
   из elevated-консоли → добавлен `commandline` (28950f23).
2. **RaiseException в BeforeInstall НЕ прерывает Setup** — [Run] entry всё
   равно выполнился, Environment чужой службы был перезаписан. Guard перенесён
   в `PrepareToInstall` (настоящий EAbort до любых изменений).
3. **Exit codes в silent-режиме недостоверны как единственный признак**: abort
   через PrepareToInstall/EAbort и suppressed msgbox возвращает 0; обязательны
   postcondition-проверки и /LOG.
4. Корректный binPath для sc: `binPath= "\"<exe>\" service"` (иначе exit 1639);
   собирается в Pascal (ScCreateParams) — INI-синтаксис не имеет escape `\"`.
5. Реальный DACL после `icacls /inheritance:r` содержит `D:PAI…` (флаг AI),
   regex read-back учитывает `P[A-Z]*`.

## 7. Definition of Done — статус

- Матрица fresh/repair/foreign/deny-ACL — PASS на Setup SHA `c20c3858…`/
  `ba03f60f…` (таблица выше). Reparse — NOT RUN.
- Failure-кейсы (sc 1639, DACL mismatch, foreign) воспроизведены и ведут себя
  fail-closed.
- Static vs VM verified разделены в этой секции. Не входит: PC-130,
  kill-switch, холодная матрица (см. CURRENT_STATE §6.1 — cold-start
  auto-ready BLOCKED, отдельная работа).

## 1. SDDL fix (R1) — static checked ✓ / VM verified ✗

- База `93e8970` НЕ содержит recovery-коммит `010f5f50`
  (`codex/recovery-review`): в `script/windows_installer.iss:175` значение
  `THRONE_SERVICE_SDDL` кончается на `(A;;GA;;;' + Sid + ''` — без
  закрывающей `)`.
- Перенесено в нашу ветку (одно-символьный фикс: `+ Sid + ')'')`) вместе с
  portable regression `tests/proxycore/test_installer_sddl_contract.py`
  (парсит реальную Pascal-конкатенацию; 2 SID-кейса).
- Результат: на базе — FAILED (обе SIД-подстановки без `)`, воспроизведено);
  после фикса — OK.
- **Остаток REC-01 по R1 (обязательно):** Inno build нового Setup, Windows
  SDDL parser (ConvertStringSecurityDescriptorToSecurityDescriptor), read-back
  service Environment, запуск службы после свежей установки — только в VM.

## 2. Installer hardening (R4) — DRAFT DIFF, не применён

Ниже — целевые изменения `script/windows_installer.iss` (эскиз; каждый пункт
требует Inno-сборки и fault-matrix в VM перед принятием; готовые процедуры
должны падать fail-closed и НЕ оставлять пол-установленной службы).

### 2.1 Checked procedures вместо сырых `[Run]` команд

```pascal
function RunChecked(const Cmd, Params: String; ExpectCode: Integer;
  const What: String): Boolean;
var Code: Integer;
begin
  Result := False;
  if not Exec(Cmd, Params, '', SW_HIDE, ewWaitUntilTerminated, Code) then begin
    Log(Format('%s: exec failed %d', [What, Code])); Exit;
  end;
  if Code <> ExpectCode then begin
    Log(Format('%s: exit %d, expected %d', [What, Code, ExpectCode])); Exit;
  end;
  Result := True;
end;
```

- `sc create` / `sc stop` / `sc delete` / `icacls` вызывать только через
  `RunChecked`; любая ошибка → откат (`sc delete` нашей службы) и Abort Setup
  с диалогом. Проверка postcondition: `sc query` → RUNNING/STOPPED,
  `sc sdshow`/Environment read-back.

### 2.2 Ownership guard перед repair/uninstall (foreign collision)

```pascal
function IsOurServiceImage(const QueryOut: String): Boolean;
// sc qc ProxyCoreService -> BINARY_PATH_NAME должен начинаться с
// '<OurInstallDir>\ThroneCore.exe' (без учёта регистра, сравнение путей
// с нормализацией). Иначе: foreign service с нашим именем -> отказ ДО
// любых изменений Environment/файлов, диалог с фактическим путём.
```

- Repair: если служба существует и не наша → Abort (не менять Environment).
- Uninstall: `sc stop`/`sc delete` только при ownership-подтверждении;
  чужую службу не удалять никогда.

### 2.3 ImagePath с отдельными кавычками вокруг exe

```pascal
// binPath для sc create:
'"' + ExpandConstant('{app}\ThroneCore.exe') + '" service'
```

- Проверяемый контракт: `sc qc` → ImagePath = `"...ThroneCore.exe" service`
  (внутренние кавычки вокруг исполняемого файла; путь с пробелами обязан
  проходить). Вписать в portable regression (парсер читает реальную строку).

### 2.4 Reparse / DACL / unsafe ACE (после 2.1–2.3)

- Отказ установки, если `{app}` или `{commonappdata}\ProxyCore` — reparse
  point (GetFileAttributes → FILE_ATTRIBUTE_REPARSE_POINT), в т.ч. при repair.
- `icacls /inheritance:r` + точный ACE-набор; postcondition: read-back DACL
  равен ожидаемому (SDDL-сравнение), отсутствие explicit ACE для
  произвольных пользователей (только SYSTEM/Administrators/+выбранный SID).
- Admin-режим: проверять, что выбранный каталог не «защищённый» (Program
  Files чужого приложения, корни дисков, Windows) — явный deny-list + ACL
  read-back.
- Grant при UAC другого пользователя: назначать по supported account types
  (локальный пользователь по имени через Get-LocalUser с явной проверкой, что
  это не группа-все/не аноним); unsupported → отказ, широкий grant запрещён.

### 2.5 Service-less vs production install

- Пока решение не закреплено владельцем: diagnostic service-less install не
  объявлять успешным V1; различать режимы в UI и логах.

## 3. Матрица приёмки (все — только VM verified)

fresh install; repair; чужая одноимённая служба; путь с пробелами; unsafe
explicit ACE; reparse dir; SID allow/deny; SCM Running; read-back. Failure
cases обязаны падать fail-closed. Никаких claims про PC-130/kill-switch.
Не включать auto-start/store.

## 4. Definition of Done (REC-01 целиком)

1. Все поддерживаемые кейсы матрицы PASS на конкретном Setup SHA (зафиксировать
   хэш Setup.exe и SHA дерева).
2. Failure-кейсы воспроизведены и падают ожидаемо.
3. Отчёт здесь обновлён: static vs VM verified разделены.
