# run-vm-smoke.ps1 - REC-00/REC-00B stand smoke orchestration.
# One documented sequence: cold-start VM -> guest command -> file transfer
# (both ways, hash-verified) -> compile smoke on a pinned source archive ->
# export logs with exit codes. Read docs/recovery/CURRENT_STATE.md section 6.
#
# Credentials: either pass -GuestPassword (SecureString, e.g. from
# Read-Host -AsSecureString; owner path) or rely on the guest-generated
# credential stored by the REC-00B bootstrap in transient-less guest property
# REC00B_CRED for user recagent. The secret is never printed or written to
# logs/git; it lives in process memory only.
#
# Exit codes: 0 ok; 2 guest execution service not ready; 3 preflight/cred;
# 4 smoke/transfer/build failure (details in OutDir logs).
param(
    [string]$VmName = 'PC130-Evidence',
    [string]$GuestUser = 'recagent',
    [Security.SecureString]$GuestPassword,
    [string]$SourceTgz = '',
    [string]$ToolsTgz = '',
    [string]$OutDir = 'D:\GLM_project\vm-evidence\recovery',
    [string]$GuestBase = 'C:\rec00',
    [string]$CredProperty = 'REC00B_CRED',
    # -Offline: build with GOPROXY=off (module cache must be complete).
    # Default allows the guest to download missing modules once (NAT).
    [switch]$Offline
)

$ErrorActionPreference = 'Stop'
$VBox = 'C:\Program Files\Oracle\VirtualBox\VBoxManage.exe'
$stamp = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
$summary = Join-Path $OutDir ("SUMMARY-{0}.txt" -f $stamp)

function Fail([string]$Message, [int]$Code) {
    [Console]::Error.WriteLine("run-vm-smoke: $Message")
    Add-Content -Path $summary "FATAL: $Message (exit $Code)"
    exit $Code
}

if (-not (Test-Path $VBox)) { Fail 'VBoxManage not found' 3 }
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
Add-Content -Path $summary "=== run-vm-smoke $stamp VM=$VmName ==="

