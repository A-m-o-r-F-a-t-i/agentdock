#requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string] $CoreBinary,
    [Parameter(Mandatory = $true)][string] $TestRoot
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath($TestRoot)
if (Test-Path -LiteralPath $root) { throw 'Activity runtime test requires a fresh isolated directory.' }
$binary = (Resolve-Path -LiteralPath $CoreBinary).Path
$homePath = Join-Path $root 'home'
$workspace = Join-Path $root 'workspace 中文'
New-Item -ItemType Directory -Path $homePath,$workspace -Force | Out-Null
$probe = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,0)
$probe.Start(); $port = $probe.LocalEndpoint.Port; $probe.Stop()
$origin = "http://127.0.0.1:$port"
$credential = [Guid]::NewGuid().ToString('N')
$handler = [Net.Http.HttpClientHandler]::new(); $handler.UseProxy = $false; $handler.AllowAutoRedirect = $false
$client = [Net.Http.HttpClient]::new($handler)
$client.Timeout = [TimeSpan]::FromSeconds(20)
$client.DefaultRequestHeaders.Authorization = [Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer',$credential)
$client.DefaultRequestHeaders.Accept.ParseAdd('application/json, text/event-stream')
$process = $null; $stdout = $null; $stderr = $null; $generation = 0; $rpcId = 0; $mcpSession = ''
$cases = [Collections.Generic.List[string]]::new()
$failure = $null

function Require([bool]$Condition,[string]$Message) { if (-not $Condition) { throw $Message } }
function Start-Fixture {
    $script:generation++
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $binary; $start.WorkingDirectory = $workspace
    $start.UseShellExecute = $false; $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true; $start.RedirectStandardError = $true
    foreach ($key in @($start.Environment.Keys)) {
        if ($key.StartsWith('AGENTDOCK_',[StringComparison]::OrdinalIgnoreCase)) { [void]$start.Environment.Remove($key) }
    }
    $start.Environment['AGENTDOCK_HOME'] = $homePath
    $start.Environment['AGENTDOCK_DEFAULT_DIR'] = $workspace
    $start.Environment['AGENTDOCK_AUTH_TOKEN'] = $credential
    $start.Environment['AGENTDOCK_AGENTS_AUTO_LOAD'] = 'false'
    foreach ($argument in @('-host','127.0.0.1','-port',"$port")) { $start.ArgumentList.Add($argument) }
    $script:process = [Diagnostics.Process]::new(); $script:process.StartInfo = $start
    Require ($script:process.Start()) 'Isolated Core did not start.'
    $script:stdout = $script:process.StandardOutput.ReadToEndAsync(); $script:stderr = $script:process.StandardError.ReadToEndAsync()
    $ready = $false; $deadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        if ($script:process.HasExited) { throw 'Isolated Core exited before health check; inspect fixture logs.' }
        try { $response = $client.GetAsync("$origin/healthz").GetAwaiter().GetResult(); $ready = $response.IsSuccessStatusCode; $response.Dispose() } catch { }
        if (-not $ready) { Start-Sleep -Milliseconds 100 }
    } while (-not $ready -and [DateTime]::UtcNow -lt $deadline)
    Require $ready 'Isolated Core did not become healthy.'
    $script:mcpSession = ''
    [void](Invoke-Rpc 'initialize' @{protocolVersion='2025-03-26';capabilities=@{};clientInfo=@{name='activity-runtime-regression';version='1'}})
}
function Stop-Fixture {
    if ($null -eq $script:process) { return }
    if (-not $script:process.HasExited) { $script:process.Kill($true) }
    $script:process.WaitForExit()
    [IO.File]::WriteAllText((Join-Path $root "core-$generation.log"), $script:stdout.GetAwaiter().GetResult() + $script:stderr.GetAwaiter().GetResult())
    $script:process.Dispose(); $script:process = $null
}
function Invoke-Rpc([string]$Method,[hashtable]$Parameters) {
    $script:rpcId++
    $request = [Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::Post,"$origin/mcp")
    $request.Content = [Net.Http.StringContent]::new((@{jsonrpc='2.0';id=$script:rpcId;method=$Method;params=$Parameters} | ConvertTo-Json -Depth 30 -Compress),[Text.Encoding]::UTF8,'application/json')
    if ($script:mcpSession) { $request.Headers.Add('Mcp-Session-Id',$script:mcpSession) }
    try {
        $response = $client.SendAsync($request).GetAwaiter().GetResult()
        try {
            Require $response.IsSuccessStatusCode "MCP HTTP error $([int]$response.StatusCode)."
            if ($response.Headers.Contains('Mcp-Session-Id')) { $script:mcpSession = @($response.Headers.GetValues('Mcp-Session-Id'))[0] }
            $text = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            if ($text.TrimStart().StartsWith('{')) { $payload = $text | ConvertFrom-Json -AsHashtable }
            else {
                $data = @($text -split "`n" | Where-Object { $_.StartsWith('data:') } | ForEach-Object { $_.Substring(5).Trim() })
                Require ($data.Count -gt 0) 'MCP response contained no JSON data.'
                $payload = $data[-1] | ConvertFrom-Json -AsHashtable
            }
            if ($payload.ContainsKey('error')) { throw "MCP RPC error: $($payload.error.message)" }
            return $payload.result
        } finally { $response.Dispose() }
    } finally { $request.Dispose() }
}
function Invoke-Tool([string]$Name,[hashtable]$Arguments) {
    $response = Invoke-Rpc 'tools/call' @{name=$Name;arguments=$Arguments}
    if ($response.ContainsKey('isError') -and $response.isError) { throw "Tool $Name rejected fixture input: $($response.content | ConvertTo-Json -Compress -Depth 8)" }
    if ($response.ContainsKey('structuredContent')) { return $response.structuredContent }
    return ($response.content[0].text | ConvertFrom-Json -AsHashtable)
}
function Read-Local([string]$Path) {
    $response = $client.GetAsync($origin+$Path).GetAwaiter().GetResult()
    try { Require $response.IsSuccessStatusCode "Local request failed: $Path"; return ($response.Content.ReadAsStringAsync().GetAwaiter().GetResult() | ConvertFrom-Json -AsHashtable) }
    finally { $response.Dispose() }
}
function New-Task([string]$Title) {
    return (Invoke-Tool 'task_manage' @{action='create';title=$Title;goal='isolated execution recovery';steps=@(@{id='verify';title='Verify'});completion_conditions=@('correct execution state')}).task_id
}
try {
    Start-Fixture
    $id = New-Task 'Native activity test'
    $legacyId = New-Task 'Legacy task fixture'
    $fork = Invoke-Tool 'task_manage' @{action='thread_fork';task_id=$id;title='Isolated branch'}
    $thread = $fork.thread.id
    $command = Invoke-Tool 'exec_command' @{cmd="Write-Output 'activity-中文'; Write-Output `$env:FIXTURE_SECRET; Start-Sleep -Milliseconds 350; exit 7";task_id=$id;thread_id=$thread;execution_mode='sync';env=@{FIXTURE_SECRET='never-persist-fixture-value'}}
    Require ($command.command_ok -eq $false -and $command.exit_code -eq 7 -and $command.stdout.Contains('activity-中文')) 'Real command state or Chinese output changed.'
    $edit = Invoke-Tool 'file_edit' @{action='add';path='delivery.txt';target_kind='artifact';task_id=$id;thread_id=$thread;content='isolated delivery'}
    $target = $edit.workspace_target
    Require (Test-Path -LiteralPath (Join-Path $target.resolved_path 'delivery.txt')) 'Artifact did not land under its routed task directory.'
    $page = Read-Local "/internal/runtime/tasks/$id/threads/$thread/activity"
    $events = @($page.events)
    Require (@($events | Where-Object kind -EQ 'command.started').Count -eq 1) 'Command start event count is incorrect.'
    Require (@($events | Where-Object kind -EQ 'command.completed').Count -eq 1) 'Command completion event count is incorrect.'
    Require (@($events | Where-Object kind -EQ 'file.changed').Count -eq 1) 'File event is missing.'
    Require (-not (($page | ConvertTo-Json -Depth 30).Contains('never-persist-fixture-value'))) 'Secret leaked into the activity API.'
    $cases.Add('real command, file routing, typed events and redaction')
    $request = [Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::Get,"$origin/internal/runtime/tasks/$id/activity/stream?thread_id=$thread")
    $resume = [string]($events[0].seq)
    $request.Headers.Add('Last-Event-ID',$resume)
    $cancel = [Threading.CancellationTokenSource]::new([TimeSpan]::FromSeconds(10))
    $reader = $null; $response = $null
    try {
        $response = $client.SendAsync($request,[Net.Http.HttpCompletionOption]::ResponseHeadersRead,$cancel.Token).GetAwaiter().GetResult()
        Require $response.IsSuccessStatusCode 'SSE refused an authenticated loopback client.'
        $reader = [IO.StreamReader]::new($response.Content.ReadAsStreamAsync().GetAwaiter().GetResult())
        $seen = [Collections.Generic.HashSet[string]]::new(); $last = [uint64]$resume
        while ($last -lt [uint64]$events[-1].seq) {
            $line = $reader.ReadLineAsync($cancel.Token).AsTask().GetAwaiter().GetResult()
            Require ($null -ne $line) 'SSE stream ended before replay completed.'
            if (-not $line.StartsWith('data:')) { continue }
            $value = $line.Substring(5).Trim() | ConvertFrom-Json -AsHashtable
            if (-not $value.ContainsKey('event_id')) { continue }
            Require ($value.task_id -eq $id -and $value.thread_id -eq $thread) 'SSE returned another task or thread.'
            Require ($seen.Add($value.event_id) -and [uint64]$value.seq -gt $last) 'SSE replay duplicated or reordered events.'
            $last = [uint64]$value.seq
        }
    } finally { $cancel.Cancel(); if ($null -ne $reader) { $reader.Dispose() }; if ($null -ne $response) { $response.Dispose() }; $cancel.Dispose(); $request.Dispose() }
    $cases.Add('authenticated Last-Event-ID SSE replay and thread isolation')
    foreach ($header in @('Host','X-Forwarded-For','Tailscale-User-Login')) {
        $request = [Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::Get,"$origin/internal/runtime/activity/stream")
        if ($header -eq 'Host') { $request.Headers.Host='public.example.test' } else { $request.Headers.Add($header,'external') }
        $response = $client.SendAsync($request).GetAwaiter().GetResult()
        try { Require ([int]$response.StatusCode -eq 403) "Activity stream was exposed through $header." }
        finally { $response.Dispose(); $request.Dispose() }
    }
    $cases.Add('public authority and proxy activity access denied')
    [void](Invoke-Tool 'task_manage' @{action='thread_checkpoint';task_id=$id;thread_id=$thread;summary='command and file verified';next_action='review recovered state';current_step_id='verify'})
    [void](Invoke-Tool 'task_manage' @{action='thread_switch';task_id=$id;thread_id=$thread})
    Stop-Fixture
    $legacyPath = Join-Path $homePath "tasks\$legacyId.json"
    $legacy = Get-Content -LiteralPath $legacyPath -Raw | ConvertFrom-Json -AsHashtable
    foreach ($key in @('active_thread_id','active_thread','workspace_id','outcome','archived_at')) { [void]$legacy.Remove($key) }
    $legacy | ConvertTo-Json -Depth 30 | Set-Content -LiteralPath $legacyPath -Encoding utf8NoBOM
    Remove-Item -LiteralPath (Join-Path $homePath "tasks\threads\$legacyId") -Recurse -Force
    Start-Fixture
    $context = Invoke-Tool 'agentdock_context' @{}
    $restored = @($context.tasks.items | Where-Object task_id -EQ $id)[0]
    Require ($restored.active_thread.thread_id -eq $thread -and $restored.active_thread.next_action -eq 'review recovered state') 'Restart lost the selected thread checkpoint.'
    $old = Invoke-Tool 'task_manage' @{action='get';task_id=$legacyId}
    Require ($old.task.active_thread_id -eq 'main') 'Legacy task did not obtain its virtual main thread.'
    Require (-not (Test-Path -LiteralPath (Join-Path $homePath "tasks\threads\$legacyId"))) 'Read-only legacy recovery eagerly wrote thread state.'
    $cases.Add('Core restart preserves activity/checkpoints and lazily reads legacy tasks')
    [void](Invoke-Tool 'task_manage' @{action='cancel';task_id=$id;summary='fixture complete'})
    [void](Invoke-Tool 'task_manage' @{action='archive';task_id=$id})
    $listed = Invoke-Tool 'task_manage' @{action='list'}
    Require (@($listed.tasks | Where-Object id -EQ $id).Count -eq 0) 'Archived task remained in the default list.'
    $archived = Invoke-Tool 'task_manage' @{action='get';task_id=$id}
    Require ($archived.task.outcome -eq 'cancelled') 'Cancellation outcome was lost.'
    [void](Invoke-Tool 'task_manage' @{action='unarchive';task_id=$id})
    $cases.Add('cancel, archive and unarchive preserve history')
} catch { $failure = $_ }
finally {
    Stop-Fixture
    $client.Dispose()
    $journal = @(Get-ChildItem -LiteralPath (Join-Path $homePath 'tasks\activity') -Filter '*.jsonl' -ErrorAction SilentlyContinue)
    foreach ($file in $journal) { if ([IO.File]::ReadAllText($file.FullName).Contains('never-persist-fixture-value')) { $failure = 'Secret leaked into durable activity.' } }
    @{passed=($null -eq $failure);cases=$cases.ToArray();generations=$generation;production_touched=$false;error=$(if($failure){[string]$failure}else{''})} | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $root 'result.json') -Encoding utf8NoBOM
}
if ($failure) { throw $failure }
Write-Output 'Native Core activity, SSE, workspace and restart regressions passed.'
