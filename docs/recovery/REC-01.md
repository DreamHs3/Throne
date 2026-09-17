# REC-01 — installer SDDL + hardening (рабочий документ)

Статус: **REC-01 ЗАКРЫТ (2026-09-18, REC-01C-раунд).** Блокирующие находки
№7 (payload утекал в junction-цель до отказа), №8 (protected-dir deny-list
не реализован) и №9 (rollback repair-ошибки удалял существующую службу)
устранены и VM verified на Setup `15eec3e4…5a49` (source `44108f44`):
матрица §5.2. Дополнительно закрыты: ownership по учётной записи службы,
ABSENT/ERROR-контракт TryReadServiceImagePath, ACL-проверка каталога
бинарника. Принятая задокументированная граница: exit-коды silent-режима
остаются недостоверными (находка №6) — вердикты только по postconditions.
Холодные кейсы — вне REC-01.
Ветка: `agent/rec01-installer-static`
(итог раунда REC-01C: `44108f44`; история: `a854a45f` → `28950f23` →
`cdd40710`).

Провенанс REC-01C: проверенный source = `44108f44`
(«fix(REC-01C): extract the first Program Files path component…», ветка
`agent/rec01-installer-static`, push `DreamHs3/GLM_project` fast-forward),
Setup (service-only test package) = `15eec3e4f41dc20161fb3f404589b42d8f2152bf3ea2e51d37244f3d507e5a49`
(`artifacts/ProxyCoreSetup-rec01c-final.exe`; собран в госте ISCC 6.7.3 из
дерева `C:\rec01\src` c этим `.iss`; payload `ThroneCore.exe`
`5397ef3c…` — архивный, неизменный).

## 5.2 REC-01C раунд (2026-09-17/18, Setup `15eec3e4…5a49` = source `44108f44`)

Канал исполнения: после деградации guestcontrol-spawn — печать payload'ов
(`go-c1-*.cmd`) в elevated-консоль гостя (evidence, High IL) + файловый
канал для артефактов. Инцидент канала и жёсткий reset задокументированы в
`journal.txt` (записи 2026-09-17T21:3x–22:1x) и
`vm-evidence/recovery/rec01/REC-01C-REPORT.md`.

