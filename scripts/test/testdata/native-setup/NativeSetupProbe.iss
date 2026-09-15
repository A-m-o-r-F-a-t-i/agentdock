; Isolated Inno parent for the native launch regression. No product install,
; registry entry, shortcuts, service, or persistent installation is created.
#ifndef OutputDirectory
  #define OutputDirectory "."
#endif

[Setup]
AppId=AgentDock.NativeSetupRegression
AppName=AgentDock Native Setup Regression
AppVersion=1.0.1
DefaultDirName={tmp}\AgentDockNativeProbe
CreateAppDir=no
Uninstallable=no
CreateUninstallRegKey=no
PrivilegesRequired=lowest
UsePreviousAppDir=no
DisableDirPage=yes
DisableProgramGroupPage=yes
DisableWelcomePage=yes
DisableReadyPage=yes
DisableFinishedPage=yes
SetupLogging=yes
OutputDir={#OutputDirectory}
OutputBaseFilename=AgentDockNativeProbe
Compression=lzma2
SolidCompression=yes

[Code]
var
  NativeExitCode: Integer;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  NativeLauncher, RequestFile: String;
begin
  Result := '';
  NativeLauncher := ExpandConstant('{param:LAUNCHER}');
  RequestFile := ExpandConstant('{param:REQUEST}');
  if not FileExists(NativeLauncher) or not FileExists(RequestFile) then begin
    Result := 'Native regression launcher/request is missing';
    exit;
  end;
  if not Exec(NativeLauncher, '--setup-exec "' + RequestFile + '"', '', SW_HIDE,
      ewWaitUntilTerminated, NativeExitCode) then begin
    Result := 'Native regression launcher failed: ' + IntToStr(NativeExitCode);
    exit;
  end;
  Log('Native execution completed with exit ' + IntToStr(NativeExitCode));
end;

function GetCustomSetupExitCode: Integer;
begin
  Result := NativeExitCode;
end;
