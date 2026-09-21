[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string] $ReleaseDirectory,
    [Parameter(Mandatory = $true)]
    [string] $BuildReport,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^\d+\.\d+\.\d+$')]
    [string] $ExpectedVersion,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[0-9a-fA-F]{40}$')]
    [string] $ExpectedCommit,
    [Parameter(Mandatory = $true)]
    [ValidateSet('release', 'candidate-not-released')]
    [string] $ExpectedChannel,
    [ValidateSet('signed', 'unsigned')]
    [string] $ExpectedAuthenticode = 'unsigned'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Resolve-RequiredFile {
    param([string] $Path, [string] $Description)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "$Description was not found: $Path"
    }
    return (Resolve-Path -LiteralPath $Path).Path
}

function Assert-Checksum {
    param([string] $PayloadPath, [string] $ChecksumPath)

    $payload = Resolve-RequiredFile -Path $PayloadPath -Description 'Release payload'
    $checksum = Resolve-RequiredFile -Path $ChecksumPath -Description 'Checksum file'
    $line = [IO.File]::ReadAllText($checksum).Trim()
    $match = [regex]::Match($line, '^(?<hash>[0-9a-fA-F]{64})\s{2}(?<name>[^\r\n]+)$')
    if (-not $match.Success) {
        throw "Invalid SHA-256 file format: $checksum"
    }
    if ($match.Groups['name'].Value -ne [IO.Path]::GetFileName($payload)) {
        throw "Checksum filename does not match its payload: $checksum"
    }
    $expected = $match.Groups['hash'].Value.ToLowerInvariant()
    $actual = (Get-FileHash -LiteralPath $payload -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected) {
        throw "SHA-256 mismatch: $payload"
    }
    return $actual
}

$releaseRoot = [IO.Path]::GetFullPath($ReleaseDirectory)
if (-not (Test-Path -LiteralPath $releaseRoot -PathType Container)) {
    throw "Release directory was not found: $releaseRoot"
}
$reportPath = Resolve-RequiredFile -Path $BuildReport -Description 'Build report'
$expectedNames = @(
    'AgentDockSetup-amd64.exe',
    'AgentDockSetup-amd64.exe.sha256',
    'agentdock_windows_amd64.zip',
    'agentdock_windows_amd64.zip.sha256',
    'install.ps1',
    'install.ps1.sha256'
) | Sort-Object
$actualNames = @(Get-ChildItem -LiteralPath $releaseRoot -File | Select-Object -ExpandProperty Name | Sort-Object)
$difference = @(Compare-Object -ReferenceObject $expectedNames -DifferenceObject $actualNames)
if ($difference.Count -ne 0) {
    throw "Unexpected Windows release asset set: $($actualNames -join ', ')"
}

$digests = [ordered]@{}
foreach ($name in @('AgentDockSetup-amd64.exe', 'agentdock_windows_amd64.zip', 'install.ps1')) {
    $digests[$name] = Assert-Checksum `
        -PayloadPath (Join-Path $releaseRoot $name) `
        -ChecksumPath (Join-Path $releaseRoot "$name.sha256")
}

$report = [IO.File]::ReadAllText($reportPath) | ConvertFrom-Json
if ([string]$report.version -ne $ExpectedVersion) {
    throw "Build report version mismatch: $($report.version)"
}
if ([string]$report.commit -ne $ExpectedCommit.ToLowerInvariant()) {
    throw "Build report commit mismatch: $($report.commit)"
}
if ([string]$report.channel -ne $ExpectedChannel) {
    throw "Build report channel mismatch: $($report.channel)"
}
if ([bool]$report.source_dirty) {
    throw 'Release build report marks the source as dirty.'
}
$platforms = @($report.platforms)
if ($platforms.Count -ne 1 -or [string]$platforms[0] -ne 'windows/amd64') {
    throw "Unexpected build platforms: $($platforms -join ', ')"
}
if ([string]$report.agentdock_authenticode -ne $ExpectedAuthenticode) {
    throw "AgentDock Authenticode state mismatch: $($report.agentdock_authenticode)"
}
if ([string]$report.cloudflared_authenticode -ne 'valid') {
    throw 'cloudflared Authenticode verification was not recorded as valid.'
}

$temporaryRoot = Join-Path ([IO.Path]::GetTempPath()) ('agentdock-release-verify-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temporaryRoot | Out-Null
try {
    Expand-Archive `
        -LiteralPath (Join-Path $releaseRoot 'agentdock_windows_amd64.zip') `
        -DestinationPath $temporaryRoot
    $corePath = Resolve-RequiredFile `
        -Path (Join-Path $temporaryRoot 'agentdock.exe') `
        -Description 'Packaged AgentDock Core'
    $core = (& $corePath version --json | Out-String).Trim() | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0) {
        throw 'Packaged AgentDock Core version query failed.'
    }
    if ([string]$core.version -ne $ExpectedVersion) {
        throw "Packaged Core version mismatch: $($core.version)"
    }
    if ([string]$core.commit -ne $ExpectedCommit.Substring(0, 12).ToLowerInvariant()) {
        throw "Packaged Core commit mismatch: $($core.commit)"
    }
    if ([string]$core.platform -ne 'windows/amd64') {
        throw "Packaged Core platform mismatch: $($core.platform)"
    }
} finally {
    Remove-Item -LiteralPath $temporaryRoot -Recurse -Force -ErrorAction SilentlyContinue
}

$setupVersion = (Get-Item -LiteralPath (Join-Path $releaseRoot 'AgentDockSetup-amd64.exe')).VersionInfo.ProductVersion.Trim()
if ($setupVersion -ne $ExpectedVersion) {
    throw "Offline Setup product version mismatch: $setupVersion"
}

[ordered]@{
    version = $ExpectedVersion
    commit = $ExpectedCommit.ToLowerInvariant()
    channel = $ExpectedChannel
    platform = 'windows/amd64'
    agentdock_authenticode = $ExpectedAuthenticode
    cloudflared_authenticode = 'valid'
    assets = $digests
    verified_at = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')
} | ConvertTo-Json -Depth 5
