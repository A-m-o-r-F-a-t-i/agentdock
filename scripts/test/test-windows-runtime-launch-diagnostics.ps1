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
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('agentdock runtime diagnostics test ' + [Guid]::NewGuid().ToString('N'))
$childScript = Join-Path $testRoot 'child.ps1'
$taskPrefix = 'AgentDock Setup Runtime '
$tempPrefix = 'agentdock-setup-runtime-'
$beforeTasks = @(Get-ScheduledTask -ErrorAction Stop | Where-Object { $_.TaskName.StartsWith($taskPrefix) } | ForEach-Object TaskName)
$beforeTempDirs = @(Get-ChildItem -LiteralPath ([IO.Path]::GetTempPath()) -Directory -Filter "$tempPrefix*" -ErrorAction SilentlyContinue | ForEach-Object FullName)

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
        'Task Scheduler result: 4294967295',
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
    Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
}
