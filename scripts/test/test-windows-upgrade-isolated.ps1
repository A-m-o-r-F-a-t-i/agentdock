[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$Archive,
    [Parameter(Mandatory=$true)][string]$BaselineArchive,
    [Parameter(Mandatory=$true)][string]$CloudflaredBinary,
    [Parameter(Mandatory=$true)][string]$ReportPath,
    [string]$ExpectedVersion='1.0.0',
    [string]$InstallerPath='',
    [string]$BaselineInstallerPath='',
    [ValidateSet('script','setup')][string]$BaselineChannel='script',
    [switch]$SeedStaleEnvironment,
    [switch]$ExerciseRollback
)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$ProgressPreference='SilentlyContinue'
if(-not $InstallerPath){$InstallerPath=Join-Path $PSScriptRoot '..\install\install.ps1'}
$InstallerPath=(Resolve-Path -LiteralPath $InstallerPath).Path
if(-not $BaselineInstallerPath){$BaselineInstallerPath=$InstallerPath}
$BaselineInstallerPath=(Resolve-Path -LiteralPath $BaselineInstallerPath).Path
$Archive=(Resolve-Path -LiteralPath $Archive).Path
$BaselineArchive=(Resolve-Path -LiteralPath $BaselineArchive).Path
$CloudflaredBinary=(Resolve-Path -LiteralPath $CloudflaredBinary).Path
$ReportPath=[IO.Path]::GetFullPath($ReportPath)
$utf8=New-Object Text.UTF8Encoding($false)
$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('agentdock upgrade 回归 '+[Guid]::NewGuid().ToString('N'))
$runtimeRoot=Join-Path $testRoot '安装目录 with spaces'
$installDir=Join-Path $runtimeRoot 'bin'
$homeRoot=Join-Path $testRoot '用户数据'
$workRoot=Join-Path $testRoot '工作目录'
$suffix=[Guid]::NewGuid().ToString('N')
$names=@("AgentDockTestCore-$suffix","AgentDockTestTray-$suffix","AgentDockTestTunnel-$suffix")
$runKey='HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$oldEnv=@{}
Get-ChildItem Env:AGENTDOCK_* | ForEach-Object {$oldEnv[$_.Name]=$_.Value}
$listener=New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback,0)
$listener.Start();$port=$listener.LocalEndpoint.Port;$listener.Stop()
$cases=New-Object Collections.Generic.List[object]
$allPassed=$false
$baselineVersion=''
$targetVersion=''

