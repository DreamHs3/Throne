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
PrivilegesRequiredOverridesAllowed=dialog
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
Filename: "{sys}\sc.exe"; Parameters: "create ProxyCoreService binPath= ""{app}\ThroneCore.exe service"" start= demand"; Flags: runhidden; Check: IsAdminInstallMode; AfterInstall: SetupServiceEnv
; PC-120: the service data folder must be SYSTEM+Administrators only, so
; inheritance from ProgramData is removed.
Filename: "{sys}\icacls.exe"; Parameters: """{commonappdata}\ProxyCore"" /inheritance:r /grant:r SYSTEM:(OI)(CI)F /grant:r *S-1-5-32-544:(OI)(CI)F"; Flags: runhidden; Check: IsAdminInstallMode
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
  User := GetUserNameString;
  TmpFile := ExpandConstant('{tmp}\ownersid.txt');
  // Windows PowerShell 5.1 exits 0 even after non-terminating errors, so
  // ErrorActionPreference=Stop is what makes the ResultCode <> 0 checks
  // below actually fire on failure.
  PsParams := '-NoProfile -Command "$ErrorActionPreference = ''Stop''; (Get-LocalUser -Name ''' + User +
    ''').Sid.Value | Out-File -FilePath ''' + TmpFile + ''' -Encoding ascii"';
  if not Exec('powershell.exe', PsParams, '', SW_HIDE, ewWaitUntilTerminated, ResultCode) or (ResultCode <> 0) then
  begin
    SuppressibleMsgBox('Could not determine the SID of the installing user (PowerShell exit code ' + IntToStr(ResultCode) + ').', mbError, MB_OK, IDOK);
    RaiseException('SetupServiceEnv: SID lookup failed');
  end;
  if not LoadStringFromFile(TmpFile, SidRaw) then
  begin
    SuppressibleMsgBox('Could not read the installing user''s SID from the temporary file.', mbError, MB_OK, IDOK);
    RaiseException('SetupServiceEnv: SID file read failed');
  end;
  Sid := Trim(SidRaw);
  if Sid = '' then
  begin
    SuppressibleMsgBox('Could not determine the SID of the installing user "' + User + '".', mbError, MB_OK, IDOK);
    RaiseException('SetupServiceEnv: empty SID');
  end;
  DataDir := ExpandConstant('{commonappdata}\ProxyCore');
  // Same command shape as the VM evidence round 2: REG_MULTI_SZ under the
  // service's Environment key, applied by stopping/starting the service —
  // here the service simply starts on demand later.
  PsParams := '-NoProfile -Command "$ErrorActionPreference = ''Stop''; Set-ItemProperty -Path ''HKLM:\SYSTEM\CurrentControlSet\Services\ProxyCoreService'' -Name Environment -Type MultiString -Value @(''THRONE_SERVICE_SDDL=D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;' + Sid + ')'',''THRONE_SERVICE_ALLOWED_SIDS=' + Sid + ''',''THRONE_SERVICE_DATA_DIR=' + DataDir + ''')"';
  if not Exec('powershell.exe', PsParams, '', SW_HIDE, ewWaitUntilTerminated, ResultCode) or (ResultCode <> 0) then
  begin
    SuppressibleMsgBox('Could not write the ProxyCoreService environment (PowerShell exit code ' + IntToStr(ResultCode) + ').', mbError, MB_OK, IDOK);
    RaiseException('SetupServiceEnv: Environment write failed');
  end;
  if not ForceDirectories(DataDir) then
  begin
    SuppressibleMsgBox('Could not create the service data folder "' + DataDir + '".', mbError, MB_OK, IDOK);
    RaiseException('SetupServiceEnv: data folder creation failed');
  end;
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
  App: String;
  ResultCode: Integer;
begin
  App := ExpandConstant('{app}');
  if CurUninstallStep = usUninstall then
  begin
    // PC-120: stop and delete the service before killing leftover processes.
    // Results are ignored — the service may not exist (non-admin install).
    // The service's Environment key is removed by Windows together with the
    // service itself, it is never touched separately.
    Exec(ExpandConstant('{sys}\sc.exe'), 'stop ProxyCoreService', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
    Exec(ExpandConstant('{sys}\sc.exe'), 'delete ProxyCoreService', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
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

