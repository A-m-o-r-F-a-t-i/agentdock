[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string] $FilePath,
    [Parameter(Mandatory = $true)][string] $AgentDockBinary,
    [string] $Arguments = '',
    [switch] $WaitForExit,
    [switch] $PassThruOutput,
    [ValidateRange(1, 600)]
    [int] $TimeoutSeconds = 150
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# The payload's GUI-subsystem shim owns native user-session launch, receipts,
# deadlines and task cleanup. No scheduled action runs powershell.exe.
$resolvedBinary = (Resolve-Path -LiteralPath $AgentDockBinary).Path
$launcher = Join-Path (Split-Path -Parent $resolvedBinary) 'agentdock-tray-shim.exe'
if (-not (Test-Path -LiteralPath $launcher -PathType Leaf)) {
    throw "Native Setup launcher was not found in the payload: $launcher"
}
$resolvedTarget = (Resolve-Path -LiteralPath $FilePath).Path
$requestRoot = Join-Path ([IO.Path]::GetTempPath()) ('agentdock-setup-runtime-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $requestRoot -Force | Out-Null
$requestPath = Join-Path $requestRoot 'request.json'
$environment = @{}
foreach ($key in @('AGENTDOCK_HOME', 'AGENTDOCK_DEFAULT_DIR')) {
    $value = [Environment]::GetEnvironmentVariable($key, 'Process')
    if (-not [string]::IsNullOrWhiteSpace($value)) { $environment[$key] = $value }
}
$request = @{
    file_path = $resolvedTarget
    arguments = $Arguments
    wait_for_exit = [bool] $WaitForExit
    timeout_seconds = $TimeoutSeconds
    environment = $environment
}
$utf8 = New-Object Text.UTF8Encoding($false)
[IO.File]::WriteAllText($requestPath, ($request | ConvertTo-Json -Depth 6), $utf8)
$process = New-Object Diagnostics.Process
try {
    $process.StartInfo = New-Object Diagnostics.ProcessStartInfo
    $process.StartInfo.FileName = $launcher
    $process.StartInfo.Arguments = "--setup-launch `"$requestPath`""
    $process.StartInfo.UseShellExecute = $false
    $process.StartInfo.CreateNoWindow = $true
    $process.StartInfo.RedirectStandardOutput = $true
    $process.StartInfo.RedirectStandardError = $true
    $process.StartInfo.StandardOutputEncoding = $utf8
    $process.StartInfo.StandardErrorEncoding = $utf8
    if (-not $process.Start()) { throw 'Native Setup launcher did not start.' }
    $stdoutTask = $process.StandardOutput.ReadToEndAsync()
    $stderrTask = $process.StandardError.ReadToEndAsync()
    if (-not $process.WaitForExit(($TimeoutSeconds + 25) * 1000)) {
        $process.Kill()
        throw "Native Setup launcher exceeded its deadline ($TimeoutSeconds seconds)."
    }
    $process.WaitForExit()
    $stdout = $stdoutTask.GetAwaiter().GetResult()
    $stderr = $stderrTask.GetAwaiter().GetResult()
    if ($process.ExitCode -ne 0) {
        throw "Native Setup launcher failed (exit $($process.ExitCode)): $($stderr.Trim()) $($stdout.Trim())"
    }
    if ($PassThruOutput -and -not [string]::IsNullOrWhiteSpace($stdout)) {
        Write-Output $stdout.TrimEnd()
    }
} finally {
    $process.Dispose()
    # Diagnostic text has already been propagated to the enclosing installer log.
    # The native worker also unregisters its task if the broker exits first.
    Remove-Item -LiteralPath $requestRoot -Recurse -Force -ErrorAction SilentlyContinue
}
