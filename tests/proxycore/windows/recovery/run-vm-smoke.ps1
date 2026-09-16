# run-vm-smoke.ps1 - REC-00 stand smoke orchestration.
# One documented sequence: cold-start VM -> guest command -> build known
# source -> export logs. Read CURRENT_STATE.md section 6 for stand status.
# Exit codes: 0 ok; 2 guest execution not ready; 3 preflight; 4 smoke/build.
param(
    [string]$VmName = 'PC130-Evidence',
    [string]$GuestUser = 'evidence',
    [Security.SecureString]$GuestPassword,
    [string]$SourceDir = '',
    [string]$OutDir = 'D:\GLM_project\vm-evidence\recovery',
    [string]$HostToolsDir = 'D:\GLM_project\tools'
)

$ErrorActionPreference = 'Stop'
$VBox = 'C:\Program Files\Oracle\VirtualBox\VBoxManage.exe'
$stamp = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
$summary = Join-Path $OutDir ("SUMMARY-{0}.txt" -f $stamp)

function Fail([string]$Message, [int]$Code) {
    # Deterministic exit regardless of $ErrorActionPreference.
    [Console]::Error.WriteLine("run-vm-smoke: $Message")
    exit $Code
}

if (-not (Test-Path $VBox)) { Fail 'VBoxManage not found' 3 }
if (-not $GuestPassword) { Fail 'GuestPassword required (never stored)' 3 }
$BSTR = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($GuestPassword)
$Plain = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($BSTR)
[Runtime.InteropServices.Marshal]::ZeroFreeBSTR($BSTR)

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

function Run-Logged([string]$Name, [string[]]$VBoxArgs) {
    # Captures VBoxManage stdout+stderr and its exit code into OutDir.
    $file = Join-Path $OutDir ("{0}-{1}.log" -f $stamp, $Name)
    & $VBox @VBoxArgs *>&1 | Tee-Object -FilePath $file
    $script:VBoxExit = $LASTEXITCODE
    Add-Content -Path $summary ("{0}: vbox_exit={1} -> {2}" -f $Name, $LASTEXITCODE, $file)
}

function Guest-Args([string[]]$CmdArgs) {
    return @('--nologo', 'guestcontrol', $VmName, 'run',
        '--exe', 'C:\Windows\System32\cmd.exe',
        '--username', $GuestUser, '--password', $Plain,
        '--wait-stdout', '--wait-stderr', '--') + $CmdArgs
}

function GuestRun([string]$Name, [string[]]$CmdArgs) {
    Run-Logged $Name (Guest-Args $CmdArgs)
    if ($script:VBoxExit -ne 0) {
        if (Select-String -Path (Join-Path $OutDir ("{0}-{1}.log" -f $stamp, $Name)) `
                -Pattern 'not ready' -SimpleMatch:$false -Quiet) {
            Write-Warning 'Guest execution service not ready - known BLOCKED, see README.md'
            exit 2
        }
        exit 4
    }
}

# --- 1. Preflight / cold start -------------------------------------------------
if (-not (& $VBox list vms | Select-String ('"{0}"' -f $VmName))) {
    Fail "VM $VmName not registered" 3
}
$state = (& $VBox showvminfo $VmName --machinereadable |
    Select-String 'VMState="(.+)"').Matches[0].Groups[1].Value
if ($state -ne 'running') {
    Run-Logged 'coldstart' @('--nologo', 'startvm', $VmName, '--type', 'headless')
    $deadline = (Get-Date).AddMinutes(15); $lvl = 0
    while ($lvl -lt 3 -and (Get-Date) -lt $deadline) {
        Start-Sleep -Seconds 20
        $m = & $VBox showvminfo $VmName --machinereadable | Select-String 'GuestAdditionsRunLevel=(\d)'
        if ($m) { $lvl = [int]$m.Matches[0].Groups[1].Value }
    }
    if ($lvl -lt 3) { Fail 'Guest Additions did not reach runlevel 3' 3 }
}

# --- 2-4. Guest commands ---------------------------------------------------------
GuestRun 'tiny-smoke' @('cmd', '/c', 'echo REC00_SMOKE & ver & echo %DATE% %TIME%')
GuestRun 'utc-clock'  @('cmd', '/c', 'powershell -NoProfile -Command "Get-Date -AsUTC | Out-String"')
GuestRun 'diskfree'   @('cmd', '/c', 'fsutil volume diskfree C:')

# --- 5. File transfer round-trip (test-owned objects only) ---------------------
$probe = Join-Path $env:TEMP "rec00-probe-$stamp.txt"
"rec00 file transfer probe $stamp" | Set-Content -Path $probe
Run-Logged 'copyto' @('--nologo', 'guestcontrol', $VmName, 'copyto',
    '--username', $GuestUser, '--password', $Plain, '--target-directory',
    'C:\Users\evidence\AppData\Local\Temp', $probe)
if ($script:VBoxExit -ne 0) { exit 4 }
$back = Join-Path $env:TEMP "rec00-probe-back-$stamp.txt"
Run-Logged 'copyfrom' @('--nologo', 'guestcontrol', $VmName, 'copyfrom',
    '--username', $GuestUser, '--password', $Plain, '--target-directory', $env:Temp,
    "C:\Users\evidence\AppData\Local\Temp\$(Split-Path $probe -Leaf)")
if ($script:VBoxExit -ne 0) { exit 4 }
if ((Get-Content $probe) -ne (Get-Content $back)) { Fail 'round-trip mismatch' 4 }

# --- 6. Compile smoke: portable Go + known source --------------------------------
if ($SourceDir -and (Test-Path (Join-Path $SourceDir 'core\server\go.mod'))) {
    Run-Logged 'copy-source' @('--nologo', 'guestcontrol', $VmName, 'copyto',
        '--username', $GuestUser, '--password', $Plain, '--recursive',
        '--target-directory', 'C:\rec00\src', $SourceDir)
    if ($script:VBoxExit -ne 0) { exit 4 }
    Run-Logged 'copy-go' @('--nologo', 'guestcontrol', $VmName, 'copyto',
        '--username', $GuestUser, '--password', $Plain, '--recursive',
        '--target-directory', 'C:\rec00\tools', "$HostToolsDir\go")
    if ($script:VBoxExit -ne 0) { exit 4 }
    GuestRun 'compile-smoke' @('cmd', '/c',
        'set PATH=C:\rec00\tools\go\bin;%PATH%&& cd /d C:\rec00\src\core\server && go build ./... && echo BUILD_OK')
} else {
    Write-Warning 'SourceDir not provided or go.mod missing - compile smoke skipped'
}

Get-ChildItem $OutDir -Filter "*$stamp*.log" | Get-FileHash -Algorithm SHA256 |
    ForEach-Object { Add-Content -Path $summary ("{0}  {1}" -f $_.Hash, $_.Path) }
Write-Host "REC-00 smoke sequence finished, logs: $OutDir"
exit 0