| # | Кейс | Тест выполнен | Требование выполнено |
|---|---|---|---|
| U1 | Базовый silent uninstall (наша служба + LocalSystem) | PASS: exit 0, служба удалена по ownership (ImagePath+account), ключ/Env удалены | да (позитивная ветка ownership) |
| T4 | Reparse `{app}` junction (jtarget + канарейка) | PASS ×2 (на промежуточном `d481f43d` и финальном `15eec3e4`): **exit 7**, отказ в PrepareToInstall ДО [Files]; jtarget = только канарейка (fc: no differences); служба 1060; data-каталога нет | **да** (находка №7 устранена: payload не утекает) |
| T5 | Reparse data-каталога (junction `C:\ProgramData\ProxyCore`) | PASS: exit 7, отказ именно на reparse; djtarget не тронут (канарейка цела); `{app}` не создан; служба 1060. **Первый прогон поймал ЛОЖНЫЙ отказ deny-list на дефолтном `{app}`** (баг арифметики первого компонента — исправлен `44108f44`, portable-зеркал-тест добавлен) | **да** (находка доработана и подтверждена) |
| T7 | Protected dir `/DIR=C:\Windows` | PASS: отказ deny-list «Windows directory subtree», ноль мутаций (нет файлов в C:\Windows, нет службы); exit 1 (silent-путь NextButtonClick — задокументировано) | **да** (находка №8 устранена) |
| T9 | Foreign same-name service (notepad.exe) | PASS: exit 7, отказ называет ImagePath+account; чужая служба не тронута; файлов/Env нет | да (регресс не выявлен) |
| T9b | НОВЫЙ: наш ImagePath, чужая учётка (`obj=NT AUTHORITY\LocalService`) | PASS: exit 7, отказ «not ours (ImagePath: <наш>, account: NT AUTHORITY\LocalService)»; служба подмены не тронута, Env не записан | **да** (новое: ownership учитывает учётную запись LocalSystem) |
| F1 | Fresh install smoke + postconditions | PASS: exit 0; ImagePath заквотирован; ObjectName=LocalSystem; Env 3 значения (грант SID evidence `-1000`); DACL SY+BA; `net start` → RUNNING | да |
| T3 | Repair при unsafe ACE (Everyone) + sentinel-Environment | PASS: отказ fail-closed (VerifyDataDirAcl), **служба СОХРАНЕНА** (`RollbackService: pre-existing service kept, its previous Environment restored`), **Environment == sentinel байт-в-байт** (restore доказанно перезаписывает), data/app целы; ACE Everyone остаётся (существовал до попытки — не ослабление) | **да** (находка №9 устранена) |
| T3b | Восстановление: снять ACE + повторный repair | PASS: exit 0, DACL SY+BA (без Everyone), Env = значения установщика, `net start` → RUNNING | да (repair smoke) |
| T3c | Fresh-rollback: грязный data-каталог БЕЗ службы → fresh install | PASS: отказ VerifyDataDirAcl → **`RollbackService: sc delete exit 0` — удалена только служба этой попытки** (после отказа 1060); первый прогон кейса не сработал из-за гонки с асинхронной зачисткой unins000 (задание пересоздало каталог чистым) — чекер был прав, кейс перезапущен раздельно (t3cA/t3cB) | **да** (rollback удаляет только свою службу) |
| T6b | Ownership-safe uninstall (foreign takeover notepad) | PASS: `belongs to "…notepad.exe" (account: LocalSystem), left untouched`; служба выжила; файлы удалены | да (регресс не выявлен, лог дополнен account) |
| ACL | VerifyAppDirAcl (новое) | PASS на дефолтной установке (Program Files ACL: BU/AU/пакеты — только RX; владелец BA) — false positive нет | да (новое требование: каталог бинарника не может быть изменён/подменён обычным пользователем) |
| GUI | Запуск установленного GUI обычным пользователем | PASS: launch через unelevated Explorer; заголовок **`ProxyCore 0.0.0` без `[Admin]`** (скриншот r2c-07); **token integrity процесса Throne.exe (pid 4536) = S-1-16-8192 Medium** (читано из elevated-контекста High S-1-16-12288) | **да** (non-elevated UI доказан токеном, а не заголовком) |

Snapshot стенда после матрицы: `REC01C-POST` `8162d18a-17f9-4b02-94d0-15452df9cb2a`
(стенд восстановлен: GUI-пакет `e44b532c…` из cdd40710, служба demand+STOPPED).
Артефакты: `vm-evidence/recovery/rec01/r2/` (`t4-*`, `t5-*`, `t7-*`, `t9*`,
`fresh-*`, `t3*`, `t6b-*`, `gui2-*`, `diag-*`, payload'ы `go-c1-*.cmd`,
консоль `console/r2c-*.png`).

Ограничение честности: полный GUI-пакет из дерева `44108f44` в CI не
пересобирался (REC-01C меняет только `windows_installer.iss`; GUI-часть не
затронута; service-only Setup — полный эквивалент инсталлятора для матрицы).
При следующей CI-сборке полного пакета хэш Setup'а изменится — привязка
`15eec3e4…` остаётся валидной для проверенной матрицы.

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
7. **[РЕШЕНА в REC-01C, коммит `aff8be27`, VM verified T4 (§5.2)]**
   Reparse на `{app}`: payload уходит в цель до отказа (R2 T4) —
   `[Files]` копирует через junction раньше, чем `SetupServiceEnv` проверяет
   reparse; служба и data при этом чисты. Исправление: reparse-проверка
   (`ReparsePathViolation`, включая существующие родительские компоненты)
   перенесена в `PrepareToInstall` до первой записи payload; T4 повторён на
   `15eec3e4…` — exit 7, цель/канарейка не тронуты. Reparse на data-каталоге
   чист (T5).
