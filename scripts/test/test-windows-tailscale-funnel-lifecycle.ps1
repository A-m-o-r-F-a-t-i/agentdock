#requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string] $Archive,
    [Parameter(Mandatory=$true)][string] $FakeTailscaleBinary,
    [Parameter(Mandatory=$true)][string] $ReportRoot,
    [string] $ExpectedVersion = '1.1.0'
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$repository = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$Archive = (Resolve-Path -LiteralPath $Archive).Path
$FakeTailscaleBinary = (Resolve-Path -LiteralPath $FakeTailscaleBinary).Path
$root = [IO.Path]::GetFullPath($ReportRoot)
if (Test-Path -LiteralPath $root) { throw 'Funnel installer E2E requires a fresh fixture directory.' }
if (Get-ScheduledTask -TaskName 'AgentDock' -TaskPath '\' -ErrorAction SilentlyContinue) { throw 'Do not run installer E2E next to a production elevated AgentDock task.' }
$runtimeRoot = Join-Path $root 'runtime with spaces'
$homeRoot = Join-Path $root 'user-state'
$workspace = Join-Path $root 'workspace'
$fixturePath = Join-Path $root 'fake-tailscale.json'
$installer = Join-Path $repository 'scripts\install\install.ps1'
$uninstaller = Join-Path $repository 'scripts\install\uninstall-windows.ps1'
$prefix = 'AgentDockFunnelE2E-' + [Guid]::NewGuid().ToString('N')
$names = @("$prefix-Core","$prefix-Tray","$prefix-Cloudflare")
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$originalEnvironment = @{}
Get-ChildItem Env:AGENTDOCK_* | ForEach-Object { $originalEnvironment[$_.Name]=$_.Value }
$originalFakeState = $env:FAKE_TAILSCALE_STATE
$utf8 = [Text.UTF8Encoding]::new($false)
$listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,0)
$listener.Start(); $port=$listener.LocalEndpoint.Port; $listener.Stop()
$origin = 'https://device.example-tailnet.ts.net'
$cases = [Collections.Generic.List[string]]::new()
$passed = $false
New-Item -ItemType Directory -Force -Path $root,$runtimeRoot,$homeRoot,$workspace | Out-Null

function Save-Json([string] $Path, [object] $Value) {
    [IO.File]::WriteAllText($Path, ($Value | ConvertTo-Json -Depth 12), $utf8)
}
function Invoke-TestProcess([string] $Executable, [string[]] $Arguments, [string] $Name, [bool] $ExpectFailure=$false) {
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName=$Executable; $start.WorkingDirectory=$root
    $start.UseShellExecute=$false; $start.CreateNoWindow=$true
    $start.RedirectStandardOutput=$true; $start.RedirectStandardError=$true
    foreach($argument in $Arguments){$start.ArgumentList.Add($argument)}
    $process=[Diagnostics.Process]::new();$process.StartInfo=$start
    try {
        if(-not $process.Start()){throw "$Name did not start."}
        $out=$process.StandardOutput.ReadToEndAsync();$err=$process.StandardError.ReadToEndAsync()
        if(-not $process.WaitForExit(240000)){$process.Kill($true);$process.WaitForExit();throw "$Name timed out."}
        $output=$out.GetAwaiter().GetResult()+"`n"+$err.GetAwaiter().GetResult()
        $output=$output -replace '(?im)(Bearer Token:|OAuth login password:)[^\r\n]*','$1 [REDACTED]'
        [IO.File]::WriteAllText((Join-Path $root "$Name.log"),$output,$utf8)
        if(($process.ExitCode -eq 0) -eq $ExpectFailure){throw "$Name returned unexpected exit code $($process.ExitCode); inspect the isolated log."}
    } finally {$process.Dispose()}
}
function Invoke-FixtureInstall([string] $Script,[string] $Name,[bool] $ExpectFailure=$false) {
    $arguments=@('-NoLogo','-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',$Script,
        '-Version','latest','-OfflineArchive',$Archive,'-OfflineChecksumFile',"$Archive.sha256",
        '-InstallDir',(Join-Path $runtimeRoot 'bin'),'-InstallChannel','setup','-RegisterStartup',
        '-CorePrivilegeMode','standard','-TunnelMode','none','-Port',"$port",'-StartupValueName',$names[0],
        '-TrayStartupValueName',$names[1],'-CloudflaredStartupValueName',$names[2])
    Invoke-TestProcess (Join-Path $env:WINDIR 'System32\WindowsPowerShell\v1.0\powershell.exe') $arguments $Name $ExpectFailure
}
function Assert-FixtureHealthy {
    $deadline=[DateTime]::UtcNow.AddSeconds(30)
    do {
        try {$health=Invoke-RestMethod "http://127.0.0.1:$port/healthz" -TimeoutSec 2;if($health.version -eq $ExpectedVersion){return}}catch{}
        Start-Sleep -Milliseconds 200
    }while([DateTime]::UtcNow -lt $deadline)
    throw 'The isolated Core did not recover at the expected version.'
}
function Assert-FunnelPreserved([hashtable] $Files) {
    Assert-FixtureHealthy
    $manifest=Get-Content -LiteralPath (Join-Path $runtimeRoot 'runtime.json') -Raw | ConvertFrom-Json
    if($manifest.public_access_provider -ne 'tailscale' -or $manifest.public_access_mode -ne 'funnel' -or $manifest.public_access_url -ne $origin -or $manifest.tailscale_binary -ne $FakeTailscaleBinary -or $manifest.tunnel_mode -ne 'none' -or $manifest.port -ne $port){throw 'Installer lost the provider projection or port.'}
    if($manifest.PSObject.Properties['public_url'] -and $manifest.public_url){throw 'Legacy public_url must remain empty.'}
    foreach($name in $Files.Keys){if([IO.File]::ReadAllText((Join-Path $runtimeRoot $name)) -ne $Files[$name]){throw "Installer changed preserved $name"}}
    if([IO.File]::ReadAllText((Join-Path $homeRoot 'user-marker.txt')) -ne 'preserve'){throw 'Installer changed user data.'}
    $fixture=Get-Content $fixturePath -Raw | ConvertFrom-Json
    $writers=@($fixture.commands | Where-Object {$_ -contains '--set-path=/'})
    if($writers.Count -ne 0){throw 'Installer unexpectedly reconfigured the persistent Funnel.'}
}

