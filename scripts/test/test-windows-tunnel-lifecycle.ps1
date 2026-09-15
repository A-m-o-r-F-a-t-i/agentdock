#requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string] $AgentDockBinary,
    [Parameter(Mandatory = $true)][string] $CloudflaredBinary,
    [Parameter(Mandatory = $true)][string] $HungCloudflaredBinary,
    [Parameter(Mandatory = $true)][string] $RuntimeRoot,
    [ValidateRange(1024, 65535)][int] $Port = 29761,
    [ValidateRange(1, 10)][int] $Cycles = 3,
    [string] $ReportPath = ''
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath($RuntimeRoot)
$production = Join-Path $env:LOCALAPPDATA 'AgentDock'
if (Test-Path -LiteralPath $root) { throw "Test requires a fresh isolated root: $root" }
if ($root.StartsWith($production, [StringComparison]::OrdinalIgnoreCase)) { throw 'Production runtime is not a valid test target.' }
if (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue) { throw "Test port $Port is already occupied." }
foreach ($path in @($AgentDockBinary, $CloudflaredBinary, $HungCloudflaredBinary)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Missing test binary: $path" }
}
$AgentDockBinary = (Resolve-Path $AgentDockBinary).Path
$testHome = Join-Path $root 'test-home'
$workspace = Join-Path $root 'test-workspace'
$logRoot = Join-Path $root 'test-logs'
New-Item -ItemType Directory -Path $root, $testHome, $workspace, $logRoot, (Join-Path $root 'bin') -Force | Out-Null
$steps = [Collections.Generic.List[object]]::new()
$runningControllers = [Collections.Generic.List[Diagnostics.Process]]::new()
$controller = $AgentDockBinary
$sequence = 0
$failure = $null
$startedAt = [DateTime]::UtcNow

function Start-TestCommand {
    param([string[]] $CommandArguments)
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $script:controller
    $start.WorkingDirectory = $root
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($argument in $CommandArguments) { $start.ArgumentList.Add($argument) }
    $start.Environment['AGENTDOCK_HOME'] = $testHome
    $start.Environment['AGENTDOCK_DEFAULT_DIR'] = $workspace
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $start
    if (-not $process.Start()) { throw 'Test controller failed to start.' }
    $runningControllers.Add($process)
    return [pscustomobject]@{
        Process = $process
        Stdout = $process.StandardOutput.ReadToEndAsync()
        Stderr = $process.StandardError.ReadToEndAsync()
        Watch = [Diagnostics.Stopwatch]::StartNew()
    }
}

function Finish-TestCommand {
    param([object] $Command, [string] $Label, [int] $TimeoutSeconds = 180, [switch] $AllowFailure)
    if (-not $Command.Process.WaitForExit($TimeoutSeconds * 1000)) {
        $Command.Process.Kill($true)
        $Command.Process.WaitForExit()
        throw "Controller timed out: $Label"
    }
    $Command.Process.WaitForExit()
    $stdout = $Command.Stdout.GetAwaiter().GetResult()
    $stderr = $Command.Stderr.GetAwaiter().GetResult()
    $Command.Watch.Stop()
    $script:sequence++
    [IO.File]::WriteAllText((Join-Path $logRoot ("{0:00}-{1}.log" -f $script:sequence, $Label)), "$stdout`n$stderr")
    $result = [pscustomobject]@{ action = $Label; elapsed_ms = $Command.Watch.ElapsedMilliseconds; exit_code = $Command.Process.ExitCode }
    $steps.Add($result)
    Write-Host "$Label exit=$($result.exit_code) elapsed_ms=$($result.elapsed_ms)"
    if ($result.exit_code -ne 0 -and -not $AllowFailure) { throw "$Label failed: $stderr $stdout" }
    return [pscustomobject]@{ Result = $result; Stdout = $stdout; Stderr = $stderr }
}

function Invoke-TestAction {
    param([string] $Component, [string] $Action, [int] $TimeoutSeconds = 180)
    return Finish-TestCommand (Start-TestCommand @($Component, $Action, '--runtime-root', $root)) "$Component-$Action" $TimeoutSeconds
}

