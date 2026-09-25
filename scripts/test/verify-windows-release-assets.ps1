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
    [string] $ExpectedAuthenticode = 'unsigned',
    [ValidateSet('amd64','arm64')][string] $Architecture = 'amd64',
    [ValidateSet('amd64','arm64')][string[]] $Architectures = @('amd64')
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

function Invoke-PackagedSkillBootstrap {
    param([string] $CorePath, [string] $BundleDirectory, [string] $IsolatedHome)

    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $CorePath
    $start.WorkingDirectory = $IsolatedHome
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $start.Environment['AGENTDOCK_HOME'] = $IsolatedHome
    foreach ($argument in @('skill', 'bootstrap', '--bundle', $BundleDirectory)) {
        $start.ArgumentList.Add($argument)
    }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $start
    try {
        if (-not $process.Start()) { throw 'Could not start packaged Skill bootstrap.' }
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(60000)) {
            $process.Kill($true)
            [void] $process.WaitForExit(5000)
            throw 'Packaged Skill bootstrap exceeded 60 seconds.'
        }
        $output = $stdout.GetAwaiter().GetResult()
        $errors = $stderr.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0) {
            throw "Packaged Skill bootstrap failed (exit $($process.ExitCode)): $errors $output"
        }
        return $output
    } finally {
        $process.Dispose()
    }
}

$releaseRoot = [IO.Path]::GetFullPath($ReleaseDirectory)
if (-not (Test-Path -LiteralPath $releaseRoot -PathType Container)) {
    throw "Release directory was not found: $releaseRoot"
}
$reportPath = Resolve-RequiredFile -Path $BuildReport -Description 'Build report'
if ($Architectures -notcontains $Architecture -or @($Architectures | Select-Object -Unique).Count -ne $Architectures.Count) { throw 'Invalid architecture verification scope.' }
$expectedNames = @('install.ps1','install.ps1.sha256') + @($Architectures | ForEach-Object {
    "AgentDockSetup-$_.exe"; "AgentDockSetup-$_.exe.sha256"; "agentdock_windows_$_.zip"; "agentdock_windows_$_.zip.sha256"
}) | Sort-Object
$actualNames = @(Get-ChildItem -LiteralPath $releaseRoot -File | Select-Object -ExpandProperty Name | Sort-Object)
$difference = @(Compare-Object -ReferenceObject $expectedNames -DifferenceObject $actualNames)
if ($difference.Count -ne 0) {
    throw "Unexpected Windows release asset set: $($actualNames -join ', ')"
}

