#requires -Version 7.0
[CmdletBinding()]
param(
    [ValidateSet('amd64','arm64')][string[]] $Architectures = @('amd64','arm64'),
    [string] $OutputDirectory = '',
    [string] $CloudflaredBinary = '',
    [switch] $SignedBuild,
    [switch] $Candidate
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$repository = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) { $OutputDirectory = Join-Path $repository 'dist\windows-release' }
$outputRoot = [IO.Path]::GetFullPath($OutputDirectory)
$releaseRoot = Join-Path $outputRoot 'release'
New-Item -ItemType Directory -Force -Path $outputRoot,$releaseRoot | Out-Null
$version = (& go -C $repository run ./tools/release version).Trim()
if ($LASTEXITCODE -ne 0 -or $version -notmatch '^\d+\.\d+\.\d+$') { throw 'Could not read the release version.' }
if ($version -eq '1.1.2' -and -not $Candidate) {
    & go -C $repository run ./tools/release verify-acceptance (Join-Path $repository 'docs/releases/v1.1.2-acceptance.json')
    if ($LASTEXITCODE -ne 0) { throw 'Formal 1.1.2 build is blocked. Use -Candidate only for isolated verification, never as release acceptance.' }
}
$sourceChanges = @(& git -C $repository status --porcelain)
if ($LASTEXITCODE -ne 0) { throw 'Could not inspect source state.' }
if (-not $Candidate -and $version -eq '1.1.2' -and $sourceChanges.Count -gt 0) { throw 'Formal release requires a clean verified source commit.' }
$commit = (& git -C $repository rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0) { throw 'Could not read the source commit.' }
$buildDate = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')
$ldflags = "-s -w -X github.com/uvwt/agentdock/internal/buildinfo.Commit=$commit -X github.com/uvwt/agentdock/internal/buildinfo.BuildDate=$buildDate"
$originalGoOS,$originalGoArch,$originalCGO = $env:GOOS,$env:GOARCH,$env:CGO_ENABLED
$utf8 = [Text.UTF8Encoding]::new($false)

function Assert-NativeExit([string] $Operation) {
    if ($LASTEXITCODE -ne 0) { throw "$Operation failed with exit code $LASTEXITCODE." }
}
function Write-Checksum([string] $Path) {
    $hash = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText("$Path.sha256", "$hash  $([IO.Path]::GetFileName($Path))`n", $utf8)
}

