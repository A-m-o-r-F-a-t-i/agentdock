#requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$CoreBinary,
    [Parameter(Mandatory=$true)][string]$ShimBinary,
    [Parameter(Mandatory=$true)][string]$LegacyArchive,
    [Parameter(Mandatory=$true)][string]$TestRoot
)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$root=[IO.Path]::GetFullPath($TestRoot)
if(Test-Path -LiteralPath $root){throw 'Use a fresh isolated compatibility fixture root.'}
$core=(Resolve-Path -LiteralPath $CoreBinary).Path
$shim=(Resolve-Path -LiteralPath $ShimBinary).Path
$archive=(Resolve-Path -LiteralPath $LegacyArchive).Path
$expected=([IO.File]::ReadAllText($archive+'.sha256').Trim() -split '\s+')[0]
if((Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant() -ne $expected){throw 'Legacy archive checksum mismatch.'}
$dataHome=Join-Path $root 'home'
$workspace=Join-Path $root 'workspace'
$bin=Join-Path $root 'bin'
$oldGeneration=Join-Path $root 'versions\v1.1.1'
$newGeneration=Join-Path $root 'versions\v1.1.2'
New-Item -ItemType Directory -Force -Path $root,$dataHome,$workspace,$bin,$oldGeneration,$newGeneration | Out-Null
$oldCore=Join-Path $oldGeneration 'agentdock-core.exe'
$newCore=Join-Path $newGeneration 'agentdock-core.exe'
$entry=Join-Path $bin 'agentdock.exe'
$trayEntry=Join-Path $bin 'agentdock-tray.exe'
Copy-Item $core $newCore
Copy-Item $shim $entry
Copy-Item $shim $trayEntry
$zip=[IO.Compression.ZipFile]::OpenRead($archive)
try{[IO.Compression.ZipFileExtensions]::ExtractToFile($zip.GetEntry('agentdock.exe'),$oldCore)}finally{$zip.Dispose()}
# A Core copy stands in for the old tray: even a failed guard cannot touch the
# real desktop singleton. Its harmless version command would expose the failure.
Copy-Item $oldCore (Join-Path $oldGeneration 'agentdock-tray.exe')
$probe=[Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,0)
$probe.Start();$port=$probe.LocalEndpoint.Port;$probe.Stop()
$origin="http://127.0.0.1:$port"
$credential=[Guid]::NewGuid().ToString('N')
$headers=@{Authorization='Bearer '+$credential;Accept='application/json, text/event-stream'}
@{schema_version=1;install_root=$root;agentdock_home=$dataHome;agentdock_default_dir=$workspace;agentdock_binary=$entry;tray_binary=$trayEntry;host='127.0.0.1';port=$port;local_mcp_url=$origin+'/mcp';tunnel_mode='none';privilege_mode='standard';install_channel='script'} | ConvertTo-Json | Set-Content (Join-Path $root 'runtime.json') -Encoding utf8NoBOM
$project=Join-Path $workspace 'project-sentinel.txt'
[IO.File]::WriteAllText($project,'preserve-project')
$cases=[Collections.Generic.List[string]]::new()
$running=$null
$failure=$null
$rpc=0
function Require([bool]$condition,[string]$message){if(-not $condition){throw $message}}
function Set-Generation([string]$version){
    @{schema_version=1;active_version=$version;fallback_version='v1.1.1';state='committed';updated_at=[DateTime]::UtcNow.ToString('o')} | ConvertTo-Json | Set-Content (Join-Path $root 'active-version.json') -Encoding utf8NoBOM
}
function Start-Owned([string]$binary,[string[]]$arguments){
    $info=[Diagnostics.ProcessStartInfo]::new($binary)
    $info.UseShellExecute=$false;$info.CreateNoWindow=$true;$info.WorkingDirectory=$workspace
    $info.RedirectStandardOutput=$true;$info.RedirectStandardError=$true
    foreach($key in @($info.Environment.Keys)){if($key.StartsWith('AGENTDOCK_',[StringComparison]::OrdinalIgnoreCase)){[void]$info.Environment.Remove($key)}}
    $info.Environment['AGENTDOCK_HOME']=$dataHome
    $info.Environment['AGENTDOCK_DEFAULT_DIR']=$workspace
    $info.Environment['AGENTDOCK_AUTH_TOKEN']=$credential
    $info.Environment['AGENTDOCK_AGENTS_AUTO_LOAD']='false'
    foreach($argument in $arguments){$info.ArgumentList.Add($argument)}
    $process=[Diagnostics.Process]::new();$process.StartInfo=$info
    Require ($process.Start()) 'Fixture process did not start.'
    return @{Process=$process;Out=$process.StandardOutput.ReadToEndAsync();Err=$process.StandardError.ReadToEndAsync()}
}
function Stop-Owned($owned){
    if($null -eq $owned){return}
    $process=$owned.Process
    if(-not $process.HasExited){$process.Kill($true)}
    $process.WaitForExit();$process.Dispose()
}
function Invoke-Checked([string]$binary,[string[]]$arguments,[string]$expectedError=''){
    $owned=Start-Owned $binary $arguments
    try{
        Require ($owned.Process.WaitForExit(15000)) 'Fixture command exceeded its deadline.'
        $output=$owned.Out.GetAwaiter().GetResult()+$owned.Err.GetAwaiter().GetResult()
        if($expectedError){Require ($owned.Process.ExitCode -ne 0 -and $output.Contains($expectedError)) 'Expected compatibility refusal was not returned.'}
        else{Require ($owned.Process.ExitCode -eq 0) 'Fixture inspection failed.'}
        return $output.Replace($credential,'[REDACTED]')
    }finally{Stop-Owned $owned}
}
function Start-Server{
    $script:running=Start-Owned $entry @('-host','127.0.0.1','-port',"$port")
    $deadline=[DateTime]::UtcNow.AddSeconds(20)
    do{
        if($running.Process.HasExited){throw 'Fixture server exited before health.'}
        try{$health=Invoke-RestMethod ($origin+'/healthz') -TimeoutSec 2;if($health.version -eq '1.1.2'){return}}catch{}
        Start-Sleep -Milliseconds 100
    }while([DateTime]::UtcNow -lt $deadline)
    throw 'Fixture server failed its health check.'
}
function Local([string]$path,[hashtable]$body=$null){
    if($null -eq $body){return Invoke-RestMethod ($origin+$path) -Headers $headers -TimeoutSec 10}
    return Invoke-RestMethod ($origin+$path) -Method Post -Headers $headers -ContentType 'application/json' -Body ($body | ConvertTo-Json -Depth 15 -Compress) -TimeoutSec 10
}
function Call([string]$name,[hashtable]$arguments){
    $script:rpc++
    $response=Local '/mcp' @{jsonrpc='2.0';id=$rpc;method='tools/call';params=@{name=$name;arguments=$arguments;_meta=@{'openai/session'='compatibility-fixture'}}}
    return $response.result
}
try{
    Set-Generation 'v1.1.2'
    $version=Invoke-Checked $newCore @('version','--json') | ConvertFrom-Json
    Require ($version.execution_policy_version -eq 1) 'New Core does not advertise policy capability.'
    Start-Server
    $task=Call 'task_manage' @{action='create';title='Compatibility fixture';goal='Preserve task and policy';completion_conditions=@('retained');steps=@(@{id='verify';title='Verify'})}
    $taskId=$task.structuredContent.task_id
    $policy=Local '/internal/runtime/permissions/effective'
    [void](Local '/internal/runtime/permissions' @{scope='global';mode='readonly';expected_revision=$policy.policy.revision})
    $taskFile=Join-Path $dataHome "tasks\$taskId.json"
    $policyFile=Join-Path $dataHome 'execution\permissions\policy.json'
    $savedTask=[IO.File]::ReadAllText($taskFile)
    $savedPolicy=[IO.File]::ReadAllText($policyFile)
    Stop-Owned $running;$running=$null
    $cases.Add('new Core persists a real task and readonly policy through authenticated local APIs')
    Set-Generation 'v1.1.1'
    $old=Invoke-Checked $entry @('version','--json') | ConvertFrom-Json
    Require ($old.version -eq '1.1.1') 'Fixture is not the actual 1.1.1 Core.'
    $denied='EXECUTION_POLICY_DOWNGRADE_BLOCKED'
    [void](Invoke-Checked $entry @('-host','127.0.0.1','-port',"$port") $denied)
    [void](Invoke-Checked $entry @('service','start','--runtime-root',$root) $denied)
    [void](Invoke-Checked $newCore @('service','start','--runtime-root',$root) $denied)
    [void](Invoke-Checked $trayEntry @('version','--json') $denied)
    $cases.Add('actual legacy Core is refused by stable server entry, managed service entry and tray entry')
    [void](Invoke-Checked $entry @('service','status','--runtime-root',$root))
    Require ([IO.File]::ReadAllText($policyFile) -eq $savedPolicy) 'Compatibility refusal changed policy.'
    Set-Generation 'v1.1.2'
    $pointerFile=Join-Path $root 'active-version.json'
    $pointer=[IO.File]::ReadAllText($pointerFile)
    [void](Invoke-Checked $newCore @('update','--local-archive',$archive,'--checksum',($archive+'.sha256'),'--target-version','1.1.1') 'must be newer than current')
    Require ([IO.File]::ReadAllText($pointerFile) -eq $pointer) 'Rejected downgrade changed active generation.'
    $cases.Add('managed local-archive downgrade is rejected before generation activation')
    Start-Server
    $denial=Call 'file_edit' @{action='add';path='must-not-exist.txt';content='blocked'}
    Require ($denial.isError -eq $true -and $denial.structuredContent.code -eq 'PERMISSION_DENIED') 'Restored capable Core lost readonly enforcement.'
    Require (-not (Test-Path (Join-Path $workspace 'must-not-exist.txt'))) 'Readonly policy allowed a write.'
    $cases.Add('restoring the capable Core retains the original readonly enforcement')
    Require ([IO.File]::ReadAllText($taskFile) -eq $savedTask) 'Task data changed.'
    Require ([IO.File]::ReadAllText($policyFile) -eq $savedPolicy) 'Policy data changed.'
    Require ([IO.File]::ReadAllText($project) -eq 'preserve-project') 'Project source changed.'
    $cases.Add('task, policy and project bytes are preserved')
}catch{$failure=$_}
finally{
    Stop-Owned $running
    @{passed=($null -eq $failure);cases=$cases.ToArray();production_touched=$false;legacy_archive_sha256=$expected;error=$(if($failure){$failure.Exception.Message}else{''});scope='1.1.2 managed update/service/shim paths; does not cover replacing the guarded shim with an old installer'} | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $root 'result.json') -Encoding utf8NoBOM
}
if($failure){throw $failure}
Get-Content (Join-Path $root 'result.json') -Raw