$digests = [ordered]@{}
foreach ($name in @("AgentDockSetup-$Architecture.exe", "agentdock_windows_$Architecture.zip", 'install.ps1')) {
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
if (@(Compare-Object @($Architectures | ForEach-Object {"windows/$_"}) $platforms).Count -ne 0) {
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
        -LiteralPath (Join-Path $releaseRoot "agentdock_windows_$Architecture.zip") `
        -DestinationPath $temporaryRoot
    $corePath = Resolve-RequiredFile `
        -Path (Join-Path $temporaryRoot 'agentdock.exe') `
        -Description 'Packaged AgentDock Core'
    $nativeArchitecture = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    $native = ($Architecture -eq 'amd64' -and $nativeArchitecture -eq 'X64') -or ($Architecture -eq 'arm64' -and $nativeArchitecture -eq 'Arm64')
    $expectedMachine = if ($Architecture -eq 'arm64') { 0xaa64 } else { 0x8664 }
    foreach ($binaryName in @('agentdock.exe','agentdock-tray.exe','agentdock-arbiter.exe','agentdock-shim.exe','agentdock-tray-shim.exe')) {
        $binaryPath = Resolve-RequiredFile (Join-Path $temporaryRoot $binaryName) 'Required packaged executable'
        $image = [IO.File]::ReadAllBytes($binaryPath)
        if ($image.Length -lt 64) { throw 'Invalid PE header.' }
        $pe = [BitConverter]::ToInt32($image,0x3c)
        if ($pe -lt 0 -or $pe + 26 -gt $image.Length -or [BitConverter]::ToUInt32($image,$pe) -ne 0x4550 -or [BitConverter]::ToUInt16($image,$pe+4) -ne $expectedMachine) { throw "Incorrect PE architecture: $binaryName" }
    }
    $buildMetadata = (& go version -m $corePath | Out-String)
    if ($LASTEXITCODE -ne 0 -or -not $buildMetadata.Contains('GOARCH='+$Architecture) -or -not $buildMetadata.Contains($ExpectedCommit)) { throw 'Packaged Core build metadata does not match the verified source/architecture.' }
    $desktopProduct = (Get-Item (Join-Path $temporaryRoot 'agentdock-tray.exe')).VersionInfo.ProductName
    $packagedLicense = Resolve-RequiredFile (Join-Path $temporaryRoot 'share\agentdock\LICENSE') 'Repository license'
    if ((Get-FileHash -LiteralPath $packagedLicense -Algorithm SHA256).Hash -ne (Get-FileHash -LiteralPath (Join-Path $PSScriptRoot '..\..\LICENSE') -Algorithm SHA256).Hash) { throw 'Windows package license mismatch.' }
    if ($desktopProduct -ne 'AgentDock Workbench') { throw "Unexpected desktop product name: $desktopProduct" }
    $expectedSkills = @('agentdock-user-guide','skill-authoring','skill-installation')
    $bootstrapState = if ($native) { 'passed' } else { 'not_run_non_native_architecture' }
    if ($native) {
    $core = (& $corePath version --json | Out-String).Trim() | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0) {
        throw 'Packaged AgentDock Core version query failed.'
    }
    if ([string]$core.product_name -ne 'AgentDock Workbench') { throw 'Packaged Core product name mismatch.' }
    if ([string]$core.version -ne $ExpectedVersion) {
        throw "Packaged Core version mismatch: $($core.version)"
    }
    if ([string]$core.commit -ne $ExpectedCommit.Substring(0, 12).ToLowerInvariant()) {
        throw "Packaged Core commit mismatch: $($core.commit)"
    }
    if ([string]$core.platform -ne "windows/$Architecture") {
        throw "Packaged Core platform mismatch: $($core.platform)"
    }

    # Exercise the payload actually shipped to Setup, not a handwritten manifest.
    # Only this temporary Skill home is changed; no Setup, server or task is started.
    $bundleDirectory = Join-Path $temporaryRoot 'share/agentdock/core-skills'
    $skillHome = Join-Path $temporaryRoot 'skill-bootstrap-home'
    New-Item -ItemType Directory -Path $skillHome | Out-Null
    foreach ($phase in @('fresh', 'repeat')) {
        $bootstrap = Invoke-PackagedSkillBootstrap -CorePath $corePath -BundleDirectory $bundleDirectory -IsolatedHome $skillHome
        $manifest = [IO.File]::ReadAllText((Join-Path $bundleDirectory 'manifest.json')) | ConvertFrom-Json
        $expectedSkills = @('agentdock-user-guide', 'skill-authoring', 'skill-installation')
        $actualSkills = @($manifest.skills | ForEach-Object { [string]$_.name } | Sort-Object)
        if (@(Compare-Object $expectedSkills $actualSkills).Count -ne 0) {
            throw 'Packaged core Skill inventory does not match the required bundle.'
        }
        $expectedLines = @($manifest.skills | ForEach-Object { "bundled skill installed: $($_.name) $($_.version)" } | Sort-Object)
        $actualLines = @($bootstrap -split '\r?\n' | Where-Object { $_.Length -gt 0 } | Sort-Object)
        if (@(Compare-Object $expectedLines $actualLines).Count -ne 0) {
            throw "Packaged Skill bootstrap returned an incomplete $phase result."
        }
        foreach ($entry in $manifest.skills) {
            $selectionPath = Join-Path $skillHome "skills/.state/$($entry.name).json"
            $selection = [IO.File]::ReadAllText($selectionPath) | ConvertFrom-Json
            if (-not $selection.system -or [string]$selection.active_version -ne [string]$entry.version) {
                throw "Packaged Skill was not activated correctly: $($entry.name), phase=$phase"
            }
            [void](Resolve-RequiredFile -Path (Join-Path $skillHome "skills/.system/$($entry.name)/SKILL.md") -Description 'Installed core Skill')
        }
    }
    }
} finally {
    Remove-Item -LiteralPath $temporaryRoot -Recurse -Force -ErrorAction SilentlyContinue
}

$setupVersion = (Get-Item -LiteralPath (Join-Path $releaseRoot "AgentDockSetup-$Architecture.exe")).VersionInfo.ProductVersion.Trim()
if ($setupVersion -ne $ExpectedVersion) {
    throw "Offline Setup product version mismatch: $setupVersion"
}

[ordered]@{
    version = $ExpectedVersion
    commit = $ExpectedCommit.ToLowerInvariant()
    channel = $ExpectedChannel
    platform = "windows/$Architecture"
    product_name = 'AgentDock Workbench'
    native_execution = $native
    agentdock_authenticode = $ExpectedAuthenticode
    cloudflared_authenticode = 'valid'
    core_skill_bootstrap = @{ fresh = $bootstrapState; repeat = $bootstrapState; count = $expectedSkills.Count }
    assets = $digests
    verified_at = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')
} | ConvertTo-Json -Depth 5