Push-Location $repository
try {
    if ([string]::IsNullOrWhiteSpace($CloudflaredBinary)) {
        $CloudflaredBinary = Join-Path $outputRoot 'cloudflared.exe'
        $url = 'https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-amd64.exe'
        for ($attempt = 1; $attempt -le 4; $attempt++) {
            try { Invoke-WebRequest -Uri $url -OutFile $CloudflaredBinary -TimeoutSec 120; break }
            catch { if ($attempt -eq 4) { throw }; Start-Sleep -Seconds 2 }
        }
    }
    $CloudflaredBinary = (Resolve-Path -LiteralPath $CloudflaredBinary).Path
    $signature = Get-AuthenticodeSignature -LiteralPath $CloudflaredBinary
    if ($signature.Status -ne [Management.Automation.SignatureStatus]::Valid) { throw 'The official cloudflared payload has an invalid Authenticode signature.' }
    if ($SignedBuild -and ([string]::IsNullOrWhiteSpace($env:WINDOWS_SIGNING_CERT_BASE64) -or [string]::IsNullOrWhiteSpace($env:WINDOWS_SIGNING_CERT_PASSWORD))) { throw 'SignedBuild requires both Windows signing secrets.' }
    if (-not $SignedBuild) { Write-Host 'AgentDock payloads are explicitly unsigned; cloudflared signature verification remains required.' }

    $helperRoot = Join-Path $outputRoot 'wsl-helper'
    & (Join-Path $PSScriptRoot 'build-wsl-helper-payload.ps1') -OutputDirectory $helperRoot
    foreach ($architecture in $Architectures | Select-Object -Unique) {
        $payload = Join-Path $outputRoot "payload-$architecture"
        $panel = Join-Path $outputRoot "control-panel-$architecture"
        New-Item -ItemType Directory -Force -Path $payload,$panel | Out-Null
        $env:CGO_ENABLED = '0'; $env:GOOS = 'windows'; $env:GOARCH = $architecture
        & go build -trimpath -ldflags $ldflags -o (Join-Path $payload 'agentdock.exe') ./cmd/agentdock
        Assert-NativeExit 'Core build'
        & go build -trimpath -ldflags '-s -w' -o (Join-Path $payload 'agentdock-arbiter.exe') ./cmd/agentdock-arbiter
        Assert-NativeExit 'Arbiter build'
        & go build -trimpath -ldflags '-s -w' -o (Join-Path $payload 'agentdock-shim.exe') ./cmd/agentdock-shim
        Assert-NativeExit 'CUI shim build'
        & go build -trimpath -ldflags '-s -w -H=windowsgui' -o (Join-Path $payload 'agentdock-tray-shim.exe') ./cmd/agentdock-shim
        Assert-NativeExit 'GUI shim build'
        $rid = if ($architecture -eq 'arm64') { 'win-arm64' } else { 'win-x64' }
        & dotnet publish ./desktop/windows/control-panel/AgentDock.ControlPanel.csproj -c Release -r $rid --self-contained true -o $panel
        Assert-NativeExit 'WPF publish'
        Copy-Item -LiteralPath (Join-Path $panel 'agentdock-tray.exe') -Destination (Join-Path $payload 'agentdock-tray.exe') -Force
        Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'assets\agentdock.ico') -Destination (Join-Path $payload 'agentdock.ico') -Force
        & python ./packaging/build-core-skill-bundle.py --output (Join-Path $payload 'share\agentdock\core-skills')
        Assert-NativeExit 'Core Skill bundle'
        Copy-Item -LiteralPath (Join-Path $repository 'LICENSE') -Destination (Join-Path $payload 'share\agentdock\LICENSE') -Force
        if (Test-Path -LiteralPath (Join-Path $payload 'wsl-helper')) { Remove-Item -LiteralPath (Join-Path $payload 'wsl-helper') -Recurse -Force }
        Copy-Item -LiteralPath $helperRoot -Destination (Join-Path $payload 'wsl-helper') -Recurse
        if ($SignedBuild) {
            & (Join-Path $PSScriptRoot 'sign-windows.ps1') -Path @('agentdock.exe','agentdock-tray.exe','agentdock-arbiter.exe','agentdock-shim.exe','agentdock-tray-shim.exe').ForEach({ Join-Path $payload $_ })
        }
        $archive = Join-Path $releaseRoot "agentdock_windows_$architecture.zip"
        $paths = @('agentdock.exe','agentdock-tray.exe','agentdock-arbiter.exe','agentdock-shim.exe','agentdock-tray-shim.exe','agentdock.ico','share','wsl-helper').ForEach({ Join-Path $payload $_ })
        Compress-Archive -LiteralPath $paths -DestinationPath $archive -Force
        Write-Checksum $archive
        $parameters = @{
            Version=$version; Architecture=$architecture; AgentDockArchive=$archive
            AgentDockChecksumFile="$archive.sha256"; CloudflaredBinary=$CloudflaredBinary; OutputDirectory=$releaseRoot
        }
        if ($SignedBuild) { $parameters.SignedBuild=$true }
        & (Join-Path $PSScriptRoot 'build-windows-offline-setup.ps1') @parameters
        $setup = Join-Path $releaseRoot "AgentDockSetup-$architecture.exe"
        if (-not (Test-Path -LiteralPath $setup -PathType Leaf)) { throw "Setup payload is missing: $architecture" }
        Write-Checksum $setup
        Write-Host "Windows $architecture release assets ready."
    }
    Copy-Item -LiteralPath (Join-Path $repository 'scripts\install\install.ps1') -Destination (Join-Path $releaseRoot 'install.ps1') -Force
    Write-Checksum (Join-Path $releaseRoot 'install.ps1')
    [ordered]@{
        version=$version; channel=$(if($Candidate){'candidate-not-released'}else{'release'}); source_dirty=($sourceChanges.Count -gt 0); changed_paths=$sourceChanges; commit=$commit; build_date=$buildDate; platforms=@($Architectures | ForEach-Object {"windows/$_"})
        agentdock_authenticode=$(if($SignedBuild){'signed'}else{'unsigned'}); cloudflared_authenticode='valid'
        wsl_helpers='Windows feature payload only; no separate Linux release'
    } | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $outputRoot 'build-report.json') -Encoding utf8NoBOM
} finally {
    $env:GOOS=$originalGoOS; $env:GOARCH=$originalGoArch; $env:CGO_ENABLED=$originalCGO
    Pop-Location
}
