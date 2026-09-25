{ Native process launch for every Setup/Uninstall console command. }
var
  NativeRequestSequence: Integer;
  NativeLastRequestDirectory: String;

function NativeJSONQuote(const Value: String): String;
var
  I: Integer;
begin
  Result := '"';
  for I := 1 to Length(Value) do
  begin
    case Ord(Value[I]) of
      34: Result := Result + '\"';
      92: Result := Result + '\\';
      9: Result := Result + '\t';
      10: Result := Result + '\n';
      13: Result := Result + '\r';
    else
      if Ord(Value[I]) < 32 then
        RaiseException(CustomMessage('NativeCommandControlCharacter'))
      else
        Result := Result + Value[I];
    end;
  end;
  Result := Result + '"';
end;

function NativeSetupLauncher(): String;
var
  Source: String;
begin
  Result := ExpandConstant('{tmp}\agentdock-setup-launcher.exe');
  if FileExists(Result) then Exit;
  if IsUninstaller then
  begin
    Source := ExpandConstant('{app}\installer\agentdock-setup-launcher.exe');
    if not CopyFile(Source, Result, True) then
      RaiseException(CustomMessage('NativeUninstallLauncherCopyFailed'));
  end
  else
    ExtractTemporaryFile('agentdock-setup-launcher.exe');
end;

function NativePowerShellPath(): String;
begin
  { The GUI executor is native-width. System32 resolves in its own process,
    even when Inno itself is 32-bit; do not pass a Sysnative pseudo-path. }
  Result := ExpandConstant('{win}\System32\WindowsPowerShell\v1.0\powershell.exe');
end;

function NativeSetupCommand(const Target: String; const Arguments: String;
  TimeoutSeconds: Integer; WaitForExit: Boolean; var ExitCode: Integer): Boolean;
var
  Launcher, RequestFile, RequestJSON, Mode, WaitValue: String;
  Diagnostic: AnsiString;
begin
  ExitCode := 1;
  Launcher := NativeSetupLauncher();
  NativeRequestSequence := NativeRequestSequence + 1;
  NativeLastRequestDirectory := ExpandConstant('{tmp}\agentdock-native-') + IntToStr(NativeRequestSequence);
  if not ForceDirectories(NativeLastRequestDirectory) then
  begin
    Result := False;
    Exit;
  end;
  RequestFile := AddBackslash(NativeLastRequestDirectory) + 'request.json';
  if WaitForExit then begin Mode := '--setup-exec'; WaitValue := 'true'; end
  else begin Mode := '--setup-launch'; WaitValue := 'false'; end;
  RequestJSON := '{"file_path":' + NativeJSONQuote(Target) +
    ',"arguments":' + NativeJSONQuote(Arguments) +
    ',"wait_for_exit":' + WaitValue + ',"timeout_seconds":' + IntToStr(TimeoutSeconds) +
    ',"environment":{}}';
  if not SaveStringToFile(RequestFile, Utf8Encode(RequestJSON), False) then
  begin
    Result := False;
    Exit;
  end;
  Result := Exec(Launcher, Mode + ' "' + RequestFile + '"', '', SW_HIDE, ewWaitUntilTerminated, ExitCode);
  Log('AgentDock native command: mode=' + Mode + '; exit=' + IntToStr(ExitCode) +
    '; diagnostics=' + NativeLastRequestDirectory);
  if (not Result) or (ExitCode <> 0) then
  begin
    if LoadStringFromFile(AddBackslash(NativeLastRequestDirectory) + 'stderr.log', Diagnostic) then
      Log('AgentDock native stderr: ' + Copy(Utf8Decode(Diagnostic), 1, 16000));
  end;
end;