function Save-Report {
    $report=[ordered]@{ passed=$allPassed; baseline=$baselineVersion; target=$targetVersion; platform='windows/amd64'; privilege='standard'; custom_path='Chinese and spaces'; stale_runtime_environment=[bool]$SeedStaleEnvironment; install_channel='setup script'; gui_window='not asserted (shared desktop singleton)'; cases=@($cases.ToArray()) }
    [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($ReportPath)) | Out-Null
    [IO.File]::WriteAllText($ReportPath,($report|ConvertTo-Json -Depth 8),$utf8)
}
function Record-Case([string]$Name){
    $cases.Add([ordered]@{name=$Name;passed=$true})
    Save-Report
    Write-Host "PASS: $Name"
}
function Read-Health([string]$Expected){
    $deadline=[DateTime]::UtcNow.AddSeconds(30)
    do{
        try{
            $health=Invoke-RestMethod -Uri "http://127.0.0.1:$port/healthz" -TimeoutSec 2
            if($health.version -and ((-not $Expected) -or $health.version -eq $Expected)){return [string]$health.version}
        }catch{}
        Start-Sleep -Milliseconds 300
    }while([DateTime]::UtcNow -lt $deadline)
    throw "Isolated Core did not become healthy at expected version $Expected."
}
function Assert-Preserved([bool]$AllowLegacy=$false) {
    if([IO.File]::ReadAllText((Join-Path $homeRoot 'user-data-sentinel.txt')) -ne 'preserve-user-data'){throw 'User data marker changed'}
    $manifest=Get-Content -LiteralPath (Join-Path $runtimeRoot 'runtime.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    if(-not $AllowLegacy -or $manifest.PSObject.Properties['agentdock_home']) {
        if($manifest.agentdock_home -ne $homeRoot -or $manifest.agentdock_default_dir -ne $workRoot){throw 'Unicode data/workspace paths were not preserved'}
    }
    $pointerPath=Join-Path $runtimeRoot 'active-version.json'
    if((Test-Path -LiteralPath $pointerPath) -or -not $AllowLegacy) {
        $pointer=Get-Content -LiteralPath $pointerPath -Raw -Encoding UTF8 | ConvertFrom-Json
        if($pointer.state -ne 'committed'){throw "Pointer is $($pointer.state), expected committed"}
    }
    foreach($file in $credentialHashes.Keys){
        $path=Join-Path $runtimeRoot $file
        if((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -ne $credentialHashes[$file]){throw "Credential file was replaced: $file"}
    }
}
function Run-Installer([string]$Payload,[string]$Channel,[string]$Script,[string]$Name,[bool]$ExpectFailure=$false){
    $log=Join-Path $testRoot ($Name+'.log')
    $result=Join-Path $testRoot ($Name+'.ini')
    $args=@('-NoLogo','-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',$Script,
        '-Version','latest','-OfflineArchive',$Payload,'-OfflineChecksumFile',($Payload+'.sha256'),
        '-OfflineCloudflaredBinary',$CloudflaredBinary,'-InstallDir',$installDir,'-RegisterStartup',
        '-InstallChannel',$Channel,'-CorePrivilegeMode','standard','-TunnelMode','none','-Port',"$port",
        '-StartupValueName',$names[0],'-TrayStartupValueName',$names[1],'-CloudflaredStartupValueName',$names[2],'-ResultFile',$result)
    $previousErrorAction=$ErrorActionPreference
    try {
        $ErrorActionPreference='Continue'
        & powershell.exe @args *> $log
        $exit=$LASTEXITCODE
    } finally {$ErrorActionPreference=$previousErrorAction}
    if($ExpectFailure){
        if($exit -eq 0){throw "$Name unexpectedly succeeded"}
    }elseif($exit -ne 0){
        # Never print generated credentials from installer output.
        $safe=(Get-Content -LiteralPath $log -Raw) -replace '(?im)(Bearer Token:|OAuth login password:)[^\r\n]*','$1 [REDACTED]'
        throw "$Name failed with exit $exit`n$safe"
    }
}
try{
    # Standard-user production installs need no fixed scheduled task. Refuse to
    # run on a machine whose elevated AgentDock task could be affected.
    if(Get-ScheduledTask -TaskName 'AgentDock' -TaskPath '\' -ErrorAction SilentlyContinue){throw 'Run this regression in a separate user profile when a fixed AgentDock scheduled task exists.'}
    foreach($key in $oldEnv.Keys){[Environment]::SetEnvironmentVariable($key,$null,'Process')}
    $env:AGENTDOCK_HOME=$homeRoot;$env:AGENTDOCK_DEFAULT_DIR=$workRoot
    if($SeedStaleEnvironment) {
        $env:AGENTDOCK_INSTRUCTIONS_FILE=Join-Path $testRoot 'deleted-instructions.md'
        $env:AGENTDOCK_ACP_MAX_CONCURRENT_PROMPTS='invalid-old-value'
        $env:AGENTDOCK_BROWSER_EXECUTABLE_PATH=Join-Path $testRoot 'deleted-browser.exe'
    }
    New-Item -ItemType Directory -Path $testRoot,$homeRoot,$workRoot -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $homeRoot 'user-data-sentinel.txt'),'preserve-user-data',$utf8)
    $credentialHashes=@{}
    Run-Installer $BaselineArchive $BaselineChannel $BaselineInstallerPath 'baseline'
    $baselineVersion=Read-Health ''
    Get-ChildItem -LiteralPath $runtimeRoot -File -Filter '*.dpapi' | ForEach-Object {$credentialHashes[$_.Name]=(Get-FileHash $_.FullName -Algorithm SHA256).Hash}
    if($credentialHashes.Count -eq 0){throw 'Baseline did not create protected credentials'}
    Assert-Preserved $true
    Record-Case 'Baseline installed and healthy'
    if($ExerciseRollback){
        $faultDir=Join-Path $testRoot 'fault-injection'
        New-Item -ItemType Directory -Path $faultDir -Force | Out-Null
        $faultScript=Join-Path $faultDir 'install.ps1'
        $source=[IO.File]::ReadAllText($InstallerPath)
        $needle='    # Provision is complete here.'
        if(-not $source.Contains($needle)){throw 'Fault injection point missing'}
        $source=$source.Replace($needle,"    throw 'isolated-test: fail after Engine trial, before commit'`r`n"+$needle)
        [IO.File]::WriteAllText($faultScript,$source,$utf8)
        Copy-Item (Join-Path (Split-Path $InstallerPath) 'launch-windows-process.ps1') $faultDir
        Run-Installer $Archive 'setup' $faultScript 'injected-rollback' $true
        [void](Read-Health $baselineVersion)
        Assert-Preserved $true
        $transaction=Get-Content (Join-Path $runtimeRoot 'install\transaction.json') -Raw -Encoding UTF8 | ConvertFrom-Json
        if($transaction.state -ne 'rolled_back'){throw "Injected failure left transaction $($transaction.state)"}
        Record-Case 'Failed trial restores prior Core, pointer, credentials and user data'
    }
    Run-Installer $Archive 'setup' $InstallerPath 'upgrade'
    $targetVersion=Read-Health $ExpectedVersion
    if($targetVersion -ne $ExpectedVersion){throw "Upgrade reports unexpected version: $targetVersion"}
    Assert-Preserved
    Record-Case "Upgrade to $ExpectedVersion commits and remains healthy"
    Run-Installer $Archive 'setup' $InstallerPath 'same-version-repair'
    [void](Read-Health $targetVersion)
    Assert-Preserved
    if(@(Get-ChildItem (Join-Path $runtimeRoot 'versions') -Directory -Filter '.repair-backup-*').Count -ne 0){throw 'Same-version repair left a backup generation'}
    Record-Case 'Same-version overwrite commits without leftover repair generation'
    $allPassed=$true
}catch{
    $cases.Add([ordered]@{name='failure';passed=$false;message=$_.Exception.Message.Split([Environment]::NewLine)[0]})
    throw
}finally{
    # Match full fixture paths, never terminate a process by name alone.
    if(Test-Path -LiteralPath $runtimeRoot){
        Get-Process -ErrorAction SilentlyContinue | ForEach-Object {
            try{
                if($_.Path -and $_.Path.StartsWith($testRoot+'\',[StringComparison]::OrdinalIgnoreCase)){
                    Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
                }
            }catch{}
        }
    }
    foreach($name in $names){Remove-ItemProperty -LiteralPath $runKey -Name $name -ErrorAction SilentlyContinue}
    $currentPath=[Environment]::GetEnvironmentVariable('Path','User')
    $cleanPath=($currentPath -split ';' | Where-Object { $_.Trim().TrimEnd('\') -ne $installDir.TrimEnd('\') }) -join ';'
    [Environment]::SetEnvironmentVariable('Path',$cleanPath,'User')
    Get-ChildItem Env:AGENTDOCK_* | ForEach-Object {[Environment]::SetEnvironmentVariable($_.Name,$null,'Process')}
    foreach($key in $oldEnv.Keys){[Environment]::SetEnvironmentVariable($key,$oldEnv[$key],'Process')}
    Save-Report
    if($allPassed){Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue}
    else{Write-Host "Isolated failure fixtures retained: $testRoot"}
}