try {
    foreach($key in $originalEnvironment.Keys){[Environment]::SetEnvironmentVariable($key,$null,'Process')}
    $env:AGENTDOCK_HOME=$homeRoot;$env:AGENTDOCK_DEFAULT_DIR=$workspace
    $env:FAKE_TAILSCALE_STATE=$fixturePath
    [IO.File]::WriteAllText((Join-Path $homeRoot 'user-marker.txt'),'preserve',$utf8)
    Save-Json $fixturePath @{config=@{};commands=@()}
    Invoke-FixtureInstall $installer 'fresh-local'
    Assert-FixtureHealthy
    $cases.Add('fresh local install')
    # Seed only this isolated test runtime as an already configured Funnel.
    # The real Core/DPAPI/installer run normally; the fake binary never accesses tailscaled.
    $manifestPath=Join-Path $runtimeRoot 'runtime.json'
    $manifest=Get-Content $manifestPath -Raw | ConvertFrom-Json -AsHashtable
    $manifest['public_access_provider']='tailscale';$manifest['public_access_mode']='funnel'
    $manifest['public_access_url']=$origin;$manifest['tailscale_binary']=$FakeTailscaleBinary
    $manifest['tunnel_mode']='none';$manifest.Remove('public_url')
    Save-Json $manifestPath $manifest
    [IO.File]::WriteAllText((Join-Path $runtimeRoot 'server-url.txt'),$origin,$utf8)
    $stamp=[DateTime]::UtcNow.ToString('o')
    Save-Json (Join-Path $runtimeRoot 'tailscale-funnel-state.json') @{
        schema_version=1;managed=$true;enabled=$true;public_origin=$origin;https_port=443
        local_origin="http://127.0.0.1:$port";device_id='fake-node';dns_name='device.example-tailnet.ts.net'
        configured_at=$stamp;verified_at=$stamp
    }
    Save-Json $fixturePath @{commands=@();config=@{
        TCP=@{'443'=@{HTTPS=$true}}
        Web=@{'device.example-tailnet.ts.net:443'=@{Handlers=@{'/'=@{Proxy="http://127.0.0.1:$port"};'/other'=@{Proxy='http://127.0.0.1:9000'}}}}
        AllowFunnel=@{'device.example-tailnet.ts.net:443'=$true}
    }}
    $files=@{}
    foreach($name in @('server-url.txt','tailscale-funnel-state.json','control-panel-settings.json')){
        $path=Join-Path $runtimeRoot $name;if(Test-Path $path){$files[$name]=[IO.File]::ReadAllText($path)}
    }
    Get-ChildItem -LiteralPath $runtimeRoot -Filter '*.dpapi' -File | ForEach-Object {$files[$_.Name]=[IO.File]::ReadAllText($_.FullName)}
    Invoke-FixtureInstall $installer 'funnel-repair'
    Assert-FunnelPreserved $files
    $cases.Add('repair preserves provider, port, Origin, ownership and credentials')
    $faultDir=Join-Path $root 'fault';New-Item -ItemType Directory -Path $faultDir | Out-Null
    Copy-Item (Join-Path $repository 'scripts\install\*') $faultDir
    $faultScript=Join-Path $faultDir 'install.ps1'
    $source=[IO.File]::ReadAllText($faultScript)
    $needle='    # Provision is complete here.'
    if(($source.Split(@($needle),[StringSplitOptions]::None)).Count -ne 2){throw 'Unique installer failure-injection point missing.'}
    [IO.File]::WriteAllText($faultScript,$source.Replace($needle,"    throw 'isolated Funnel install failure before commit'`r`n"+$needle),[Text.UTF8Encoding]::new($true))
    Invoke-FixtureInstall $faultScript 'funnel-rollback' $true
    $faultOutput = [IO.File]::ReadAllText((Join-Path $root 'funnel-rollback.log'))
    if (-not $faultOutput.Contains('isolated Funnel install failure before commit')) {
        throw ('Failure occurred before the intended rollback injection: ' + $faultOutput.Substring([Math]::Max(0,$faultOutput.Length-5000)))
    }
    Assert-FunnelPreserved $files
    $transaction=Get-Content (Join-Path $runtimeRoot 'install\transaction.json') -Raw | ConvertFrom-Json
    if($transaction.state -ne 'rolled_back'){throw ('Failed repair did not roll back: state=' + $transaction.state + '. ' + $faultOutput.Substring([Math]::Max(0,$faultOutput.Length-5000)))}
    $cases.Add('failed repair restores Core and complete Funnel configuration')
    $arguments=@('-NoLogo','-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',$uninstaller,'-InstallDir',(Join-Path $runtimeRoot 'bin'))
    Invoke-TestProcess (Join-Path $env:WINDIR 'System32\WindowsPowerShell\v1.0\powershell.exe') $arguments 'funnel-uninstall'
    $fixture=Get-Content $fixturePath -Raw | ConvertFrom-Json -AsHashtable
    $handlers=$fixture.config.Web['device.example-tailnet.ts.net:443'].Handlers
    if($handlers.ContainsKey('/') -or $handlers['/other'].Proxy -ne 'http://127.0.0.1:9000'){throw 'Uninstall did not preserve the unrelated mapping.'}
    if(-not(Test-Path $FakeTailscaleBinary) -or [IO.File]::ReadAllText((Join-Path $homeRoot 'user-marker.txt')) -ne 'preserve'){throw 'Uninstall removed client or user data.'}
    foreach($command in $fixture.commands){if($command -contains 'reset' -or $command -contains 'down' -or $command -contains 'logout'){throw 'Unsafe Tailscale command observed.'}}
    $cases.Add('uninstall removes only AgentDock root and preserves client, sibling mapping and user data')
    $passed=$true
} finally {
    Get-CimInstance Win32_Process | Where-Object {$_.ExecutablePath -and $_.ExecutablePath.StartsWith($root+'\',[StringComparison]::OrdinalIgnoreCase)} | ForEach-Object {Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue}
    foreach($name in $names){Remove-ItemProperty -LiteralPath $runKey -Name $name -ErrorAction SilentlyContinue}
    $currentPath=[Environment]::GetEnvironmentVariable('Path','User')
    $cleanPath=($currentPath -split ';' | Where-Object {-not $_.Trim().StartsWith($root+'\',[StringComparison]::OrdinalIgnoreCase)}) -join ';'
    [Environment]::SetEnvironmentVariable('Path',$cleanPath,'User')
    Get-ChildItem Env:AGENTDOCK_* | ForEach-Object {[Environment]::SetEnvironmentVariable($_.Name,$null,'Process')}
    foreach($key in $originalEnvironment.Keys){[Environment]::SetEnvironmentVariable($key,$originalEnvironment[$key],'Process')}
    $env:FAKE_TAILSCALE_STATE=$originalFakeState
    Save-Json (Join-Path $root 'result.json') @{passed=$passed;version=$ExpectedVersion;cases=$cases.ToArray();real_tailscale_modified=$false}
}
if(-not $passed){throw 'Funnel installer lifecycle failed.'}
Write-Host 'Tailscale installer lifecycle passed; no real Tailscale configuration was modified.'
