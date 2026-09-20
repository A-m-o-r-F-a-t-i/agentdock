#requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string] $TestRoot,
    [string] $Dotnet = 'dotnet',
    [string] $BuildRoot = ''
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath($TestRoot)
if (Test-Path -LiteralPath $root) { throw 'Activity regression requires a fresh test directory.' }
if (-not $BuildRoot) { $BuildRoot = $root + '-build' }
$build = [IO.Path]::GetFullPath($BuildRoot).TrimEnd('\') + '\'
$repository = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
if ($build.StartsWith($repository + '\', [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Keep activity build outputs outside the source worktree.'
}
$project = Join-Path $PSScriptRoot 'testdata\activity-center\ActivityCenterTests.csproj'
& $Dotnet build $project -c Release "-p:BaseOutputPath=$build" -p:UseSharedCompilation=false --nologo
if ($LASTEXITCODE -ne 0) { throw 'Activity desktop regression build failed.' }
$executable = Join-Path $build 'Release\net8.0-windows10.0.19041.0\win-x64\ActivityCenterTests.exe'
if (-not (Test-Path -LiteralPath $executable)) { throw 'Activity regression executable was not produced.' }
$start = [Diagnostics.ProcessStartInfo]::new()
$start.FileName = $executable
$start.WorkingDirectory = $build
$start.UseShellExecute = $false
$start.CreateNoWindow = $true
$start.RedirectStandardOutput = $true
$start.RedirectStandardError = $true
$start.ArgumentList.Add($root)
$process = [Diagnostics.Process]::new()
$process.StartInfo = $start
try {
    if (-not $process.Start()) { throw 'Activity regression did not start.' }
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    if (-not $process.WaitForExit(120000)) {
        $process.Kill($true)
        $process.WaitForExit()
        throw 'Activity regression exceeded its two-minute deadline.'
    }
    $output = $stdout.GetAwaiter().GetResult() + $stderr.GetAwaiter().GetResult()
    Write-Output $output
    if ($process.ExitCode -ne 0) { throw "Activity regression failed with exit $($process.ExitCode)." }
    $result = Get-Content -LiteralPath (Join-Path $root 'result.json') -Raw | ConvertFrom-Json
    if (-not $result.passed) { throw 'Activity regression did not produce a passing receipt.' }
} finally { $process.Dispose() }
