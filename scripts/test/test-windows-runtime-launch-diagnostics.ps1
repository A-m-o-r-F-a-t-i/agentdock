[CmdletBinding()]
param(
    [string] $LauncherPath = '',
    [Parameter(Mandatory = $true)]
    [ValidateNotNullOrEmpty()]
    [string] $AgentDockBinary
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ([string]::IsNullOrWhiteSpace($LauncherPath)) {
    $LauncherPath = Join-Path $PSScriptRoot '..\install\launch-windows-process.ps1'
}
$resolvedLauncher = (Resolve-Path -LiteralPath $LauncherPath).Path
$resolvedAgentDockBinary = (Resolve-Path -LiteralPath $AgentDockBinary).Path
$nativeLauncher = Join-Path (Split-Path $resolvedAgentDockBinary) 'agentdock-tray-shim.exe'
$image = [IO.File]::ReadAllBytes($nativeLauncher)
$peOffset = [BitConverter]::ToInt32($image, 0x3c)
if ([BitConverter]::ToUInt16($image, $peOffset + 24 + 68) -ne 2) {
    throw 'Native Setup launcher is not a GUI-subsystem executable.'
}
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('agentdock runtime diagnostics test ' + [Guid]::NewGuid().ToString('N'))
$childScript = Join-Path $testRoot 'child.ps1'
$taskPrefix = 'AgentDock Setup Native '
$tempPrefix = 'agentdock-setup-runtime-'
$beforeTasks = @(Get-ScheduledTask -ErrorAction Stop | Where-Object { $_.TaskName.StartsWith($taskPrefix) } | ForEach-Object TaskName)
$beforeTempDirs = @(Get-ChildItem -LiteralPath ([IO.Path]::GetTempPath()) -Directory -Filter "$tempPrefix*" -ErrorAction SilentlyContinue | ForEach-Object FullName)
$longChildPid = 0
$longChildScript = Join-Path $testRoot 'long-lived-child.ps1'

try {
    New-Item -ItemType Directory -Path $testRoot -Force | Out-Null
    [IO.File]::WriteAllText(
        $childScript,
        "[Console]::Out.WriteLine('runtime-diagnostic-stdout')`r`n" +
            "[Console]::Error.WriteLine('runtime-diagnostic-stderr')`r`n" +
            "exit -1`r`n",
        [Text.UTF8Encoding]::new($false)
    )

    $arguments = "-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File `"$childScript`""
    $failureMessage = ''
    try {
        & $resolvedLauncher `
            -FilePath (Join-Path $PSHOME 'powershell.exe') `
            -AgentDockBinary $resolvedAgentDockBinary `
            -Arguments $arguments `
            -WaitForExit `
            -TimeoutSeconds 30
    } catch {
        $failureMessage = $_.Exception.Message
    }

    if ([string]::IsNullOrWhiteSpace($failureMessage)) {
        throw 'Runtime launcher unexpectedly reported success for exit code -1.'
    }
    foreach ($expected in @(
        'Runtime process exited with exit code -1',
        'Child exit status (unsigned): 4294967295',
        'stderr: runtime-diagnostic-stderr',
        'stdout: runtime-diagnostic-stdout'
    )) {
        if (-not $failureMessage.Contains($expected)) {
            throw "Runtime launcher diagnostics are missing '$expected':`n$failureMessage"
        }
    }

    [IO.File]::WriteAllText(
        $childScript,
        "[Console]::Out.WriteLine('runtime-success')`r`nexit 0`r`n",
        [Text.UTF8Encoding]::new($false)
    )
    & $resolvedLauncher `
        -FilePath (Join-Path $PSHOME 'powershell.exe') `
        -AgentDockBinary $resolvedAgentDockBinary `
        -Arguments $arguments `
        -WaitForExit `
        -TimeoutSeconds 30

    # Confirm JSON round-trips UTF-8 paths without host codepage substitution.
    $payload = '{"path":"' + [char]0x6D4B + [char]0x8BD5 + ' with spaces"}'
    [IO.File]::WriteAllText($childScript,
        "[Console]::OutputEncoding=[Text.Encoding]::UTF8; [Console]::WriteLine('" + $payload + "'); exit 0",
        [Text.UTF8Encoding]::new($true))
    $returned = & $resolvedLauncher -FilePath (Join-Path $PSHOME 'powershell.exe') `
        -AgentDockBinary $resolvedAgentDockBinary -Arguments $arguments -WaitForExit -PassThruOutput
    if ($returned.Trim() -ne $payload) { throw 'UTF-8 Engine JSON was not preserved.' }

    # Repeated fast no-wait launches must acknowledge Process.Start, not a timestamp.
    for ($i = 0; $i -lt 5; $i++) {
        & $resolvedLauncher -FilePath (Join-Path $env:WINDIR 'System32\cmd.exe') `
            -AgentDockBinary $resolvedAgentDockBinary -Arguments '/c exit 0'
    }

    # Task completion/deletion must not terminate the released background Tray.
    # Use an isolated long-lived child rather than the production Tray process.
    $pidPath = Join-Path $testRoot 'long-lived-child.pid'
    $escapedPidPath = $pidPath.Replace("'", "''")
    [IO.File]::WriteAllText($longChildScript,
        "[IO.File]::WriteAllText('$escapedPidPath', [string]`$PID); Start-Sleep -Seconds 30",
        [Text.UTF8Encoding]::new($false))
    $longArguments = "-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File `"$longChildScript`""
    & $resolvedLauncher -FilePath (Join-Path $PSHOME 'powershell.exe') -AgentDockBinary $resolvedAgentDockBinary -Arguments $longArguments
    $deadline = [DateTime]::UtcNow.AddSeconds(5)
    while (-not (Test-Path $pidPath) -and [DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 50 }
    if (-not (Test-Path $pidPath)) { throw 'Released child never acknowledged its start.' }
    $longChildPid = [int]([IO.File]::ReadAllText($pidPath).Trim())
    Start-Sleep -Milliseconds 750
    $longChild = Get-CimInstance Win32_Process -Filter "ProcessId=$longChildPid"
    if ($null -eq $longChild -or -not $longChild.CommandLine.Contains($longChildScript)) { throw 'Task deletion terminated the released child.' }
    Stop-Process -Id $longChildPid -ErrorAction Stop
    $longChildPid = 0

    # A reused request path must not acknowledge an earlier successful receipt.
    $retryRoot = Join-Path $testRoot 'retry'
    New-Item -ItemType Directory -Path $retryRoot -Force | Out-Null
    $retryRequest = Join-Path $retryRoot 'request.json'
    $utf8 = New-Object Text.UTF8Encoding($false)
    $request = @{
        file_path = (Join-Path $env:WINDIR 'System32\cmd.exe'); arguments = '/c exit 23'
        wait_for_exit = $true; timeout_seconds = 10; environment = @{}
    }
    [IO.File]::WriteAllText($retryRequest, ($request | ConvertTo-Json -Depth 6), $utf8)
    [IO.File]::WriteAllText((Join-Path $retryRoot 'result.json'), '{"task_name":"stale","exit_code":0,"pid":1}', $utf8)
    $retry = New-Object Diagnostics.Process
    try {
        $retry.StartInfo = New-Object Diagnostics.ProcessStartInfo
        $retry.StartInfo.FileName = $nativeLauncher
        $retry.StartInfo.Arguments = "--setup-launch `"$retryRequest`""
        $retry.StartInfo.UseShellExecute = $false
        $retry.StartInfo.CreateNoWindow = $true
        $retry.StartInfo.RedirectStandardError = $true
        if (-not $retry.Start()) { throw 'Retry test did not start.' }
        $stderrTask = $retry.StandardError.ReadToEndAsync()
        if (-not $retry.WaitForExit(30000)) { $retry.Kill(); throw 'Retry test timed out.' }
        $retry.WaitForExit()
        $retryError = $stderrTask.GetAwaiter().GetResult()
        if ($retry.ExitCode -eq 0 -or -not $retryError.Contains('exit code 23')) { throw "A stale receipt masked the current child failure: $retryError" }
    } finally { $retry.Dispose() }

    $afterTasks = @(Get-ScheduledTask -ErrorAction Stop | Where-Object { $_.TaskName.StartsWith($taskPrefix) } | ForEach-Object TaskName)
    $newTasks = @($afterTasks | Where-Object { $_ -notin $beforeTasks })
    if ($newTasks.Count -gt 0) {
        throw "Runtime launcher left temporary scheduled tasks behind: $($newTasks -join ', ')"
    }

    $afterTempDirs = @(Get-ChildItem -LiteralPath ([IO.Path]::GetTempPath()) -Directory -Filter "$tempPrefix*" -ErrorAction SilentlyContinue | ForEach-Object FullName)
    $newTempDirs = @($afterTempDirs | Where-Object { $_ -notin $beforeTempDirs })
    if ($newTempDirs.Count -gt 0) {
        throw "Runtime launcher left temporary diagnostic directories behind: $($newTempDirs -join ', ')"
    }

    Write-Host 'Windows Setup runtime diagnostic launcher validation passed.'
} finally {
    if ($longChildPid -gt 0) {
        $ownedChild = Get-CimInstance Win32_Process -Filter "ProcessId=$longChildPid" -ErrorAction SilentlyContinue
        if ($null -ne $ownedChild -and $ownedChild.CommandLine.Contains($longChildScript)) { Stop-Process -Id $longChildPid -ErrorAction SilentlyContinue }
    }
    Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
}
