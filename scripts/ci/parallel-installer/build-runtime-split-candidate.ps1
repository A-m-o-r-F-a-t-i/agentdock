#requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('amd64', 'arm64')]
    [string] $Architecture,
    [string] $OutputRoot = '',
    [string] $SourceCommit = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$repository = (Resolve-Path (Join-Path $PSScriptRoot '..\..\..')).Path
if ([string]::IsNullOrWhiteSpace($OutputRoot)) {
    $OutputRoot = Join-Path $repository 'artifacts\parallel-installer'
}
$outputRootPath = [IO.Path]::GetFullPath($OutputRoot)
$rid = if ($Architecture -eq 'arm64') { 'win-arm64' } else { 'win-x64' }
$project = Join-Path $repository 'desktop\windows\control-panel\AgentDock.ControlPanel.csproj'
$architectureRoot = Join-Path $outputRootPath $Architecture
$baselineDirectory = Join-Path $architectureRoot 'baseline-self-contained'
$splitDirectory = Join-Path $architectureRoot 'runtime-split-candidate'
$reportDirectory = Join-Path $architectureRoot 'reports'

# This script owns only its two publish trees and runtime-split reports.
# Preserve Core binaries, compiled tests and diagnostics produced by earlier
# workflow steps under the same architecture root.
foreach ($ownedPath in @($baselineDirectory, $splitDirectory, $reportDirectory)) {
    Remove-Item -LiteralPath $ownedPath -Recurse -Force -ErrorAction SilentlyContinue
}
New-Item -ItemType Directory -Path $baselineDirectory, $splitDirectory, $reportDirectory -Force | Out-Null

if ([string]::IsNullOrWhiteSpace($SourceCommit)) {
    $SourceCommit = (& git -C $repository rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'Could not determine source commit.' }
}

function Assert-NativeExit([string] $Operation) {
    if ($LASTEXITCODE -ne 0) {
        throw "$Operation failed with exit code $LASTEXITCODE."
    }
}

function Get-DirectoryMetrics([string] $Path) {
    $files = @(Get-ChildItem -LiteralPath $Path -Recurse -File | Sort-Object FullName)
    $totalBytes = [Int64] 0
    foreach ($file in $files) { $totalBytes += [Int64] $file.Length }
    return [ordered]@{
        directory_bytes = $totalBytes
        file_count = $files.Count
        executable_count = @($files | Where-Object Extension -eq '.exe').Count
        assembly_count = @($files | Where-Object Extension -eq '.dll').Count
        satellite_resource_count = @($files | Where-Object Name -like '*.resources.dll').Count
        deps_json_count = @($files | Where-Object Name -like '*.deps.json').Count
        runtimeconfig_json_count = @($files | Where-Object Name -like '*.runtimeconfig.json').Count
    }
}

function Write-PayloadManifest([string] $Path, [string] $Destination) {
    $entries = foreach ($file in Get-ChildItem -LiteralPath $Path -Recurse -File | Sort-Object FullName) {
        $relative = [IO.Path]::GetRelativePath($Path, $file.FullName).Replace('\', '/')
        [ordered]@{
            path = $relative
            bytes = [Int64] $file.Length
            sha256 = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
        }
    }
    [ordered]@{
        schema_version = 1
        source_commit = $SourceCommit
        architecture = $Architecture
        rid = $rid
        files = @($entries)
    } | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $Destination -Encoding utf8NoBOM
}

Push-Location $repository
try {
    & dotnet restore $project -r $rid
    Assert-NativeExit 'WPF restore'

    & dotnet publish $project `
        -c Release `
        -r $rid `
        --self-contained true `
        --no-restore `
        -p:AgentDockRuntimeSplit=false `
        -p:PublishSingleFile=true `
        -p:ContinuousIntegrationBuild=true `
        -p:Deterministic=true `
        -o $baselineDirectory
    Assert-NativeExit 'Baseline self-contained WPF publish'

    & dotnet publish $project `
        -c Release `
        -r $rid `
        --self-contained false `
        --no-restore `
        -p:AgentDockRuntimeSplit=true `
        -p:PublishSingleFile=false `
        -p:ContinuousIntegrationBuild=true `
        -p:Deterministic=true `
        -o $splitDirectory
    Assert-NativeExit 'Runtime-split WPF publish'
} finally {
    Pop-Location
}

$requiredSplitFiles = @(
    'agentdock-tray.exe',
    'agentdock-tray.dll',
    'agentdock-tray.deps.json',
    'agentdock-tray.runtimeconfig.json',
    'System.Security.Cryptography.ProtectedData.dll'
)
foreach ($required in $requiredSplitFiles) {
    $path = Join-Path $splitDirectory $required
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Runtime-split candidate is missing required launch dependency: $required"
    }
}
$satelliteResources = @(Get-ChildItem -LiteralPath $splitDirectory -Recurse -File -Filter '*.resources.dll')
if ($satelliteResources.Count -lt 1) {
    throw 'Runtime-split candidate is missing localized satellite resources.'
}

