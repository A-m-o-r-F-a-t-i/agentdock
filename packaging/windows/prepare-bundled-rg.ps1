#requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string] $Destination,
    [string] $CacheDirectory = (Join-Path ([IO.Path]::GetTempPath()) 'agentdock-build-cache'),
    [switch] $VerifyOnly
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$repository = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$specPath = Join-Path $repository 'internal\bundledrg\windows-amd64.json'
$spec = Get-Content -LiteralPath $specPath -Raw | ConvertFrom-Json
if ($spec.schema_version -ne 1 -or $spec.platform -ne 'windows/amd64' -or
    $spec.archive_sha256 -notmatch '^[0-9a-f]{64}$' -or $spec.url -match '/latest/' -or
    -not $spec.url.StartsWith('https://github.com/BurntSushi/ripgrep/releases/download/')) {
    throw 'Unsupported or unpinned ripgrep specification.'
}
$destinationRoot = [IO.Path]::GetFullPath($Destination)

function Assert-Bundle([string] $Root) {
    $manifestPath = Join-Path $Root 'manifest.json'
    $manifest = Get-Item -LiteralPath $manifestPath
    if ($manifest.Length -gt 16384 -or ($manifest.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Invalid bundled manifest.' }
    $actual = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    if (($actual | ConvertTo-Json -Depth 8 -Compress) -cne ($spec | ConvertTo-Json -Depth 8 -Compress)) { throw 'Bundled manifest differs from pinned specification.' }
    foreach ($file in $spec.files) {
        if ($file.path -notin @('rg.exe','COPYING','LICENSE-MIT','UNLICENSE')) { throw 'Unexpected ripgrep file name.' }
        $path = Join-Path $Root $file.path
        $item = Get-Item -LiteralPath $path -ErrorAction Stop
        if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -or $item.Length -ne $file.size) {
            throw "Incomplete or redirected bundled ripgrep file: $($file.path)"
        }
        if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -ne $file.sha256) {
            throw "Bundled ripgrep SHA-256 mismatch: $($file.path)"
        }
    }
    $image = [IO.File]::ReadAllBytes((Join-Path $Root 'rg.exe'))
    $pe = [BitConverter]::ToInt32($image, 0x3c)
    if ($pe -lt 64 -or $pe -gt $image.Length - 26 -or [BitConverter]::ToUInt32($image, $pe) -ne 0x4550 -or
        [BitConverter]::ToUInt16($image, $pe + 4) -ne 0x8664 -or [BitConverter]::ToUInt16($image, $pe + 24) -ne 0x20b) {
        throw 'Bundled ripgrep must be a Windows x64 PE image.'
    }
}

if ($VerifyOnly -or (Test-Path -LiteralPath $destinationRoot)) {
    Assert-Bundle $destinationRoot
} else {
    $cache = [IO.Path]::GetFullPath($CacheDirectory)
    New-Item -ItemType Directory -Path $cache -Force | Out-Null
    $archivePath = Join-Path $cache $spec.asset
    if (-not (Test-Path -LiteralPath $archivePath)) {
        $partial = $archivePath + '.partial-' + [guid]::NewGuid().ToString('N')
        try {
            Invoke-WebRequest -Uri $spec.url -OutFile $partial -TimeoutSec 120
            if ((Get-Item -LiteralPath $partial).Length -ne $spec.archive_size -or
                (Get-FileHash -LiteralPath $partial -Algorithm SHA256).Hash.ToLowerInvariant() -ne $spec.archive_sha256) {
                throw 'Official ripgrep download failed pinned size/SHA-256 verification.'
            }
            Move-Item -LiteralPath $partial -Destination $archivePath
        } finally {
            if (Test-Path -LiteralPath $partial) { Remove-Item -LiteralPath $partial -Force }
        }
    }
    if ((Get-Item -LiteralPath $archivePath).Length -ne $spec.archive_size -or
        (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant() -ne $spec.archive_sha256) {
        throw 'Cached ripgrep archive is incomplete or corrupt. It was not executed or silently replaced.'
    }
    $parent = Split-Path -Parent $destinationRoot
    New-Item -ItemType Directory -Path $parent -Force | Out-Null
    $staging = Join-Path $parent ('.rg-stage-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $staging | Out-Null
    try {
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        $archive = [IO.Compression.ZipFile]::OpenRead($archivePath)
        try {
            foreach ($file in $spec.files) {
                $name = $spec.archive_root + '/' + $file.path
                $entries = @($archive.Entries | Where-Object FullName -CEQ $name)
                if ($entries.Count -ne 1 -or $entries[0].Length -ne $file.size) { throw "Pinned archive file is missing/duplicate/incomplete: $name" }
                [IO.Compression.ZipFileExtensions]::ExtractToFile($entries[0], (Join-Path $staging $file.path), $false)
            }
        } finally { $archive.Dispose() }
        Copy-Item -LiteralPath $specPath -Destination (Join-Path $staging 'manifest.json')
        Assert-Bundle $staging
        Move-Item -LiteralPath $staging -Destination $destinationRoot
    } finally {
        if (Test-Path -LiteralPath $staging) { Remove-Item -LiteralPath $staging -Recurse -Force }
    }
}
[pscustomobject]@{
    version = $spec.version
    platform = $spec.platform
    path = (Join-Path $destinationRoot 'rg.exe')
    sha256 = $spec.files[0].sha256
    licence = $spec.license
}
