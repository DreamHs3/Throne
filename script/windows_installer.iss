#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef AppVersionMajor
  #define AppVersionMajor "0"
#endif
#ifndef AppVersionMinor
  #define AppVersionMinor "0"
#endif
#ifndef AppVersionPatch
  #define AppVersionPatch "0"
#endif
#ifndef AppVersionBuild
  #define AppVersionBuild "0"
#endif

[Setup]
; PC-010: a dedicated AppId is what lets ProxyCore and Throne be installed at
; the same time — sharing the GUID would make this installer treat a Throne
; installation as its own previous version.
AppId={{4E7D2A19-8C3B-4F6E-9A51-D2B08C7F4E63}
AppName=ProxyCore
AppVersion={#AppVersion}
AppVerName=ProxyCore {#AppVersion}
AppPublisher=ProxyCore
VersionInfoVersion={#AppVersionMajor}.{#AppVersionMinor}.{#AppVersionPatch}.{#AppVersionBuild}
VersionInfoProductName=ProxyCore
VersionInfoDescription=ProxyCore Setup
VersionInfoCopyright=ProxyCore
SourceDir=..
OutputDir=deployment
OutputBaseFilename=ProxyCoreSetup
SetupIconFile=res\Throne.ico
UninstallDisplayName=ProxyCore
UninstallDisplayIcon={app}\Throne.exe
WizardStyle=modern
PrivilegesRequired=lowest
; REC-01: commandline override added so silent installs can select admin mode
; (/ALLUSERS). With only "dialog", a /VERYSILENT run always used the lowest
; default and silently skipped the service half even from an elevated prompt
; (observed on the stand 2026-09-16). Interactive behavior is unchanged: the
; dialog still offers the choice.
PrivilegesRequiredOverridesAllowed=dialog commandline
DefaultDirName={code:DefaultInstallDir}
DirExistsWarning=no
DisableProgramGroupPage=yes
ArchitecturesInstallIn64BitMode=win64
CloseApplications=force
RestartApplications=no
Compression=lzma2/ultra64
SolidCompression=yes
LZMAUseSeparateProcess=yes
LZMANumBlockThreads=4
; The default block is 4x the dictionary (256 MB), which would leave two of the four threads idle.
LZMABlockSize=118784

[Messages]
SelectDirBrowseLabel=To continue, click Next. If the folder you choose is not named ProxyCore, Setup creates a ProxyCore folder inside it, so uninstalling only ever removes ProxyCore's own folder.

[Files]
Source: "deployment\windows-amd64\*"; DestDir: "{app}"; Excludes: "*.pdb"; Flags: ignoreversion; Check: IsX64OS; MinVersion: 10.0.17763
Source: "deployment\windowslegacy-amd64\*"; DestDir: "{app}"; Excludes: "*.pdb"; Flags: ignoreversion; Check: IsX64OS; OnlyBelowVersion: 10.0.17763
Source: "deployment\windows-arm64\*"; DestDir: "{app}"; Excludes: "*.pdb"; Flags: ignoreversion; Check: IsArm64
Source: "deployment\windowslegacy-386\*"; DestDir: "{app}"; Excludes: "*.pdb"; Flags: ignoreversion; Check: IsX86OS

[Icons]
Name: "{autoprograms}\ProxyCore"; Filename: "{app}\Throne.exe"
Name: "{autodesktop}\ProxyCore"; Filename: "{app}\Throne.exe"

[Registry]
Root: HKA; Subkey: "Software\ProxyCore"; ValueType: string; ValueName: "InstallPath"; ValueData: "{app}"; Flags: uninsdeletekey

[Run]
; PC-120: the service is demand-started by the UI, never by Setup. The
; Environment grant written by SetupServiceEnv lives under the service's
; registry key, so it must run after sc.exe created the service — hence
; AfterInstall on the entry below, not CurStepChanged/ssPostInstall:
; non-postinstall [Run] entries are processed BEFORE ssPostInstall fires,
; so a literal ssPostInstall placement would run the icacls entry below
; BEFORE the data folder exists (silent no-op, exit code ignored) and leave
; it unhardened. AfterInstall chains create -> env+mkdir -> icacls
; deterministically. (Call-site deviation from handoff step 1.2, see ADR.)
; REC-01 (R4): ImagePath now carries dedicated quotes around the executable
; and the whole binPath value is one quoted group, so paths with spaces
; resolve to our exe and a planted "C:\Program.exe" can never win SCM's
; unquoted-path resolution. Built in Pascal (ScCreateParams) because the .iss
; string syntax has no backslash escape for quotes. The foreign-service guard
; runs in PrepareToInstall, before anything is modified.
Filename: "{sys}\sc.exe"; Parameters: "{code:ScCreateParams}"; Flags: runhidden; Check: IsAdminInstallMode; AfterInstall: SetupServiceEnv
; PC-120: the service data folder must be SYSTEM+Administrators only, so
; inheritance from ProgramData is removed.
; REC-01 (R4): AfterInstall VerifyDataDirAcl reads the DACL back and fails
; closed unless it is exactly inheritance-disabled SYSTEM+Administrators.
Filename: "{sys}\icacls.exe"; Parameters: """{commonappdata}\ProxyCore"" /inheritance:r /grant:r SYSTEM:(OI)(CI)F /grant:r *S-1-5-32-544:(OI)(CI)F"; Flags: runhidden; Check: IsAdminInstallMode; AfterInstall: VerifyDataDirAcl
Filename: "{app}\Throne.exe"; Description: "{cm:LaunchProgram,ProxyCore}"; Flags: postinstall nowait skipifsilent

[Code]
var
  DeleteUserData: Boolean;
  // PC-120: standalone notice label on the Finish page (non-admin installs).
  // Appending to FinishedLabel does NOT render: the label rect in WizardStyle
  // modern clips overflowing lines. A dedicated word-wrapped label is used
  // instead; the RunList checklist is full-height (209px at 768p, VM-proven)
  // with a single item, so it is shrunk to one row first — otherwise anything
  // anchored below it lands off-page (the exact "caption set but invisible"
  // symptom of both previous attempts).
  ServiceNoticeLabel: TNewStaticText;

// ---------------------------------------------------------------------------
// REC-01 (R4) hardening helpers. Every privileged mutation is followed by a
// read-back postcondition; any mismatch rolls the service back and aborts
// Setup (fail closed), so a failed run never leaves a half-installed service.
// ---------------------------------------------------------------------------
const
  ServiceName = 'ProxyCoreService';
  INVALID_FILE_ATTRIBUTES = $FFFFFFFF;

function GetFileAttributes(lpFileName: String): Cardinal;
external 'GetFileAttributesW@kernel32.dll stdcall';

function IsReparsePoint(const Dir: String): Boolean;
var
  Attr: Cardinal;
begin
  // FILE_ATTRIBUTE_REPARSE_POINT / INVALID_FILE_ATTRIBUTES are Inno built-ins.
  Attr := GetFileAttributes(Dir);
  Result := (Attr <> INVALID_FILE_ATTRIBUTES) and
    ((Attr and FILE_ATTRIBUTE_REPARSE_POINT) <> 0);
end;

// REC-01C: checks <Dir> and every EXISTING ancestor component. A missing leaf
// cannot be a reparse point, but a junction anywhere further up the chain
// redirects the payload just the same, so the walk must not stop at the
// components that do not exist yet.
function ReparsePathViolation(const Dir: String): String;
var
  P: String;
begin
  Result := '';
  P := RemoveBackslashUnlessRoot(Dir);
  while Length(P) > 3 do
  begin
    if DirExists(P) and IsReparsePoint(P) then
    begin
      Result := P + ' is a reparse point (junction or symlink)';
      Exit;
    end;
    while (Length(P) > 0) and (P[Length(P)] <> '\') do
      P := Copy(P, 1, Length(P) - 1);
    P := RemoveBackslashUnlessRoot(P);
  end;
end;

// REC-01C: called from PrepareToInstall, i.e. BEFORE the [Files] section
// writes the first payload byte. (R2 T4: the old check lived at the top of
// SetupServiceEnv, which runs after [Files]; the payload was already copied
// through an {app} junction into the target by the time Setup refused.)
function InstallTargetViolations: String;
begin
  Result := ReparsePathViolation(ExpandConstant('{app}'));
  if (Result = '') and IsAdminInstallMode then
    Result := ReparsePathViolation(ExpandConstant('{commonappdata}\ProxyCore'));
end;

procedure RollbackService;
var
  Code: Integer;
begin
  // Best effort: 1060 (not installed) is fine here.
  Exec(ExpandConstant('{sys}\sc.exe'), 'delete ' + ServiceName, '',
    SW_HIDE, ewWaitUntilTerminated, Code);
  Log('RollbackService: sc delete exit ' + IntToStr(Code));
end;

// R4: sc arguments for create. binPath= takes ONE quoted group whose inner
// \" quotes mark the executable, so the registry ImagePath becomes
// "<app>\ThroneCore.exe" service — correctly quoted for SCM paths with
// spaces and immune to unquoted-path binary planting.
function ScCreateParams(Param: String): String;
begin
  Result := 'create ' + ServiceName + ' binPath= "\"' +
    ExpandConstant('{app}\ThroneCore.exe') + '\" service" start= demand';
end;

procedure FailStep(const What, Detail: String);
begin
  Log(What + ' FAILED: ' + Detail);
  SuppressibleMsgBox(What + ' failed: ' + Detail + #13#10#13#10 +
    'Setup rolls back the service it created and aborts.',
    mbError, MB_OK, IDOK);
  RollbackService;
  RaiseException(What + ' failed: ' + Detail);
end;

// Reads the ImagePath of an existing <ServiceName> service. Results:
// 0 = ImagePath read, 1 = service absent, 2 = query failed.
function TryReadServiceImagePath(var ImagePath: String): Integer;
var
  TmpFile, PsParams: String;
  Raw: AnsiString;
  Code: Integer;
begin
  TmpFile := ExpandConstant('{tmp}\svcimagepath.txt');
  DeleteFile(TmpFile);
  PsParams := '-NoProfile -Command "$ErrorActionPreference = ''Stop''; ' +
    'try { (Get-ItemProperty -Path ''HKLM:\SYSTEM\CurrentControlSet\Services\' +
    ServiceName + ''' -Name ImagePath).ImagePath | Out-File -FilePath ''' +
    TmpFile + ''' -Encoding ascii; exit 0 } catch { exit 1 }"';
  if not Exec('powershell.exe', PsParams, '', SW_HIDE, ewWaitUntilTerminated, Code) then
  begin
    Result := 2;
    Exit;
  end;
  if Code <> 0 then
  begin
    Result := 1; // service key absent
    Exit;
  end;
  if not LoadStringFromFile(TmpFile, Raw) then
  begin
    Result := 2;
    Exit;
  end;
  ImagePath := Raw;
  // Out-File writes a trailing CRLF; strip trailing whitespace.
  while (Length(ImagePath) > 0) and
      ((ImagePath[Length(ImagePath)] = #13) or
       (ImagePath[Length(ImagePath)] = #10) or
       (ImagePath[Length(ImagePath)] = ' ')) do
    ImagePath := Copy(ImagePath, 1, Length(ImagePath) - 1);
  Result := 0;
end;

// R4: a same-named service that is not ours is never modified. Checked in
// PrepareToInstall (below) so a foreign collision aborts Setup before ANY
// change - files, service, Environment, ACL - are made. (A RaiseException in
// a BeforeInstall function does NOT abort Setup: on the stand the [Run] entry
// still executed and overwrote the foreign service's Environment.)
function ForeignServiceCollision: String;
var
  ImagePath, OurPath: String;
  Res: Integer;
begin
  Result := '';
  Res := TryReadServiceImagePath(ImagePath);
  if Res = 2 then
    Result := 'could not query the state of the ' + ServiceName + ' service';
  if Res = 0 then
  begin
    OurPath := '"' + ExpandConstant('{app}\ThroneCore.exe') + '" service';
    if CompareText(Trim(ImagePath), OurPath) <> 0 then
      Result := 'a service named "' + ServiceName + '" already exists but points to: ' +
        Trim(ImagePath);
  end;
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  Result := '';
  // REC-01C: reparse refusal for install/data paths runs before ANY change -
  // files, service, Environment, ACL - in both per-user and admin mode.
  Result := InstallTargetViolations;
  if (Result = '') and IsAdminInstallMode then
    Result := ForeignServiceCollision;
  if Result <> '' then
    Log('PrepareToInstall: refusing to continue - ' + Result);
end;

// No legacy Throne lookup here: ProxyCore must never install into (or
// upgrade over) a Throne installation.
function DefaultInstallDir(Param: String): String;
begin
  if IsAdminInstallMode then
    Result := ExpandConstant('{autopf}\ProxyCore')
  else
    Result := ExpandConstant('{localappdata}\ProxyCore');
end;

function NextButtonClick(CurPageID: Integer): Boolean;
var
  Dir, Probe: String;
  Created: Boolean;
begin
  Result := True;
  if CurPageID <> wpSelectDir then
    Exit;
  Dir := RemoveBackslashUnlessRoot(WizardDirValue);
  // Uninstalling can delete <dir>\config, so ProxyCore must get a folder of its own.
  if CompareText(ExtractFileName(Dir), 'ProxyCore') <> 0 then
  begin
    Dir := AddBackslash(Dir) + 'ProxyCore';
    WizardForm.DirEdit.Text := Dir;
  end;
  if IsAdminInstallMode then
    Exit;
  Created := not DirExists(Dir);
  Probe := AddBackslash(Dir) + '.throne-write-test';
  Result := ForceDirectories(Dir) and SaveStringToFile(Probe, '', False);
  DeleteFile(Probe);
  if Created then
    RemoveDir(Dir);
  if not Result then
    SuppressibleMsgBox('You do not have permission to install to "' + Dir + '".' + #13#10#13#10 +
      'Choose a different folder, or restart Setup and choose to install for all users.', mbError, MB_OK, IDOK);
end;

// PC-120: fills in the per-user service grant (THRONE_SERVICE_*), then
// creates the service data folder BEFORE the icacls [Run] entry below runs
// (icacls needs an existing folder). Runs only in admin install mode (via
// the sc.exe entry's AfterInstall — a skipped entry never fires it) and
// strictly after the service exists — HKLM\SYSTEM\...\Services\ProxyCoreService
// cannot be written before sc create. Any failure aborts Setup: a silent
// skip would leave the service without a grant, which is worse than no
// service at all.
procedure SetupServiceEnv;
var
  User, TmpFile, Sid, DataDir, PsParams: String;
  SidRaw: AnsiString;
  ResultCode: Integer;
begin
  DataDir := ExpandConstant('{commonappdata}\ProxyCore');
  User := GetUserNameString;
  TmpFile := ExpandConstant('{tmp}\ownersid.txt');
  // Windows PowerShell 5.1 exits 0 even after non-terminating errors, so
  // ErrorActionPreference=Stop is what makes the ResultCode <> 0 checks
  // below actually fire on failure.
  PsParams := '-NoProfile -Command "$ErrorActionPreference = ''Stop''; (Get-LocalUser -Name ''' + User +
    ''').Sid.Value | Out-File -FilePath ''' + TmpFile + ''' -Encoding ascii"';
  if not Exec('powershell.exe', PsParams, '', SW_HIDE, ewWaitUntilTerminated, ResultCode) or (ResultCode <> 0) then
    FailStep('SetupServiceEnv', 'could not determine the SID of the installing user (PowerShell exit code ' + IntToStr(ResultCode) + ')');
  if not LoadStringFromFile(TmpFile, SidRaw) then
    FailStep('SetupServiceEnv', 'could not read the installing user''s SID from the temporary file');
  Sid := Trim(SidRaw);
  if Sid = '' then
    FailStep('SetupServiceEnv', 'could not determine the SID of the installing user "' + User + '"');
  // Same command shape as the VM evidence round 2: REG_MULTI_SZ under the
  // service's Environment key, applied by stopping/starting the service —
  // here the service simply starts on demand later.
  PsParams := '-NoProfile -Command "$ErrorActionPreference = ''Stop''; Set-ItemProperty -Path ''HKLM:\SYSTEM\CurrentControlSet\Services\' + ServiceName + ''' -Name Environment -Type MultiString -Value @(''THRONE_SERVICE_SDDL=D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;' + Sid + ')'',''THRONE_SERVICE_ALLOWED_SIDS=' + Sid + ''',''THRONE_SERVICE_DATA_DIR=' + DataDir + ''')"';
  if not Exec('powershell.exe', PsParams, '', SW_HIDE, ewWaitUntilTerminated, ResultCode) or (ResultCode <> 0) then
    FailStep('SetupServiceEnv', 'could not write the ProxyCoreService environment (PowerShell exit code ' + IntToStr(ResultCode) + ')');
  // R4: read the Environment back and compare every value byte-for-byte.
  PsParams := '-NoProfile -Command "$ErrorActionPreference = ''Stop''; $v = (Get-ItemProperty -Path ''HKLM:\SYSTEM\CurrentControlSet\Services\' + ServiceName + ''').Environment; if (($v.Count -eq 3) -and ($v[0] -eq ''THRONE_SERVICE_SDDL=D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;' + Sid + ')'') -and ($v[1] -eq ''THRONE_SERVICE_ALLOWED_SIDS=' + Sid + ''') -and ($v[2] -eq ''THRONE_SERVICE_DATA_DIR=' + DataDir + ''')) { exit 0 } else { exit 1 }"';
  if not Exec('powershell.exe', PsParams, '', SW_HIDE, ewWaitUntilTerminated, ResultCode) or (ResultCode <> 0) then
    FailStep('SetupServiceEnv', 'service Environment read-back does not match the expected values (PowerShell exit code ' + IntToStr(ResultCode) + ')');
  if not ForceDirectories(DataDir) then
    FailStep('SetupServiceEnv', 'could not create the service data folder "' + DataDir + '"');
  Log('SetupServiceEnv: service Environment verified against expected values');
end;

// R4: read the data folder DACL back and fail closed unless it is exactly
// inheritance-disabled SYSTEM+Administrators (the icacls [Run] entry above).
procedure VerifyDataDirAcl;
var
  PsParams: String;
  ResultCode: Integer;
begin
  PsParams := '-NoProfile -Command "$ErrorActionPreference = ''Stop''; $s = (Get-Acl -Path ''' +
    ExpandConstant('{commonappdata}\ProxyCore') +
    ''').Sddl; if ($s -match ''D:P[A-Z]*\(A;OICI;FA;;;SY\)\(A;OICI;FA;;;BA\)$'') { exit 0 } else { Write-Output $s; exit 1 }"';
  if not Exec('powershell.exe', PsParams, '', SW_HIDE, ewWaitUntilTerminated, ResultCode) or (ResultCode <> 0) then
    FailStep('VerifyDataDirAcl', 'service data folder DACL read-back is not SYSTEM+Administrators-only (PowerShell exit code ' + IntToStr(ResultCode) + ')');
  Log('VerifyDataDirAcl: DACL verified as inheritance-disabled SYSTEM+Administrators');
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep <> ssPostInstall then
    Exit;
  DeleteFile(ExpandConstant('{app}\uninstall.exe'));
end;

// PC-120: non-admin installs run without the service; say so on the Finish page.
procedure InitializeWizard();
begin
  ServiceNoticeLabel := TNewStaticText.Create(WizardForm);
  ServiceNoticeLabel.Parent := WizardForm.FinishedPage;
  ServiceNoticeLabel.Left := WizardForm.FinishedLabel.Left;
  ServiceNoticeLabel.Width := WizardForm.FinishedLabel.Width;
  ServiceNoticeLabel.WordWrap := True;
  ServiceNoticeLabel.Height := ScaleY(40);
  ServiceNoticeLabel.Caption := '';
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  if (CurPageID = wpFinished) and (not IsAdminInstallMode) then
  begin
    // RunList spans the whole client area (H=209 at 768p) for a single item:
    // shrink it to one row, the notice takes the freed space (Top ~= 206,
    // bottom ~= 246 — client bottom is ~= 365, VM-verified geometry).
    WizardForm.RunList.Height := ScaleY(30);
    ServiceNoticeLabel.Top := WizardForm.RunList.Top + WizardForm.RunList.Height + ScaleY(8);
    // ASCII hyphen on purpose: the .iss is UTF-8 without BOM, so a U+2014
    // em-dash would depend on the guest codepage; the message must render
    // on any locale.
    ServiceNoticeLabel.Caption := 'Service not installed - reinstall as administrator for service mode.';
  end
  else
    ServiceNoticeLabel.Caption := '';
end;

procedure StopThrone;
var
  Locator, Service, Processes, Process: Variant;
  Prefix, ExePath: String;
  I: Integer;
  Stopped: Boolean;
begin
  Prefix := Lowercase(AddBackslash(ExpandConstant('{app}')));
  Stopped := False;
  try
    Locator := CreateOleObject('WbemScripting.SWbemLocator');
    Service := Locator.ConnectServer('.', 'root\CIMV2');
    Processes := Service.ExecQuery('SELECT * FROM Win32_Process WHERE Name = ''Throne.exe'' OR Name = ''ThroneCore.exe''');
    for I := 0 to Processes.Count - 1 do
    begin
      Process := Processes.ItemIndex(I);
      if not VarIsNull(Process.ExecutablePath) then
      begin
        // Pascal Script converts a Variant to String on assignment, but not when passed as a String parameter.
        ExePath := Process.ExecutablePath;
        // Only our own folder's processes are touched; a running Throne from
        // its own directory is never killed.
        if Pos(Prefix, Lowercase(ExePath)) = 1 then
        begin
          Process.Terminate(0);
          Stopped := True;
        end;
      end;
    end;
  except
    Log('Could not stop ProxyCore: ' + GetExceptionMessage);
  end;
  if Stopped then
    Sleep(1000);
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  App, ImagePath, OurPath: String;
  Res, ResultCode: Integer;
begin
  App := ExpandConstant('{app}');
  if CurUninstallStep = usUninstall then
  begin
    // REC-01 (R4): stop and delete the service only when it is ours. A
    // same-named foreign service is logged and left untouched; when no
    // service exists the steps are skipped. The service's Environment key
    // is removed by Windows together with the service itself, it is never
    // touched separately.
    Res := TryReadServiceImagePath(ImagePath);
    if Res = 0 then
    begin
      OurPath := '"' + App + '\ThroneCore.exe" service';
      if CompareText(Trim(ImagePath), OurPath) = 0 then
      begin
        Exec(ExpandConstant('{sys}\sc.exe'), 'stop ' + ServiceName, '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
        Exec(ExpandConstant('{sys}\sc.exe'), 'delete ' + ServiceName, '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
      end
      else
        Log('Uninstall: service ' + ServiceName + ' belongs to "' + Trim(ImagePath) + '", left untouched');
    end
    else
      Log('Uninstall: no ' + ServiceName + ' service present');
    StopThrone;
    DeleteUserData := SuppressibleMsgBox('Also delete your ProxyCore profiles, settings and logs, including the service data folder?' + #13#10#13#10 +
      'Choose No if you plan to reinstall ProxyCore later and want to keep them.', mbConfirmation, MB_YESNO, IDYES) = IDYES;
  end
  else if (CurUninstallStep = usPostUninstall) and DeleteUserData then
  begin
    if FileExists(App + '\config\throne.db') then
      DelTree(App + '\config', True, True, True);
    // Where ProxyCore keeps its config when its own folder is not writable
    // (Qt's AppConfigLocation). A Throne installation's data under
    // <AppData>\Throne is never touched.
    DelTree(ExpandConstant('{localappdata}\ProxyCore\config'), True, True, True);
    RemoveDir(ExpandConstant('{localappdata}\ProxyCore'));
    // PC-120: the service data folder, if the user opted to remove it.
    DelTree(ExpandConstant('{commonappdata}\ProxyCore'), True, True, True);
    RemoveDir(App);
  end;
end;

