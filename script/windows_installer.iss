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
Filename: "{app}\Throne.exe"; Description: "{cm:LaunchProgram,ProxyCore}"; Flags: postinstall nowait skipifsilent

[Code]
var
  DeleteUserData: Boolean;

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

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep <> ssPostInstall then
    Exit;
  DeleteFile(ExpandConstant('{app}\uninstall.exe'));
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
begin
  App := ExpandConstant('{app}');
  if CurUninstallStep = usUninstall then
  begin
    StopThrone;
    DeleteUserData := SuppressibleMsgBox('Also delete your ProxyCore profiles, settings and logs?' + #13#10#13#10 +
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
    RemoveDir(App);
  end;
end;
