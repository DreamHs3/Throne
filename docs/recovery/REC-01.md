# REC-01 — installer SDDL + hardening (рабочий документ)

Статус: **REC-01 матрица исполнена полностью (2026-09-16/17 ночь + R2-сессия
2026-09-17 вечер). R1/R4 — VM verified. Reparse-кейс — RUN: отказ
fail-closed подтверждён, но при reparse на `{app}` payload копируется в цель
ДО отказа (находка №7, следующий раунд). Protected-dir deny-list — НЕ
реализован (FAIL, находка №8). Rollback repair-ошибки удаляет существующую
службу (FAIL, находка №9). Холодные кейсы — вне REC-01.**
Ветка: `agent/rec01-installer-static`
(a854a45f → 28950f23 → 07685be8; итог `cdd4071010f627bc199c6a3f3891005f88d033a9`).

Провенанс: проверенный source = `cdd4071010f627bc199c6a3f3891005f88d033a9`,
Setup (service-only test package) =
`ba03f60f38300a876d6e5ab14aa33eb5dca9d5ba9eff017cf7df9633ee2c9df1`
(`artifacts/ProxyCoreSetup-hardened-cdd40710.exe`; собран в госте ISCC 6.7.3
из дерева `cdd40710`, `.iss` в `transfer/windows_installer.iss` идентичен
коммиту с точностью до CRLF). HEAD `9c4841b3` отличается от `cdd40710`
только документацией (`docs/recovery/*.md`) — пересборка Setup не требуется.

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
| 13 | Reparse-point отказ | ba03f60f | RUN в R2-сессии (см. 5.1, T4/T5) | r2/t4-*, r2/t5-* |

### 5.1 R2-сессия (2026-09-17 вечер, тот же Setup ba03f60f)

Все кейсы на `artifacts/ProxyCoreSetup-hardened-cdd40710.exe`
(`ba03f60f…`); артефакты `vm-evidence/recovery/rec01/r2/` (+ console/ r2-*).
Оркестрация: консоль (elevated, evidence) + файловый канал guestcontrol;
скрипты `rec01-r2-push/fetch/setup-verify.ps1` там же.

| # | Кейс | Результат | Артефакт |
|---|---|---|---|
| T1 | Тихий uninstall проверенной установки | PASS: служба 1060, ключ/Env отсутствуют, data и app каталоги удалены | r2/t4a-* (чистое состояние), t1-unins.log |
| T2 | Silent fresh install (default, /ALLUSERS) | **PASS**: exit 0; ImagePath `"…\ThroneCore.exe" service`; Env read-back (SDDL+SID -1000 evidence); DACL SY+BA; ThroneCore.exe sha256 `5397ef3c…` = архивному | r2/t2-exit.txt, t2-post.txt, t2-install.log |
| T3 | Unsafe explicit ACE (Everyone) при repair | Отказ **fail-closed** (VerifyDataDirAcl не пропустил, abort), НО: RollbackService **удалил ранее существовавшую службу** (находка №9 → item rollback repair = FAIL); тихий exit 0 маскирует abort; ACE остался в DACL | r2/t3-exit.txt (0), t3-steps.txt, t3-post.txt (1060), t3-repair.log |
| T3b | Восстановление после T3 | PASS: `/remove *S-1-1-0` + повторный repair → служба возвращена, DACL SY+BA | r2/go-t3b, t3b-steps.txt |
| T4 | Reparse на `{app}` (junction → jtarget с канарейкой) | Отказ сработал (`SetupServiceEnv FAILED: … reparse point`, служба откатилась, data не создан, канарейка цела — hash совпал), **НО payload (ThroneCore.exe, unins000.*) скопирован через junction в целевой каталог ДО отказа** (находка №7); exit 0 | r2/t4-exit.txt, t4-steps.txt, t4-canary-*, t4-install.log |
| T5 | Reparse на `{commonappdata}\ProxyCore` | **PASS**: отказ до создания/записи data (djtarget не тронут, канарейка цела), служба откатилась; файлы app — в обычном {app} (ожидаемо) | r2/t5-exit.txt, t5-steps.txt, t5-canary-*, t5-install.log |
| T6 | Ownership-safe uninstall (foreign takeover) | **PASS**: `sc config binPath= notepad.exe` → uninstall → журнал `belongs to "C:\Windows\System32\notepad.exe", left untouched`, служба НЕ удалена/не остановлена; после — фейковая служба удалена вручную | r2/t6b-steps.txt, t6b-unins.log |
| T7 | Protected install directory (`/DIR=C:\Windows`) | **FAIL (не реализовано)**: установка прошла (exit 0, файлы в `C:\Windows\ProxyCore`, служба создана); deny-list отсутствует в коде; после кейса удалено | r2/t7-exit.txt, t7-steps.txt |
| T8 | Путь с пробелами (`/DIR="C:\rec01 sp ace"`) | **PASS**: ImagePath с отдельными кавычками вокруг exe, `net start` → RUNNING, стоп после проверки | r2/t8-exit.txt, t8-qc.txt, t8-start.txt |
| T9 | Намеренный отказ (foreign same-name service) | **PASS**: `PrepareToInstall: refusing…` ДО любых изменений; чужая служба нетронута, файлов/Env нет; **exit code 7** | r2/t9-exit.txt (7), t9-steps.txt, t9-install.log |
| T10 | Модель пользователя при UAC (установка elevated как recagent) | **PASS**: грант = SID самого установившего локального пользователя (`…-1001` recagent; в T2/T11 `…-1000` evidence) — явный SID по Get-LocalUser, не группа/не аноним | r2/t10-exit.txt, t10-env.txt |
| T11 | Финальная fresh install + SCM Running | PASS: exit 0, грант `-1000`, `net start` → RUNNING; snapshot `REC01-R2-POST` `12859391-…` | r2/t11-exit.txt, t11-env.txt, t11-start.txt |
| GUI | Полный пакет с GUI из cdd40710 (CI run 35267101876, DreamHs3/Throne, ветка `ci/rec01-full-gui`) | **PASS**: `Throne-0.0.0-windows-universal-installer.exe` sha256 `e44b532c…`; установка exit 0 (Throne.exe+ThroneCore.exe+libcronet.dll); окно `[Admin] ProxyCore 0.0.0` запущено, лог «Core Has Successfully Connected» (child-core GUI-пути; управление runtime через GUI НЕ заявляется — R5/REC-03) | r2/ci/gui-setup-sha256.txt, r2/gui-exit.txt, r2/gui-post.txt, console/r2-31-gui-launch.png |

