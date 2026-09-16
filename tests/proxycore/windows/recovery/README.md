# windows/recovery — стендовые smoke-последовательности (REC-00)

Назначение: одна документированная последовательность действий, которая
воспроизводит: cold-start VM → guest command → сборка known source → выгрузка
логов. Владелец стенда: см. `docs/recovery/CURRENT_STATE.md` §6.

## Текущее состояние: BLOCKED (guest execution)

`VBoxManage guestcontrol …` падает с `VBOX_E_IPRT_ERROR: The guest execution
service is not ready (yet)` (2 попытки, 2026-09-16). Пока компонент guest
control в госте не заработает и не будут предоставлены учётные данные
пользователя `evidence`, шаги 2–6 последовательности NOT RUN. Сам скрипт
закончится кодом 2 с той же диагностикой — это ожидаемое поведение, а не сбой
скрипта. Отсутствие сети в стенде — ограничение сетевой матрицы, НЕ повод
переустанавливать тулчейн или пересоздавать VM.

## Последовательность (один запуск)

```powershell
# Из корня checkout ProxyCore. Пароль НЕ сохранять в файлах/логах/истории.
$cred = Read-Host -AsSecureString 'Guest password (evidence)'
.\tests\proxycore\windows\recovery\run-vm-smoke.ps1 `
  -VmName PC130-Evidence -GuestUser evidence -GuestPassword $cred `
  -SourceDir (Get-Location).Path -OutDir D:\GLM_project\vm-evidence\recovery
exit $LASTEXITCODE
```

Шаги скрипта (каждый пишет свой лог + exit code в OutDir):

1. Preflight: VBoxManage доступен, VM зарегистрирована; если выключена —
   cold start (`startvm --type headless`) и ожидание GuestAdditions RunLevel 3.
2. Tiny smoke: `cmd /c ver` + метка времени; stdout/stderr/exit code в лог.
3. Часы: `powershell Get-Date -AsUTC` (VM настроена на локальное время,
   `rtcuseutc=off` — UTC фиксируется выводом команды, не настройкой).
4. Место: `fsutil volume diskfree C:`.
5. Передача файлов: копирование пробного файла в гостя и обратно с
   побайтовым сравнением; удаляются только объекты, созданные этим прогоном.
6. Compile smoke: копирование дерева исходников known SHA + portable Go
   (`tools/go`, `tools/protoc-31.1`), `go build` core-сервера в госте,
   лог сборки + exit code.
7. Итог: сводный `SUMMARY-<UTC>.txt` с перечнем логов и их sha256.

Правила: сеть/службы/TUN/firewall в госте скрипт НЕ трогает; elevation в госте
не запрашивает (проверка controlled elevation — отдельный шаг REC-01+);
snapshot `REC00-CLEAN-BASE-OS` не удаляется и не откатывается без решения
владельца; сетевые матрицы выполняются в disposable clone VM, не здесь.

## Выходные коды

| Код | Смысл |
|---|---|
| 0 | все шаги прошли |
| 2 | guest execution service не готов (текущий BLOCKED) |
| 3 | ошибка preflight (нет VBoxManage/VM/не дождались GA) |
| 4 | ошибка smoke/сборки (детали в логах OutDir) |