# --- 0. Resolve credentials ----------------------------------------------------
$Plain = $null
if ($GuestPassword) {
    $BSTR = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($GuestPassword)
    $Plain = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($BSTR)
    [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($BSTR)
} else {
    $raw = & $VBox guestproperty get $VmName $CredProperty 2>&1 | Out-String
    if ($raw -match 'Value:\s*(\S+)') { $Plain = $Matches[1] }
    if (-not $Plain) { Fail "no password param and guest property $CredProperty not set" 3 }
}

function Run-Logged([string]$Name, [string[]]$VBoxArgs) {
    $file = Join-Path $OutDir ("{0}-{1}.log" -f $stamp, $Name)
    # Keep native stderr as text: with EAP=Stop PS5.1 would throw NativeCommandError.
    $prevEap = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
    try { & $VBox @VBoxArgs *>&1 | Tee-Object -FilePath $file } finally { $ErrorActionPreference = $prevEap }
    $script:VBoxExit = $LASTEXITCODE
    Add-Content -Path $summary ("{0}: vbox_exit={1} -> {2}" -f $Name, $LASTEXITCODE, $file)
}
function GcArgs([string[]]$CmdArgs) {
    @('--nologo', 'guestcontrol', $VmName, 'run',
        '--exe', 'C:\Windows\System32\cmd.exe',
        '--username', $GuestUser, '--password', $Plain,
        '--wait-stdout', '--wait-stderr', '--') + $CmdArgs
}
function GuestRun([string]$Name, [string[]]$CmdArgs) {
    Run-Logged $Name (GcArgs $CmdArgs)
    if ($script:VBoxExit -ne 0) {
        if (Select-String -Path (Join-Path $OutDir ("{0}-{1}.log" -f $stamp, $Name)) `
                -Pattern 'not ready' -Quiet) {
            Fail 'guest execution service not ready (see README)' 2
        }
        if (Select-String -Path (Join-Path $OutDir ("{0}-{1}.log" -f $stamp, $Name)) `
                -Pattern 'not able to logon' -Quiet) {
            Fail 'guest logon failed (credentials)' 3
        }
        Fail "guest step $Name failed" 4
    }
}

# --- 1. Preflight / cold start -------------------------------------------------
if (-not (& $VBox list vms | Select-String ('"{0}"' -f $VmName))) { Fail "VM $VmName not registered" 3 }
$state = (& $VBox showvminfo $VmName --machinereadable |
    Select-String 'VMState="(.+)"').Matches[0].Groups[1].Value
if ($state -ne 'running') {
    Run-Logged 'coldstart' @('--nologo', 'startvm', $VmName, '--type', 'headless')
    # RunLevel 2 (VBoxService) is enough for guestcontrol; 3 needs an
    # interactive session which this VM has only after manual/bootstrap logon.
    $deadline = (Get-Date).AddMinutes(20); $lvl = 0
    while ($lvl -lt 2 -and (Get-Date) -lt $deadline) {
        Start-Sleep -Seconds 20
        $m = & $VBox showvminfo $VmName --machinereadable | Select-String 'GuestAdditionsRunLevel=(\d)'
        if ($m) { $lvl = [int]$m.Matches[0].Groups[1].Value }
    }
    if ($lvl -lt 2) { Fail 'Guest Additions did not reach runlevel 2' 3 }
}

# --- 1b. Wait until the guest control channel actually answers ------------------
# RunLevel 2 reports before VBoxService accepts sessions (observed on every
# boot of this image); poll with a harmless command before real steps.
$ctlReady = $false
$deadline = (Get-Date).AddMinutes(10)
while ((Get-Date) -lt $deadline) {
    Run-Logged 'ctl-ready-poll' @(GcArgs @('cmd', '/c', 'echo READY'))
    if ($script:VBoxExit -eq 0) { $ctlReady = $true; break }
    Start-Sleep -Seconds 20
}
if (-not $ctlReady) { Fail 'guest control channel did not become ready in 10 min' 2 }

# --- 2-4. Guest commands ---------------------------------------------------------
GuestRun 'tiny-smoke' @('cmd', '/c', 'echo REC_SMOKE_OK & ver & whoami & echo EXIT:%ERRORLEVEL%')
# No quotes/pipes: guest cmd re-parses the line, so keep one token for -Command.
GuestRun 'utc-clock'  @('cmd', '/c', 'powershell -NoProfile -Command [DateTime]::UtcNow')
GuestRun 'diskfree'   @('cmd', '/c', 'dir C:\ | findstr /C:"bytes free" & echo EXIT:%ERRORLEVEL%')

# --- 5. File transfer round-trip with hash check -------------------------------
GuestRun 'make-base-dir' @('cmd', '/c',
    "if not exist $GuestBase md $GuestBase & echo EXIT:%ERRORLEVEL%")
$probe = Join-Path $OutDir "probe-$stamp.txt"
"run-vm-smoke probe $stamp" | Set-Content -Path $probe
$hostHash = (Get-FileHash $probe -Algorithm SHA256).Hash
Run-Logged 'copyto-probe' @('--nologo', 'guestcontrol', $VmName, 'copyto',
    '--username', $GuestUser, '--password', $Plain,
    '--target-directory', "$GuestBase\", $probe)
if ($script:VBoxExit -ne 0) { Fail 'copyto probe failed' 4 }
GuestRun 'hash-probe-in-guest' @('cmd', '/c',
    "certutil -hashfile $GuestBase\probe-$stamp.txt SHA256 & echo EXIT:%ERRORLEVEL%")
$guestHashLog = Get-Content (Join-Path $OutDir ("{0}-hash-probe-in-guest.log" -f $stamp)) -Raw
if ($guestHashLog -notmatch [regex]::Escape($hostHash)) { Fail "probe hash mismatch guest vs host" 4 }
$backDir = Join-Path $OutDir 'roundtrip'
New-Item -ItemType Directory -Force -Path $backDir | Out-Null
Run-Logged 'copyfrom-probe' @('--nologo', 'guestcontrol', $VmName, 'copyfrom',
    '--username', $GuestUser, '--password', $Plain,
    '--target-directory', "$backDir\", "$GuestBase\probe-$stamp.txt")
if ($script:VBoxExit -ne 0) { Fail 'copyfrom probe failed' 4 }
$backHash = (Get-FileHash (Join-Path $backDir "probe-$stamp.txt") -Algorithm SHA256).Hash
if ($backHash -ne $hostHash) { Fail 'round-trip hash mismatch' 4 }
Add-Content -Path $summary "probe sha256 $hostHash verified host->guest->host"

# --- 6. Toolchain (install only if missing) --------------------------------------
GuestRun 'go-present-check' @('cmd', '/c',
    "if exist $GuestBase\go\bin\go.exe (echo GO_PRESENT) else (echo GO_MISSING) & echo EXIT:%ERRORLEVEL%")
$goCheck = Get-Content (Join-Path $OutDir ("{0}-go-present-check.log" -f $stamp)) -Raw
if ($goCheck -match 'GO_MISSING') {
    if (-not $ToolsTgz -or -not (Test-Path $ToolsTgz)) { Fail 'guest toolchain missing and -ToolsTgz not provided' 4 }
    Run-Logged 'copyto-tools' @('--nologo', 'guestcontrol', $VmName, 'copyto',
        '--username', $GuestUser, '--password', $Plain,
        '--target-directory', "$GuestBase\", $ToolsTgz)
    if ($script:VBoxExit -ne 0) { Fail 'copyto tools archive failed' 4 }
    GuestRun 'extract-tools' @('cmd', '/c',
        "tar -xzf $GuestBase\$(Split-Path $ToolsTgz -Leaf) -C $GuestBase & echo EXIT:%ERRORLEVEL%")
}
GuestRun 'go-version' @('cmd', '/c', "$GuestBase\go\bin\go.exe version & echo EXIT:%ERRORLEVEL%")

# --- 7. Compile smoke on pinned source ------------------------------------------
if ($SourceTgz -and (Test-Path $SourceTgz)) {
    Run-Logged 'copyto-src' @('--nologo', 'guestcontrol', $VmName, 'copyto',
        '--username', $GuestUser, '--password', $Plain,
        '--target-directory', "$GuestBase\", $SourceTgz)
    if ($script:VBoxExit -ne 0) { Fail 'copyto source archive failed' 4 }
    GuestRun 'extract-src' @('cmd', '/c',
        "md $GuestBase\src 2>nul & tar -xzf $GuestBase\$(Split-Path $SourceTgz -Leaf) -C $GuestBase\src & echo EXIT:%ERRORLEVEL%")
    $proxySetting = if ($Offline) { 'set GOPROXY=off&&' } else { '' }
    GuestRun 'compile-smoke' @('cmd', '/c',
        "set PATH=$GuestBase\go\bin;%PATH%&& set GOPATH=$GuestBase\gopath&& set GOCACHE=$GuestBase\gocache&& set GOMODCACHE=$GuestBase\gopath\pkg\mod&& $proxySetting cd /d $GuestBase\src\core\server && go build ./... && go build -o ThroneCore-rec00b.exe . && echo BUILD_OK")
    GuestRun 'hash-binary' @('cmd', '/c',
        "certutil -hashfile $GuestBase\src\core\server\ThroneCore-rec00b.exe SHA256 & echo EXIT:%ERRORLEVEL%")
    $artifactDir = Join-Path $OutDir 'artifacts'
    New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null
    Run-Logged 'copyfrom-binary' @('--nologo', 'guestcontrol', $VmName, 'copyfrom',
        '--username', $GuestUser, '--password', $Plain,
        '--target-directory', "$artifactDir\", "$GuestBase\src\core\server\ThroneCore-rec00b.exe")
    Add-Content -Path $summary ("built artifact: {0}" -f (Join-Path $artifactDir 'ThroneCore-rec00b.exe'))
} else {
    Add-Content -Path $summary 'compile smoke skipped: -SourceTgz not provided'
}

Remove-Variable Plain
Get-ChildItem $OutDir -Filter "*$stamp*.log" | Get-FileHash -Algorithm SHA256 |
    ForEach-Object { Add-Content -Path $summary ("{0}  {1}" -f $_.Hash, $_.Path) }
Write-Host "run-vm-smoke finished OK, summary: $summary"
exit 0
