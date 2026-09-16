# REC-01 — installer SDDL + hardening (рабочий документ, DRAFT)

Статус: **IN PROGRESS — static checked частично, VM verified — НЕТ.**
Ветка статической части: `agent/rec01-installer-static` (от
`agent/rec00-current-state`).

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