function Get-TestProcesses {
    $items = @(Get-CimInstance Win32_Process | Where-Object {
        $_.ExecutablePath -and $_.ExecutablePath.StartsWith($root + '\', [StringComparison]::OrdinalIgnoreCase)
    })
    return [pscustomobject]@{
        Core = @($items | Where-Object { $_.CommandLine -match '\bservice\s+launch-core\b' })
        Supervisor = @($items | Where-Object { $_.CommandLine -match '\btunnel\s+launch\b' })
        Cloudflared = @($items | Where-Object { $_.Name -in @('cloudflared.exe', 'cloudflared-hang.exe') })
    }
}

function Assert-TestHealthy {
    param([int] $TimeoutSeconds = 15)
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    do {
        try {
            $health = Invoke-RestMethod "http://127.0.0.1:$Port/healthz" -TimeoutSec 2
            if ($health.ok -and $health.version.TrimStart('v') -eq $version.TrimStart('v')) { return }
        } catch { }
        Start-Sleep -Milliseconds 200
    } while ([DateTime]::UtcNow -lt $deadline)
    throw 'Isolated Core did not become healthy at the expected version.'
}

function Assert-ReadySnapshot {
    Assert-TestHealthy
    $processes = Get-TestProcesses
    if ($processes.Core.Count -ne 1 -or $processes.Supervisor.Count -ne 1 -or $processes.Cloudflared.Count -ne 1) {
        throw "Unexpected process counts: core=$($processes.Core.Count) supervisor=$($processes.Supervisor.Count) cloudflared=$($processes.Cloudflared.Count)"
    }
    $url = (Get-Content (Join-Path $root 'quick-tunnel-url.txt') -Raw).Trim()
    $manifestNow = Get-Content (Join-Path $root 'runtime.json') -Raw | ConvertFrom-Json
    if ($url -notmatch '^https://[a-z0-9-]+\.trycloudflare\.com$' -or $manifestNow.public_url -ne $url) { throw 'Quick URL readiness and manifest do not agree.' }
    return [pscustomobject]@{ core_pid = $processes.Core[0].ProcessId; supervisor_pid = $processes.Supervisor[0].ProcessId; cloudflared_pid = $processes.Cloudflared[0].ProcessId; url = $url }
}

try {
    $metadata = (Finish-TestCommand (Start-TestCommand @('version', '--json')) 'version').Stdout | ConvertFrom-Json
    $version = 'v' + $metadata.version.TrimStart('v')
    $generation = Join-Path $root "versions\$version"
    New-Item -ItemType Directory -Path $generation -Force | Out-Null
    $script:controller = Join-Path $generation 'agentdock-core.exe'
    Copy-Item -LiteralPath $AgentDockBinary -Destination $controller
    Copy-Item -LiteralPath $AgentDockBinary -Destination (Join-Path $root 'bin\agentdock.exe')
    $cloudflared = Join-Path $root 'bin\cloudflared.exe'
    $hungCloudflared = Join-Path $root 'bin\cloudflared-hang.exe'
    Copy-Item -LiteralPath $CloudflaredBinary -Destination $cloudflared
    Copy-Item -LiteralPath $HungCloudflaredBinary -Destination $hungCloudflared
    $unique = 'AgentDockNativeTest-' + [Guid]::NewGuid().ToString('N')
    $manifest = [ordered]@{
        schema_version = 1; install_root = $root; agentdock_binary = (Join-Path $root 'bin\agentdock.exe')
        agentdock_home = $testHome; agentdock_default_dir = $workspace; privilege_mode = 'standard'
        cloudflared_binary = $cloudflared; host = '127.0.0.1'; port = $Port
        local_mcp_url = "http://127.0.0.1:$Port/mcp"; tunnel_mode = 'quick'; public_url = ''; install_channel = 'test'
        startup_value_name = $unique; tray_startup_value_name = "$unique-Tray"; cloudflared_startup_value_name = "$unique-Tunnel"
    }
    $manifest | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $root 'runtime.json') -Encoding utf8NoBOM
    @{ schema_version = 1; state = 'committed'; active_version = $version; updated_at = [DateTime]::UtcNow.ToString('o') } |
        ConvertTo-Json | Set-Content (Join-Path $root 'active-version.json') -Encoding utf8NoBOM
    'quick' | Set-Content (Join-Path $root 'cloudflared-mode.txt') -Encoding utf8NoBOM
    [Security.Principal.WindowsIdentity]::GetCurrent().User.Value | Set-Content (Join-Path $root 'credential-owner-sid.txt') -Encoding utf8NoBOM
    [void](Invoke-TestAction 'service' 'start' 60)
    Assert-TestHealthy
    [void](Invoke-TestAction 'tunnel' 'start')
    $before = Assert-ReadySnapshot
    for ($i = 0; $i -lt 3; $i++) {
        [void](Invoke-TestAction 'tunnel' 'start')
        $after = Assert-ReadySnapshot
        if ($after.supervisor_pid -ne $before.supervisor_pid -or $after.cloudflared_pid -ne $before.cloudflared_pid -or $after.url -ne $before.url) { throw 'Idempotent start replaced an already managed generation Tunnel.' }
    }
    for ($i = 0; $i -lt $Cycles; $i++) {
        $action = if ($i % 2 -eq 0) { 'regenerate' } else { 'restart' }
        [void](Invoke-TestAction 'tunnel' $action)
        $after = Assert-ReadySnapshot
        if ($after.url -eq $before.url -or $after.supervisor_pid -eq $before.supervisor_pid) { throw 'Regeneration did not replace the prior supervised Quick Tunnel.' }
        $before = $after
    }
    for ($i = 0; $i -lt 2; $i++) {
        $stop = Invoke-TestAction 'tunnel' 'stop' 20
        if ($stop.Result.elapsed_ms -ge 10000) { throw 'Tunnel stop exceeded the 10 second regression budget.' }
    }
    Assert-TestHealthy

    # Replace only this test manifest's child with a deliberately non-ready
    # fixture; stop must cancel provisioning instead of waiting 35/125 seconds.
    $manifestNow = Get-Content (Join-Path $root 'runtime.json') -Raw | ConvertFrom-Json
    $manifestNow.cloudflared_binary = $hungCloudflared
    $manifestNow | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $root 'runtime.json') -Encoding utf8NoBOM
    $pendingStart = Start-TestCommand @('tunnel', 'start', '--runtime-root', $root)
    $deadline = [DateTime]::UtcNow.AddSeconds(10)
    do {
        if ((Get-TestProcesses).Cloudflared.Count -gt 0) { break }
        Start-Sleep -Milliseconds 100
    } while ([DateTime]::UtcNow -lt $deadline)
    if ((Get-TestProcesses).Cloudflared.Count -ne 1) { throw 'Non-ready fixture did not start.' }
    $stop = Invoke-TestAction 'tunnel' 'stop' 20
    if ($stop.Result.elapsed_ms -ge 10000) { throw 'Stop did not preempt an in-flight start.' }
    $interrupted = Finish-TestCommand $pendingStart 'interrupted-start' 10 -AllowFailure
    if ($interrupted.Result.exit_code -eq 0) { throw 'Non-ready fixture was incorrectly reported ready.' }
    $remaining = Get-TestProcesses
    if ($remaining.Cloudflared.Count -ne 0 -or $remaining.Supervisor.Count -ne 0) { throw 'Stop left a supervisor or child process behind.' }
    Assert-TestHealthy

    # Also stop while the real Quick URL is being applied to Core. This window
    # includes stopping the old Core and health-checking the replacement.
    $manifestNow = Get-Content (Join-Path $root 'runtime.json') -Raw | ConvertFrom-Json
    $manifestNow.cloudflared_binary = $cloudflared
    $manifestNow | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $root 'runtime.json') -Encoding utf8NoBOM
    $reloadStart = Start-TestCommand @('tunnel', 'start', '--runtime-root', $root)
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    $reloadObserved = $false
    do {
        $applyingURL = ''
        try { $applyingURL = [IO.File]::ReadAllText((Join-Path $root 'server-url.txt')).Trim() } catch { }
        if ($applyingURL -ne '' -and -not (Test-Path (Join-Path $root 'quick-tunnel-url.txt'))) { $reloadObserved = $true; break }
        Start-Sleep -Milliseconds 5
    } while ([DateTime]::UtcNow -lt $deadline)
    if (-not $reloadObserved) { throw 'Core reload cancellation window was not observed; test is inconclusive.' }
    $stop = Invoke-TestAction 'tunnel' 'stop' 20
    if ($stop.Result.elapsed_ms -ge 10000) { throw 'Stopping a Core reload exceeded the regression budget.' }
    [void](Finish-TestCommand $reloadStart 'core-reload-interrupted-start' 10 -AllowFailure)
    Assert-TestHealthy
    $remaining = Get-TestProcesses
    if ($remaining.Cloudflared.Count -ne 0 -or $remaining.Supervisor.Count -ne 0) { throw 'Core reload cancellation left Tunnel processes behind.' }
} catch {
    $failure = $_
} finally {
    if (Test-Path (Join-Path $root 'runtime.json')) {
        try { [void](Invoke-TestAction 'tunnel' 'stop' 20) } catch { if ($null -eq $failure) { $failure = $_ } }
        try { [void](Invoke-TestAction 'service' 'stop' 20) } catch { if ($null -eq $failure) { $failure = $_ } }
    }
    foreach ($process in $runningControllers) {
        try { if (-not $process.HasExited) { $process.Kill($true); $process.WaitForExit() } } catch { }
        $process.Dispose()
    }
    if ([string]::IsNullOrWhiteSpace($ReportPath)) { $ReportPath = Join-Path $root 'result.json' }
    $report = [ordered]@{
        success = ($null -eq $failure); started_at = $startedAt.ToString('o'); completed_at = [DateTime]::UtcNow.ToString('o')
        runtime_root = $root; port = $Port; regeneration_cycles = $Cycles; steps = $steps.ToArray()
        candidate_sha256 = (Get-FileHash -LiteralPath $AgentDockBinary -Algorithm SHA256).Hash.ToLowerInvariant()
        cloudflared_sha256 = (Get-FileHash -LiteralPath $CloudflaredBinary -Algorithm SHA256).Hash.ToLowerInvariant()
        error = $(if ($null -eq $failure) { '' } else { $failure.Exception.Message })
    }
    $report | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $ReportPath -Encoding utf8NoBOM
}
if ($null -ne $failure) { throw $failure }
Write-Host "Windows isolated real Tunnel lifecycle validation passed: $ReportPath"
