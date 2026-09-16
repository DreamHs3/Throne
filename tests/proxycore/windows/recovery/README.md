# windows/recovery — стендовые smoke-последовательности (REC-00 / REC-00B)

Назначение: одна документированная последовательность, воспроизводящая цикл
«cold-start VM → гостевая команда → передача файла (в обе стороны, сверка
SHA-256) → compile smoke на зафиксированном source archive → выгрузка логов и
exit codes». Актуальное состояние стенда: `docs/recovery/CURRENT_STATE.md` §6.

## Каналы управления стендом

1. **guestcontrol (основной):** `VBoxManage guestcontrol <vm> run/copyto/copyfrom`
   от пользователя `recagent`. Учётная запись создана bootstrap'ом REC-00B
   специально для автоматизации; пароль сгенерирован ВНУТРИ гостя и хранится
   только в guest property `REC00B_CRED` (флаг RDONLYGUEST) конфигурации VM —
   он не печатается, не пишется в Git/отчёты/логи и не передаётся в чат.
   Скрипт читает его сам. Владелец может пользоваться и своим паролем
   `evidence` через `-GuestPassword (Read-Host -AsSecureString)`.
2. **Консоль (fallback/bootstrap):** `VBoxManage controlvm <vm>
   keyboardputscancode|keyboardputstring` + `screenshotpng`; чтение экрана —
   offline OCR (WinRT `Windows.Media.Ocr`, host-скрипт в
   `vm-evidence/recovery/rec00b/scripts/ocr.ps1`). Использовалась для
   диагностики и поднятия VBoxService (см. CURRENT_STATE §6, REC-00B).

Важно: `keyboardputstring` передаёт строку через CRT-аргументы Windows — пары
backslash схлопываются (`\\`→`\`); UNC-пути печатать как `\\\\vboxsvr\\...`
из bash. Гостевые команды не должны содержать вложенных кавычек/пайпов: cmd
гостя переразбирает строку заново.

## Предусловия стенда

- VBoxService (Guest Additions) должен работать: после установки GA ДО первой
  перезагрузки сервис может не стартовать («never run since last boot»). При
  ошибке `guest execution service is not ready (yet)` — стартовать сервис в
  госте (elevated: `net start VBoxService`); START_TYPE у него AUTO_START,
  после первой реальной перезагрузки поднимается сам.
- Пайплайн выхода: exit 2 = guest execution не готов; exit 3 = preflight/нет
  кредов; exit 4 = сбой smoke/передачи/сборки; 0 = успех.

## Последовательность (один запуск)

```powershell
# из корня checkout ProxyCore
.\tests\proxycore\windows\recovery\run-vm-smoke.ps1 `
  -SourceTgz D:\GLM_project\vm-evidence\recovery\rec00b\transfer\rec00b-src-facebecf.tgz `
  -ToolsTgz D:\GLM_project\vm-evidence\recovery\rec00b\transfer\rec00b-tools.tgz `
  -OutDir  D:\GLM_project\vm-evidence\recovery\rec00b\run<N>
```

Шаги (каждый пишет `<UTC-stamp>-<имя>.log` + vbox exit code в OutDir и
сводный `SUMMARY-*.txt`):

1. Креды: параметр SecureString ИЛИ guest property `REC00B_CRED` (по умолчанию).
2. Preflight/cold start: VM зарегистрирована; если выключена — `startvm
   --type headless` и ожидание GuestAdditions RunLevel 3 (до 20 мин).
3. tiny smoke (`echo/ver/whoami` + `%ERRORLEVEL%`), UTC-часы
   (`[DateTime]::UtcNow`), свободное место (`fsutil volume diskfree C:`).
4. Передача файла: probe → `copyto` → SHA-256 в госте (`certutil`) →
   `copyfrom` → SHA-256 на хосте → сравнение всех трёх.
5. Тулчейн: если в госте нет `C:\rec00\go\bin\go.exe` — `copyto` одного
   tar.gz (portable Go + module cache) и `tar -xzf`; затем `go version`.
6. Compile smoke: `copyto`+распаковка source-архива фиксированного SHA;
   `go build ./...` и `go build -o ThroneCore-rec00b.exe .` с
   `GOPROXY=off` (только локальный module cache, без сети);
   SHA-256 бинарника; `copyfrom` бинарника в `<OutDir>\artifacts\`.

Правила: сеть/службы/TUN/firewall скрипт в госте не трогает; snapshot'ы
(`REC00-CLEAN-BASE-OS`, `REC00B-TOOLCHAIN-READY`) не удаляются и не
откатываются; сетевые матрицы PC-130+ — только в disposable clone, не здесь.

## Bootstrap cred (когда REC00B_CRED отсутствует, например после пересоздания VM)

Из гостевой elevated-консоли (см. канал 2) однократно:
`powershell -NoProfile -Command "$p=-join((48..57)+(65..90)+(97..122)|Get-Random -Count 24|%{[char]$_});net user recagent $p /add /y;net localgroup administrators recagent /add /y;vboxcontrol guestproperty set REC00B_CRED $p --flags RDONLYGUEST"`
— пароль существует только в SAM гостя и guest property; нигде не печатается.
Удалять при выводе стенда из эксплуатации: `net user recagent /delete` +
`VBoxManage guestproperty delete <vm> REC00B_CRED`.
