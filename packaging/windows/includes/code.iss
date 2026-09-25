[Code]
var
  UpgradeModePage: TInputOptionWizardPage;
  StartupPage: TInputOptionWizardPage;
  DesktopShortcutCheckBox: TNewCheckBox;
  PurgeState: Boolean;
  UninstallCleanupExecuted: Boolean;
  ResultFilePath: String;
  ExistingInstallDetected: Boolean;
  ExistingInstallVersion: String;
  ExistingInstallSource: String;
  ResolvedInstallRoot: String;
  RequestedTunnelMode: String;
  RequestedServerURL: String;
  InstallProgressPage: TOutputProgressWizardPage;
  InstallWarningCode: String;
  InstallWarningMessage: String;

#include "native-launch.iss"

function GetLocalizedMessage(Key: String): String;
begin
  Result := CustomMessage(Key);
end;

function LocalizedInstallFailure(ErrorCode, OriginalMessage: String; ExitCode: Integer): String;
begin
  Result := GetLocalizedMessage('InstallerFailureGuidance');
  if ErrorCode = 'setup-elevated-context' then
    Result := GetLocalizedMessage('ElevatedSetupUnsupported')
  else if ErrorCode = 'tunnel-token-required' then
    Result := GetLocalizedMessage('TokenRecoveryRequired')
  else if ErrorCode = 'credential-user-mismatch' then
    Result := GetLocalizedMessage('CredentialUserMismatch')
  else if ErrorCode = 'install-validation-failed' then
    Result := GetLocalizedMessage('InstallerValidationFailed')
  else if ErrorCode = 'task-scheduler-unavailable' then
    Result := GetLocalizedMessage('TaskSchedulerUnavailable')
  else if ErrorCode = 'stale-rollback-recovery-unsafe' then
    Result := GetLocalizedMessage('StaleRollbackRecoveryUnsafe')
  else if ErrorCode = 'stale-rollback-recovery-failed' then
    Result := GetLocalizedMessage('StaleRollbackRecoveryFailed')
  else if ErrorCode = 'elevated-task-rollback-failed' then
    Result := GetLocalizedMessage('ElevatedTaskRollbackFailed')
  else if ErrorCode = 'elevated-unavailable' then
    Result := GetLocalizedMessage('ElevatedUnavailable')
  else if ErrorCode = 'runtime-activation-failed' then
    Result := GetLocalizedMessage('RuntimeActivationFailed')
  else if ErrorCode = 'stale-rollback-recovery-required' then
    Result := GetLocalizedMessage('AdapterRecoveryRequired')
  else if ErrorCode = 'rollback-failed' then
    Result := GetLocalizedMessage('InstallerRollbackFailed');
  Result := Result + #13#10 + GetLocalizedMessage('InstallerExitCode') + ' ' + IntToStr(ExitCode);
  if ErrorCode <> '' then
    Result := Result + ' (' + ErrorCode + ')';
  if OriginalMessage <> '' then
    Result := Result + #13#10#13#10 + GetLocalizedMessage('OriginalInstallerDiagnostic') + #13#10 + OriginalMessage;
end;

function ResolveInstallRoot(): String;
var
  UninstallKey: String;
  InstallLocation: String;
begin
  Result := Trim(ExpandConstant('{param:DIR|}'));
  if Result <> '' then
    Exit;

  UninstallKey := 'Software\Microsoft\Windows\CurrentVersion\Uninstall\{#AppIdValue}_is1';
  if RegQueryStringValue(HKCU, UninstallKey, 'InstallLocation', InstallLocation) and
    (Trim(InstallLocation) <> '') then
  begin
    Result := RemoveBackslashUnlessRoot(Trim(InstallLocation));
    Exit;
  end;

  Result := ExpandConstant('{localappdata}\AgentDock');
end;

function ExistingInstallRoot(): String;
begin
  Result := ResolvedInstallRoot;
end;

function ValidateSelectedInstallDirectory(): String;
var
  RegisteredRoot: String;
  SelectedRoot: String;
  UninstallKey: String;