8. **[РЕШЕНА в REC-01C, коммиты `a53f2ab6`+`9ca6d311`+`44108f44`, VM
   verified T7 (§5.2)]** Protected-dir deny-list НЕ реализован (R2 T7 FAIL):
   тихая установка в `C:\Windows` проходила. Исправление:
   `ProtectedDirViolation` — ОТДЕЛЬНАЯ политика выбора пути (корни дисков,
   subtree Windows, чужие Program Files; интерактив — NextButtonClick,
   silent — PrepareToInstall), НЕ замена ACL-проверке `VerifyAppDirAcl`
   (каталог бинарника: запрет write/delete/reacl/owner для кого-либо кроме
   SYSTEM/Administrators/CREATOR OWNER/TrustedInstaller). T7 повторён —
   отказ, ноль мутаций. T5 дополнительно поймал и помог исправить ложный
   отказ deny-list на дефолтном `{app}` (арифметика первого компонента).
9. **[РЕШЕНА в REC-01C, коммит `1f6e6e98`, VM verified T3/T3c (§5.2)]**
   Rollback repair-ошибки уничтожал существующую службу (R2 T3 FAIL):
   `FailStep`→`RollbackService` делал `sc delete` и при repair. Исправление:
   PrepareToInstall классифицирует fresh/repair; при repair перед мутациями
   захватывается Environment (`CaptureServiceEnvironment`, ошибка чтения =
   отказ до мутаций); rollback сохраняет существующую службу и восстанавливает
   Environment, удалять может только службу, созданную текущей попыткой
   (fresh, T3c). T3 повторён: служба сохранена, Environment == sentinel.
   Восстановление после R2 T3 (снять ACE + re-repair) более не требуется как
   аварийная процедура — штатный repair (T3b) восстанавливает всё сам.
10. Стенд (R2): после save/resume гостевой канал guestcontrol деградирует
    (VERR_UNRESOLVED_ERROR, плавающие copyto/run) — лечится перезагрузкой
    гостя; `Register-ScheduledTask -Principal` + `-Password` несовместимы
    (AmbiguousParameterSet), S4U чужого пользователя — Access denied; пароль
    recagent ротирован после попадания в консоль-скриншот (см. journal.txt,
    MANUAL-заметки 2026-09-17).

## 7. Definition of Done — статус

- **REC-01 ЗАКРЫТ (2026-09-18).** Все блокирующие находки устранены и
  VM verified на Setup SHA `15eec3e4f41dc20161fb3f404589b42d8f2152bf3ea2e51d37244f3d507e5a49`
  = source `44108f44` (матрица §5.2); DoD п.1–3 выполнены: матрица §3
  исполнена на конкретном Setup SHA (R2: `ba03f60f…` = `cdd40710` — история;
  REC-01C: `15eec3e4…` = `44108f44`), failure-кейсы падают fail-closed БЕЗ
  разрушения существующего состояния, static vs VM разделены ниже.
- Сводка вердиктов R2 (Setup `ba03f60f…`, история): PASS —
  T1,T2,T3b,T5,T6,T8,T9,T10,T11,GUI; FAIL (блокирующие, исправлены в
  REC-01C — см. §5.2 и находки №7–9): T4, T7, T3. NOT RUN — нет.
- Сводка вердиктов REC-01C (Setup `15eec3e4…`, §5.2): PASS —
  U1, T4, T5, T7, T9, T9b, F1, T3, T3b, T3c, T6b, ACL, GUI; NOT RUN — нет.
- Принятые границы (не блокируют, задокументированы): exit-коды silent
  (находка №6) — вердикты по postconditions; полный GUI-пакет из дерева
  `44108f44` в CI не пересобирался (installer-only изменения); «тихие»
  остатки unins000.exe/`config` после uninstall — старое поведение
  деинсталлятора, вне REC-01.
- Не входит: PC-130, kill-switch, холодная матрица (см. CURRENT_STATE §6.1 —
  cold-start auto-ready BLOCKED, отдельная работа).
- Сокращённый пакет без Throne.exe (все ночные, R2 и REC-01C прогоны) =
  **service-only test package**; полный пакет с GUI проверен на установку +
  запуск (R2 GUI, `e44b532c…`) и на non-elevated запуск (REC-01C GUI: IL
  Medium, заголовок без `[Admin]`).

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
