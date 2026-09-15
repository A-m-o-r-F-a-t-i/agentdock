[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string] $SourceHome,

    [Parameter(Mandatory = $true)]
    [string] $DestinationHome,

    [Parameter(Mandatory = $true)]
    [string] $RepositoryRoot,

    [string] $PluginPlanPath = "",

    [switch] $Force
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Resolve-FullPath {
    param([Parameter(Mandatory = $true)][string] $Path)
    return [IO.Path]::GetFullPath($Path)
}

function Read-JsonFile {
    param([Parameter(Mandatory = $true)][string] $Path)
    return Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json
}

function Write-JsonFile {
    param(
        [Parameter(Mandatory = $true)][string] $Path,
        [Parameter(Mandatory = $true)] $Value
    )
    $parent = Split-Path -Parent $Path
    if ($parent) {
        New-Item -ItemType Directory -Path $parent -Force | Out-Null
    }
    $json = $Value | ConvertTo-Json -Depth 40
    [IO.File]::WriteAllText($Path, $json + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
}

function Copy-Tree {
    param(
        [Parameter(Mandatory = $true)][string] $Source,
        [Parameter(Mandatory = $true)][string] $Destination
    )
    if (-not (Test-Path -LiteralPath $Source -PathType Container)) {
        throw "Source directory does not exist: $Source"
    }
    if (Test-Path -LiteralPath $Destination) {
        throw "Destination already exists: $Destination"
    }
    Write-Verbose "Copying directory '$Source' -> '$Destination'"
    New-Item -ItemType Directory -Path $Destination -Force | Out-Null
    $arguments = @(
        $Source, $Destination, '/E', '/COPY:DAT', '/DCOPY:DAT', '/R:2', '/W:1',
        '/XJ', '/NFL', '/NDL', '/NJH', '/NJS', '/NP',
        '/XF', '*.log', '*.tmp', '*.lock',
        '/XD', '.locks', '.tmp', '__pycache__', '.pytest_cache'
    )
    & robocopy @arguments | Out-Null
    Write-Verbose "robocopy exit code for '$Source': $LASTEXITCODE"
    if ($LASTEXITCODE -ge 8) {
        throw "robocopy failed ($LASTEXITCODE): $Source -> $Destination"
    }
}

function Get-Sha256Hex {
    param([Parameter(Mandatory = $true)][string] $Path)

    $stream = [IO.File]::OpenRead($Path)
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        return ([BitConverter]::ToString($sha.ComputeHash($stream))).Replace('-', '').ToLowerInvariant()
    }
    finally {
        $sha.Dispose()
        $stream.Dispose()
    }
}

