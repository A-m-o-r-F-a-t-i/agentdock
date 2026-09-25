#requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string] $Archive,
    [Parameter(Mandatory = $true)][string] $CloudflaredBinary,
    [Parameter(Mandatory = $true)][string] $BaselineArchive,
    [Parameter(Mandatory = $true)][string] $BaselineInstaller,
    [Parameter(Mandatory = $true)][string] $TestRoot,
    [string] $ExpectedVersion = '1.1.0',
    [string] $SourceHome = '',
    [string] $CoreFailureWrapper = '',
    [switch] $IncludeFreshInstall
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$root = [IO.Path]::GetFullPath($TestRoot)
if (Test-Path $root) { throw 'Inno regression requires a fresh test root.' }
if (Get-ScheduledTask -TaskName 'AgentDock' -TaskPath '\' -ErrorAction SilentlyContinue) { throw 'A fixed production elevated task exists; use another Windows profile.' }
$repository = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$id = [Guid]::NewGuid().ToString().ToUpperInvariant()
$prefix = 'AgentDockTest-' + $id
$names = @("$prefix-Core", "$prefix-Tray", "$prefix-Tunnel")
$testHome = Join-Path $root 'user data 中文'
$workspace = Join-Path $root 'workspace'
$runtimeRoot = Join-Path $root '安装目录 with spaces'
$copyRoot = Join-Path $root 'source'
$payload = Join-Path $root 'payload'
$normalOutput = Join-Path $root 'normal-setup'
$faultOutput = Join-Path $root 'fault-setup'
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$results = [Collections.Generic.List[object]]::new()
$faults = [Collections.Generic.List[object]]::new()
$failure = $null
$oldVersion = ''
$preservedData = @{}
$legacyActivity = ''
$initialEnvironment = @{}
Get-ChildItem Env:AGENTDOCK_* | ForEach-Object { $initialEnvironment[$_.Name] = $_.Value }
$initialPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
$listener.Start(); $port = $listener.LocalEndpoint.Port; $listener.Stop()
$setupPath = Join-Path $normalOutput 'AgentDockSetup-amd64.exe'
$faultSetup = Join-Path $faultOutput 'AgentDockSetup-amd64.exe'
$coreFailureOutput = Join-Path $root 'core-failure-setup'
$productionRoot = Join-Path $env:LOCALAPPDATA 'AgentDock'
$productionPointer = Join-Path $productionRoot 'active-version.json'
$productionPointerHash = if (Test-Path $productionPointer) { (Get-FileHash $productionPointer).Hash } else { '' }
$compiler = Join-Path $env:LOCALAPPDATA 'Programs\Inno Setup 6\ISCC.exe'
if (-not (Test-Path $compiler)) { $compiler = (Get-Command ISCC.exe -ErrorAction Stop).Source }
New-Item -ItemType Directory -Path $root, $testHome, $workspace, $payload, $normalOutput, $faultOutput, (Join-Path $copyRoot 'packaging'), (Join-Path $copyRoot 'scripts') -Force | Out-Null
if (-not ('AgentDock.Testing.ConsoleWindowWatch' -as [type])) { Add-Type -Path (Join-Path $PSScriptRoot 'helpers\ConsoleWindowWatch.cs') }
[AgentDock.Testing.ConsoleWindowWatch]::ValidateEventDelivery()
$beforeTasks = @(Get-ScheduledTask | Where-Object TaskName -Like 'AgentDock Setup Native *' | ForEach-Object TaskName)

function Set-TestText([string] $Path, [string] $Content) { [IO.File]::WriteAllText($Path, $Content, [Text.UTF8Encoding]::new($true)) }
function Replace-Once([string] $Text, [string] $Old, [string] $New) {
    if (($Text.Split(@($Old), [StringSplitOptions]::None)).Count -ne 2) { throw "Fixture replacement is not unique: $Old" }
    return $Text.Replace($Old, $New)
}
function Invoke-IsolatedProcess([string] $Executable, [string[]] $Arguments, [string] $Label, [int] $ExpectedExit = 0, [bool] $Observe = $false) {
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $Executable; $start.WorkingDirectory = $root
    $start.UseShellExecute = $false; $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true; $start.RedirectStandardError = $true
    foreach ($argument in $Arguments) { $start.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::new(); $process.StartInfo = $start
    $watch = if ($Observe) { [AgentDock.Testing.ConsoleWindowWatch]::new() } else { $null }
    $timer = [Diagnostics.Stopwatch]::StartNew()
    try {
        if (-not $process.Start()) { throw "$Label failed to launch." }
        $stdout = $process.StandardOutput.ReadToEndAsync(); $stderr = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(360000)) { $process.Kill($true); $process.WaitForExit(); throw "$Label exceeded 360 seconds." }
        $process.WaitForExit()
        [IO.File]::WriteAllText((Join-Path $root ($Label + '.process.log')), $stdout.GetAwaiter().GetResult() + "`n" + $stderr.GetAwaiter().GetResult())
        if ($ExpectedExit -eq 0 -and $process.ExitCode -ne 0) { throw "$Label exited $($process.ExitCode); inspect the isolated log." }
        if ($ExpectedExit -ne 0 -and $process.ExitCode -eq 0) { throw "$Label accepted the injected installation failure." }
        if ($null -ne $watch) {
            Start-Sleep -Milliseconds 100
            if ($watch.Failure) { throw $watch.Failure }
            $events = @($watch.Snapshot())
            $events | ConvertTo-Json -Depth 6 | Set-Content (Join-Path $root ($Label + '.windows.json')) -Encoding utf8NoBOM
            if ($events.Count -ne 0) { throw "$Label showed a console window." }
        }
        $results.Add([pscustomobject]@{ case = $Label; exit_code = $process.ExitCode; elapsed_ms = $timer.ElapsedMilliseconds; observed = $Observe; new_visible_consoles = 0 })
        Write-Host "PASS $Label exit=$($process.ExitCode) elapsed_ms=$($timer.ElapsedMilliseconds)"
    } finally {
        if ($null -ne $watch) { $watch.Dispose() }
        $process.Dispose()
    }
}
function Invoke-Setup([string] $Setup, [string] $Label, [bool] $ExpectFailure = $false) {
    $arguments = @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/SP-', "/DIR=$runtimeRoot", "/GROUP=$prefix", "/PORT=$port", '/MODE=local', '/AUTOSTART=1', '/ADMINMODE=standard', "/LOG=$(Join-Path $root ($Label + '.inno.log'))")
    Invoke-IsolatedProcess $Setup $arguments $Label $(if ($ExpectFailure) { 1 } else { 0 }) $true
}
function Assert-Health([string] $Version) {
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        try { $health = Invoke-RestMethod "http://127.0.0.1:$port/healthz" -TimeoutSec 2; if ($health.ok -and $health.version -eq $Version) { return } } catch { }
        Start-Sleep -Milliseconds 100
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "Isolated runtime failed health/version check: $Version"
}
function Assert-Preservation([string] $State, [string] $Version, [hashtable] $Hashes) {
    Assert-Health $Version
    $tx = Get-Content (Join-Path $runtimeRoot 'install\transaction.json') -Raw | ConvertFrom-Json
    $receipt = Get-Content (Join-Path $runtimeRoot 'install\result.json') -Raw | ConvertFrom-Json
    $pointerPath = Join-Path $runtimeRoot 'active-version.json'
    if (Test-Path $pointerPath) {
        $pointer = Get-Content $pointerPath -Raw | ConvertFrom-Json
        if ($pointer.state -ne 'committed') { throw 'Restored generation pointer is not committed.' }
    } elseif ($Version -eq $ExpectedVersion) { throw 'The new generation pointer is missing.' }
    # Legacy 0.8.3 used a flat binary. A correct rollback restores that layout,
    # including the original absence of active-version.json.
    if ($tx.state -ne $State -or $receipt.state -ne $State -or -not $receipt.healthy) { throw "Unhealthy or incomplete transaction: $State" }
    foreach ($name in $Hashes.Keys) {
        if ((Get-FileHash (Join-Path $runtimeRoot $name)).Hash -ne $Hashes[$name]) { throw "Protected credential changed: $name" }
    }
    if ([IO.File]::ReadAllText((Join-Path $testHome 'test-preserve.txt')) -ne 'preserve') { throw 'User data marker changed.' }
    Assert-UserFixture
}

function Set-StaleAdapterRollbackFixture([string] $SourceVersion, [string] $TargetVersion) {
    $transactionId = [Guid]::NewGuid().ToString('N')
    $timestamp = [DateTime]::UtcNow.ToString('o')
    $source = 'v' + $SourceVersion.TrimStart('v')
    $target = 'v' + $TargetVersion.TrimStart('v')
    $failure = [ordered]@{
        code = 'external_rollback_failed'
        message = 'injected OS adapter rollback failure after restoring the source runtime'
        at = $timestamp
    }
    $transaction = [ordered]@{
        schema_version = 1
        transaction_id = $transactionId
        platform = 'windows'
        action = 'install'
        source_version = $source
        target_version = $target
        active_version = $source
        fallback_version = $source
        state = 'failed'
        phase = 'rollback'
        install_root = $runtimeRoot
        runtime_root = $runtimeRoot
        agentdock_home = $testHome
        agentdock_default_dir = $workspace
        started_at = $timestamp
        updated_at = $timestamp
        completed_at = $timestamp
        failure = $failure
    }
    $result = [ordered]@{
        schema_version = 1
        transaction_id = $transactionId
        platform = 'windows'
        action = 'install'
        state = 'failed'
        phase = 'rollback'
        version = $target
        active_version = $source
        fallback_version = $source
        healthy = $false
        failure = $failure
        started_at = $timestamp
        completed_at = $timestamp
    }
    $installStateRoot = Join-Path $runtimeRoot 'install'
    $resultsRoot = Join-Path $installStateRoot 'results'
    New-Item -ItemType Directory -Path $resultsRoot -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $installStateRoot 'transaction.json'), ($transaction | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText((Join-Path $installStateRoot 'result.json'), ($result | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText((Join-Path $resultsRoot ($transactionId + '.json')), ($result | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
    Assert-Health $SourceVersion
    return $transactionId
}

function Assert-UserFixture {
    foreach ($name in $preservedData.Keys) {
        if ([IO.File]::ReadAllText((Join-Path $testHome $name)) -ne $preservedData[$name]) { throw "Upgrade changed user fixture: $name" }
    }
    $journal = Join-Path $testHome 'tasks\activity\00000000000000000001.jsonl'
    if ($legacyActivity -and (-not (Test-Path $journal) -or -not [IO.File]::ReadAllText($journal).StartsWith($legacyActivity, [StringComparison]::Ordinal))) { throw 'Upgrade rewrote or removed the original legacy activity records.' }
    if ([IO.File]::ReadAllText((Join-Path $workspace 'project-sentinel.txt')) -ne 'preserve-project-source') { throw 'Install, rollback or uninstall changed a project file.' }
    $registry = Get-Content (Join-Path $testHome 'workspaces.json') -Raw | ConvertFrom-Json
    $registered = @($registry.workspaces | Where-Object workspace_id -EQ 'wsp_1111111111111111')[0]
    if ($registered.root -ne $workspace -or $registered.rules_revision -ne 9 -or $registered.artifact_root -ne (Join-Path $root 'custom-artifacts')) { throw 'Upgrade changed the registered workspace policy.' }
}

function Get-ProductionStartupFingerprint {
    $snapshot = [ordered]@{}
    $values = Get-ItemProperty $runKey -ErrorAction SilentlyContinue
    if ($null -ne $values) {
        foreach ($name in @('AgentDock','AgentDockTray','AgentDockTunnel','AgentDockCloudflared')) {
            if ($values.PSObject.Properties[$name]) { $snapshot[$name] = [string]$values.$name }
        }
    }
    return ($snapshot | ConvertTo-Json -Compress)
}
$productionStartupBefore = Get-ProductionStartupFingerprint
[IO.File]::WriteAllText((Join-Path $root 'production-startup-before.json'),$productionStartupBefore)

try {
    foreach ($key in $initialEnvironment.Keys) { [Environment]::SetEnvironmentVariable($key, $null, 'Process') }
    $env:AGENTDOCK_HOME = $testHome; $env:AGENTDOCK_DEFAULT_DIR = $workspace
    if ($SourceHome) {
        foreach ($part in @('skills','plugins','mcp')) {
            $source = Join-Path $SourceHome $part
            if (Test-Path $source) {
                & robocopy $source (Join-Path $testHome $part) /E /XJ /R:0 /W:0 /XD .locks .tmp .data /NFL /NDL /NJH /NJS /NP > $null
                if ($LASTEXITCODE -ge 8) { throw "Capability copy failed: $part" }
            }
        }
    }
    [IO.File]::WriteAllText((Join-Path $testHome 'test-preserve.txt'), 'preserve')
    [IO.File]::WriteAllText((Join-Path $workspace 'project-sentinel.txt'), 'preserve-project-source')
    Copy-Item (Join-Path $repository 'packaging\windows') (Join-Path $copyRoot 'packaging\windows') -Recurse
    Copy-Item (Join-Path $repository 'scripts\install') (Join-Path $copyRoot 'scripts\install') -Recurse
    $definition = Join-Path $copyRoot 'packaging\windows\AgentDock.iss'
    $definitionText = [IO.File]::ReadAllText($definition).Replace('D6788C7A-4104-48D4-B5C3-F4858B5606EA', $id).Replace('DefaultGroupName=AgentDock Workbench', "DefaultGroupName=$prefix")
    Set-TestText $definition $definitionText
    $codePath = Join-Path $copyRoot 'packaging\windows\includes\code.iss'
    $code = [IO.File]::ReadAllText($codePath)
    # A different Windows profile would have its own LOCALAPPDATA fallback.
    # Redirect only that test-build fallback; never read/write production root.
    $code = $code.Replace("ExpandConstant('{localappdata}\AgentDock')", "'" + $runtimeRoot.Replace("'", "''") + "'")
    $code = Replace-Once $code "    if StartupPage.Values[0] or (TunnelMode = 'quick') or (TunnelMode = 'named') then" ("    Parameters := Parameters + ' -StartupValueName " + $names[0] + ' -TrayStartupValueName ' + $names[1] + ' -CloudflaredStartupValueName ' + $names[2] + "';`r`n    if StartupPage.Values[0] or (TunnelMode = 'quick') or (TunnelMode = 'named') then")
    # Never create or remove the real desktop shortcut in the test build.
    $code = Replace-Once $code "ShortcutPath := AddBackslash(ExpandConstant('{userdesktop}')) +" "ShortcutPath := AddBackslash(ExpandConstant('{app}')) +"
    Set-TestText $codePath $code
    $definitionText = $definitionText.Replace("{userdesktop}\{code:GetLocalizedMessage|DesktopShortcutName}.lnk", "{app}\{code:GetLocalizedMessage|DesktopShortcutName}.lnk")
    Set-TestText $definition $definitionText
    Copy-Item $Archive (Join-Path $payload 'agentdock_windows_amd64.zip')
    Copy-Item ($Archive + '.sha256') (Join-Path $payload 'agentdock_windows_amd64.zip.sha256')
    Copy-Item $CloudflaredBinary (Join-Path $payload 'cloudflared.exe')
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [IO.Compression.ZipFile]::OpenRead($Archive)
    try { [IO.Compression.ZipFileExtensions]::ExtractToFile($zip.GetEntry('agentdock-tray-shim.exe'), (Join-Path $payload 'agentdock-setup-launcher.exe'), $true) } finally { $zip.Dispose() }
    Invoke-IsolatedProcess $compiler @("/DAppVersion=$ExpectedVersion", "/DOutputDir=$normalOutput", "/DOfflinePayloadDir=$payload", $definition) 'compile-normal'
    $installScript = Join-Path $copyRoot 'scripts\install\install.ps1'
    $installText = [IO.File]::ReadAllText($installScript)
    Set-TestText $installScript (Replace-Once $installText '    # Provision is complete here.' "    throw 'isolated fault after trial, before commit'`r`n    # Provision is complete here.")
    Invoke-IsolatedProcess $compiler @("/DAppVersion=$ExpectedVersion", "/DOutputDir=$faultOutput", "/DOfflinePayloadDir=$payload", $definition) 'compile-fault'
    Set-TestText $installScript $installText

    if ($CoreFailureWrapper) {
        $failingPayload = Join-Path $root 'core-failure-payload'
        $failingExtract = Join-Path $root 'core-failure-extract'
        New-Item -ItemType Directory -Path $failingPayload, $coreFailureOutput -Force | Out-Null
        [IO.Compression.ZipFile]::ExtractToDirectory($Archive, $failingExtract)
        Move-Item (Join-Path $failingExtract 'agentdock.exe') (Join-Path $failingExtract 'agentdock-real.exe')
        Copy-Item -LiteralPath $CoreFailureWrapper -Destination (Join-Path $failingExtract 'agentdock.exe')
        $failingArchive = Join-Path $failingPayload 'agentdock_windows_amd64.zip'
        [IO.Compression.ZipFile]::CreateFromDirectory($failingExtract, $failingArchive)
        ((Get-FileHash $failingArchive).Hash.ToLowerInvariant() + '  agentdock_windows_amd64.zip') | Set-Content ($failingArchive + '.sha256') -Encoding ascii
        Copy-Item (Join-Path $payload 'cloudflared.exe'), (Join-Path $payload 'agentdock-setup-launcher.exe') $failingPayload
        Invoke-IsolatedProcess $compiler @("/DAppVersion=$ExpectedVersion", "/DOutputDir=$coreFailureOutput", "/DOfflinePayloadDir=$failingPayload", $definition) 'compile-core-failure'
    }

    $baselineArgs = @('-NoLogo','-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',$BaselineInstaller,'-Version','latest','-OfflineArchive',$BaselineArchive,'-OfflineChecksumFile',($BaselineArchive+'.sha256'),'-OfflineCloudflaredBinary',$CloudflaredBinary,'-InstallDir',(Join-Path $runtimeRoot 'bin'),'-InstallChannel','script','-RegisterStartup','-CorePrivilegeMode','standard','-TunnelMode','none','-Port',"$port",'-StartupValueName',$names[0],'-TrayStartupValueName',$names[1],'-CloudflaredStartupValueName',$names[2])
    Invoke-IsolatedProcess (Join-Path $env:WINDIR 'System32\WindowsPowerShell\v1.0\powershell.exe') $baselineArgs 'baseline'
    $oldVersion = (Invoke-RestMethod "http://127.0.0.1:$port/healthz" -TimeoutSec 5).version
    $stamp = [DateTime]::UtcNow.ToString('o')
    $preservedData['tasks\tsk_1111111111111111.json'] = (@{schema_version=1;id='tsk_1111111111111111';title='Legacy upgrade fixture';goal='Preserve user task';status='active';phase='execute';conditions=@(@{id='cond_01';text='preserved';created_at=$stamp});steps=@(@{id='verify';title='Verify';status='pending';phase='execute';updated_at=$stamp});events=@();created_at=$stamp;updated_at=$stamp} | ConvertTo-Json -Depth 8)
    $preservedData['skills\upgrade-fixture\SKILL.md'] = "---`nname: upgrade-fixture`ndescription: Isolated upgrade fixture.`nversion: 1.0.0`n---`n# Preserve this user Skill`n"
    $preservedData['plugins\upgrade-fixture\plugin.json'] = '{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"upgrade-fixture","version":"1.0.0","description":"Isolated user plugin fixture."}'
    $preservedData['mcp\upgrade-fixture.txt'] = 'User MCP companion state must survive install and uninstall.'
    # Introduce 1.1.2 policy only after a successful upgrade. A prior untouched
    # 1.1.1 installation does not yet own this policy contract.
    $newPolicyFixture = (@{
        schema_version=1; revision=7; global_mode='rules'; scopes=@(); updated_at=$stamp
        rules=@(@{id='deny_fixture_delete';tool='file_edit';action='delete';effect='deny';reason='Preserve the user file-delete prohibition.'})
    } | ConvertTo-Json -Depth 8)
    $legacyRecords = @(
        @{schema_version=1;seq=1;event_id='evt_legacy_start';created_at=$stamp;task_id='tsk_1111111111111111';thread_id='main';workspace_id='wsp_1111111111111111';kind='command.started';status='running';tool_name='exec_command';session_id='legacy-upgrade-session'},
        @{schema_version=1;seq=2;event_id='evt_legacy_output';created_at=$stamp;task_id='tsk_1111111111111111';thread_id='main';workspace_id='wsp_1111111111111111';kind='command.output';tool_name='exec_command';session_id='legacy-upgrade-session';output_preview="preserved legacy output`n"},
        @{schema_version=1;seq=3;event_id='evt_legacy_complete';created_at=$stamp;task_id='tsk_1111111111111111';thread_id='main';workspace_id='wsp_1111111111111111';kind='command.completed';status='success';tool_name='exec_command';session_id='legacy-upgrade-session';exit_code=0}
    )
    $legacyActivity = (($legacyRecords | ForEach-Object { $_ | ConvertTo-Json -Depth 8 -Compress }) -join "`n") + "`n"
    $activityRoot = Join-Path $testHome 'tasks\activity'
    New-Item -ItemType Directory -Path $activityRoot -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $activityRoot '00000000000000000001.jsonl'), $legacyActivity, [Text.UTF8Encoding]::new($false))
    foreach ($name in $preservedData.Keys) {
        $fixtureFile = Join-Path $testHome $name
        New-Item -ItemType Directory -Path (Split-Path $fixtureFile -Parent) -Force | Out-Null
        [IO.File]::WriteAllText($fixtureFile,$preservedData[$name],[Text.UTF8Encoding]::new($false))
    }
    @{schema_version=1;default_workspace_id='wsp_1111111111111111';workspaces=@(@{workspace_id='wsp_1111111111111111';name='Upgrade fixture';kind='directory';runtime='windows';root=$workspace;default_workdir='.';artifact_root=(Join-Path $root 'custom-artifacts');scratch_root=(Join-Path $root 'custom-scratch');cache_root=(Join-Path $root 'custom-cache');rules_revision=9;created_at=$stamp;updated_at=$stamp})} | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $testHome 'workspaces.json') -Encoding utf8NoBOM
    $hashes = @{}
    Get-ChildItem $runtimeRoot -Filter '*.dpapi' -File | ForEach-Object { $hashes[$_.Name] = (Get-FileHash $_.FullName).Hash }
    if ($hashes.Count -eq 0) { throw 'Baseline has no protected credentials.' }
    if ($CoreFailureWrapper) {
        Invoke-Setup (Join-Path $coreFailureOutput 'AgentDockSetup-amd64.exe') 'inno-core-start-rollback' $true
        Assert-Preservation 'rolled_back' $oldVersion $hashes
    }
    Invoke-Setup $faultSetup 'inno-upgrade-rollback' $true
    Assert-Preservation 'rolled_back' $oldVersion $hashes
    $staleRollbackTransaction = Set-StaleAdapterRollbackFixture -SourceVersion $oldVersion -TargetVersion $ExpectedVersion
    Invoke-Setup $setupPath 'inno-stale-external-rollback-recovery'
    Assert-Preservation 'committed' $ExpectedVersion $hashes
    $recoveredTransaction = Get-Content (Join-Path $runtimeRoot "install\results\$staleRollbackTransaction.json") -Raw | ConvertFrom-Json
    if ($recoveredTransaction.state -ne 'rolled_back' -or $recoveredTransaction.failure.code -ne 'abandoned') {
        throw 'Setup did not preserve the recovered stale rollback receipt.'
    }
    $policyName = 'execution\permissions\policy.json'
    $preservedData[$policyName] = $newPolicyFixture
    $policyPath = Join-Path $testHome $policyName
    New-Item -ItemType Directory -Path (Split-Path $policyPath -Parent) -Force | Out-Null
    [IO.File]::WriteAllText($policyPath, $newPolicyFixture, [Text.UTF8Encoding]::new($false))
    $generationHash = (Get-FileHash (Join-Path $runtimeRoot "versions\v$ExpectedVersion\agentdock-core.exe")).Hash
    Invoke-Setup $faultSetup 'inno-same-version-rollback' $true
    Assert-Preservation 'rolled_back' $ExpectedVersion $hashes
    if ((Get-FileHash (Join-Path $runtimeRoot "versions\v$ExpectedVersion\agentdock-core.exe")).Hash -ne $generationHash) { throw 'Same-version rollback changed original generation bytes.' }
    Invoke-Setup $setupPath 'inno-same-version-repair'
    Assert-Preservation 'committed' $ExpectedVersion $hashes
    Invoke-IsolatedProcess (Join-Path $runtimeRoot 'unins000.exe') @('/VERYSILENT','/SUPPRESSMSGBOXES','/NORESTART',"/LOG=$(Join-Path $root 'uninstall.inno.log')") 'inno-uninstall' 0 $true
    if ([IO.File]::ReadAllText((Join-Path $testHome 'test-preserve.txt')) -ne 'preserve') { throw 'Uninstall removed user data without consent.' }
    Assert-UserFixture
    if ($IncludeFreshInstall) {
        Invoke-Setup $setupPath 'inno-fresh'
        Assert-Preservation 'committed' $ExpectedVersion @{}
        Invoke-IsolatedProcess (Join-Path $runtimeRoot 'unins000.exe') @('/VERYSILENT','/SUPPRESSMSGBOXES','/NORESTART') 'inno-fresh-uninstall' 0 $true
        # Preserve the old-data fixtures while testing a genuinely empty home
        # and installation directory at the same isolated paths.
        Move-Item -LiteralPath $testHome -Destination (Join-Path $root 'preserved-user-data')
        Move-Item -LiteralPath $runtimeRoot -Destination (Join-Path $root 'preserved-installation')
        New-Item -ItemType Directory -Path $testHome -Force | Out-Null
        Invoke-Setup $setupPath 'inno-clean-profile-install'
        Assert-Health $ExpectedVersion
        $cleanPointer = Get-Content (Join-Path $runtimeRoot 'active-version.json') -Raw | ConvertFrom-Json
        if ($cleanPointer.state -ne 'committed') { throw 'Clean profile install did not commit.' }
        if ([IO.File]::ReadAllText((Join-Path $workspace 'project-sentinel.txt')) -ne 'preserve-project-source') { throw 'Clean profile install changed project source.' }
        Invoke-IsolatedProcess (Join-Path $runtimeRoot 'unins000.exe') @('/VERYSILENT','/SUPPRESSMSGBOXES','/NORESTART') 'inno-clean-profile-uninstall' 0 $true
    }
    $leftovers = @(Get-ScheduledTask | Where-Object { $_.TaskName -like 'AgentDock Setup Native *' -and $_.TaskName -notin $beforeTasks })
    if ($leftovers.Count) { throw 'Temporary native tasks remain after the Inno matrix.' }
    [IO.File]::WriteAllText((Join-Path $root 'production-startup-after.json'),(Get-ProductionStartupFingerprint))
    if ((Get-ProductionStartupFingerprint) -ne $productionStartupBefore) { throw 'Production startup configuration changed during isolated regression.' }
    if ($productionPointerHash -and (Get-FileHash $productionPointer).Hash -ne $productionPointerHash) { throw 'Production generation pointer changed during isolated regression.' }
} catch { $failure = $_ }
finally {
    Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($root + '\', [StringComparison]::OrdinalIgnoreCase) } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    foreach ($name in $names) { Remove-ItemProperty $runKey -Name $name -ErrorAction SilentlyContinue }
    $currentPath = [Environment]::GetEnvironmentVariable('Path','User')
    $cleanPath = ($currentPath -split ';' | Where-Object { -not $_.Trim().StartsWith($root + '\', [StringComparison]::OrdinalIgnoreCase) }) -join ';'
    [Environment]::SetEnvironmentVariable('Path',$cleanPath,'User')
    $uninstallKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\{$id}_is1"
    if (Test-Path $uninstallKey) { Remove-Item $uninstallKey -Recurse -Force }
    $group = Join-Path ([Environment]::GetFolderPath('StartMenu')) "Programs\$prefix"
    if (Test-Path $group) { Remove-Item $group -Recurse -Force }
    Get-ChildItem Env:AGENTDOCK_* | ForEach-Object { [Environment]::SetEnvironmentVariable($_.Name,$null,'Process') }
    foreach ($key in $initialEnvironment.Keys) { [Environment]::SetEnvironmentVariable($key,$initialEnvironment[$key],'Process') }
    [ordered]@{
        passed = ($null -eq $failure); version = $ExpectedVersion; baseline_version = $oldVersion; runtime_root = $runtimeRoot; port = $port
        app_id = $id; production_state_unchanged = ((Get-ProductionStartupFingerprint) -eq $productionStartupBefore -and (-not $productionPointerHash -or (Get-FileHash $productionPointer).Hash -eq $productionPointerHash)); payload_sha256 = (Get-FileHash $Archive).Hash.ToLowerInvariant()
        baseline_sha256 = (Get-FileHash $BaselineArchive).Hash.ToLowerInvariant(); cases = $results.ToArray()
        preserved_user_fixtures = @('legacy Task','legacy Activity','permission policy bytes','project source file','user Skill','user Plugin','MCP companion state','Workspace policy')
        legacy_permission_enforcement = 'policy-byte preservation only; request enforcement is verified separately by the Core data-upgrade test'
        setup_identity = 'isolated AppId, startup names, shortcut and install-root fallback; production payload binaries unchanged'
        fresh_install_scope = $(if ($IncludeFreshInstall) { 'retained user data reinstall and empty user-data/installation directory' } else { 'not requested' })
        observer_self_test = 'passed'; error = $(if ($null -eq $failure) { '' } else { $failure.Exception.Message })
    } | ConvertTo-Json -Depth 10 | Set-Content (Join-Path $root 'result.json') -Encoding utf8NoBOM
}
if ($null -ne $failure) { throw $failure }
Write-Host 'Actual Inno isolated upgrade, rollback, repair and uninstall matrix passed.'
