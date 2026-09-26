#requires -Version 5.1
[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repository = (Resolve-Path (Join-Path $PSScriptRoot '..\..\..')).Path
$installerScript = Join-Path $repository 'scripts\install\install.ps1'
. $installerScript -LibraryOnly

Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem

function New-TestArchive {
    param(
        [Parameter(Mandatory = $true)][string] $Path,
        [scriptblock] $Mutate = $null,
        [switch] $BackslashPaths
    )

    $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    $archive = [IO.Compression.ZipArchive]::new($stream, [IO.Compression.ZipArchiveMode]::Create, $false)
    try {
        $entries = [ordered]@{
            'agentdock.exe' = 'core'
            'agentdock-tray.exe' = 'tray'
            'agentdock.ico' = 'icon'
            'agentdock-arbiter.exe' = 'arbiter'
            'agentdock-shim.exe' = 'shim'
            'agentdock-tray-shim.exe' = 'tray-shim'
            'share/agentdock/core-skills/manifest.json' = '{}'
            'share/agentdock/core-skills/README.md' = 'skill'
            'wsl-helper/manifest.json' = '{}'
            'wsl-helper/agentdock-wsl-helper-linux-amd64' = 'amd64'
            'wsl-helper/agentdock-wsl-helper-linux-arm64' = 'arm64'
            'docs/not-installed.txt' = 'irrelevant'
        }
        foreach ($pair in $entries.GetEnumerator()) {
            $name = if ($BackslashPaths) { $pair.Key.Replace('/', '\') } else { $pair.Key }
            Add-TestEntry -Archive $archive -Name $name -Content $pair.Value
        }
        if ($null -ne $Mutate) { & $Mutate $archive }
    } finally {
        $archive.Dispose()
        $stream.Dispose()
    }
}

function Add-TestEntry {
    param(
        [Parameter(Mandatory = $true)] [IO.Compression.ZipArchive] $Archive,
        [Parameter(Mandatory = $true)] [string] $Name,
        [string] $Content = '',
        [Nullable[Int32]] $ExternalAttributes = $null
    )

    $entry = $Archive.CreateEntry($Name, [IO.Compression.CompressionLevel]::Optimal)
    if ($null -ne $ExternalAttributes) {
        # PowerShell unwraps Nullable[Int32] parameters to an Int32 value when
        # supplied, so accessing .Value is not portable across pwsh versions.
        $entry.ExternalAttributes = [Int32] $ExternalAttributes
    }
    $entryStream = $entry.Open()
    try {
        $bytes = [Text.Encoding]::UTF8.GetBytes($Content)
        $entryStream.Write($bytes, 0, $bytes.Length)
    } finally {
        $entryStream.Dispose()
    }
}

function Assert-True([bool] $Condition, [string] $Message) {
    if (-not $Condition) { throw $Message }
}

function Assert-ArchiveRejected {
    param(
        [Parameter(Mandatory = $true)][string] $Name,
        [Parameter(Mandatory = $true)][scriptblock] $Mutate,
        [Parameter(Mandatory = $true)][string] $ExpectedMessage
    )

    $root = Join-Path $env:RUNNER_TEMP ("wb05-archive-$Name-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $root -Force | Out-Null
    $archivePath = Join-Path $root 'payload.zip'
    $destination = Join-Path $root 'extract'
    try {
        New-TestArchive -Path $archivePath -Mutate $Mutate
        $caught = $null
        try {
            Expand-AgentDockReleaseArchive -ArchivePath $archivePath -DestinationPath $destination | Out-Null
        } catch {
            $caught = $_
        }
        Assert-True ($null -ne $caught) "$Name archive was accepted"
        Assert-True ($caught.Exception.Message.Contains($ExpectedMessage)) "$Name error did not contain '$ExpectedMessage': $($caught.Exception.Message)"
        Assert-True (-not (Test-Path -LiteralPath $destination)) "$Name wrote output before catalogue validation completed"
    } finally {
        Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue
    }
}

$normalRoot = Join-Path $env:RUNNER_TEMP ("wb05-archive-normal-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $normalRoot -Force | Out-Null
try {
    $normalArchive = Join-Path $normalRoot 'payload.zip'
    $normalDestination = Join-Path $normalRoot 'extract'
    New-TestArchive -Path $normalArchive
    $summary = Expand-AgentDockReleaseArchive -ArchivePath $normalArchive -DestinationPath $normalDestination
    Assert-True ((Get-Content -LiteralPath (Join-Path $normalDestination 'agentdock.exe') -Raw) -eq 'core') 'core payload was not extracted'
    Assert-True (Test-Path -LiteralPath (Join-Path $normalDestination 'share\agentdock\core-skills\manifest.json')) 'Skill manifest was not extracted'
    Assert-True (-not (Test-Path -LiteralPath (Join-Path $normalDestination 'docs\not-installed.txt'))) 'irrelevant payload was written'
    Assert-True ($summary.entry_count -eq 12) "unexpected catalogue count: $($summary.entry_count)"
    Assert-True ($summary.selected_entry_count -eq 11) "unexpected selected count: $($summary.selected_entry_count)"
    Assert-True ($summary.skipped_bytes -gt 0) 'skipped byte evidence was not recorded'

    $legacyArchive = Join-Path $normalRoot 'powershell51.zip'
    $legacyDestination = Join-Path $normalRoot 'legacy-extract'
    New-TestArchive -Path $legacyArchive -BackslashPaths -Mutate {
        param($archive)
        Add-TestEntry -Archive $archive -Name 'share\agentdock\'
    }
    $legacy = Expand-AgentDockReleaseArchive -ArchivePath $legacyArchive -DestinationPath $legacyDestination
    Assert-True ($legacy.selected_entry_count -eq 11) 'legacy backslash ZIP did not preserve selected payload'
    Assert-True ((Get-Content -LiteralPath (Join-Path $legacyDestination 'share\agentdock\core-skills\manifest.json') -Raw) -eq '{}') 'legacy nested payload was not extracted'
    Assert-True (-not (Test-Path -LiteralPath (Join-Path $legacyDestination 'docs\not-installed.txt'))) 'legacy normalization bypassed selection'
} finally {
    Remove-Item -LiteralPath $normalRoot -Recurse -Force -ErrorAction SilentlyContinue
}

Assert-ArchiveRejected -Name 'traversal' -ExpectedMessage 'path' -Mutate {
    param($archive)
    Add-TestEntry -Archive $archive -Name '../escape.exe' -Content 'escape'
}

Assert-ArchiveRejected -Name 'duplicate' -ExpectedMessage 'duplicate' -Mutate {
    param($archive)
    Add-TestEntry -Archive $archive -Name 'AGENTDOCK.EXE' -Content 'duplicate'
}

foreach ($unsafe in @('..\escape.exe', 'share\..\escape.exe', '\rooted.exe', '\\server\share\file.exe', 'C:\drive.exe', 'share\\empty.exe')) {
    Assert-ArchiveRejected -Name 'backslash-unsafe' -ExpectedMessage 'path' -Mutate {
        param($archive)
        Add-TestEntry -Archive $archive -Name $unsafe -Content 'escape'
    }
}

Assert-ArchiveRejected -Name 'separator-alias' -ExpectedMessage 'duplicate' -Mutate {
    param($archive)
    Add-TestEntry -Archive $archive -Name 'share\agentdock\core-skills\manifest.json' -Content 'duplicate'
}

Assert-ArchiveRejected -Name 'backslash-parent-file' -ExpectedMessage 'file/directory conflict' -Mutate {
    param($archive)
    Add-TestEntry -Archive $archive -Name 'collision' -Content 'file'
    Add-TestEntry -Archive $archive -Name 'collision\child' -Content 'child'
}

$symlinkMode = [UInt32]::Parse('A1FF0000', [Globalization.NumberStyles]::HexNumber)
$symlinkAttributes = [BitConverter]::ToInt32([BitConverter]::GetBytes($symlinkMode), 0)
Assert-ArchiveRejected -Name 'symlink' -ExpectedMessage 'non-regular' -Mutate {
    param($archive)
    Add-TestEntry -Archive $archive -Name 'unused-link' -Content 'agentdock.exe' -ExternalAttributes $symlinkAttributes
}

Assert-ArchiveRejected -Name 'file-directory-conflict' -ExpectedMessage 'file/directory conflict' -Mutate {
    param($archive)
    Add-TestEntry -Archive $archive -Name 'collision' -Content 'file'
    Add-TestEntry -Archive $archive -Name 'collision/child' -Content 'child'
}

$timingRoot = Join-Path $env:RUNNER_TEMP ("wb05-timing-" + [Guid]::NewGuid().ToString('N'))
try {
    $timing = [pscustomobject]@{
        StartedAt = [DateTime]::UtcNow
        Stopwatch = [Diagnostics.Stopwatch]::StartNew()
        ReusedCachedPayload = $true
        Stages = [System.Collections.Generic.List[object]]::new()
    }
    Invoke-SetupMeasuredStage -Timing $timing -Stage 'verify' -Action { 'ok' } | Out-Null
    Write-SetupTimingSummary -Timing $timing -RuntimeRoot $timingRoot
    $written = Get-Content -LiteralPath (Join-Path $timingRoot 'install\setup-timing.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    Assert-True ($written.schema_version -eq 1) 'timing schema was not written'
    Assert-True ($written.reused_cached_payload -eq $true) 'cached payload marker was not written'
    Assert-True (@($written.stages).Count -eq 1) 'timing stage count is invalid'
    Assert-True ($written.stages[0].stage -eq 'verify') 'timing stage name is invalid'
} finally {
    Remove-Item -LiteralPath $timingRoot -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Host 'WB05 Setup archive and timing tests passed.'