begin
  Result := '';
  UninstallKey := 'Software\Microsoft\Windows\CurrentVersion\Uninstall\{#AppIdValue}_is1';
  if not RegQueryStringValue(HKCU, UninstallKey, 'InstallLocation', RegisteredRoot) or
    (Trim(RegisteredRoot) = '') then
    RegisteredRoot := ExpandConstant('{localappdata}\AgentDock');
  RegisteredRoot := RemoveBackslashUnlessRoot(ExpandFileName(Trim(RegisteredRoot)));
  SelectedRoot := RemoveBackslashUnlessRoot(ExpandFileName(WizardDirValue()));
  { One current-user installation owns the startup entries and listening port. }
  if (CompareText(RegisteredRoot, SelectedRoot) <> 0) and
    (FileExists(AddBackslash(RegisteredRoot) + 'runtime.json') or
     FileExists(AddBackslash(RegisteredRoot) + 'bin\agentdock.exe')) then
    Result := GetLocalizedMessage('InstallDirectoryInUse') + #13#10#13#10 + RegisteredRoot;
end;

function DetectExistingInstallation(): Boolean;
var
  UninstallKey: String;
  BinaryPath: String;
  VersionValue: String;
  InstallLocation: String;
begin
  ExistingInstallVersion := '';
  ExistingInstallSource := '';
  UninstallKey := 'Software\Microsoft\Windows\CurrentVersion\Uninstall\{#AppIdValue}_is1';

  BinaryPath := AddBackslash(ExistingInstallRoot()) + 'bin\agentdock.exe';
  if not FileExists(BinaryPath) and
    not FileExists(AddBackslash(ExistingInstallRoot()) + 'runtime.json') and
    not FileExists(AddBackslash(ExistingInstallRoot()) + 'start-agentdock.ps1') then
  begin
    Result := False;
    Exit;
  end;

  if RegQueryStringValue(HKCU, UninstallKey, 'InstallLocation', InstallLocation) and
    (CompareText(RemoveBackslashUnlessRoot(ExpandFileName(InstallLocation)),
      RemoveBackslashUnlessRoot(ExpandFileName(ExistingInstallRoot()))) = 0) and
    RegQueryStringValue(HKCU, UninstallKey, 'DisplayVersion', VersionValue) then
  begin
    ExistingInstallVersion := Trim(VersionValue);
    ExistingInstallSource := 'setup';
    Result := True;
    Exit;
  end;

  BinaryPath := AddBackslash(ExistingInstallRoot()) + 'bin\agentdock.exe';
  if FileExists(BinaryPath) or
    FileExists(AddBackslash(ExistingInstallRoot()) + 'runtime.json') or
    FileExists(AddBackslash(ExistingInstallRoot()) + 'start-agentdock.ps1') then
  begin
    if GetVersionNumbersString(BinaryPath, VersionValue) then
      ExistingInstallVersion := Trim(VersionValue);
    ExistingInstallSource := 'powershell';
    Result := True;
    Exit;
  end;

  Result := False;
end;

function LegacyAgentDockScheduledTaskExists(): Boolean;
var
  ExitCode: Integer;
  SchTasksPath: String;
begin
  SchTasksPath := ExpandConstant('{win}\System32\schtasks.exe');
  if not FileExists(SchTasksPath) then
  begin
    Log('Windows schtasks.exe is unavailable; skipping legacy AgentDock task detection.');
    Result := False;
    Exit;
  end;

  Result :=
    NativeSetupCommand(
      SchTasksPath,
      '/Query /TN "\AgentDock"',
      30,
      True,
      ExitCode) and
    (ExitCode = 0);
  if Result then
    Log('AgentDock legacy scheduled task detected.');
end;

function RuntimeUsesElevatedCore(): Boolean;
var
  Content: AnsiString;
  Normalized: String;
  ManifestPath: String;