$runtimeConfigPath = Join-Path $splitDirectory 'agentdock-tray.runtimeconfig.json'
$runtimeConfig = Get-Content -LiteralPath $runtimeConfigPath -Raw -Encoding UTF8 | ConvertFrom-Json
$frameworks = @()
$runtimeOptions = $runtimeConfig.runtimeOptions
if ($null -ne $runtimeOptions.PSObject.Properties['framework']) {
    $frameworks += $runtimeConfig.runtimeOptions.framework
}
if ($null -ne $runtimeOptions.PSObject.Properties['frameworks']) {
    $frameworks += @($runtimeConfig.runtimeOptions.frameworks)
}
if (@($frameworks | Where-Object name -eq 'Microsoft.WindowsDesktop.App').Count -lt 1) {
    throw 'Runtime-split candidate does not declare Microsoft.WindowsDesktop.App.'
}

$baselineArchive = Join-Path $architectureRoot "agentdock-tray-$Architecture-baseline-self-contained.zip"
$splitArchive = Join-Path $architectureRoot "agentdock-tray-$Architecture-runtime-split-candidate.zip"
Compress-Archive -Path (Join-Path $baselineDirectory '*') -DestinationPath $baselineArchive -CompressionLevel Optimal -Force
Compress-Archive -Path (Join-Path $splitDirectory '*') -DestinationPath $splitArchive -CompressionLevel Optimal -Force

$manifestPath = Join-Path $reportDirectory 'runtime-split-manifest.json'
Write-PayloadManifest -Path $splitDirectory -Destination $manifestPath
$baselineMetrics = Get-DirectoryMetrics -Path $baselineDirectory
$splitMetrics = Get-DirectoryMetrics -Path $splitDirectory
$baselineArchiveBytes = [Int64] (Get-Item -LiteralPath $baselineArchive).Length
$splitArchiveBytes = [Int64] (Get-Item -LiteralPath $splitArchive).Length
$directoryReduction = [Int64] $baselineMetrics.directory_bytes - [Int64] $splitMetrics.directory_bytes
$archiveReduction = $baselineArchiveBytes - $splitArchiveBytes
$minimumGoal = [Int64] (90MB)
$maximumGoal = [Int64] (160MB)

$report = [ordered]@{
    schema_version = 1
    lane = '05-installer'
    source_commit = $SourceCommit
    architecture = $Architecture
    rid = $rid
    installation_tests = $false
    signed = $false
    accepted_for_release = $false
    candidate_scope = 'WPF framework-dependent runtime split only; not wired into production Setup or generation activation'
    runtime_requirement = @($frameworks | ForEach-Object {
        [ordered]@{ name = [string] $_.name; version = [string] $_.version }
    })
    baseline = [ordered]@{
        layout = 'self-contained-single-file'
        directory = $baselineMetrics
        archive_bytes = $baselineArchiveBytes
        archive_sha256 = (Get-FileHash -LiteralPath $baselineArchive -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    runtime_split = [ordered]@{
        layout = 'framework-dependent-multi-file'
        directory = $splitMetrics
        archive_bytes = $splitArchiveBytes
        archive_sha256 = (Get-FileHash -LiteralPath $splitArchive -Algorithm SHA256).Hash.ToLowerInvariant()
        manifest = [IO.Path]::GetFileName($manifestPath)
        required_launch_dependencies = $requiredSplitFiles
        localized_satellite_resources = @($satelliteResources | ForEach-Object {
            [IO.Path]::GetRelativePath($splitDirectory, $_.FullName).Replace('\', '/')
        })
    }
    delta = [ordered]@{
        directory_reduction_bytes = $directoryReduction
        archive_reduction_bytes = $archiveReduction
        archive_reduction_mib = [Math]::Round($archiveReduction / 1MB, 2)
        target_window_mib = '90-160'
        meets_target_window = ($archiveReduction -ge $minimumGoal -and $archiveReduction -le $maximumGoal)
    }
    performance_evidence = [ordered]@{
        package_size_measured_in_actions = $true
        cold_start_measured = $false
        warm_start_measured = $false
        install_time_measured = $false
        note = 'CI publish and archive size are candidate evidence only; real Windows install and cold/warm startup measurements remain required.'
    }
}
$reportPath = Join-Path $reportDirectory 'runtime-split-report.json'
$report | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $reportPath -Encoding utf8NoBOM

# The ZIPs plus the hash manifest are the portable candidate evidence. Remove
# expanded duplicate trees before artifact upload to keep CI storage bounded.
Remove-Item -LiteralPath $baselineDirectory -Recurse -Force
Remove-Item -LiteralPath $splitDirectory -Recurse -Force

Write-Host "Runtime-split candidate ready: $Architecture"
Write-Host ("Baseline archive: {0:N2} MiB" -f ($baselineArchiveBytes / 1MB))
Write-Host ("Split archive: {0:N2} MiB" -f ($splitArchiveBytes / 1MB))
Write-Host ("Reduction: {0:N2} MiB" -f ($archiveReduction / 1MB))
Write-Host "Report: $reportPath"
