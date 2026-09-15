#requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string] $SourceHome,
    [Parameter(Mandatory = $true)][string] $OldBinary,
    [Parameter(Mandatory = $true)][string] $NewBinary,
    [Parameter(Mandatory = $true)][string] $TestRoot,
    [ValidateRange(1024, 65535)][int] $Port = 29762,
    [ValidateRange(1, 20)][int] $Samples = 5
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath($TestRoot)
if (Test-Path -LiteralPath $root) { throw 'Inventory comparison requires a fresh test root.' }
if (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue) { throw 'Inventory test port is occupied.' }
$testHome = Join-Path $root 'home'
$workspace = Join-Path $root 'workspace'
New-Item -ItemType Directory -Path $testHome, $workspace -Force | Out-Null
# Only portable capability files and switches. No production tokens, OAuth,
# Nexus, tasks, runtime startup state or secret environment is copied.
foreach ($name in @('skills', 'plugins', 'mcp')) {
    $source = Join-Path $SourceHome $name
    if (Test-Path $source) {
        & robocopy $source (Join-Path $testHome $name) /E /XJ /R:0 /W:0 /XD .locks .tmp .data /NFL /NDL /NJH /NJS /NP > $null
        if ($LASTEXITCODE -ge 8) { throw "Capability copy failed: $name" }
    }
}
$rows = [Collections.Generic.List[object]]::new()
$identities = @{}
$failure = $null
$started = [DateTime]::UtcNow
try {
    foreach ($entry in @(@{ name = 'old'; binary = $OldBinary }, @{ name = 'new'; binary = $NewBinary })) {
        $candidate = Join-Path $root ($entry.name + '.exe')
        Copy-Item -LiteralPath $entry.binary -Destination $candidate
        $start = [Diagnostics.ProcessStartInfo]::new()
        $start.FileName = $candidate
        $start.WorkingDirectory = $workspace
        $start.UseShellExecute = $false
        $start.CreateNoWindow = $true
        $start.RedirectStandardOutput = $true
        $start.RedirectStandardError = $true
        foreach ($key in @($start.Environment.Keys)) { if ($key.StartsWith('AGENTDOCK_', [StringComparison]::OrdinalIgnoreCase)) { [void]$start.Environment.Remove($key) } }
        $token = [Guid]::NewGuid().ToString('N')
        $start.Environment['AGENTDOCK_HOME'] = $testHome
        $start.Environment['AGENTDOCK_DEFAULT_DIR'] = $workspace
        $start.Environment['AGENTDOCK_AUTH_TOKEN'] = $token
        foreach ($argument in @('-host', '127.0.0.1', '-port', [string]$Port)) { $start.ArgumentList.Add($argument) }
        $process = [Diagnostics.Process]::new()
        $process.StartInfo = $start
        $client = [Net.Http.HttpClient]::new()
        $client.Timeout = [TimeSpan]::FromSeconds(15)
        $client.DefaultRequestHeaders.Authorization = [Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer', $token)
        if (-not $process.Start()) { throw 'Isolated inventory Core did not start.' }
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        try {
            $ready = $false
            $deadline = [DateTime]::UtcNow.AddSeconds(45)
            do {
                if ($process.HasExited) { throw "Isolated $($entry.name) Core exited: $($stderr.GetAwaiter().GetResult())" }
                try { $response = $client.GetAsync("http://127.0.0.1:$Port/healthz").GetAwaiter().GetResult(); $ready = $response.IsSuccessStatusCode; $response.Dispose() } catch { }
                if (-not $ready) { Start-Sleep -Milliseconds 100 }
            } while (-not $ready -and [DateTime]::UtcNow -lt $deadline)
            if (-not $ready) { throw 'Isolated inventory Core did not become healthy.' }
            for ($sample = 1; $sample -le $Samples; $sample++) {
                foreach ($route in @('plugins', 'skills', 'skills?summary=true', 'mcp')) {
                    $watch = [Diagnostics.Stopwatch]::StartNew()
                    $response = $client.GetAsync("http://127.0.0.1:$Port/internal/runtime/$route").GetAwaiter().GetResult()
                    $body = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
                    $watch.Stop()
                    if (-not $response.IsSuccessStatusCode) { throw "Inventory $route failed: $([int]$response.StatusCode)" }
                    $payload = $body | ConvertFrom-Json
                    $property = if ($route.StartsWith('skills')) { 'skills' } elseif ($route -eq 'mcp') { 'servers' } else { 'plugins' }
                    $items = @($payload.$property)
                    $phase = if ($payload.PSObject.Properties['timing_ms']) { $payload.timing_ms } else { $null }
                    $rows.Add([pscustomobject]@{ binary = $entry.name; sample = $sample; route = $route; elapsed_ms = $watch.ElapsedMilliseconds; count = $items.Count; timing_ms = $phase })
                    if ($sample -eq 1) {
                        $names = @($items | ForEach-Object { if ($property -eq 'skills') { $_.skill } else { $_.name } } | Sort-Object)
                        $identity = $names | ConvertTo-Json -Compress
                        if ($entry.name -eq 'old') { $identities[$route] = $identity }
                        elseif ($identities[$route] -ne $identity) { throw "Inventory membership changed during optimization: $route" }
                    }
                    $response.Dispose()
                }
            }
        } finally {
            $client.Dispose()
            if (-not $process.HasExited) { $process.Kill($true) }
            $process.WaitForExit()
            [IO.File]::WriteAllText((Join-Path $root ($entry.name + '-stdout.log')), $stdout.GetAwaiter().GetResult())
            [IO.File]::WriteAllText((Join-Path $root ($entry.name + '-stderr.log')), $stderr.GetAwaiter().GetResult())
            $process.Dispose()
            $token = $null
        }
    }
} catch { $failure = $_ }
finally {
    [ordered]@{
        success = ($null -eq $failure); started_at = $started.ToString('o'); completed_at = [DateTime]::UtcNow.ToString('o')
        samples = $Samples; port = $Port; rows = $rows.ToArray(); membership_unchanged = ($null -eq $failure)
        old_sha256 = (Get-FileHash $OldBinary -Algorithm SHA256).Hash.ToLowerInvariant()
        new_sha256 = (Get-FileHash $NewBinary -Algorithm SHA256).Hash.ToLowerInvariant()
        error = $(if ($null -eq $failure) { '' } else { $failure.Exception.Message })
    } | ConvertTo-Json -Depth 10 | Set-Content (Join-Path $root 'result.json') -Encoding utf8NoBOM
}
if ($null -ne $failure) { throw $failure }
$rows | Format-Table binary,sample,route,elapsed_ms,count -AutoSize
Write-Host 'Isolated old/new capability inventory comparison passed.'