Вердикты оркестратора (`rec01-setup-verify.ps1`, postcondition-based):
T2 → `PASS`; T9 → `REFUSED` (exit 7 записан, неtrusted); T3 → `FAIL`
(exit 0 при отсутствующей службе — доказывает, что exit 0 недостаточен).

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
6. **Exit codes уточнены (R2)**: отказ в `PrepareToInstall` (foreign service,
   reparse-стиль гвардии) → **exit 7**; abort через `FailStep`/`RaiseException`
   в середине установки (DACL mismatch, reparse в SetupServiceEnv, sc-ошибка)
   → **exit 0** (suppressed msgbox). В обоих случаях exit-код не является
   признаком успеха/отказа — вердикт только по postconditions
   (`rec01-setup-verify.ps1`: T2→PASS, T9→REFUSED, T3→FAIL на exit 0).
7. **Reparse на `{app}`: payload уходит в цель до отказа (R2 T4)** —
   `[Files]` копирует через junction раньше, чем `SetupServiceEnv` проверяет
   reparse; служба и data при этом чисты. Исправление следующего раунда:
   перенести reparse-проверку в `PrepareToInstall` (рядом с
   `ForeignServiceCollision`). Reparse на data-каталоге чист (T5).
8. **Protected-dir deny-list НЕ реализован (R2 T7 FAIL)**: тихая установка в
   `C:\Windows` проходит (файлы в `C:\Windows\ProxyCore`). Пункт 2.4
   (deny-list: корни дисков, Windows, чужие Program Files) — следующий раунд.
9. **Rollback repair-ошибки уничтожает существующую службу (R2 T3 FAIL)**:
   `FailStep`→`RollbackService` делает `sc delete` и при repair, хотя при
   ошибке середины установки существовавшая исправная служба должна
   сохраняться/восстанавливаться; небезопасный ACE при этом остаётся в DACL.
   Восстановление: снять ACE + повторный repair (T3b). Исправление: ветка
   rollback для repair (re-create + env restore) вместо delete.
10. Стенд (R2): после save/resume гостевой канал guestcontrol деградирует
    (VERR_UNRESOLVED_ERROR, плавающие copyto/run) — лечится перезагрузкой
    гостя; `Register-ScheduledTask -Principal` + `-Password` несовместимы
    (AmbiguousParameterSet), S4U чужого пользователя — Access denied; пароль
    recagent ротирован после попадания в консоль-скриншот (см. journal.txt,
    MANUAL-заметки 2026-09-17).

## 7. Definition of Done — статус

- **Матрица §3 исполнена полностью** (R2-сессия 5.1): fresh (T2), repair
  (T3/T3b), чужая одноимённая служба (T6/T9), путь с пробелами (T8), unsafe
  explicit ACE (T3), reparse dir (T4/T5), SID allow/deny (T2/T10/T9), SCM
  Running (T8/T11), read-back (все кейсы) — на Setup SHA
  `ba03f60f…` = source `cdd40710`.
- Сводка вердиктов: PASS — T1,T2,T3b,T5,T6,T8,T9,T10,T11,GUI;
  FAIL (находки, не блокируют принятие выполненной матрицы) — T4 (утечка
  payload до reparse-отказа), T7 (protected-dir не реализован), T3 (rollback
  repair удаляет существующую службу); NOT RUN — нет.
- Failure-кейсы воспроизведены и падают fail-closed, КРОМЕ разрушения
  существующей службы при repair-ошибке (находка №9 — в следующий раунд).
- Static vs VM verified разделены в этой секции. Не входит: PC-130,
  kill-switch, холодная матрица (см. CURRENT_STATE §6.1 — cold-start
  auto-ready BLOCKED, отдельная работа).
- Сокращённый пакет без Throne.exe (все ночные и R2 прогоны) =
  **service-only test package**; полный пакет с GUI собран в CI из
  `cdd40710` (run 35267101876) и проверен на установку+запуск окна (5.1 GUI).

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
