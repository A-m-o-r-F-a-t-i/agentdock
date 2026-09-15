#requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string] $AgentDockBinary,
    [Parameter(Mandatory = $true)][string] $ReportPath,
    [string] $InnoHarnessPath = ''
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$core = (Resolve-Path -LiteralPath $AgentDockBinary).Path
$native = Join-Path (Split-Path $core) 'agentdock-tray-shim.exe'
if (-not [string]::IsNullOrWhiteSpace($InnoHarnessPath)) { $InnoHarnessPath = (Resolve-Path -LiteralPath $InnoHarnessPath).Path }
$adapter = (Resolve-Path (Join-Path $PSScriptRoot '..\install\launch-windows-process.ps1')).Path
$report = [IO.Path]::GetFullPath($ReportPath)
$testRoot = Join-Path (Split-Path $report) ('native setup 测试 ' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $testRoot -Force | Out-Null
if (-not ('AgentDock.Testing.ConsoleWindowWatch' -as [type])) { Add-Type -Path (Join-Path $PSScriptRoot 'helpers\ConsoleWindowWatch.cs') }
[AgentDock.Testing.ConsoleWindowWatch]::ValidateEventDelivery()
$beforeTasks = @(Get-ScheduledTask | Where-Object { $_.TaskName -like 'AgentDock Setup Native *' } | ForEach-Object TaskName)
$parentIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
$parentElevated = [Security.Principal.WindowsPrincipal]::new($parentIdentity).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
$parentSession = [Diagnostics.Process]::GetCurrentProcess().SessionId
$startedAt = [DateTime]::UtcNow
$results = [Collections.Generic.List[object]]::new()
$failure = $null
$probe = Join-Path $testRoot 'probe.ps1'
@'
Add-Type -TypeDefinition 'using System; using System.Runtime.InteropServices; public static class NativeSetupProbe { [DllImport("kernel32.dll")] public static extern IntPtr GetConsoleWindow(); }'
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
@{
    sid = $identity.User.Value
    session = [Diagnostics.Process]::GetCurrentProcess().SessionId
    elevated = [Security.Principal.WindowsPrincipal]::new($identity).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    is64bit = [Environment]::Is64BitProcess
    console = [NativeSetupProbe]::GetConsoleWindow().ToInt64()
    text = '中文 UTF8'
} | ConvertTo-Json -Compress
'@ | Set-Content -LiteralPath $probe -Encoding utf8BOM

function Invoke-NativeCase {
    param([string] $Label, [string] $Target, [string] $Arguments, [int] $ExpectedExit = 0, [int] $TimeoutSeconds = 30)
    $caseRoot = Join-Path $testRoot $Label
    New-Item -ItemType Directory -Path $caseRoot -Force | Out-Null
    $requestPath = Join-Path $caseRoot 'request.json'
    @{ file_path = $Target; arguments = $Arguments; wait_for_exit = $true; timeout_seconds = $TimeoutSeconds; environment = @{} } |
        ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $requestPath -Encoding utf8NoBOM
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $native
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $start.StandardOutputEncoding = [Text.UTF8Encoding]::new($false)
    $start.StandardErrorEncoding = [Text.UTF8Encoding]::new($false)
    if ([string]::IsNullOrWhiteSpace($InnoHarnessPath)) {
        $start.ArgumentList.Add('--setup-exec')
        $start.ArgumentList.Add($requestPath)
    } else {
        $start.FileName = $InnoHarnessPath
        foreach ($argument in @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/SP-', "/LAUNCHER=$native", "/REQUEST=$requestPath", "/LOG=$(Join-Path $caseRoot 'inno.log')")) { $start.ArgumentList.Add($argument) }
    }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $start
    $timer = [Diagnostics.Stopwatch]::StartNew()
    try {
        if (-not $process.Start()) { throw "$Label did not start." }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(($TimeoutSeconds + 15) * 1000)) { $process.Kill($true); $process.WaitForExit(); throw "$Label exceeded its deadline." }
        $process.WaitForExit()
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        $receipt = Get-Content (Join-Path $caseRoot 'result.json') -Raw | ConvertFrom-Json
        $results.Add([pscustomobject]@{ label = $Label; exit_code = $process.ExitCode; elapsed_ms = $timer.ElapsedMilliseconds; child_pid = $receipt.pid; receipt = (Join-Path $caseRoot 'result.json') })
        if ($process.ExitCode -ne $ExpectedExit) { throw "$Label expected exit $ExpectedExit, got $($process.ExitCode): $stderr" }
        if ($ExpectedExit -eq 124 -and (Get-Process -Id $receipt.pid -ErrorAction SilentlyContinue)) { throw 'Timed-out direct child was not reaped.' }
        if (-not [string]::IsNullOrWhiteSpace($InnoHarnessPath)) {
            # The GUI installer does not forward inherited streams. Receipts
            # and regular log files are the protocol for the real Inno parent.
            $stdout = [IO.File]::ReadAllText((Join-Path $caseRoot 'stdout.log'))
            $stderr = [IO.File]::ReadAllText((Join-Path $caseRoot 'stderr.log'))
        }
        if ($ExpectedExit -eq 23 -and -not $stderr.Contains('native-exec-failure')) { throw 'Child stderr was lost.' }
        return $stdout
    } finally { $process.Dispose() }
}

function Assert-NativeIdentity {
    param([object] $Identity, [bool] $Expected64Bit)
    if ($Identity.sid -ne $parentIdentity.User.Value -or $Identity.session -ne $parentSession) { throw 'Native execution changed the logged-in user or session.' }
    if ($Identity.elevated -and -not $parentElevated) { throw 'Native execution unexpectedly elevated privileges.' }
    if ($Identity.is64bit -ne $Expected64Bit -or $Identity.console -ne 0 -or $Identity.text -ne '中文 UTF8') { throw 'Native execution architecture, console or UTF-8 assertion failed.' }
}

$watch = [AgentDock.Testing.ConsoleWindowWatch]::new()
try {
    $ps64 = Join-Path $env:WINDIR 'System32\WindowsPowerShell\v1.0\powershell.exe'
    $ps32 = Join-Path $env:WINDIR 'SysWOW64\WindowsPowerShell\v1.0\powershell.exe'
    $arguments = "-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File `"$probe`""
    Assert-NativeIdentity ((Invoke-NativeCase 'direct-ps64' $ps64 $arguments) | ConvertFrom-Json) $true
    Assert-NativeIdentity ((Invoke-NativeCase 'direct-ps32' $ps32 $arguments) | ConvertFrom-Json) $false

    $nestedScript = Join-Path $testRoot 'nested-broker.ps1'
    "`$ErrorActionPreference = 'Stop'; & '$($adapter.Replace("'", "''"))' -FilePath '$($ps32.Replace("'", "''"))' -AgentDockBinary '$($core.Replace("'", "''"))' -Arguments '$($arguments.Replace("'", "''"))' -WaitForExit -PassThruOutput" |
        Set-Content -LiteralPath $nestedScript -Encoding utf8BOM
    $nestedArguments = "-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File `"$nestedScript`""
    Assert-NativeIdentity ((Invoke-NativeCase 'ps32-native-broker-ps32' $ps32 $nestedArguments 0 60) | ConvertFrom-Json) $false

    [void](Invoke-NativeCase 'negative-exit' (Join-Path $env:WINDIR 'System32\cmd.exe') '/d /c echo native-exec-failure 1>&2 & exit 23' 23)
    [void](Invoke-NativeCase 'timeout' $ps32 '-NoLogo -NoProfile -NonInteractive -Command "Start-Sleep -Seconds 10"' 124 1)
    Start-Sleep -Milliseconds 150
    if ($watch.Failure) { throw $watch.Failure }
    $windows = @($watch.Snapshot())
    if ($windows.Count -ne 0) { throw "Observed $($windows.Count) newly visible console windows during native setup execution." }
    $newTasks = @(Get-ScheduledTask | Where-Object { $_.TaskName -like 'AgentDock Setup Native *' -and $_.TaskName -notin $beforeTasks })
    if ($newTasks.Count -ne 0) { throw 'Native execution leaked temporary scheduled tasks.' }
} catch { $failure = $_ }
finally {
    $watch.Dispose()
    $windows = @($watch.Snapshot())
    [ordered]@{
        success = ($null -eq $failure); started_at = $startedAt.ToString('o'); completed_at = [DateTime]::UtcNow.ToString('o')
        launcher_sha256 = (Get-FileHash -LiteralPath $native -Algorithm SHA256).Hash.ToLowerInvariant()
        inno_harness = $InnoHarnessPath
        session = $parentSession; parent_elevated = $parentElevated; cases = $results.ToArray()
        visible_console_events = $windows; observer_error = $watch.Failure
        observer_self_test = 'passed-offscreen-window-show-event'
        error = $(if ($null -eq $failure) { '' } else { $failure.Exception.Message })
    } | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $report -Encoding utf8NoBOM
}
if ($null -ne $failure) { throw $failure }
Write-Host "Native Setup direct and nested no-console execution passed: $report"