function Get-StableFingerprint {
    param([Parameter(Mandatory = $true)][string] $Root)
    $rootPath = [IO.Path]::GetFullPath($Root).TrimEnd('\')
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        $enumerationErrors = @()
        $files = @(Get-ChildItem -LiteralPath $rootPath -Recurse -File -Force -ErrorAction SilentlyContinue -ErrorVariable +enumerationErrors)
        $lines = New-Object 'System.Collections.Generic.List[string]'
        $retry = $enumerationErrors.Count -gt 0
        foreach ($file in $files) {
            $relative = $file.FullName.Substring($rootPath.Length).TrimStart('\')
            if ($relative -match '(^|\\)(tmp|\.tmp|\.locks)(\\|$)' -or
                $relative -match '(?i)(^|\\)[^\\]+\.(log|lock|tmp)$') {
                continue
            }
            try {
                $hash = Get-Sha256Hex -Path $file.FullName
                $lines.Add(($relative.Replace('\', '/') + '|' + $file.Length + '|' + $hash))
            }
            catch [System.IO.FileNotFoundException], [System.Management.Automation.ItemNotFoundException] {
                $retry = $true
                break
            }
        }
        if (-not $retry) {
            $ordered = [string]::Join("`n", ($lines | Sort-Object))
            $bytes = [Text.Encoding]::UTF8.GetBytes($ordered)
            $sha = [Security.Cryptography.SHA256]::Create()
            try {
                return ([BitConverter]::ToString($sha.ComputeHash($bytes))).Replace('-', '').ToLowerInvariant()
            }
            finally {
                $sha.Dispose()
            }
        }
        Start-Sleep -Milliseconds (200 * $attempt)
    }
    throw "Could not obtain a stable source fingerprint after 3 attempts: $rootPath"
}

function Get-SkillVersion {
    param([Parameter(Mandatory = $true)][string] $SkillRoot)
    $document = Get-Content -LiteralPath (Join-Path $SkillRoot 'SKILL.md') -Raw -Encoding UTF8
    $match = [regex]::Match($document, '(?m)^version:\s*([^\s]+)\s*$')
    if (-not $match.Success) {
        throw "Skill version not found: $SkillRoot"
    }
    return $match.Groups[1].Value.Trim()
}

function Get-PropertyValue {
    param($Object, [string] $Name, $Default = $null)
    if ($null -eq $Object) {
        return $Default
    }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) {
        return $Default
    }
    return $property.Value
}

function Set-PropertyValue {
    param($Object, [string] $Name, $Value)
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) {
        $Object | Add-Member -NotePropertyName $Name -NotePropertyValue $Value
    }
    else {
        $property.Value = $Value
    }
}

function Find-McpServer {
    param($Document, [string] $Name)
    foreach ($server in @($Document.servers)) {
        if ([string]::Equals([string]$server.name, $Name, [StringComparison]::Ordinal)) {
            return $server
        }
    }
    return $null
}

$source = Resolve-FullPath $SourceHome
$destination = Resolve-FullPath $DestinationHome
$repository = Resolve-FullPath $RepositoryRoot
if (-not (Test-Path -LiteralPath $source -PathType Container)) {
    throw "AgentDock source home does not exist: $source"
}
if (-not (Test-Path -LiteralPath (Join-Path $repository 'core-skills') -PathType Container)) {
    throw "AgentDock repository core-skills directory does not exist: $repository"
}
if ([string]::Equals($source.TrimEnd('\'), $destination.TrimEnd('\'), [StringComparison]::OrdinalIgnoreCase)) {
    throw 'SourceHome and DestinationHome must be different directories.'
}
if ($destination.StartsWith($source.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase)) {
    throw 'DestinationHome may not be inside SourceHome.'
}
if (Test-Path -LiteralPath $destination) {
    if (-not $Force) {
        throw "Destination already exists: $destination"
    }
    Remove-Item -LiteralPath $destination -Recurse -Force
}
New-Item -ItemType Directory -Path $destination -Force | Out-Null
$sourceFingerprintBefore = Get-StableFingerprint $source

# Copy user state first, but never copy the legacy Skill store, old logical
# plugin registry, or volatile runtime temporary directory into the new home.
$excludedTopLevel = @('skill-store', 'plugins', 'tmp')
foreach ($entry in Get-ChildItem -LiteralPath $source -Force) {
    if ($excludedTopLevel -contains $entry.Name) {
        continue
    }
    $target = Join-Path $destination $entry.Name
    if ($entry.PSIsContainer) {
        Copy-Tree $entry.FullName $target
    }
    elseif ($entry.Extension -notin @('.log', '.lock', '.tmp')) {
        Copy-Item -LiteralPath $entry.FullName -Destination $target -Force
    }
}
Get-ChildItem -LiteralPath $destination -Recurse -Force -File -ErrorAction SilentlyContinue |
    Where-Object { $_.Extension -in @('.lock', '.log', '.tmp') } |
    Remove-Item -Force -ErrorAction SilentlyContinue

$legacySkillRoot = Join-Path $source 'skill-store'
$newSkillRoot = Join-Path $destination 'skills'
foreach ($relative in @('.system', '.versions', '.state', '.locks', '.cache', '.tmp')) {
    New-Item -ItemType Directory -Path (Join-Path $newSkillRoot $relative) -Force | Out-Null
}

$bundled = @{}
$bundledFile = Join-Path $legacySkillRoot 'bundled-skills.json'
if (Test-Path -LiteralPath $bundledFile -PathType Leaf) {
    $bundledDocument = Read-JsonFile $bundledFile
    foreach ($name in @($bundledDocument.skills)) {
        $bundled[[string]$name] = $true
    }
}

$migratedSkills = @()
$legacyStateRoot = Join-Path $legacySkillRoot 'state'
if (Test-Path -LiteralPath $legacyStateRoot -PathType Container) {
    foreach ($stateFile in Get-ChildItem -LiteralPath $legacyStateRoot -Filter '*.json' -File | Sort-Object Name) {
        $name = $stateFile.BaseName
        $oldState = Read-JsonFile $stateFile.FullName
        $activeVersion = [string](Get-PropertyValue $oldState 'active_version' '')
        if ([string]::IsNullOrWhiteSpace($activeVersion)) {
            continue
        }
        $isSystem = $bundled.ContainsKey($name)
        $versionRoot = Join-Path (Join-Path $legacySkillRoot 'installed') $name
        $activeSource = Join-Path $versionRoot $activeVersion
        if (-not (Test-Path -LiteralPath (Join-Path $activeSource 'SKILL.md') -PathType Leaf)) {
            throw "Active Skill package is missing: $name $activeVersion"
        }
        $activeDestination = if ($isSystem) {
            Join-Path (Join-Path $newSkillRoot '.system') $name
        }
        else {
            Join-Path $newSkillRoot $name
        }
        Copy-Tree $activeSource $activeDestination

        if (Test-Path -LiteralPath $versionRoot -PathType Container) {
            foreach ($versionDirectory in Get-ChildItem -LiteralPath $versionRoot -Directory | Sort-Object Name) {
                if ($versionDirectory.Name -eq $activeVersion) {
                    continue
                }
                $archiveDestination = Join-Path (Join-Path (Join-Path $newSkillRoot '.versions') $name) $versionDirectory.Name
                Copy-Tree $versionDirectory.FullName $archiveDestination
            }
        }

        $history = @((Get-PropertyValue $oldState 'history' @()))
        $updatedAt = Get-PropertyValue $oldState 'updated_at' ([DateTime]::UtcNow.ToString('o'))
        $selection = [ordered]@{
            active_version = $activeVersion
            history = $history
            disabled = [bool](Get-PropertyValue $oldState 'disabled' $false)
            system = $isSystem
            updated_at = $updatedAt
        }
        Write-JsonFile (Join-Path (Join-Path $newSkillRoot '.state') ($name + '.json')) $selection
        $migratedSkills += $name
    }
}

[IO.File]::WriteAllText(
    (Join-Path (Join-Path $newSkillRoot '.system') '.agentdock-system-skills.marker'),
    "agentdock-system-skills-v1`n",
    [Text.UTF8Encoding]::new($false))

# Overlay the exact core Skills shipped by the source tree used to build the
# installer. This keeps the migrated copy compatible when it is copied after
# Setup bootstraps the same release.
$upgradedCoreSkills = @()
$coreSkillsRoot = Join-Path $repository 'core-skills'
foreach ($coreSkill in Get-ChildItem -LiteralPath $coreSkillsRoot -Directory | Sort-Object Name) {
    if (-not (Test-Path -LiteralPath (Join-Path $coreSkill.FullName 'SKILL.md') -PathType Leaf)) {
        continue
    }
    $name = $coreSkill.Name
    $newVersion = Get-SkillVersion $coreSkill.FullName
    $selectionPath = Join-Path (Join-Path $newSkillRoot '.state') ($name + '.json')
    $selection = if (Test-Path -LiteralPath $selectionPath -PathType Leaf) {
        Read-JsonFile $selectionPath
    }
    else {
        [pscustomobject]@{}
    }
    $oldVersion = [string](Get-PropertyValue $selection 'active_version' '')
    $systemPath = Join-Path (Join-Path $newSkillRoot '.system') $name
    if (Test-Path -LiteralPath $systemPath -PathType Container) {
        if (-not [string]::IsNullOrWhiteSpace($oldVersion) -and $oldVersion -ne $newVersion) {
            $oldArchive = Join-Path (Join-Path (Join-Path $newSkillRoot '.versions') $name) $oldVersion
            if (-not (Test-Path -LiteralPath $oldArchive)) {
                Copy-Tree $systemPath $oldArchive
            }
        }
        Remove-Item -LiteralPath $systemPath -Recurse -Force
    }
    Copy-Tree $coreSkill.FullName $systemPath

    $history = New-Object 'System.Collections.Generic.List[string]'
    if (-not [string]::IsNullOrWhiteSpace($oldVersion) -and $oldVersion -ne $newVersion) {
        $history.Add($oldVersion)
    }
    foreach ($versionValue in @((Get-PropertyValue $selection 'history' @()))) {
        $version = [string]$versionValue
        if (-not [string]::IsNullOrWhiteSpace($version) -and $version -ne $newVersion -and -not $history.Contains($version)) {
            $history.Add($version)
        }
    }
    Write-JsonFile $selectionPath ([ordered]@{
        active_version = $newVersion
        history = @($history)
        disabled = [bool](Get-PropertyValue $selection 'disabled' $false)
        system = $true
        updated_at = [DateTime]::UtcNow.ToString('o')
    })
    $upgradedCoreSkills += $name
    if ($migratedSkills -notcontains $name) {
        $migratedSkills += $name
    }
}

$mcpRegistryPath = Join-Path (Join-Path $destination 'mcp') 'servers.json'
$mcpDocument = if (Test-Path -LiteralPath $mcpRegistryPath -PathType Leaf) {
    Read-JsonFile $mcpRegistryPath
}
else {
    [pscustomobject]@{ version = 1; servers = @() }
}
$removedMcp = [ordered]@{}
$archiveOnlyMcp = [ordered]@{}
$migratedPlugins = @()
$pluginPlan = $null
if (-not [string]::IsNullOrWhiteSpace($PluginPlanPath)) {
    $pluginPlan = Read-JsonFile (Resolve-FullPath $PluginPlanPath)
}

if ($null -ne $pluginPlan) {
    $pluginRoot = Join-Path $destination 'plugins'
    New-Item -ItemType Directory -Path (Join-Path $pluginRoot '.locks') -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $pluginRoot '.tmp') -Force | Out-Null
    $archiveRoot = Join-Path $destination 'migration-archive'

    foreach ($plugin in @($pluginPlan.plugins)) {
        $pluginName = [string]$plugin.name
        if ([string]::IsNullOrWhiteSpace($pluginName)) {
            throw 'Plugin plan contains an empty name.'
        }
        $installedPlugin = Join-Path $pluginRoot $pluginName
        New-Item -ItemType Directory -Path (Join-Path $installedPlugin '.agentdock-plugin') -Force | Out-Null
        New-Item -ItemType Directory -Path (Join-Path $installedPlugin 'skills') -Force | Out-Null

        $skillState = [ordered]@{}
        foreach ($skillNameValue in @($plugin.skills)) {
            $skillName = [string]$skillNameValue
            $userPath = Join-Path $newSkillRoot $skillName
            $systemPath = Join-Path (Join-Path $newSkillRoot '.system') $skillName
            $activePath = if (Test-Path -LiteralPath $userPath -PathType Container) { $userPath } elseif (Test-Path -LiteralPath $systemPath -PathType Container) { $systemPath } else { $null }
            if ($null -eq $activePath) {
                throw "Plugin Skill is not installed in migrated tree: $pluginName/$skillName"
            }
            Copy-Tree $activePath (Join-Path (Join-Path $installedPlugin 'skills') $skillName)
            $selectionPath = Join-Path (Join-Path $newSkillRoot '.state') ($skillName + '.json')
            $selection = Read-JsonFile $selectionPath
            $skillState[$skillName] = -not [bool](Get-PropertyValue $selection 'disabled' $false)

            $skillArchive = Join-Path $archiveRoot (Join-Path 'plugin-skill-history' (Join-Path $pluginName $skillName))
            New-Item -ItemType Directory -Path $skillArchive -Force | Out-Null
            Copy-Item -LiteralPath $selectionPath -Destination (Join-Path $skillArchive 'selection.json') -Force
            $versionsPath = Join-Path (Join-Path $newSkillRoot '.versions') $skillName
            if (Test-Path -LiteralPath $versionsPath -PathType Container) {
                Copy-Tree $versionsPath (Join-Path $skillArchive 'versions')
                Remove-Item -LiteralPath $versionsPath -Recurse -Force
            }
            Remove-Item -LiteralPath $activePath -Recurse -Force
            Remove-Item -LiteralPath $selectionPath -Force
        }

        $mcpMap = [ordered]@{}
        $mcpState = [ordered]@{}
        foreach ($serverPlan in @($plugin.mcp_servers)) {
            $serverName = [string]$serverPlan.name
            $existing = Find-McpServer $mcpDocument $serverName
            if ($null -eq $existing) {
                throw "Plugin MCP server is missing from migrated registry: $pluginName/$serverName"
            }
            $config = ($existing | ConvertTo-Json -Depth 40 | ConvertFrom-Json)
            $memberEnabled = [bool](Get-PropertyValue $existing 'enabled' $true)
            $enabledOverride = $serverPlan.PSObject.Properties['enabled']
            if ($null -ne $enabledOverride) {
                $memberEnabled = [bool]$enabledOverride.Value
            }
            foreach ($field in @('command', 'cwd', 'args', 'url', 'timeout_ms')) {
                $override = $serverPlan.PSObject.Properties[$field]
                if ($null -ne $override) {
                    Set-PropertyValue $config $field $override.Value
                }
            }
            Set-PropertyValue $config 'enabled' $true
            $implementationSource = [string](Get-PropertyValue $serverPlan 'implementation_source' '')
            $implementationTarget = [string](Get-PropertyValue $serverPlan 'implementation_target' '')
            if (-not [string]::IsNullOrWhiteSpace($implementationSource)) {
                if ([string]::IsNullOrWhiteSpace($implementationTarget)) {
                    throw "implementation_target is required for $pluginName/$serverName"
                }
                Copy-Tree (Resolve-FullPath $implementationSource) (Join-Path $installedPlugin $implementationTarget)
            }
            $mcpMap[$serverName] = $config
            $mcpState[$serverName] = $memberEnabled
            $removedMcp[$serverName] = $existing
        }

        $manifest = [ordered]@{
            schema_version = 1
            name = $pluginName
            description = [string]$plugin.description
            version = [string]$plugin.version
            mcpServers = $mcpMap
        }
        $state = [ordered]@{
            enabled = [bool](Get-PropertyValue $plugin 'enabled' $true)
            skills = $skillState
            mcpServers = $mcpState
        }
        Write-JsonFile (Join-Path (Join-Path $installedPlugin '.agentdock-plugin') 'plugin.json') $manifest
        Write-JsonFile (Join-Path (Join-Path $installedPlugin '.agentdock-plugin') 'state.json') $state
        $migratedPlugins += $pluginName
    }

    foreach ($serverNameValue in @((Get-PropertyValue $pluginPlan 'archive_mcp_servers' @()))) {
        $serverName = [string]$serverNameValue
        $existing = Find-McpServer $mcpDocument $serverName
        if ($null -ne $existing) {
            $removedMcp[$serverName] = $existing
            $archiveOnlyMcp[$serverName] = $true
        }
    }

    $remainingServers = @()
    foreach ($server in @($mcpDocument.servers)) {
        if (-not $removedMcp.Contains([string]$server.name)) {
            $remainingServers += $server
        }
    }
    $mcpDocument.servers = $remainingServers
    Write-JsonFile $mcpRegistryPath $mcpDocument
    if ($removedMcp.Count -gt 0) {
        Write-JsonFile (Join-Path $archiveRoot 'removed-standalone-mcp.json') ([ordered]@{ version = 1; servers = @($removedMcp.Values) })
    }
    if ($archiveOnlyMcp.Count -gt 0) {
        $mcpEnvironmentRoot = Join-Path (Join-Path $destination 'env') 'mcp'
        $mcpEnvironmentArchive = Join-Path $archiveRoot 'mcp-env'
        foreach ($serverName in @($archiveOnlyMcp.Keys)) {
            $environmentPath = Join-Path $mcpEnvironmentRoot ($serverName + '.env')
            if (-not (Test-Path -LiteralPath $environmentPath -PathType Leaf)) {
                continue
            }
            New-Item -ItemType Directory -Path $mcpEnvironmentArchive -Force | Out-Null
            Move-Item -LiteralPath $environmentPath -Destination (Join-Path $mcpEnvironmentArchive ($serverName + '.env')) -Force
        }
    }
}

$forbiddenCache = Join-Path (Join-Path $destination 'plugins') 'cache'
$forbiddenRegistry = Join-Path (Join-Path $destination 'plugins') 'plugins.json'
if (Test-Path -LiteralPath $forbiddenCache) {
    throw 'Migration produced the forbidden plugins/cache directory.'
}
if (Test-Path -LiteralPath $forbiddenRegistry) {
    throw 'Migration produced the forbidden central plugins.json registry.'
}
if (Test-Path -LiteralPath (Join-Path $destination 'skill-store')) {
    throw 'Migration left the legacy skill-store in the destination.'
}

$sourceFingerprintAfter = Get-StableFingerprint $source
if ($sourceFingerprintBefore -ne $sourceFingerprintAfter) {
    throw 'Source AgentDock home changed during migration; discard the destination and retry after stopping writers.'
}

$currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
& icacls $destination /inheritance:r /grant:r "*$currentSid`:(OI)(CI)F" '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "Failed to secure destination root ACL: $LASTEXITCODE"
}
& icacls (Join-Path $destination '*') /reset /T /C | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "Failed to restore child ACL inheritance: $LASTEXITCODE"
}

$report = [ordered]@{
    schema_version = 1
    source_home = $source
    destination_home = $destination
    source_fingerprint = $sourceFingerprintAfter
    created_at = [DateTime]::UtcNow.ToString('o')
    migrated_skills = @($migratedSkills | Sort-Object)
    migrated_plugins = @($migratedPlugins | Sort-Object)
    upgraded_core_skills = @($upgradedCoreSkills | Sort-Object)
    archived_mcp_servers = @($removedMcp.Keys | Sort-Object)
    archived_mcp_environment_files = @($archiveOnlyMcp.Keys | Where-Object {
        Test-Path -LiteralPath (Join-Path (Join-Path (Join-Path $destination 'migration-archive') 'mcp-env') ($_ + '.env'))
    } | Sort-Object)
    excluded_top_level = $excludedTopLevel
    source_modified = $false
    plugin_cache_present = $false
    central_plugin_registry_present = $false
}
Write-JsonFile (Join-Path $destination 'migration-report.json') $report
$report | ConvertTo-Json -Depth 20