begin
  Result := False;
  ManifestPath := AddBackslash(ExistingInstallRoot()) + 'runtime.json';
  if not FileExists(ManifestPath) then
    Exit;
  if not LoadStringFromFile(ManifestPath, Content) then
    Exit;
  Normalized := Lowercase(String(Content));
  StringChangeEx(Normalized, ' ', '', True);
  StringChangeEx(Normalized, #13, '', True);
  StringChangeEx(Normalized, #10, '', True);
  Result := Pos('"privilege_mode":"elevated"', Normalized) > 0;
end;

procedure LoadExistingSettings();
var
  RunKey: String;
begin
  if not ExistingInstallDetected then
    Exit;

  RunKey := 'Software\Microsoft\Windows\CurrentVersion\Run';
  StartupPage.Values[0] :=
    RegValueExists(HKCU, RunKey, 'AgentDock') or
    RegValueExists(HKCU, RunKey, 'AgentDockTray') or
    LegacyAgentDockScheduledTaskExists();
  StartupPage.Values[1] := RuntimeUsesElevatedCore() or LegacyAgentDockScheduledTaskExists();
end;

procedure ApplyExistingInstallPresentation();
var
  Details: String;
begin
  if not ExistingInstallDetected then
    Exit;

  WizardForm.WelcomeLabel1.Caption := GetLocalizedMessage('UpgradeWelcome');
  Details := '';
  if ExistingInstallVersion <> '' then
    Details := GetLocalizedMessage('UpgradeExistingVersion') + ' ' + ExistingInstallVersion + #13#10;
  Details := Details + GetLocalizedMessage('UpgradeTargetVersion') + ' {#AppVersion}' + #13#10#13#10;
  if ExistingInstallSource = 'setup' then
    Details := Details + GetLocalizedMessage('UpgradeSetupManaged')
  else
    Details := Details + GetLocalizedMessage('UpgradeLegacyManaged');
  WizardForm.WelcomeLabel2.Caption := Details;
  Log('AgentDock existing installation detected: source=' + ExistingInstallSource +
    ', version=' + ExistingInstallVersion + ', root=' + ExistingInstallRoot());
end;

function QuoteArgument(const Value: String): String;
begin
  Result := '"' + Value + '"';
end;

function LaunchRuntimeProcess(const Filename: String; const Arguments: String): Boolean;
var
  ExitCode: Integer;
begin
  Result := NativeSetupCommand(
    Filename,
    Arguments,
    60,
    False,
    ExitCode);
  if Result and (ExitCode <> 0) then
  begin
    Log('AgentDock runtime launch broker exited with code ' + IntToStr(ExitCode) + '.');
    Result := False;
  end;
end;

function SelectedTunnelMode(): String;
begin
  if RequestedTunnelMode = 'quick' then
    Result := 'quick'
  else if RequestedTunnelMode = 'named' then
    Result := 'named'
  else if (RequestedTunnelMode = 'local') or (RequestedTunnelMode = 'none') then
    Result := 'none'
  else
    Result := 'auto';
end;

procedure InitializeWizard();
var
  AutoStartParam: String;
begin
  Log('AgentDock active language: ' + ActiveLanguage());
  ResolvedInstallRoot := ResolveInstallRoot();
  ExistingInstallDetected := DetectExistingInstallation();
  RequestedTunnelMode := Lowercase(Trim(ExpandConstant('{param:MODE|}')));
  if RequestedTunnelMode = '' then
    RequestedTunnelMode := 'auto';
  RequestedServerURL := Trim(ExpandConstant('{param:SERVERURL|}'));

  UpgradeModePage := CreateInputOptionPage(
    wpSelectDir,
    GetLocalizedMessage('UpgradeModeCaption'),
    GetLocalizedMessage('UpgradeModeDescription'),
    GetLocalizedMessage('UpgradeModeSubCaption'),
    True,
    False
  );
  UpgradeModePage.Add(GetLocalizedMessage('UpgradeKeepSettings'));
  UpgradeModePage.Add(GetLocalizedMessage('UpgradeChangeSettings'));
  UpgradeModePage.SelectedValueIndex := 0;

  StartupPage := CreateInputOptionPage(
    UpgradeModePage.ID,
    GetLocalizedMessage('StartupPageCaption'),
    GetLocalizedMessage('StartupPageDescription'),
    GetLocalizedMessage('StartupPageSubCaption'),
    False,
    False
  );
  StartupPage.Add(GetLocalizedMessage('StartupOption'));
  StartupPage.Add(GetLocalizedMessage('ElevatedCoreOption'));
  StartupPage.Values[0] := True;
  StartupPage.Values[1] := False;

  LoadExistingSettings();

  AutoStartParam := Lowercase(ExpandConstant('{param:AUTOSTART|}'));
  if (AutoStartParam = '0') or (AutoStartParam = 'false') then
    StartupPage.Values[0] := False
  else if (AutoStartParam = '1') or (AutoStartParam = 'true') then
    StartupPage.Values[0] := True;

  AutoStartParam := Lowercase(ExpandConstant('{param:ADMINMODE|}'));
  if (AutoStartParam = '0') or (AutoStartParam = 'false') or (AutoStartParam = 'standard') then
    StartupPage.Values[1] := False
  else if (AutoStartParam = '1') or (AutoStartParam = 'true') or (AutoStartParam = 'elevated') then
    StartupPage.Values[1] := True;

  ApplyExistingInstallPresentation();

  InstallProgressPage := CreateOutputProgressPage(
    GetLocalizedMessage('OfflineProgressCaption'),
    GetLocalizedMessage('OfflineProgressDescription')
  );

  DesktopShortcutCheckBox := TNewCheckBox.Create(WizardForm);
  DesktopShortcutCheckBox.Parent := WizardForm.FinishedPage;
  DesktopShortcutCheckBox.Left := WizardForm.FinishedLabel.Left;
  DesktopShortcutCheckBox.Top := WizardForm.FinishedLabel.Top + WizardForm.FinishedLabel.Height + ScaleY(18);
  DesktopShortcutCheckBox.Width := WizardForm.FinishedPage.ClientWidth -
    DesktopShortcutCheckBox.Left - ScaleX(8);
  DesktopShortcutCheckBox.Caption := GetLocalizedMessage('CreateDesktopShortcut');
  DesktopShortcutCheckBox.Checked := True;
end;

function ShouldSkipPage(PageID: Integer): Boolean;
var
  PreserveExisting: Boolean;
begin
  PreserveExisting := ExistingInstallDetected and (UpgradeModePage.SelectedValueIndex = 0);
  Result :=
    ((PageID = UpgradeModePage.ID) and (not ExistingInstallDetected)) or
    (PreserveExisting and (PageID = StartupPage.ID));
end;

function ApplyDesktopControlPanelShortcut(CreateRequested: Boolean): Boolean;
var
  ShortcutPath: String;
  CreatedShortcutPath: String;
begin
  ShortcutPath := AddBackslash(ExpandConstant('{userdesktop}')) +
    GetLocalizedMessage('DesktopShortcutName') + '.lnk';
  if not CreateRequested then
  begin
    DeleteFile(ShortcutPath);
    Result := not FileExists(ShortcutPath);
    Exit;
  end;

  CreatedShortcutPath := CreateShellLink(
    ShortcutPath,
    GetLocalizedMessage('DesktopShortcutDescription'),
    ExpandConstant('{app}\bin\agentdock-tray.exe'),
    '',
    ExpandConstant('{app}'),
    ExpandConstant('{app}\installer\agentdock.ico'),
    0,
    SW_SHOWNORMAL
  );
  Result := CreatedShortcutPath <> '';
end;

function NextButtonClick(CurPageID: Integer): Boolean;
var
  DirectoryError: String;
  SelectedRoot: String;
begin
  Result := True;
  if CurPageID = wpFinished then
  begin
    if not ApplyDesktopControlPanelShortcut(DesktopShortcutCheckBox.Checked) then
      Log('AgentDock desktop shortcut state could not be applied.');
    if Pos('runtime-launch-deferred', InstallWarningCode) = 0 then
    begin
      if not LaunchRuntimeProcess(ExpandConstant('{app}\bin\agentdock-tray.exe'), '') then
        Log('AgentDock control panel could not be opened from the Finish button.');
    end
    else
      Log('AgentDock runtime activation was deferred; skipping Finish-page control panel launch.');
    Exit;
  end;
  if CurPageID = wpSelectDir then
  begin
    DirectoryError := ValidateSelectedInstallDirectory();
    if DirectoryError <> '' then
    begin
      SuppressibleMsgBox(DirectoryError, mbError, MB_OK, IDOK);
      Result := False;
      Exit;
    end;
    SelectedRoot := RemoveBackslashUnlessRoot(ExpandFileName(WizardDirValue()));
    if CompareText(SelectedRoot, ResolvedInstallRoot) <> 0 then
    begin
      ResolvedInstallRoot := RemoveBackslashUnlessRoot(ExpandFileName(WizardDirValue()));
      ExistingInstallDetected := DetectExistingInstallation();
      StartupPage.Values[0] := True;
      StartupPage.Values[1] := False;
      LoadExistingSettings();
    end;
  end;
  if (CurPageID = StartupPage.ID) and StartupPage.Values[1] then
    StartupPage.Values[0] := True;
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  PowerShellPath: String;
  InstallScriptPath: String;
  OfflineArchivePath: String;
  OfflineChecksumPath: String;
  OfflineCloudflaredPath: String;
  TokenFilePath: String;
  Parameters: String;
  TunnelMode: String;
  PrivilegeMode: String;
  ExitCode: Integer;
  ErrorCode: String;
  ErrorMessage: String;
  ErrorType: String;
  ErrorId: String;
  ErrorCategory: String;
  ErrorScript: String;
  ErrorLine: String;
  ErrorColumn: String;
  ErrorStack: String;
begin
  Result := ValidateSelectedInstallDirectory();
  if Result <> '' then
    Exit;
  TunnelMode := SelectedTunnelMode();
  if (TunnelMode = 'named') and (RequestedServerURL <> '') and
    ((Pos('https://', Lowercase(RequestedServerURL)) <> 1) or
     (Pos('"', RequestedServerURL) > 0)) then
  begin
    Result := GetLocalizedMessage('InvalidServerURL');
    Exit;
  end;
  InstallProgressPage.Show;
  try
    InstallProgressPage.SetText(GetLocalizedMessage('OfflineProgressPreparing'), '');
    InstallProgressPage.SetProgress(1, 4);
    ExtractTemporaryFile('install.ps1');
    ExtractTemporaryFile('launch-windows-process.ps1');
    ExtractTemporaryFile('agentdock_windows_{#PayloadArchitecture}.zip');
    ExtractTemporaryFile('agentdock_windows_{#PayloadArchitecture}.zip.sha256');
    ExtractTemporaryFile('cloudflared.exe');

    PowerShellPath := NativePowerShellPath();
    InstallScriptPath := ExpandConstant('{tmp}\install.ps1');
    OfflineArchivePath := ExpandConstant('{tmp}\agentdock_windows_{#PayloadArchitecture}.zip');
    OfflineChecksumPath := ExpandConstant('{tmp}\agentdock_windows_{#PayloadArchitecture}.zip.sha256');
    OfflineCloudflaredPath := ExpandConstant('{tmp}\cloudflared.exe');
    ResultFilePath := ExpandConstant('{tmp}\agentdock-install-result.ini');
    DeleteFile(ResultFilePath);
    if StartupPage.Values[1] then
      PrivilegeMode := 'elevated'
    else
      PrivilegeMode := 'standard';

    InstallProgressPage.SetProgress(2, 4);
    Parameters :=
      '-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File ' + QuoteArgument(InstallScriptPath) +
      ' -Version ' + QuoteArgument('{#AppVersion}') +
      ' -OfflineArchive ' + QuoteArgument(OfflineArchivePath) +
      ' -OfflineChecksumFile ' + QuoteArgument(OfflineChecksumPath) +
      ' -OfflineCloudflaredBinary ' + QuoteArgument(OfflineCloudflaredPath) +
      ' -InstallDir ' + QuoteArgument(ExpandConstant('{app}\bin')) +
      ' -TunnelMode ' + TunnelMode +
      ' -InstallChannel setup' +
      ' -CorePrivilegeMode ' + PrivilegeMode +
      ' -ResultFile ' + QuoteArgument(ResultFilePath);

    { PORT is intentionally a silent-setup override. The interactive installer keeps the product
      default, while isolated E2E environments can avoid colliding with an already running AgentDock. }
    if Trim(ExpandConstant('{param:PORT|}')) <> '' then
      Parameters := Parameters + ' -Port ' + QuoteArgument(Trim(ExpandConstant('{param:PORT|}')));

    if StartupPage.Values[0] or (TunnelMode = 'quick') or (TunnelMode = 'named') then
      Parameters := Parameters + ' -RegisterStartup';

    if TunnelMode = 'named' then
    begin
      if RequestedServerURL <> '' then
        Parameters := Parameters + ' -ServerUrl ' + QuoteArgument(RequestedServerURL);
      TokenFilePath := ExpandConstant('{param:TUNNELTOKENFILE|}');
      if TokenFilePath <> '' then
        Parameters := Parameters + ' -TunnelTokenFile ' + QuoteArgument(TokenFilePath);
    end;

    InstallProgressPage.SetText(GetLocalizedMessage('OfflineProgressApplying'), '');
    InstallProgressPage.SetProgress(3, 4);
    if not NativeSetupCommand(PowerShellPath, Parameters, 1800, True, ExitCode) then
    begin
      Result := GetLocalizedMessage('InstallerStartFailed');
      Exit;
    end;
    if ExitCode <> 0 then
    begin
      ErrorCode := GetIniString('AgentDock', 'Code', '', ResultFilePath);
      ErrorMessage := GetIniString('AgentDock', 'Message', '', ResultFilePath);
      ErrorType := GetIniString('AgentDock', 'ErrorType', '', ResultFilePath);
      ErrorId := GetIniString('AgentDock', 'ErrorId', '', ResultFilePath);
      ErrorCategory := GetIniString('AgentDock', 'ErrorCategory', '', ResultFilePath);
      ErrorScript := GetIniString('AgentDock', 'ErrorScript', '', ResultFilePath);
      ErrorLine := GetIniString('AgentDock', 'ErrorLine', '', ResultFilePath);
      ErrorColumn := GetIniString('AgentDock', 'ErrorColumn', '', ResultFilePath);
      ErrorStack := GetIniString('AgentDock', 'ErrorStack', '', ResultFilePath);
      if (ErrorType <> '') or (ErrorId <> '') or (ErrorCategory <> '') then
        Log('AgentDock installation diagnostics: type=' + ErrorType +
          '; id=' + ErrorId + '; category=' + ErrorCategory);
      if (ErrorScript <> '') or (ErrorLine <> '') or (ErrorColumn <> '') then
        Log('AgentDock installation location: script=' + ErrorScript +
          '; line=' + ErrorLine + '; column=' + ErrorColumn);
      if ErrorStack <> '' then
        Log('AgentDock installation stack: ' + ErrorStack);
      ErrorMessage := LocalizedInstallFailure(ErrorCode, ErrorMessage, ExitCode);
      Result := GetLocalizedMessage('InstallFailed') + ' ' + ErrorMessage;
      Exit;
    end;
    InstallWarningCode := GetIniString('AgentDock', 'WarningCode', '', ResultFilePath);
    InstallWarningMessage := GetIniString('AgentDock', 'WarningMessage', '', ResultFilePath);
    if InstallWarningCode <> '' then
      Log('AgentDock installation warning: ' + InstallWarningCode);
    if InstallWarningMessage <> '' then
      Log('AgentDock installation warning detail: ' + InstallWarningMessage);
    InstallProgressPage.SetText(GetLocalizedMessage('OfflineProgressFinishing'), '');
    InstallProgressPage.SetProgress(4, 4);
  finally
    InstallProgressPage.Hide;
  end;
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  if (CurPageID = wpReady) and ExistingInstallDetected then
    WizardForm.ReadyLabel.Caption := GetLocalizedMessage('ReadyUpgrade');
  if CurPageID = wpFinished then
  begin
    if Pos('runtime-launch-deferred', InstallWarningCode) > 0 then
      WizardForm.FinishedLabel.Caption := GetLocalizedMessage('FinishedDeferredControlPanel')
    else
      WizardForm.FinishedLabel.Caption := GetLocalizedMessage('FinishedControlPanel');
    if Pos('elevated-mode-fallback', InstallWarningCode) > 0 then
      WizardForm.FinishedLabel.Caption := WizardForm.FinishedLabel.Caption + #13#10#13#10 +
        GetLocalizedMessage('ElevatedModeFallbackNotice');
  end;
end;

function InitializeUninstall(): Boolean;
begin
  PurgeState := False;
  UninstallCleanupExecuted := False;
  if not UninstallSilent then
    PurgeState := MsgBox(
      GetLocalizedMessage('PurgeStateQuestion'),
      mbConfirmation,
      MB_YESNO
    ) = IDYES;
  Result := True;
end;

function GetUninstallParameters(Param: String): String;
begin
  Result := '-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File ' +
    QuoteArgument(ExpandConstant('{app}\installer\uninstall-windows.ps1')) +
    ' -InstallDir ' + QuoteArgument(ExpandConstant('{app}\bin')) +
    ' -KeepInstallDir';
  if PurgeState then
    Result := Result + ' -PurgeState';
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  PowerShellPath: String;
  ScriptPath: String;
  ExitCode: Integer;
begin
  if (CurUninstallStep <> usAppMutexCheck) or UninstallCleanupExecuted then
    Exit;

  UninstallCleanupExecuted := True;
  Log('AgentDock: running managed cleanup before uninstall file removal.');
  ScriptPath := ExpandConstant('{app}\installer\uninstall-windows.ps1');
  if not FileExists(ScriptPath) then
    RaiseException(GetLocalizedMessage('UninstallScriptMissing'));

  PowerShellPath := NativePowerShellPath();
  if not NativeSetupCommand(
    PowerShellPath,
    GetUninstallParameters(''),
    600,
    True,
    ExitCode
  ) then
    RaiseException(GetLocalizedMessage('UninstallScriptStartFailed'));
  if ExitCode <> 0 then
    RaiseException(
      GetLocalizedMessage('UninstallScriptFailed') + ' ' + IntToStr(ExitCode)
    );
  Log('AgentDock: managed cleanup completed successfully.');
end;

function PersistentSetupLogRoot(): String;
begin
  Result := Trim(ResolvedInstallRoot);
  if Result = '' then
    Result := ExpandConstant('{localappdata}\AgentDock');
end;

// Inno 保留 TEMP 原生日志；这里额外复制到固定目录，方便用户长期查找和反馈安装问题。
procedure PersistSetupLog();
var
  SourceLog: String;
  LogDirectory: String;
  PersistentLog: String;
begin
  SourceLog := ExpandConstant('{log}');
  if (SourceLog = '') or (not FileExists(SourceLog)) then
    Exit;

  LogDirectory := AddBackslash(PersistentSetupLogRoot()) + 'logs\installer';
  if not ForceDirectories(LogDirectory) then
  begin
    Log('AgentDock: could not create persistent installer log directory: ' + LogDirectory);
    Exit;
  end;

  PersistentLog := AddBackslash(LogDirectory) + 'setup-' +
    GetDateTimeString('yyyymmdd-hhnnss-zzz', '-', ':') + '.log';
  Log('AgentDock installer log target: ' + PersistentLog);
  if not CopyFile(SourceLog, PersistentLog, True) then
    Log('AgentDock: could not persist installer log; original log remains at: ' + SourceLog);
end;

procedure DeinitializeSetup();
begin
  PersistSetupLog();
  if ResultFilePath <> '' then
    DeleteFile(ResultFilePath);
end;
