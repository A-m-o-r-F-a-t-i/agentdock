[CmdletBinding()]
param(
    [string] $Version = 'latest',
    [string] $OfflineArchive = '',
    [string] $OfflineChecksumFile = '',
    [string] $OfflineCloudflaredBinary = '',
    [string] $InstallDir = (Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'AgentDock\bin'),
    [switch] $RegisterStartup,
    [switch] $ConfigurePublicAccess,
    [int] $Port = 8765,
    [string] $AuthToken = '',
    [ValidateSet('auto', 'none', 'quick', 'named')]
    [string] $TunnelMode = 'auto',
    [string] $ServerUrl = '',
    [string] $TunnelToken = '',
    [string] $TunnelTokenFile = '',
    [switch] $DeleteTunnelTokenFile,
    [string] $ResultFile = '',
    [ValidateSet('script', 'setup')]
    [string] $InstallChannel = 'script',
    [string] $OAuthPassword = '',
    [string] $OAuthTokenSecret = '',
    [string] $StartupValueName = 'AgentDock',
    [string] $CloudflaredStartupValueName = 'AgentDockCloudflared',
    [string] $TrayStartupValueName = 'AgentDockTray',
    [ValidateSet('standard', 'elevated')]
    [string] $CorePrivilegeMode = 'standard',
    [switch] $LibraryOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

function Invoke-SetupRuntimeProcess {
    param(
        [string] $FilePath,
        [string] $Arguments = '',
        [switch] $WaitForExit,
        [switch] $PassThruOutput,
        [int] $TimeoutSeconds = 180
    )

    if ($InstallChannel -ne 'setup') {
        throw 'The Setup runtime launcher is only valid for InstallChannel=setup.'
    }
    if (-not (Test-Path -LiteralPath $setupRuntimeLauncherPath -PathType Leaf)) {
        throw "Setup runtime launcher was not found: $setupRuntimeLauncherPath"
    }

    # The native GUI broker starts long-lived runtime commands in the same
    # signed-in user's session without creating a PowerShell console window.
    # Inno's filesystem protections remain enabled throughout installation.
    # The validated payload core stays independent of trial/rollback pointers.
    # The stable shim rejects an uncommitted installer generation, and an old
    # generation may still contain the task-start bug being repaired.
    & $setupRuntimeLauncherPath `
        -FilePath $FilePath `
        -AgentDockBinary $sourceBinary `
        -Arguments $Arguments `
        -WaitForExit:$WaitForExit `
        -PassThruOutput:$PassThruOutput `
        -TimeoutSeconds $TimeoutSeconds
}

function ConvertTo-NativeArguments {
    param([string[]] $Values)
    # Windows CommandLineToArgvW/CRT quoting, including trailing backslashes.
    return (($Values | ForEach-Object {
        $value = [string] $_
        if ($value -notmatch '[\s"]' -and $value.Length -gt 0) {
            $value
        } else {
            $escaped = [regex]::Replace($value, '(\\*)"', '$1$1\"')
            '"' + [regex]::Replace($escaped, '(\\+)$', '$1$1') + '"'
        }
    }) -join ' ')
}

function Test-OwnInstallerTransaction {
    if (-not $engineInvoked -or [string]::IsNullOrWhiteSpace($engineTransactionId)) { return $false }
    $path = Join-Path $runtimeDir 'install\transaction.json'
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { return $false }
    try {
        $transaction = Get-Content -LiteralPath $path -Raw -Encoding UTF8 | ConvertFrom-Json
        return [string]::Equals([string]$transaction.transaction_id, $engineTransactionId, [StringComparison]::Ordinal)
    } catch { return $false }
}

function Get-AgentDockArchitecture {
    $architecture = $env:PROCESSOR_ARCHITECTURE
    if ($env:PROCESSOR_ARCHITEW6432) {
        $architecture = $env:PROCESSOR_ARCHITEW6432
    }
    if ([string]::IsNullOrWhiteSpace($architecture)) {
        $architecture = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    }

    switch ($architecture.ToUpperInvariant()) {
        'AMD64' { return 'amd64' }
        'X64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        default { throw "Unsupported Windows architecture: $architecture" }
    }
}

function Get-ReleaseBaseUrl {
    param([string] $RequestedVersion)

    $customBaseUrl = [Environment]::GetEnvironmentVariable('AGENTDOCK_RELEASE_BASE_URL')
    if (-not [string]::IsNullOrWhiteSpace($customBaseUrl)) {
        return $customBaseUrl.TrimEnd('/')
    }

    if ($RequestedVersion -eq 'latest') {
        return 'https://github.com/A-m-o-r-F-a-t-i/AgentDock-Workbench/releases/latest/download'
    }

    $normalizedVersion = $RequestedVersion
    if (-not $normalizedVersion.StartsWith('v')) {
        $normalizedVersion = "v$normalizedVersion"
    }
    return "https://github.com/A-m-o-r-F-a-t-i/AgentDock-Workbench/releases/download/$normalizedVersion"
}

function Get-CloudflaredReleaseBaseUrl {
    $customBaseUrl = [Environment]::GetEnvironmentVariable('AGENTDOCK_CLOUDFLARED_RELEASE_BASE_URL')
    if (-not [string]::IsNullOrWhiteSpace($customBaseUrl)) {
        return $customBaseUrl.TrimEnd('/')
    }
    return 'https://github.com/cloudflare/cloudflared/releases/latest/download'
}

function Get-Sha256Hex {
    param([string] $Path)

    $sha256 = [System.Security.Cryptography.SHA256]::Create()
    try {
        $stream = [IO.File]::OpenRead($Path)
        try {
            $hash = $sha256.ComputeHash($stream)
        } finally {
            $stream.Dispose()
        }
    } finally {
        $sha256.Dispose()
    }
    return [BitConverter]::ToString($hash).Replace('-', '').ToLowerInvariant()
}

function Add-UserPath {
    param([string] $Directory)

    $currentPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $pathParts = @($currentPath -split ';' | Where-Object { $_ })
    if ($pathParts -notcontains $Directory) {
        $updatedPath = (@($pathParts) + $Directory) -join ';'
        [Environment]::SetEnvironmentVariable('Path', $updatedPath, 'User')
    }
    if (($env:Path -split ';') -notcontains $Directory) {
        $env:Path = "$env:Path;$Directory"
    }
}

function New-AgentDockSecret {
    param([int] $ByteCount = 32)

    $bytes = New-Object byte[] $ByteCount
    $generator = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $generator.GetBytes($bytes)
    } finally {
        $generator.Dispose()
    }
    return -join ($bytes | ForEach-Object { $_.ToString('x2') })
}

function New-AgentDockToken {
    return New-AgentDockSecret -ByteCount 32
}

function Write-ProtectedText {
    param(
        [string] $Path,
        [string] $Value,
        [string] $Entropy
    )

    $protectedBytes = [System.Security.Cryptography.ProtectedData]::Protect(
        [Text.Encoding]::UTF8.GetBytes($Value),
        [Text.Encoding]::UTF8.GetBytes($Entropy),
        [System.Security.Cryptography.DataProtectionScope]::CurrentUser
    )
    [IO.File]::WriteAllText($Path, [Convert]::ToBase64String($protectedBytes), $Utf8NoBom)
}

function Read-ProtectedText {
    param(
        [string] $Path,
        [string] $Entropy
    )

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return ''
    }
    for ($attempt = 0; $attempt -lt 3; $attempt++) {
        try {
            $protectedBytes = [Convert]::FromBase64String([IO.File]::ReadAllText($Path).Trim())
            $plainBytes = [System.Security.Cryptography.ProtectedData]::Unprotect(
                $protectedBytes,
                [Text.Encoding]::UTF8.GetBytes($Entropy),
                [System.Security.Cryptography.DataProtectionScope]::CurrentUser
            )
            $value = [Text.Encoding]::UTF8.GetString($plainBytes)
            if (-not [string]::IsNullOrWhiteSpace($value)) {
                return $value
            }
        } catch {
            # Retry below before treating the DPAPI value as unreadable.
        }
        if ($attempt -lt 2) {
            Start-Sleep -Milliseconds 50
        }
    }
    return ''
}

function Backup-UnreadableProtectedText {
    param([string] $Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return
    }
    $data = [IO.File]::ReadAllBytes($Path)
    if ($data.Length -eq 0) {
        return
    }
    $stamp = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssfffffffZ', [Globalization.CultureInfo]::InvariantCulture)
    $backupPath = $Path + '.unreadable-' + $stamp + '-' + [Guid]::NewGuid().ToString('N') + '.bak'
    [IO.File]::WriteAllBytes($backupPath, $data)

    $directory = Split-Path -Parent $Path
    $pattern = (Split-Path -Leaf $Path) + '.unreadable-*.bak'
    $backups = @(Get-ChildItem -LiteralPath $directory -Filter $pattern -File | Sort-Object Name)
    while ($backups.Count -gt 3) {
        Remove-Item -LiteralPath $backups[0].FullName -Force
        $backups = @($backups | Select-Object -Skip 1)
    }
}

function Read-TextFile {
    param([string] $Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return ''
    }
    return [IO.File]::ReadAllText($Path).Trim()
}

function Write-TextFile {
    param(
        [string] $Path,
        [string] $Value
    )

    [IO.File]::WriteAllText($Path, $Value, $Utf8NoBom)
}

function Normalize-ServerUrl {
    param([string] $Value)

    $trimmed = $Value.Trim().TrimEnd('/')
    if ([string]::IsNullOrWhiteSpace($trimmed)) {
        throw 'A fixed Cloudflare hostname requires an HTTPS public origin.'
    }
    try {
        $uri = [Uri] $trimmed
    } catch {
        throw "Invalid public origin: $Value"
    }
    if (-not $uri.IsAbsoluteUri -or $uri.Scheme -ne 'https' -or [string]::IsNullOrWhiteSpace($uri.Host)) {
        throw "The public origin must be an absolute HTTPS URL: $Value"
    }
    if ($uri.AbsolutePath -ne '/' -or $uri.Query -or $uri.Fragment -or $uri.UserInfo) {
        throw "The public origin must not contain a path, query, fragment, or user info: $Value"
    }
    return $trimmed
}

function Resolve-TunnelMode {
    param(
        [string] $RequestedMode,
        [string] $ModePath,
        [bool] $StartupRequested,
        [bool] $PublicAccessRequested
    )

    if ($RequestedMode -ne 'auto') {
        return $RequestedMode.ToLowerInvariant()
    }

    $environmentMode = [Environment]::GetEnvironmentVariable('AGENTDOCK_TUNNEL_MODE')
    if (-not [string]::IsNullOrWhiteSpace($environmentMode)) {
        $environmentMode = $environmentMode.Trim().ToLowerInvariant()
        if (@('none', 'quick', 'named') -notcontains $environmentMode) {
            throw "AGENTDOCK_TUNNEL_MODE must be none, quick, or named: $environmentMode"
        }
        return $environmentMode
    }

    $storedMode = Read-TextFile -Path $ModePath
    if (-not [string]::IsNullOrWhiteSpace($storedMode)) {
        $storedMode = $storedMode.ToLowerInvariant()
        if (@('quick', 'named') -contains $storedMode) {
            Write-Host "Reusing public access mode: $storedMode"
            return $storedMode
        }
    }

    if (-not $StartupRequested -or -not $PublicAccessRequested) {
        return 'none'
    }

    Write-Host ''
    Write-Host 'Choose public access:'
    Write-Host '- Have a Cloudflare domain: use a fixed address for long-running clients and OAuth.'
    Write-Host '- No domain: create a temporary address for a quick trial. It changes after cloudflared restarts.'
    $answer = Read-Host 'Do you have a domain already connected to Cloudflare? [y/N]'
    if ($answer -match '^(?i:y|yes)$') {
        return 'named'
    }
    return 'quick'
}

function Read-SecretFile {
    param([string] $Path)

    if ([string]::IsNullOrWhiteSpace($Path)) {
        return ''
    }
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Secret file was not found: $Path"
    }
    $value = (Get-Content -LiteralPath $Path -Raw).Trim()
    if ([string]::IsNullOrWhiteSpace($value)) {
        throw "Secret file is empty: $Path"
    }
    return $value
}

function Resolve-AvailableTunnelToken {
    param(
        [string] $TokenPath,
        [string] $RequestedToken,
        [string] $TokenFile
    )

    $existingToken = Read-ProtectedText -Path $TokenPath -Entropy 'agentdock.cloudflare.tunnel.v1'
    $resolvedToken = $RequestedToken
    if ([string]::IsNullOrWhiteSpace($resolvedToken) -and -not [string]::IsNullOrWhiteSpace($TokenFile)) {
        $resolvedToken = Read-SecretFile -Path $TokenFile
    }
    if ([string]::IsNullOrWhiteSpace($resolvedToken)) {
        $resolvedToken = [Environment]::GetEnvironmentVariable('AGENTDOCK_CLOUDFLARE_TUNNEL_TOKEN')
    }
    if ([string]::IsNullOrWhiteSpace($resolvedToken)) {
        $resolvedToken = $existingToken
    }

    return [pscustomobject]@{
        Token = $resolvedToken
        ExistingToken = $existingToken
    }
}

function Initialize-OAuthCredentials {
    param(
        [string] $PasswordPath,
        [string] $TokenSecretPath,
        [string] $RequestedPassword,
        [string] $RequestedTokenSecret
    )

    $existingPassword = Read-ProtectedText -Path $PasswordPath -Entropy 'agentdock.oauth.password.v1'
    $password = $RequestedPassword
    if ([string]::IsNullOrWhiteSpace($password)) {
        $password = [Environment]::GetEnvironmentVariable('AGENTDOCK_OAUTH_PASSWORD')
    }
    if ([string]::IsNullOrWhiteSpace($password)) {
        $password = $existingPassword
    }
    if ([string]::IsNullOrWhiteSpace($password)) {
        $password = New-AgentDockSecret -ByteCount 12
    }
    if ($password.Length -lt 12) {
        throw 'OAuth password must contain at least 12 characters.'
    }
    if (-not [string]::Equals($password, $existingPassword, [StringComparison]::Ordinal)) {
        if ([string]::IsNullOrWhiteSpace($existingPassword)) {
            Backup-UnreadableProtectedText -Path $PasswordPath
        }
        Write-ProtectedText -Path $PasswordPath -Value $password -Entropy 'agentdock.oauth.password.v1'
    }

    $existingTokenSecret = Read-ProtectedText -Path $TokenSecretPath -Entropy 'agentdock.oauth.secret.v1'
    $tokenSecret = $RequestedTokenSecret
    if ([string]::IsNullOrWhiteSpace($tokenSecret)) {
        $tokenSecret = [Environment]::GetEnvironmentVariable('AGENTDOCK_OAUTH_TOKEN_SECRET')
    }
    if ([string]::IsNullOrWhiteSpace($tokenSecret)) {
        $tokenSecret = $existingTokenSecret
    }
    if ([string]::IsNullOrWhiteSpace($tokenSecret)) {
        $tokenSecret = New-AgentDockSecret -ByteCount 32
    }
    if ([Text.Encoding]::UTF8.GetByteCount($tokenSecret) -lt 32) {
        throw 'OAuth token secret must contain at least 32 bytes.'
    }
    if (-not [string]::Equals($tokenSecret, $existingTokenSecret, [StringComparison]::Ordinal)) {
        if ([string]::IsNullOrWhiteSpace($existingTokenSecret)) {
            Backup-UnreadableProtectedText -Path $TokenSecretPath
        }
        Write-ProtectedText -Path $TokenSecretPath -Value $tokenSecret -Entropy 'agentdock.oauth.secret.v1'
    }

    return [pscustomobject]@{
        Password = $password
        TokenSecret = $tokenSecret
    }
}

function ConvertTo-InstallResultValue {
    param(
        [string] $Value,
        [int] $MaxLength = 0
    )

    if ([string]::IsNullOrEmpty($Value)) {
        return ''
    }
    $normalized = $Value.Replace("`r", ' ').Replace("`n", ' ')
    if (($MaxLength -gt 0) -and ($normalized.Length -gt $MaxLength)) {
        return $normalized.Substring(0, $MaxLength)
    }
    return $normalized
}

function Write-InstallResult {
    param(
        [string] $Path,
        [bool] $Success,
        [string] $Message,
        [string] $InstalledVersion,
        [string] $LocalMCPUrl,
        [string] $PublicMCPUrl,
        [string] $BearerToken,
        [string] $OAuthLoginPassword,
        [string] $HealthStatus,
        [string] $PrivilegeMode,
        [string] $ErrorCode = '',
        [System.Management.Automation.ErrorRecord] $ErrorRecord = $null,
        [string] $WarningCode = '',
        [string] $WarningMessage = ''
    )

    if ([string]::IsNullOrWhiteSpace($Path)) {
        return
    }
    $parent = Split-Path -Parent $Path
    if (-not [string]::IsNullOrWhiteSpace($parent)) {
        New-Item -ItemType Directory -Path $parent -Force | Out-Null
    }

    $errorType = ''
    $errorId = ''
    $errorCategory = ''
    $errorScript = ''
    $errorLine = 0
    $errorColumn = 0
    $errorStack = ''
    if ($null -ne $ErrorRecord) {
        if ($null -ne $ErrorRecord.Exception) {
            $errorType = $ErrorRecord.Exception.GetType().FullName
        }
        $errorId = [string] $ErrorRecord.FullyQualifiedErrorId
        $errorCategory = [string] $ErrorRecord.CategoryInfo.Category
        if ($null -ne $ErrorRecord.InvocationInfo) {
            $errorScript = [string] $ErrorRecord.InvocationInfo.ScriptName
            $errorLine = $ErrorRecord.InvocationInfo.ScriptLineNumber
            $errorColumn = $ErrorRecord.InvocationInfo.OffsetInLine
        }
        $errorStack = [string] $ErrorRecord.ScriptStackTrace
    }

    $safeMessage = ConvertTo-InstallResultValue -Value $Message
    $safeWarningMessage = ConvertTo-InstallResultValue -Value $WarningMessage
    $safeErrorType = ConvertTo-InstallResultValue -Value $errorType
    $safeErrorId = ConvertTo-InstallResultValue -Value $errorId
    $safeErrorCategory = ConvertTo-InstallResultValue -Value $errorCategory
    $safeErrorScript = ConvertTo-InstallResultValue -Value $errorScript
    $safeErrorStack = ConvertTo-InstallResultValue -Value $errorStack -MaxLength 2048
    $lines = @(
        '[AgentDock]',
        "Success=$($Success.ToString().ToLowerInvariant())",
        "Code=$ErrorCode",
        "Message=$safeMessage",
        "ErrorType=$safeErrorType",
        "ErrorId=$safeErrorId",
        "ErrorCategory=$safeErrorCategory",
        "ErrorScript=$safeErrorScript",
        "ErrorLine=$errorLine",
        "ErrorColumn=$errorColumn",
        "ErrorStack=$safeErrorStack",
        "WarningCode=$WarningCode",
        "WarningMessage=$safeWarningMessage",
        "Version=$InstalledVersion",
        "LocalMCPUrl=$LocalMCPUrl",
        "PublicMCPUrl=$PublicMCPUrl",
        "BearerToken=$BearerToken",
        "OAuthPassword=$OAuthLoginPassword",
        "Health=$HealthStatus",
        "PrivilegeMode=$PrivilegeMode"
    )
    # Windows INI APIs read BOM-prefixed UTF-16 reliably, including localized errors.
    [IO.File]::WriteAllLines($Path, $lines, [Text.Encoding]::Unicode)
}

function Get-InstallerEngineFailureMessage {
    param(
        [string] $RuntimeRoot,
        [string] $FallbackMessage,
        [DateTime] $NotBeforeUtc = [DateTime]::MinValue
    )

    $engineResultPath = Join-Path $RuntimeRoot 'install\result.json'
    if (-not (Test-Path -LiteralPath $engineResultPath -PathType Leaf)) {
        return $FallbackMessage
    }

    try {
        $engineResultInfo = Get-Item -LiteralPath $engineResultPath -ErrorAction Stop
        if ($NotBeforeUtc -ne [DateTime]::MinValue -and
            $engineResultInfo.LastWriteTimeUtc -lt $NotBeforeUtc) {
            return $FallbackMessage
        }

        # Engine results are UTF-8 JSON. Read failure text from the structured file instead of
        # letting Windows PowerShell 5.1 reinterpret native UTF-8 stderr through an OEM code page.
        $engineResultText = [IO.File]::ReadAllText($engineResultPath, [Text.Encoding]::UTF8)
        $engineResult = $engineResultText | ConvertFrom-Json
        if ($null -ne $engineResult.failure -and
            -not [string]::IsNullOrWhiteSpace([string] $engineResult.failure.message)) {
            return [string] $engineResult.failure.message
        }
    } catch {
        Write-Warning "Unable to read Installer Engine failure result: $($_.Exception.Message)"
    }
    return $FallbackMessage
}

function Get-ProcessesByPath {
    param(
        [string] $ProcessName,
        [string] $BinaryPath
    )

    $normalizedBinaryPath = [IO.Path]::GetFullPath($BinaryPath)
    $matchingProcessIds = @()

    # Win32_Process exposes ExecutablePath reliably in installer contexts where
    # Get-Process.Path may be empty even for a process owned by the current user.
    try {
        $matchingProcessIds = @(Get-CimInstance Win32_Process -Filter "Name = '$ProcessName.exe'" -ErrorAction Stop |
            Where-Object {
                $_.ExecutablePath -and
                [string]::Equals(
                    [IO.Path]::GetFullPath($_.ExecutablePath),
                    $normalizedBinaryPath,
                    [StringComparison]::OrdinalIgnoreCase
                )
            } |
            Select-Object -ExpandProperty ProcessId)
    } catch {
        $matchingProcessIds = @()
    }

    if ($matchingProcessIds.Count -eq 0) {
        $matchingProcessIds = @(Get-Process -Name $ProcessName -ErrorAction SilentlyContinue | Where-Object {
            try {
                [string]::Equals(
                    [IO.Path]::GetFullPath($_.Path),
                    $normalizedBinaryPath,
                    [StringComparison]::OrdinalIgnoreCase
                )
            } catch {
                $false
            }
        } | Select-Object -ExpandProperty Id)
    }

    return @($matchingProcessIds | Sort-Object -Unique | ForEach-Object {
        [pscustomobject]@{ Id = [int] $_ }
    })
}

function Get-AgentDockProcesses {
    param([string] $BinaryPath)
    $processName = [IO.Path]::GetFileNameWithoutExtension($BinaryPath)
    if ([string]::IsNullOrWhiteSpace($processName)) {
        throw "Unable to resolve AgentDock process name from path: $BinaryPath"
    }
    return @(Get-ProcessesByPath -ProcessName $processName -BinaryPath $BinaryPath)
}

function Get-CloudflaredProcesses {
    param([string] $BinaryPath)
    return @(Get-ProcessesByPath -ProcessName 'cloudflared' -BinaryPath $BinaryPath)
}

function Get-AgentDockTrayProcesses {
    param([string] $BinaryPath)
    return @(Get-ProcessesByPath -ProcessName 'agentdock-tray' -BinaryPath $BinaryPath)
}

function Test-AgentDockTaskEligible {
    param(
        [string] $AgentDockValueName,
        [string] $CloudflaredValueName,
        [string] $TrayValueName
    )

    return $AgentDockValueName -eq 'AgentDock' -and
        $CloudflaredValueName -eq 'AgentDockCloudflared' -and
        $TrayValueName -eq 'AgentDockTray'
}

function Get-AgentDockTaskState {
    param(
        [string] $AgentDockValueName,
        [string] $CloudflaredValueName,
        [string] $TrayValueName,
        [string] $RuntimeRoot,
        [string] $StableCorePath,
        [string] $StableTrayPath,
        [string] $LegacyLauncherPath
    )

    $state = [pscustomobject]@{
        Eligible = $false
        Exists = $false
        Conflicting = $false
        WasEnabled = $false
        WasRunning = $false
        SchedulerAvailable = $true
        SchedulerError = ''
    }
    $state.Eligible = Test-AgentDockTaskEligible `
        -AgentDockValueName $AgentDockValueName `
        -CloudflaredValueName $CloudflaredValueName `
        -TrayValueName $TrayValueName
    if (-not $state.Eligible) {
        return $state
    }

    try {
        $task = Get-ScheduledTask -TaskName 'AgentDock' -TaskPath '\' -ErrorAction SilentlyContinue
    } catch {
        # fresh standard installs do not require Task Scheduler. Record capability failure instead of
        # letting ScheduledTasks/WMI/COM terminate the installer before it can write diagnostics.
        $state.SchedulerAvailable = $false
        $state.SchedulerError = $_.Exception.Message
        return $state
    }
    if ($null -eq $task) {
        return $state
    }
    $owned = $false
    $normalizedRoot = [IO.Path]::GetFullPath($RuntimeRoot).TrimEnd('\')
    $normalizedCore = [IO.Path]::GetFullPath($StableCorePath)
    $normalizedTray = [IO.Path]::GetFullPath($StableTrayPath)
    $normalizedLegacyLauncher = [IO.Path]::GetFullPath($LegacyLauncherPath)
    foreach ($action in @($task.Actions)) {
        $executePath = [string] $action.Execute
        $arguments = [string] $action.Arguments
        try {
            if (-not [string]::IsNullOrWhiteSpace($executePath)) {
                $normalizedExecute = [IO.Path]::GetFullPath($executePath)
                if ([string]::Equals($normalizedExecute, $normalizedCore, [StringComparison]::OrdinalIgnoreCase) -or
                    [string]::Equals($normalizedExecute, $normalizedTray, [StringComparison]::OrdinalIgnoreCase)) {
                    $owned = $true
                    break
                }
            }
        } catch {
        }
        if (-not [string]::IsNullOrWhiteSpace($arguments) -and
            ($arguments.IndexOf($normalizedRoot, [StringComparison]::OrdinalIgnoreCase) -ge 0 -or
             $arguments.IndexOf($normalizedLegacyLauncher, [StringComparison]::OrdinalIgnoreCase) -ge 0)) {
            $owned = $true
            break
        }
    }
    if (-not $owned) {
        $state.Conflicting = $true
        return $state
    }
    $state.Exists = $true
    $state.WasEnabled = $task.State -ne 'Disabled'
    $state.WasRunning = $task.State -eq 'Running'
    return $state
}

function Get-CurrentTaskUser {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    return [pscustomobject]@{
        Sid = $identity.User.Value
        Name = $identity.Name
    }
}

function Get-InteractiveDesktopUser {
    try {
        $userName = (Get-CimInstance Win32_ComputerSystem -ErrorAction Stop).UserName
        if ([string]::IsNullOrWhiteSpace($userName)) {
            return $null
        }
        $sid = (New-Object Security.Principal.NTAccount($userName)).Translate(
            [Security.Principal.SecurityIdentifier]
        ).Value
        return [pscustomobject]@{
            Sid = $sid
            Name = $userName
        }
    } catch {
        return $null
    }
}

function Start-ElevatedAgentDockTaskAction {
    param(
        [ValidateSet('prepare-elevated', 'prepare-standard', 'restore', 'remove')]
        [string] $Action,
        [string] $BackupDirectory,
        [string] $AdminLauncherPath,
        [string] $LauncherPath,
        [string] $RuntimeRoot,
        [pscustomobject] $TaskUser
    )

    $arguments = "--task-admin $Action"
    if (-not [string]::IsNullOrWhiteSpace($BackupDirectory)) {
        $arguments += " --backup-directory `"$BackupDirectory`""
    }
    if (-not [string]::IsNullOrWhiteSpace($LauncherPath)) {
        $arguments += " --launcher-path `"$LauncherPath`""
    }
    if (-not [string]::IsNullOrWhiteSpace($RuntimeRoot)) {
        $arguments += " --runtime-root `"$RuntimeRoot`""
    }
    if ($null -ne $TaskUser) {
        $arguments += " --user-sid `"$($TaskUser.Sid)`" --user-name `"$($TaskUser.Name)`""
    }

    try {
        $process = Start-Process `
            -FilePath $AdminLauncherPath `
            -ArgumentList $arguments `
            -Verb RunAs `
            -WindowStyle Hidden `
            -Wait `
            -PassThru
    } catch {
        # Windows or enterprise policy can reject RunAs before the UAC helper starts.
        # The main install flow decides whether that pre-start failure is safe to downgrade.
        return [pscustomobject]@{
            Started = $false
            ErrorMessage = $_.Exception.Message
        }
    }
    $succeeded = $process.ExitCode -eq 0
    return [pscustomobject]@{
        Started = $true
        Succeeded = $succeeded
        ExitCode = $process.ExitCode
        ErrorMessage = $(if ($succeeded) { '' } else { "AgentDock administrator task action failed with exit code $($process.ExitCode)." })
    }
}

function Enable-AgentDockTask {
    Enable-ScheduledTask -TaskName 'AgentDock' -TaskPath '\' -ErrorAction Stop | Out-Null
}

function Start-AgentDockTask {
    param(
        [Parameter(Mandatory = $true)][string] $AgentDockBinary,
        [string] $ExpectedUserSid = ''
    )

    if (-not (Test-Path -LiteralPath $AgentDockBinary -PathType Leaf)) {
        throw "AgentDock native task launcher was not found: $AgentDockBinary"
    }
    $arguments = @('service', 'task-start', '--task-name', 'AgentDock')
    if (-not [string]::IsNullOrWhiteSpace($ExpectedUserSid)) {
        $arguments += @('--expected-user-sid', $ExpectedUserSid)
    }
    & $AgentDockBinary @arguments | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "AgentDock native task-start failed with exit code $LASTEXITCODE."
    }
}

function Stop-ProcessesForUpgrade {
    param(
        [string] $ProcessName,
        [string] $BinaryPath
    )

    $processWasRunning = $false
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    do {
        $runningProcesses = @(Get-ProcessesByPath -ProcessName $ProcessName -BinaryPath $BinaryPath)
        if ($runningProcesses.Count -gt 0) {
            $processWasRunning = $true
        }
        foreach ($process in $runningProcesses) {
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        }
        if ($runningProcesses.Count -eq 0) {
            break
        }
        Start-Sleep -Milliseconds 250
    } while ([DateTime]::UtcNow -lt $deadline)

    $remainingProcesses = @(Get-ProcessesByPath -ProcessName $ProcessName -BinaryPath $BinaryPath)
    if ($remainingProcesses.Count -gt 0) {
        throw "Unable to stop $ProcessName at $BinaryPath."
    }
    return $processWasRunning
}

function Stop-AgentDockForUpgrade {
    param([string] $BinaryPath)
    $processName = [IO.Path]::GetFileNameWithoutExtension($BinaryPath)
    if ([string]::IsNullOrWhiteSpace($processName)) {
        throw "Unable to resolve AgentDock process name from path: $BinaryPath"
    }
    return Stop-ProcessesForUpgrade -ProcessName $processName -BinaryPath $BinaryPath
}

function Stop-CloudflaredForUpgrade {
    param([string] $BinaryPath)
    return Stop-ProcessesForUpgrade -ProcessName 'cloudflared' -BinaryPath $BinaryPath
}

function Stop-AgentDockTrayForUpgrade {
    param([string] $BinaryPath)
    return Stop-ProcessesForUpgrade -ProcessName 'agentdock-tray' -BinaryPath $BinaryPath
}

function Start-HiddenPowerShellScript {
    param([string] $ScriptPath)

    if (-not (Test-Path -LiteralPath $ScriptPath -PathType Leaf)) {
        throw "Launcher was not found: $ScriptPath"
    }
    $arguments = "-NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File `"$ScriptPath`""
    if ($InstallChannel -eq 'setup') {
        Invoke-SetupRuntimeProcess -FilePath (Join-Path $PSHOME 'powershell.exe') -Arguments $arguments
        return
    }
    Start-Process -FilePath 'powershell.exe' -ArgumentList $arguments -WindowStyle Hidden | Out-Null
}

function Start-AgentDockLauncher {
    param([string] $LauncherPath)
    Start-HiddenPowerShellScript -ScriptPath $LauncherPath
}

function Start-CloudflaredLauncher {
    param([string] $LauncherPath)
    Start-HiddenPowerShellScript -ScriptPath $LauncherPath
}

function Start-AgentDockTray {
    param([string] $BinaryPath)

    if (-not (Test-Path -LiteralPath $BinaryPath -PathType Leaf)) {
        throw "AgentDock tray was not found: $BinaryPath"
    }
    if ($InstallChannel -eq 'setup') {
        Invoke-SetupRuntimeProcess -FilePath $BinaryPath -Arguments '--background'
        return
    }
    Start-Process -FilePath $BinaryPath -ArgumentList '--background' -WindowStyle Hidden | Out-Null
}

function Test-CloudflaredBinary {
    param([string] $BinaryPath)

    if (-not (Test-Path -LiteralPath $BinaryPath -PathType Leaf)) {
        return $false
    }
    try {
        & $BinaryPath --version | Out-Null
        return $LASTEXITCODE -eq 0
    } catch {
        return $false
    }
}

function Install-CloudflaredBinary {
    param(
        [string] $DestinationBinary,
        [string] $Architecture,
        [string] $TempDirectory,
        [string] $SourceBinary = ''
    )

    $sourceOverride = $SourceBinary
    if ([string]::IsNullOrWhiteSpace($sourceOverride)) {
        $sourceOverride = [Environment]::GetEnvironmentVariable('AGENTDOCK_CLOUDFLARED_BINARY')
    }
    if (-not [string]::IsNullOrWhiteSpace($sourceOverride)) {
        if (-not (Test-CloudflaredBinary -BinaryPath $sourceOverride)) {
            throw "The supplied cloudflared binary is invalid: $sourceOverride"
        }
        $stagedPath = "$DestinationBinary.tmp.$PID"
        Copy-Item -LiteralPath $sourceOverride -Destination $stagedPath -Force
        Move-Item -LiteralPath $stagedPath -Destination $DestinationBinary -Force
        return
    }

    if (Test-CloudflaredBinary -BinaryPath $DestinationBinary) {
        return
    }

    $assetName = "cloudflared-windows-$Architecture.exe"
    $downloadPath = Join-Path $TempDirectory $assetName
    $downloadUrl = "$(Get-CloudflaredReleaseBaseUrl)/$assetName"
    Write-Host "Downloading cloudflared: $downloadUrl"
    Invoke-WebRequest -UseBasicParsing -Uri $downloadUrl -OutFile $downloadPath
    if (-not (Test-CloudflaredBinary -BinaryPath $downloadPath)) {
        throw "Downloaded cloudflared executable is invalid: $downloadUrl"
    }
    $stagedPath = "$DestinationBinary.tmp.$PID"
    Copy-Item -LiteralPath $downloadPath -Destination $stagedPath -Force
    Move-Item -LiteralPath $stagedPath -Destination $DestinationBinary -Force
}

function Wait-AgentDockHealth {
    param([int] $HealthPort)

    $healthUrl = "http://127.0.0.1:$HealthPort/healthz"
    $deadline = [DateTime]::UtcNow.AddSeconds(120)
    do {
        Start-Sleep -Milliseconds 500
        try {
            $response = Invoke-WebRequest -UseBasicParsing -Uri $healthUrl -TimeoutSec 2
            if ($response.StatusCode -eq 200) {
                return
            }
        } catch {
        }
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "AgentDock was installed, but health check failed at $healthUrl"
}

function Backup-FileState {
    param(
        [string] $Path,
        [string] $Name,
        [string] $BackupDirectory
    )

    if (Test-Path -LiteralPath $Path) {
        if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
            throw "Managed runtime path must be a regular file: $Path"
        }
        Copy-Item -LiteralPath $Path -Destination (Join-Path $BackupDirectory $Name) -Force
        New-Item -ItemType File -Path (Join-Path $BackupDirectory "$Name.present") -Force | Out-Null
    }
}

function Restore-FileState {
    param(
        [string] $Path,
        [string] $Name,
        [string] $BackupDirectory
    )

    $marker = Join-Path $BackupDirectory "$Name.present"
    $backup = Join-Path $BackupDirectory $Name
    if (Test-Path -LiteralPath $marker -PathType Leaf) {
        New-Item -ItemType Directory -Path (Split-Path -Parent $Path) -Force | Out-Null
        Copy-Item -LiteralPath $backup -Destination $Path -Force
        return
    }
    Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
}

function Get-RunValue {
    param(
        [string] $RegistryPath,
        [string] $Name
    )

    if (-not (Test-Path -LiteralPath $RegistryPath)) {
        return $null
    }
    try {
        return Get-ItemPropertyValue -LiteralPath $RegistryPath -Name $Name -ErrorAction Stop
    } catch {
        return $null
    }
}

function Set-RunValue {
    param(
        [string] $RegistryPath,
        [string] $Name,
        [string] $Value
    )

    try {
        # The standard Run key usually exists; recreating it with -Force can fail in restricted environments.
        if (-not (Test-Path -LiteralPath $RegistryPath -ErrorAction Stop)) {
            New-Item -Path $RegistryPath -ErrorAction Stop | Out-Null
        }
    } catch {
        throw "Unable to prepare current-user startup registry key '$RegistryPath' for value '$Name': $($_.Exception.Message)"
    }

    try {
        New-ItemProperty `
            -Path $RegistryPath `
            -Name $Name `
            -Value $Value `
            -PropertyType String `
            -Force `
            -ErrorAction Stop | Out-Null
    } catch {
        throw "Unable to write current-user startup registry value '$Name' at '$RegistryPath': $($_.Exception.Message)"
    }
}

function Test-AgentDockReleasePayloadPath {
    param([string] $Name)

    if (@(
        'agentdock.exe',
        'agentdock-tray.exe',
        'agentdock.ico',
        'agentdock-arbiter.exe',
        'agentdock-shim.exe',
        'agentdock-tray-shim.exe'
    ) -contains $Name) {
        return $true
    }
    return $Name.StartsWith('share/agentdock/core-skills/', [StringComparison]::Ordinal) -or
        $Name.StartsWith('wsl-helper/', [StringComparison]::Ordinal)
}

function Expand-AgentDockReleaseArchive {
    param(
        [Parameter(Mandatory = $true)][string] $ArchivePath,
        [Parameter(Mandatory = $true)][string] $DestinationPath
    )

    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem

    $maxEntries = 16384
    $maxEntryBytes = [Int64] (256MB)
    $maxTotalBytes = [Int64] (1GB)
    $archive = $null
    try {
        $archive = [IO.Compression.ZipFile]::OpenRead($ArchivePath)
        if ($archive.Entries.Count -lt 1 -or $archive.Entries.Count -gt $maxEntries) {
            throw "Release archive entry count is invalid: $($archive.Entries.Count)"
        }

        # Validate the complete central directory before creating any output.
        # The comparer mirrors Windows path semantics so case-folded duplicates
        # cannot overwrite each other during extraction.
        $kinds = [System.Collections.Generic.Dictionary[string, bool]]::new([StringComparer]::OrdinalIgnoreCase)
        $records = [System.Collections.Generic.List[object]]::new()
        $totalBytes = [Int64] 0
        $selectedBytes = [Int64] 0
        foreach ($entry in $archive.Entries) {
            $rawName = [string] $entry.FullName
            # Windows PowerShell 5.1 Compress-Archive emits backslash entries.
            # Normalize once before all validation, selection and duplicate checks;
            # UNC/rooted paths, traversal and mixed-separator aliases still fail.
            $normalizedName = $rawName.Replace('\', '/')
            if ([string]::IsNullOrEmpty($rawName) -or
                $rawName.IndexOf([char] 0) -ge 0 -or
                $normalizedName.StartsWith('/', [StringComparison]::Ordinal) -or
                $rawName.Contains(':') -or
                $rawName.Length -gt 4096) {
                throw "Release archive contains an unsafe path: $rawName"
            }

            $isDirectory = $normalizedName.EndsWith('/', [StringComparison]::Ordinal)
            $trimmedName = if ($isDirectory) { $normalizedName.Substring(0, $normalizedName.Length - 1) } else { $normalizedName }
            if ([string]::IsNullOrWhiteSpace($trimmedName)) {
                throw "Release archive contains an empty path: $rawName"
            }

            $segments = $trimmedName.Split('/')
            foreach ($segment in $segments) {
                if ([string]::IsNullOrEmpty($segment) -or
                    $segment -eq '.' -or
                    $segment -eq '..' -or
                    $segment.EndsWith('.', [StringComparison]::Ordinal) -or
                    $segment.EndsWith(' ', [StringComparison]::Ordinal) -or
                    $segment.IndexOfAny([char[]] '<>:"|?*') -ge 0 -or
                    $segment.Length -gt 255) {
                    throw "Release archive contains a non-canonical path: $rawName"
                }
                foreach ($character in $segment.ToCharArray()) {
                    if ([char]::IsControl($character)) {
                        throw "Release archive contains a control character in a path."
                    }
                }
                $deviceBase = $segment.Split([char] '.')[0].ToUpperInvariant()
                if ($deviceBase -match '^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])$') {
                    throw "Release archive contains a reserved Windows path: $rawName"
                }
            }

            $canonicalName = [string]::Join('/', $segments)
            if (-not [string]::Equals($canonicalName, $trimmedName, [StringComparison]::Ordinal)) {
                throw "Release archive contains a non-canonical path: $rawName"
            }
            if ($kinds.ContainsKey($canonicalName)) {
                throw "Release archive contains a duplicate path: $canonicalName"
            }

            $externalAttributes = [UInt32] (([Int64] $entry.ExternalAttributes) -band [Int64][UInt32]::MaxValue)
            $unixType = (($externalAttributes -shr 16) -band 0xF000)
            $dosAttributes = ($externalAttributes -band 0xFFFF)
            if (($dosAttributes -band [UInt32][IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Release archive contains a reparse-point entry: $canonicalName"
            }
            if ($isDirectory) {
                if ($entry.Length -ne 0 -or ($unixType -ne 0 -and $unixType -ne 0x4000)) {
                    throw "Release archive directory type is invalid: $canonicalName"
                }
            } elseif ($unixType -ne 0 -and $unixType -ne 0x8000) {
                throw "Release archive contains a non-regular file: $canonicalName"
            }

            $length = [Int64] $entry.Length
            if ($length -lt 0 -or $length -gt $maxEntryBytes -or $length -gt ($maxTotalBytes - $totalBytes)) {
                throw "Release archive expanded content exceeds limits: $canonicalName"
            }
            $totalBytes += $length
            $selected = (-not $isDirectory) -and (Test-AgentDockReleasePayloadPath -Name $canonicalName)
            if ($selected) { $selectedBytes += $length }
            $kinds.Add($canonicalName, $isDirectory)
            $records.Add([pscustomobject]@{
                Entry = $entry
                Name = $canonicalName
                IsDirectory = $isDirectory
                Selected = $selected
                Length = $length
            })
        }

        # Reject an explicit file that is also the parent of another entry.
        foreach ($record in $records) {
            $parts = $record.Name.Split('/')
            for ($index = 1; $index -lt $parts.Length; $index++) {
                $parent = [string]::Join('/', $parts[0..($index - 1)])
                if ($kinds.ContainsKey($parent) -and -not $kinds[$parent]) {
                    throw "Release archive contains a file/directory conflict: $parent"
                }
            }
        }

        $destinationRoot = [IO.Path]::GetFullPath($DestinationPath)
        $destinationPrefix = $destinationRoot.TrimEnd([char[]] @('\', '/')) + [IO.Path]::DirectorySeparatorChar
        New-Item -ItemType Directory -Path $destinationRoot -Force | Out-Null
        $extractedEntries = 0
        $extractedBytes = [Int64] 0
        foreach ($record in $records) {
            if (-not $record.Selected) { continue }

            $relativePath = $record.Name.Replace('/', [IO.Path]::DirectorySeparatorChar)
            $targetPath = [IO.Path]::GetFullPath((Join-Path $destinationRoot $relativePath))
            if (-not $targetPath.StartsWith($destinationPrefix, [StringComparison]::OrdinalIgnoreCase)) {
                throw "Release archive path escapes extraction root: $($record.Name)"
            }
            $targetDirectory = Split-Path -Parent $targetPath
            New-Item -ItemType Directory -Path $targetDirectory -Force | Out-Null

            $input = $null
            $output = $null
            $completed = $false
            try {
                $input = $record.Entry.Open()
                $output = New-Object IO.FileStream(
                    $targetPath,
                    [IO.FileMode]::CreateNew,
                    [IO.FileAccess]::Write,
                    [IO.FileShare]::None,
                    131072,
                    [IO.FileOptions]::SequentialScan
                )
                $buffer = New-Object byte[] 131072
                $written = [Int64] 0
                while (($read = $input.Read($buffer, 0, $buffer.Length)) -gt 0) {
                    $written += $read
                    if ($written -gt $record.Length -or $written -gt $maxEntryBytes) {
                        throw "Release archive entry expanded beyond its declared size: $($record.Name)"
                    }
                    $output.Write($buffer, 0, $read)
                }
                $output.Flush($true)
                if ($written -ne $record.Length) {
                    throw "Release archive entry size mismatch: $($record.Name)"
                }
                $completed = $true
                $extractedEntries++
                $extractedBytes += $written
            } finally {
                if ($null -ne $output) { $output.Dispose() }
                if ($null -ne $input) { $input.Dispose() }
                if (-not $completed) {
                    Remove-Item -LiteralPath $targetPath -Force -ErrorAction SilentlyContinue
                }
            }
        }

        return [pscustomobject]@{
            entry_count = $records.Count
            selected_entry_count = $extractedEntries
            declared_bytes = $totalBytes
            extracted_bytes = $extractedBytes
            skipped_bytes = ($totalBytes - $selectedBytes)
        }
    } finally {
        if ($null -ne $archive) { $archive.Dispose() }
    }
}

function Invoke-SetupMeasuredStage {
    param(
        [Parameter(Mandatory = $true)] $Timing,
        [Parameter(Mandatory = $true)][string] $Stage,
        [Parameter(Mandatory = $true)][scriptblock] $Action
    )

    $startedAt = [DateTime]::UtcNow
    $stopwatch = [Diagnostics.Stopwatch]::StartNew()
    try {
        & $Action
    } finally {
        $stopwatch.Stop()
        $completedAt = [DateTime]::UtcNow
        [void] $Timing.Stages.Add([pscustomobject]@{
            stage = $Stage
            started_at = $startedAt.ToString('o')
            completed_at = $completedAt.ToString('o')
            duration_ms = [Int64] $stopwatch.ElapsedMilliseconds
        })
    }
}

function Write-SetupTimingSummary {
    param(
        [Parameter(Mandatory = $true)] $Timing,
        [Parameter(Mandatory = $true)][string] $RuntimeRoot,
        $InstallerTiming = $null
    )

    if ([string]::IsNullOrWhiteSpace($RuntimeRoot)) { return }
    $completedAt = [DateTime]::UtcNow
    $summary = [ordered]@{
        schema_version = 1
        started_at = $Timing.StartedAt.ToString('o')
        completed_at = $completedAt.ToString('o')
        wall_duration_ms = [Int64] $Timing.Stopwatch.ElapsedMilliseconds
        reused_cached_payload = [bool] $Timing.ReusedCachedPayload
        stages = @($Timing.Stages)
        installer_engine = $InstallerTiming
    }
    $directory = Join-Path $RuntimeRoot 'install'
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
    $path = Join-Path $directory 'setup-timing.json'
    $temporaryPath = "$path.tmp.$PID"
    $encoding = New-Object Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($temporaryPath, ($summary | ConvertTo-Json -Depth 12), $encoding)
    Move-Item -LiteralPath $temporaryPath -Destination $path -Force
}

if ($LibraryOnly) {
    return
}

$setupTiming = [pscustomobject]@{
    StartedAt = [DateTime]::UtcNow
    Stopwatch = [Diagnostics.Stopwatch]::StartNew()
    ReusedCachedPayload = $false
    Stages = [System.Collections.Generic.List[object]]::new()
}
$engineTiming = $null
$effectivePrivilegeMode = $CorePrivilegeMode
$installWarningCode = ''
$installWarningMessage = ''
$installErrorCode = ''
$resolvedTunnelMode = 'none'
$existingInstallDetected = $false
$taskUser = $null
$taskState = [pscustomobject]@{
    Eligible = $false
    Exists = $false
    WasEnabled = $false
    WasRunning = $false
    SchedulerAvailable = $false
    SchedulerError = ''
}

# Initialize the result file before parameter, architecture, user, or ScheduledTasks probing.
# Inno Setup can therefore always show a concrete PowerShell failure instead of a bare exit code.
Write-InstallResult `
    -Path $ResultFile `
    -Success $false `
    -Message 'AgentDock installation is initializing.' `
    -InstalledVersion $Version `
    -LocalMCPUrl "http://127.0.0.1:$Port/mcp" `
    -PublicMCPUrl '' `
    -BearerToken '' `
    -OAuthLoginPassword '' `
    -HealthStatus 'initializing' `
    -PrivilegeMode $effectivePrivilegeMode

try {
    $Utf8NoBom = New-Object System.Text.UTF8Encoding($false)
    Add-Type -AssemblyName System.Security
    $setupRuntimeLauncherPath = Join-Path $PSScriptRoot 'launch-windows-process.ps1'

    if ($Port -lt 1 -or $Port -gt 65535) {
        throw 'Port must be between 1 and 65535.'
    }
    if ([string]::IsNullOrWhiteSpace($InstallDir)) {
        throw 'InstallDir is required.'
    }
    $userHome = [Environment]::GetFolderPath('UserProfile')
    if ([string]::IsNullOrWhiteSpace($userHome)) {
        throw 'Unable to resolve the current user profile directory.'
    }
    $architecture = Get-AgentDockArchitecture
} catch {
    Write-InstallResult `
        -Path $ResultFile `
        -Success $false `
        -Message $_.Exception.Message `
        -InstalledVersion $Version `
        -LocalMCPUrl "http://127.0.0.1:$Port/mcp" `
        -PublicMCPUrl '' `
        -BearerToken '' `
        -OAuthLoginPassword '' `
        -HealthStatus 'failed' `
        -PrivilegeMode $effectivePrivilegeMode `
        -ErrorCode 'install-validation-failed' `
        -ErrorRecord $_
    throw
}

$assetName = "agentdock_windows_$architecture.zip"
$releaseBaseUrl = ''
$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ("agentdock-install-" + [Guid]::NewGuid().ToString('N'))
$archivePath = Join-Path $tempRoot $assetName
$checksumPath = "$archivePath.sha256"
$destinationBinary = Join-Path $InstallDir 'agentdock.exe'
$destinationTrayBinary = Join-Path $InstallDir 'agentdock-tray.exe'
$destinationTrayIcon = Join-Path $InstallDir 'agentdock.ico'
$cloudflaredBinary = Join-Path $InstallDir 'cloudflared.exe'
$binaryBackup = Join-Path $tempRoot 'agentdock.exe.previous'
$trayBackup = Join-Path $tempRoot 'agentdock-tray.exe.previous'
$trayIconBackup = Join-Path $tempRoot 'agentdock.ico.previous'
$cloudflaredBackup = Join-Path $tempRoot 'cloudflared.exe.previous'
$runtimeDir = Split-Path -Parent $InstallDir
$versionsDir = Join-Path $runtimeDir 'versions'
$activeVersionPath = Join-Path $runtimeDir 'active-version.json'
$legacyManagerPath = Join-Path $runtimeDir 'installer\manage-windows.ps1'
$launcherPath = Join-Path $runtimeDir 'start-agentdock.ps1'
$cloudflaredLauncherPath = Join-Path $runtimeDir 'start-cloudflared.ps1'
$tokenPath = Join-Path $runtimeDir 'auth-token.dpapi'
$oauthPasswordPath = Join-Path $runtimeDir 'oauth-password.dpapi'
$oauthTokenSecretPath = Join-Path $runtimeDir 'oauth-token-secret.dpapi'
$credentialOwnerSidPath = Join-Path $runtimeDir 'credential-owner-sid.txt'
$serverUrlPath = Join-Path $runtimeDir 'server-url.txt'
$namedServerUrlPath = Join-Path $runtimeDir 'named-server-url.txt'
$controlPanelSettingsPath = Join-Path $runtimeDir 'control-panel-settings.json'
$tunnelModePath = Join-Path $runtimeDir 'cloudflared-mode.txt'
$tunnelTokenPath = Join-Path $runtimeDir 'cloudflared-token.dpapi'
$runtimeManifestPath = Join-Path $runtimeDir 'runtime.json'
$runtimeAgentDockHome = [Environment]::GetEnvironmentVariable('AGENTDOCK_HOME', 'Process')
$runtimeAgentDockDefaultDir = [Environment]::GetEnvironmentVariable('AGENTDOCK_DEFAULT_DIR', 'Process')
if (([string]::IsNullOrWhiteSpace($runtimeAgentDockHome) -or [string]::IsNullOrWhiteSpace($runtimeAgentDockDefaultDir)) -and
    (Test-Path -LiteralPath $runtimeManifestPath -PathType Leaf)) {
    try {
        $existingRuntimeManifest = Get-Content -LiteralPath $runtimeManifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
        if ([string]::IsNullOrWhiteSpace($runtimeAgentDockHome) -and
            $null -ne $existingRuntimeManifest.PSObject.Properties['agentdock_home']) {
            $runtimeAgentDockHome = [string] $existingRuntimeManifest.agentdock_home
        }
        if ([string]::IsNullOrWhiteSpace($runtimeAgentDockDefaultDir) -and
            $null -ne $existingRuntimeManifest.PSObject.Properties['agentdock_default_dir']) {
            $runtimeAgentDockDefaultDir = [string] $existingRuntimeManifest.agentdock_default_dir
        }
    } catch {
        # Runtime manifest validation later reports malformed state. Do not make path recovery
        # depend on a best-effort compatibility read here.
    }
}
if ([string]::IsNullOrWhiteSpace($runtimeAgentDockHome)) {
    $runtimeAgentDockHome = Join-Path $userHome '.agentdock'
}
if ([string]::IsNullOrWhiteSpace($runtimeAgentDockDefaultDir)) {
    $runtimeAgentDockDefaultDir = Join-Path $userHome 'AgentDock'
}
if (-not [IO.Path]::IsPathRooted($runtimeAgentDockHome) -or -not [IO.Path]::IsPathRooted($runtimeAgentDockDefaultDir)) {
    throw 'AGENTDOCK_HOME and AGENTDOCK_DEFAULT_DIR must resolve to absolute paths.'
}
$runtimeAgentDockHome = [IO.Path]::GetFullPath($runtimeAgentDockHome)
$runtimeAgentDockDefaultDir = [IO.Path]::GetFullPath($runtimeAgentDockDefaultDir)
$desktopVersionPath = Join-Path $runtimeDir 'desktop-version.txt'
$quickTunnelUrlPath = Join-Path $runtimeDir 'quick-tunnel-url.txt'
$cloudflaredStdoutLogPath = Join-Path $runtimeDir 'cloudflared.out.log'
$cloudflaredStderrLogPath = Join-Path $runtimeDir 'cloudflared.err.log'
$runtimeBackupDir = Join-Path $tempRoot 'runtime-backup'
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$runValueName = $StartupValueName
$cloudflaredRunValueName = $CloudflaredStartupValueName
$trayRunValueName = $TrayStartupValueName
$processWasRunning = $false
$trayProcessWasRunning = $false
$cloudflaredProcessWasRunning = $false
$agentDockStopAttempted = $false
$trayStopAttempted = $false
$cloudflaredStopAttempted = $false
$rollbackStateCaptured = $false
$engineCommitted = $false
$enginePrepared = $false
$engineTransactionId = ''
$engineInvoked = $false
$preserveRecoveryFiles = $false
$stableFilesMayBeReplaced = $false
$cloudflaredReplacementStarted = $false
$startupRegistrationChanged = $false
$trayStartupRegistrationChanged = $false
$tunnelStartupRegistrationChanged = $false
$previousRunValue = $null
$previousTrayRunValue = $null
$previousTunnelRunValue = $null
$taskBackupDirectory = Join-Path $tempRoot 'scheduled-task-backup'
$taskTransactionStarted = $false
$taskTransactionCommitted = $false
$taskRestored = $false
$generationLayoutDetected = $false
$generationBootstrapDirectory = ''
$legacyBootstrapPrepared = $false
$legacyBootstrapVersion = ''
$preserveTailscale = $false
$preservedTailscaleOrigin = ''

$managedRuntimeFiles = @(
    # Migration cleanup only: current payload no longer publishes or executes this compatibility script.
    @{ Path = $legacyManagerPath; Name = 'legacy-manage-windows.ps1' },
    @{ Path = $launcherPath; Name = 'start-agentdock.ps1' },
    @{ Path = $cloudflaredLauncherPath; Name = 'start-cloudflared.ps1' },
    @{ Path = $tokenPath; Name = 'auth-token.dpapi' },
    @{ Path = $oauthPasswordPath; Name = 'oauth-password.dpapi' },
    @{ Path = $oauthTokenSecretPath; Name = 'oauth-token-secret.dpapi' },
    @{ Path = $credentialOwnerSidPath; Name = 'credential-owner-sid.txt' },
    @{ Path = $serverUrlPath; Name = 'server-url.txt' },
    @{ Path = $namedServerUrlPath; Name = 'named-server-url.txt' },
    @{ Path = $controlPanelSettingsPath; Name = 'control-panel-settings.json' },
    @{ Path = $tunnelModePath; Name = 'cloudflared-mode.txt' },
    @{ Path = $tunnelTokenPath; Name = 'cloudflared-token.dpapi' },
    @{ Path = $runtimeManifestPath; Name = 'runtime.json' },
    @{ Path = (Join-Path $runtimeDir 'tailscale-funnel-state.json'); Name = 'tailscale-funnel-state.json' },
    @{ Path = $desktopVersionPath; Name = 'desktop-version.txt' },
    @{ Path = $quickTunnelUrlPath; Name = 'quick-tunnel-url.txt' },
    @{ Path = $activeVersionPath; Name = 'active-version.json' },
    @{ Path = (Join-Path $runtimeDir 'update\transaction.json'); Name = 'update-transaction.json' },
    @{ Path = (Join-Path $runtimeDir 'update\result.json'); Name = 'update-result.json' }
)

try {
    $existingInstallDetected =
        (Test-Path -LiteralPath $destinationBinary -PathType Leaf) -or
        (Test-Path -LiteralPath $runtimeManifestPath -PathType Leaf) -or
        (Test-Path -LiteralPath $launcherPath -PathType Leaf)
    $taskUser = Get-CurrentTaskUser
    $taskState = Get-AgentDockTaskState `
        -AgentDockValueName $runValueName `
        -CloudflaredValueName $cloudflaredRunValueName `
        -TrayValueName $trayRunValueName `
        -RuntimeRoot $runtimeDir `
        -StableCorePath $destinationBinary `
        -StableTrayPath $destinationTrayBinary `
        -LegacyLauncherPath $launcherPath
    if ($effectivePrivilegeMode -eq 'elevated' -and -not $taskState.Eligible) {
        throw 'Elevated AgentDock mode requires the default Windows startup names.'
    }
    if ($effectivePrivilegeMode -eq 'elevated' -and $taskState.Conflicting) {
        throw 'The AgentDock scheduled task belongs to another installation root. Remove or repair that installation before enabling elevated mode here.'
    }

    $existingPrivilegeMode = ''
    if (Test-Path -LiteralPath $runtimeManifestPath -PathType Leaf) {
        try {
            $existingManifest = Get-Content -LiteralPath $runtimeManifestPath -Raw | ConvertFrom-Json
            $existingPrivilegeMode = [string] $existingManifest.privilege_mode
        } catch {
            $existingPrivilegeMode = ''
        }
    }
    if (-not $taskState.SchedulerAvailable -and
        ($effectivePrivilegeMode -eq 'elevated' -or $existingPrivilegeMode -eq 'elevated')) {
        $installErrorCode = 'task-scheduler-unavailable'
        throw "Windows Task Scheduler is required to preserve administrator-enhanced AgentDock mode: $($taskState.SchedulerError)"
    }

    # Setup never changes Tailscale account or routing state. Preserve its
    # downgrade-safe legacy projection and configure mode/port in the panel.
    if (Test-Path -LiteralPath $runtimeManifestPath -PathType Leaf) {
        $accessManifest = Get-Content -LiteralPath $runtimeManifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
        $providerProperty = $accessManifest.PSObject.Properties['public_access_provider']
        if ($null -ne $providerProperty -and [string]$providerProperty.Value -eq 'tailscale') {
            if (@('auto', 'none') -notcontains $TunnelMode -or
                -not [string]::IsNullOrWhiteSpace($ServerUrl) -or
                -not [string]::IsNullOrWhiteSpace($TunnelToken) -or
                -not [string]::IsNullOrWhiteSpace($TunnelTokenFile)) {
                throw 'Tailscale Funnel is preserved during installation. Change public access in the control panel first.'
            }
            $preserveTailscale = $true
            $preservedTailscaleOrigin = Normalize-ServerUrl -Value ([string]$accessManifest.public_access_url)
            if (-not ([uri]$preservedTailscaleOrigin).Host.EndsWith('.ts.net')) { throw 'Invalid saved Tailscale device origin.' }
            $Port = [int]$accessManifest.port
            Write-Host 'Preserving Tailscale Funnel, its existing port, and its public origin.'
        }
    }
    if ($preserveTailscale) {
        $resolvedTunnelMode = 'none'
    } else {
        $resolvedTunnelMode = Resolve-TunnelMode `
            -RequestedMode $TunnelMode `
            -ModePath $tunnelModePath `
            -StartupRequested ([bool] $RegisterStartup) `
            -PublicAccessRequested ([bool] $ConfigurePublicAccess)
    }
    if ($resolvedTunnelMode -ne 'none' -or (Test-Path -LiteralPath $tunnelModePath -PathType Leaf)) {
        $RegisterStartup = $true
    }

    # Setup must stay in the signed-in desktop user's context so HKCU,
    # current-user DPAPI, and per-user Skill state remain on the right account.
    if ($InstallChannel -eq 'setup') {
        $interactiveUser = Get-InteractiveDesktopUser
        if ($null -ne $interactiveUser -and
            -not [string]::Equals(
                $interactiveUser.Sid,
                $taskUser.Sid,
                [StringComparison]::OrdinalIgnoreCase
            )) {
            $installErrorCode = 'setup-elevated-context'
            throw "AgentDock Setup is running as $($taskUser.Name), but the signed-in desktop user is $($interactiveUser.Name). Start Setup normally under the signed-in account; it requests administrator approval only for scheduled-task operations."
        }
    }

    $credentialOwnerSid = Read-TextFile -Path $credentialOwnerSidPath
    if (-not [string]::IsNullOrWhiteSpace($credentialOwnerSid) -and
        -not [string]::Equals($credentialOwnerSid, $taskUser.Sid, [StringComparison]::OrdinalIgnoreCase)) {
        $installErrorCode = 'credential-user-mismatch'
        throw 'AgentDock credentials belong to a different Windows user. Run Setup from the original user account or perform a clean reinstall.'
    }

    # Setup is non-interactive. Resolve external Tunnel credentials only after confirming the
    # current-user context, but before payload extraction or runtime mutation.
    if ($InstallChannel -eq 'setup' -and $resolvedTunnelMode -eq 'named') {
        $setupTunnelTokenState = Resolve-AvailableTunnelToken `
            -TokenPath $tunnelTokenPath `
            -RequestedToken $TunnelToken `
            -TokenFile $TunnelTokenFile
        if ([string]::IsNullOrWhiteSpace($setupTunnelTokenState.Token)) {
            $installErrorCode = 'tunnel-token-required'
            throw 'The saved Cloudflare Tunnel Token is missing or unreadable. Re-enter it in Setup.'
        }
        $TunnelToken = $setupTunnelTokenState.Token
    }

    New-Item -ItemType Directory -Path $tempRoot -Force | Out-Null
    New-Item -ItemType Directory -Path $runtimeBackupDir -Force | Out-Null
    foreach ($item in $managedRuntimeFiles) {
        Backup-FileState -Path $item.Path -Name $item.Name -BackupDirectory $runtimeBackupDir
    }
    $previousRunValue = Get-RunValue -RegistryPath $runKey -Name $runValueName
    $previousTrayRunValue = Get-RunValue -RegistryPath $runKey -Name $trayRunValueName
    $previousTunnelRunValue = Get-RunValue -RegistryPath $runKey -Name $cloudflaredRunValueName
    $rollbackStateCaptured = $true

    $existingTunnelMode = (Read-TextFile -Path $tunnelModePath).ToLowerInvariant()
    $existingActiveServerUrl = Read-TextFile -Path $serverUrlPath
    if ($existingTunnelMode -eq 'named' -and -not [string]::IsNullOrWhiteSpace($existingActiveServerUrl)) {
        Write-TextFile -Path $namedServerUrlPath -Value $existingActiveServerUrl
    }

    if (-not [string]::IsNullOrWhiteSpace($OfflineArchive)) {
        if (-not (Test-Path -LiteralPath $OfflineArchive -PathType Leaf)) {
            throw "Offline AgentDock archive was not found: $OfflineArchive"
        }
        if ([string]::IsNullOrWhiteSpace($OfflineChecksumFile) -or
            -not (Test-Path -LiteralPath $OfflineChecksumFile -PathType Leaf)) {
            throw "Offline AgentDock checksum file was not found: $OfflineChecksumFile"
        }
        Write-Host "Using bundled AgentDock payload: $OfflineArchive"
        $setupTiming.ReusedCachedPayload = $true
        Invoke-SetupMeasuredStage -Timing $setupTiming -Stage 'installer_staging' -Action {
            Copy-Item -LiteralPath $OfflineArchive -Destination $archivePath -Force
            Copy-Item -LiteralPath $OfflineChecksumFile -Destination $checksumPath -Force
        } | Out-Null
    } else {
        $releaseBaseUrl = Get-ReleaseBaseUrl -RequestedVersion $Version
        Invoke-SetupMeasuredStage -Timing $setupTiming -Stage 'download' -Action {
            Invoke-WebRequest -UseBasicParsing -Uri "$releaseBaseUrl/$assetName" -OutFile $archivePath
            Invoke-WebRequest -UseBasicParsing -Uri "$releaseBaseUrl/$assetName.sha256" -OutFile $checksumPath
        } | Out-Null
    }

    $expectedHash = ((Get-Content -LiteralPath $checksumPath -Raw).Trim() -split '\s+')[0].ToLowerInvariant()
    $actualHash = Invoke-SetupMeasuredStage -Timing $setupTiming -Stage 'verify' -Action {
        Get-Sha256Hex -Path $archivePath
    }
    if ($actualHash -ne $expectedHash) {
        throw "SHA-256 mismatch for $assetName. Expected $expectedHash, got $actualHash."
    }

    $extractDir = Join-Path $tempRoot 'extract'
    $archiveExtraction = Invoke-SetupMeasuredStage -Timing $setupTiming -Stage 'extract' -Action {
        Expand-AgentDockReleaseArchive -ArchivePath $archivePath -DestinationPath $extractDir
    }
    Write-Host ("Validated {0} archive entries and streamed {1} required payload entries ({2} bytes)." -f `
        $archiveExtraction.entry_count, $archiveExtraction.selected_entry_count, $archiveExtraction.extracted_bytes)
    $sourceBinary = Join-Path $extractDir 'agentdock.exe'
    if (-not (Test-Path -LiteralPath $sourceBinary -PathType Leaf)) {
        throw "Release archive does not contain agentdock.exe: $assetName"
    }
    $sourceTrayBinary = Join-Path $extractDir 'agentdock-tray.exe'
    $sourceTrayIcon = Join-Path $extractDir 'agentdock.ico'
    $sourceArbiter = Join-Path $extractDir 'agentdock-arbiter.exe'
    $sourceCoreShim = Join-Path $extractDir 'agentdock-shim.exe'
    $sourceTrayShim = Join-Path $extractDir 'agentdock-tray-shim.exe'
    if (-not (Test-Path -LiteralPath $sourceTrayBinary -PathType Leaf)) {
        throw "Release archive does not contain agentdock-tray.exe: $assetName"
    }
    if (-not (Test-Path -LiteralPath $sourceTrayIcon -PathType Leaf)) {
        throw "Release archive does not contain agentdock.ico: $assetName"
    }
    foreach ($generationFile in @($sourceArbiter, $sourceCoreShim, $sourceTrayShim)) {
        if (-not (Test-Path -LiteralPath $generationFile -PathType Leaf)) {
            throw "Release archive does not contain generation update component: $generationFile"
        }
    }
    $coreSkillBundle = Join-Path $extractDir 'share\agentdock\core-skills'
    $coreSkillManifest = Join-Path $coreSkillBundle 'manifest.json'
    if (-not (Test-Path -LiteralPath $coreSkillBundle -PathType Container) -or
        -not (Test-Path -LiteralPath $coreSkillManifest -PathType Leaf)) {
        throw "Release archive does not contain a valid core Skill Bundle: $assetName"
    }
    $sourceWSLHelperDir = Join-Path $extractDir 'wsl-helper'
    $sourceWSLHelperManifestPath = Join-Path $sourceWSLHelperDir 'manifest.json'
    $sourceWSLHelperAMD64 = Join-Path $sourceWSLHelperDir 'agentdock-wsl-helper-linux-amd64'
    $sourceWSLHelperARM64 = Join-Path $sourceWSLHelperDir 'agentdock-wsl-helper-linux-arm64'
    if (-not (Test-Path -LiteralPath $sourceWSLHelperDir -PathType Container) -or
        -not (Test-Path -LiteralPath $sourceWSLHelperManifestPath -PathType Leaf) -or
        -not (Test-Path -LiteralPath $sourceWSLHelperAMD64 -PathType Leaf) -or
        -not (Test-Path -LiteralPath $sourceWSLHelperARM64 -PathType Leaf)) {
        throw "Release archive does not contain a valid WSL helper payload: $assetName"
    }
    try {
        $wslHelperManifest = Get-Content -LiteralPath $sourceWSLHelperManifestPath -Raw | ConvertFrom-Json
        if ([string] $wslHelperManifest.protocol_version -ne '1') {
            throw 'WSL helper manifest protocol_version must be 1.'
        }
        $helperChecks = @(
            @{
                Architecture = 'amd64'
                ExpectedName = 'agentdock-wsl-helper-linux-amd64'
                Path = $sourceWSLHelperAMD64
                Entry = $wslHelperManifest.helpers.amd64
            },
            @{
                Architecture = 'arm64'
                ExpectedName = 'agentdock-wsl-helper-linux-arm64'
                Path = $sourceWSLHelperARM64
                Entry = $wslHelperManifest.helpers.arm64
            }
        )
        foreach ($helperCheck in $helperChecks) {
            if ([string] $helperCheck.Entry.file -ne [string] $helperCheck.ExpectedName) {
                throw "WSL helper manifest file mismatch for $($helperCheck.Architecture)."
            }
            $expectedHelperHash = ([string] $helperCheck.Entry.sha256).Trim().ToLowerInvariant()
            $actualHelperHash = Get-Sha256Hex -Path $helperCheck.Path
            if ($expectedHelperHash -notmatch '^[0-9a-f]{64}$' -or $actualHelperHash -ne $expectedHelperHash) {
                throw "WSL helper SHA-256 mismatch for $($helperCheck.Architecture)."
            }
        }
    } catch {
        throw "Release archive contains an invalid WSL helper manifest: $($_.Exception.Message)"
    }

    # Validate the unpacked payload before stopping processes or touching an existing scheduled task.
    # This catches architecture/runtime incompatibility while the previous installation is still intact.
    $preflightVersionOutput = @(& $sourceBinary version --json 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw "AgentDock payload preflight failed with exit code $LASTEXITCODE."
    }
    try {
        $preflightVersionInfo = ($preflightVersionOutput | Out-String) | ConvertFrom-Json
    } catch {
        throw "AgentDock payload preflight returned invalid version metadata: $($_.Exception.Message)"
    }
    if ($null -eq $preflightVersionInfo -or [string]::IsNullOrWhiteSpace([string] $preflightVersionInfo.version)) {
        throw 'AgentDock payload preflight did not return a version.'
    }

    $payloadVersion = 'v' + ([string] $preflightVersionInfo.version).TrimStart('v')
    $engineReadyOutput = & $sourceBinary install --engine-ready 2>$null
    if ($LASTEXITCODE -ne 0 -or ("$engineReadyOutput" -notlike '*agentdock-installer-engine*')) {
        throw 'Release archive does not contain an Installer Engine capable AgentDock binary.'
    }
    $generationBootstrapDirectory = Join-Path $versionsDir $payloadVersion

    # The authoritative generation pointer and self-update transaction state are evaluated
    # by the Installer Engine's inspect command. Setup no longer parses active-version.json
    # itself; it only consumes the structured verdict and performs the required recovery
    # action (running the stable binary to trigger self-update recovery) when unresolved.
    $existingActiveVersion = ''
    $pointerState = 'missing'
    $inspectJson = (& $sourceBinary install inspect --state-root $runtimeDir 2>$null | Out-String).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw 'AgentDock install inspect failed before reading the existing generation state.'
    }
    try {
        $installInspection = $inspectJson | ConvertFrom-Json
    } catch {
        throw "AgentDock install inspect returned invalid JSON: $($_.Exception.Message)"
    }
    $pointerState = [string] $installInspection.pointer_state
    if ($pointerState -eq 'invalid') {
        throw 'active-version.json has an unsupported schema.'
    }

    # A previous Setup can finish restoring Engine-owned files yet fail while
    # restarting the source runtime or restoring another Windows adapter. The
    # Engine deliberately persists external_rollback_failed so a later install
    # cannot silently trust partially restored Task/Registry/service state.
    # When the recorded source generation is still the committed pointer and
    # its exact version is already healthy, Setup can safely perform the same
    # explicit confirmation an operator would otherwise run by hand.
    if ([bool] $installInspection.requires_adapter_rollback_confirmation) {
        $failedTransactionId = [string] $installInspection.transaction_id
        $failedSourceVersion = [string] $installInspection.source_version
        $pointerActiveVersion = [string] $installInspection.pointer_active_version
        $sourceMatchesPointer =
            -not [string]::IsNullOrWhiteSpace($failedSourceVersion) -and
            -not [string]::IsNullOrWhiteSpace($pointerActiveVersion) -and
            [string]::Equals(
                $failedSourceVersion.TrimStart('v'),
                $pointerActiveVersion.TrimStart('v'),
                [StringComparison]::OrdinalIgnoreCase
            )
        if ([string]::IsNullOrWhiteSpace($failedTransactionId) -or
            $pointerState -ne 'committed' -or
            -not $sourceMatchesPointer) {
            $installErrorCode = 'stale-rollback-recovery-unsafe'
            throw 'AgentDock found an incomplete OS adapter rollback, but the committed runtime does not match its recorded source version. Setup left the transaction untouched.'
        }

        Write-Host 'Confirming the healthy restored AgentDock runtime before Setup retries the upgrade...'
        $adapterRecoveryOutput = @(& $sourceBinary install abandon `
            --install-root $runtimeDir `
            --runtime-root $runtimeDir `
            --transaction-id $failedTransactionId `
            --require-health 2>&1)
        $adapterRecoveryExitCode = $LASTEXITCODE
        if ($adapterRecoveryExitCode -ne 0) {
            $installErrorCode = 'stale-rollback-recovery-failed'
            $adapterRecoveryText = (($adapterRecoveryOutput | Out-String).Trim())
            throw "AgentDock could not confirm the previous restored runtime (exit $adapterRecoveryExitCode). $adapterRecoveryText"
        }

        $reinspectJson = (& $sourceBinary install inspect --state-root $runtimeDir 2>$null | Out-String).Trim()
        if ($LASTEXITCODE -ne 0) {
            $installErrorCode = 'stale-rollback-recovery-failed'
            throw 'AgentDock install inspect failed after confirming the previous restored runtime.'
        }
        try {
            $installInspection = $reinspectJson | ConvertFrom-Json
        } catch {
            $installErrorCode = 'stale-rollback-recovery-failed'
            throw "AgentDock install inspect returned invalid JSON after rollback confirmation: $($_.Exception.Message)"
        }
        $pointerState = [string] $installInspection.pointer_state
        if ([string] $installInspection.state -ne 'rolled_back' -or
            [bool] $installInspection.requires_adapter_rollback_confirmation -or
            $pointerState -ne 'committed') {
            $installErrorCode = 'stale-rollback-recovery-failed'
            throw 'AgentDock did not converge the previous failed rollback to a healthy rolled_back state. Setup left the runtime unchanged.'
        }
        Write-Host 'Previous failed rollback was verified and closed; Setup can retry the upgrade.'
    }

    if ($pointerState -ne 'missing' -and $pointerState -ne 'committed') {
        # Setup must never repair or replace a generation while an update trial is unresolved.
        # Running the stable CUI entry is also the crash-recovery trigger: a live Arbiter leaves
        # the state in trial and Setup stops safely; an abandoned trial is rolled back first.
        # Fresh installer trials use install/transaction.json. The shim only recovers
        # update/transaction.json, so an installer trial is not a committed generation layout.
        if (-not [bool] $installInspection.pending_update_transaction) {
            Write-Host 'Incomplete installer generation pointer; Setup will let the Installer Engine recover.'
        } else {
            if (-not (Test-Path -LiteralPath $destinationBinary -PathType Leaf)) {
                throw "AgentDock generation state is '$pointerState', but the stable recovery entry is missing: $destinationBinary"
            }
            Write-Host "Resolving pending AgentDock generation transaction before Setup continues..."
            $recoveryOutput = @(& $destinationBinary version --json 2>&1)
            $recoveryExitCode = $LASTEXITCODE
            if ($recoveryExitCode -ne 0) {
                $recoveryText = (($recoveryOutput | Out-String).Trim())
                throw "AgentDock generation recovery failed with exit code $recoveryExitCode. $recoveryText"
            }
            # Re-inspect after the recovery action; Setup must not touch the generation until the pointer converges to committed.
            $reinspectJson = (& $sourceBinary install inspect --state-root $runtimeDir 2>$null | Out-String).Trim()
            if ($LASTEXITCODE -ne 0) {
                throw 'AgentDock install inspect failed after generation recovery.'
            }
            try {
                $installInspection = $reinspectJson | ConvertFrom-Json
            } catch {
                throw "AgentDock install inspect returned invalid JSON: $($_.Exception.Message)"
            }
            $pointerState = [string] $installInspection.pointer_state
            if ($pointerState -ne 'committed') {
                throw "AgentDock generation transaction is still '$pointerState'; Setup will not modify an unresolved generation."
            }
        }
    }
    if ($pointerState -eq 'committed') {
        $existingActiveVersion = 'v' + ([string] $installInspection.pointer_active_version).TrimStart('v')
        $generationLayoutDetected = $true
    }

    if ($generationLayoutDetected) {
        $existingGenerationDirectory = Join-Path $versionsDir $existingActiveVersion
        $existingGenerationCore = Join-Path $existingGenerationDirectory 'agentdock-core.exe'
        $existingGenerationTray = Join-Path $existingGenerationDirectory 'agentdock-tray.exe'
        # A crash during one-time legacy migration can commit the source pointer before stable
        # Core/Tray have been replaced by shims. Probe both exact paths so retry never misses
        # a still-running legacy process just because active-version.json already exists.
        $processWasRunning =
            (@(Get-AgentDockProcesses -BinaryPath $existingGenerationCore).Count -gt 0) -or
            (@(Get-AgentDockProcesses -BinaryPath $destinationBinary).Count -gt 0)
        $trayProcessWasRunning =
            (@(Get-AgentDockTrayProcesses -BinaryPath $existingGenerationTray).Count -gt 0) -or
            (@(Get-AgentDockTrayProcesses -BinaryPath $destinationTrayBinary).Count -gt 0)
    }

    # Setup upgrades and fresh installs share one Installer transaction. Do not pre-commit
    # the target generation through Update Engine before Task/Registry/runtime adapter work.

    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    if (-not $generationLayoutDetected) {
        $processWasRunning = @(Get-AgentDockProcesses -BinaryPath $destinationBinary).Count -gt 0
    }

    # One-time pre-generation migration must establish a real known-good source before
    # the old stable binaries are replaced by shims. Until this command commits the source
    # pointer the legacy binaries remain authoritative; after it commits, a new shim can
    # always route back to the copied source generation even if Setup is interrupted.
    # pointer_state comes from the engine's inspect; a legacy layout is only possible when the pointer is missing entirely.
    if (-not $generationLayoutDetected -and $existingInstallDetected -and
        $pointerState -eq 'missing') {
        if (-not (Test-Path -LiteralPath $destinationBinary -PathType Leaf) -or
            -not (Test-Path -LiteralPath $destinationTrayBinary -PathType Leaf)) {
            throw 'Existing legacy AgentDock installation is incomplete; Core and Tray are required for generation migration.'
        }
        $legacyVersionOutput = @(& $destinationBinary version --json 2>&1)
        if ($LASTEXITCODE -ne 0) {
            throw 'Unable to read the existing legacy AgentDock version before generation migration.'
        }
        try {
            $legacyVersionInfo = ($legacyVersionOutput | Out-String) | ConvertFrom-Json
            if ($null -eq $legacyVersionInfo -or [string]::IsNullOrWhiteSpace([string] $legacyVersionInfo.version)) {
                throw 'legacy version metadata is empty'
            }
            $legacyBootstrapVersion = 'v' + ([string] $legacyVersionInfo.version).TrimStart('v')
        } catch {
            throw "Existing legacy AgentDock returned invalid version metadata: $($_.Exception.Message)"
        }
        $legacyBootstrapJson = (& $sourceBinary install prepare-windows-legacy `
            --install-root $runtimeDir `
            --legacy-version $legacyBootstrapVersion `
            --legacy-core $destinationBinary `
            --legacy-tray $destinationTrayBinary `
            --payload-dir $extractDir 2>$null | Out-String).Trim()
        if ($LASTEXITCODE -ne 0) {
            throw 'Installer Engine could not establish the legacy known-good generation.'
        }
        try {
            $legacyBootstrapResult = $legacyBootstrapJson | ConvertFrom-Json
        } catch {
            throw "Installer Engine returned invalid legacy migration JSON: $($_.Exception.Message)"
        }
        if (-not [string]::Equals([string] $legacyBootstrapResult.version, $legacyBootstrapVersion, [StringComparison]::OrdinalIgnoreCase)) {
            throw 'Installer Engine legacy migration returned a different source version.'
        }
        $legacyBootstrapPrepared = $true
    }
    if ($effectivePrivilegeMode -eq 'elevated' -or $taskState.Exists) {
        $taskAction = if ($effectivePrivilegeMode -eq 'elevated') { 'prepare-elevated' } else { 'prepare-standard' }
        $taskActionResult = Start-ElevatedAgentDockTaskAction `
            -Action $taskAction `
            -BackupDirectory $taskBackupDirectory `
            -AdminLauncherPath $sourceTrayBinary `
            -LauncherPath $destinationTrayBinary `
            -RuntimeRoot $runtimeDir `
            -TaskUser $taskUser
        if (-not $taskActionResult.Started) {
            if ($effectivePrivilegeMode -eq 'elevated' -and -not $taskState.Exists) {
                # No scheduled-task state changed because RunAs never started. A fresh install can
                # therefore continue safely without administrator-enhanced core mode.
                $effectivePrivilegeMode = 'standard'
                $installWarningCode = 'elevated-mode-fallback'
                $installWarningMessage = 'Windows did not start the administrator approval request. AgentDock continued in standard user mode.'
                Write-Warning "$installWarningMessage Details: $($taskActionResult.ErrorMessage)"
            } else {
                throw "Administrator approval for AgentDock was not completed: $($taskActionResult.ErrorMessage)"
            }
        } else {
            # Once the elevated helper actually started, task state may have changed even if the helper
            # later reports failure. Mark the transaction before checking its exit status so rollback runs.
            $taskTransactionStarted = $true
            if (-not $taskActionResult.Succeeded) {
                throw $taskActionResult.ErrorMessage
            }
            Write-Host "Prepared AgentDock scheduled task transaction: $taskAction"
        }
    }
    $serviceStopStartedAt = [DateTime]::UtcNow
    $serviceStopWatch = [Diagnostics.Stopwatch]::StartNew()
    try {
        $agentDockStopAttempted = $true
        if ($generationLayoutDetected) {
            [void] (Stop-AgentDockForUpgrade -BinaryPath $existingGenerationCore)
            # Also stop a legacy stable Core left by a crash between source-pointer commit and shim install.
            [void] (Stop-AgentDockForUpgrade -BinaryPath $destinationBinary)
        } else {
            [void] (Stop-AgentDockForUpgrade -BinaryPath $destinationBinary)
        }

        # Stable entries remain the rollback boundary for both legacy bootstrap and same-version repair.
        # Preserve them before replacement even when active-version.json already exists.
        if (Test-Path -LiteralPath $destinationBinary -PathType Leaf) {
            Copy-Item -LiteralPath $destinationBinary -Destination $binaryBackup -Force
        }
        if (Test-Path -LiteralPath $destinationTrayBinary -PathType Leaf) {
            Copy-Item -LiteralPath $destinationTrayBinary -Destination $trayBackup -Force
        }

        if (-not $generationLayoutDetected) {
            $trayProcessWasRunning = @(Get-AgentDockTrayProcesses -BinaryPath $destinationTrayBinary).Count -gt 0
        }

        $trayStopAttempted = $true
        if ($generationLayoutDetected) {
            [void] (Stop-AgentDockTrayForUpgrade -BinaryPath $existingGenerationTray)
            [void] (Stop-AgentDockTrayForUpgrade -BinaryPath $destinationTrayBinary)
        } else {
            [void] (Stop-AgentDockTrayForUpgrade -BinaryPath $destinationTrayBinary)
        }
    } finally {
        $serviceStopWatch.Stop()
        [void] $setupTiming.Stages.Add([pscustomobject]@{
            stage = 'service_stop'
            started_at = $serviceStopStartedAt.ToString('o')
            completed_at = ([DateTime]::UtcNow).ToString('o')
            duration_ms = [Int64] $serviceStopWatch.ElapsedMilliseconds
        })
    }
    if (Test-Path -LiteralPath $destinationTrayIcon -PathType Leaf) {
        Copy-Item -LiteralPath $destinationTrayIcon -Destination $trayIconBackup -Force
    }

    if ($legacyBootstrapPrepared) {
        # Core/Tray were observed and stopped through their legacy stable paths above. From this
        # point onward the prepared source generation is the known-good generation boundary.
        $existingActiveVersion = $legacyBootstrapVersion
        $existingGenerationDirectory = Join-Path $versionsDir $existingActiveVersion
        $existingGenerationCore = Join-Path $existingGenerationDirectory 'agentdock-core.exe'
        $existingGenerationTray = Join-Path $existingGenerationDirectory 'agentdock-tray.exe'
        $generationLayoutDetected = $true
    }

    # Installer Engine exclusively publishes generations, stable shim/icon, and runtime manifest.
    # PowerShell only keeps old stable-file backups so an outer Task/Registry adapter failure can
    # restore the previous installation without writing the stable payload a second time.

    Add-UserPath -Directory $InstallDir

    $agentDockHome = $runtimeAgentDockHome
    $workspace = $runtimeAgentDockDefaultDir
    # The Installer Engine must observe and exclusively create a new Home before
    # initializing clean-install permissions. Precreating it here would erase that
    # evidence; an existing Home (even without policy.json) must remain untouched.
    New-Item -ItemType Directory -Path $workspace -Force | Out-Null

    # Windows Tunnel has a long-lived supervisor that will immediately restart cloudflared after
    # an external process kill. Stop that supervisor through the currently committed generation
    # before replacing cloudflared or changing its protected Token. Legacy installs without a
    # supervisor PID keep the old process-only migration path below.
    $tunnelSupervisorPidPath = Join-Path $runtimeDir 'tunnel-supervisor.pid'
    if ($generationLayoutDetected -and
        (Test-Path -LiteralPath $tunnelSupervisorPidPath -PathType Leaf) -and
        (Test-Path -LiteralPath $existingGenerationCore -PathType Leaf)) {
        $tunnelStopOutput = @(& $existingGenerationCore tunnel stop --runtime-root $runtimeDir 2>&1)
        if ($LASTEXITCODE -ne 0) {
            $tunnelStopDetail = ($tunnelStopOutput | Out-String).Trim()
            throw "Unable to stop the existing AgentDock Tunnel supervisor before update. $tunnelStopDetail"
        }
    }

    $cloudflaredProcessWasRunning = @(Get-CloudflaredProcesses -BinaryPath $cloudflaredBinary).Count -gt 0
    $cloudflaredStopAttempted = $true
    Invoke-SetupMeasuredStage -Timing $setupTiming -Stage 'service_stop' -Action {
        [void] (Stop-CloudflaredForUpgrade -BinaryPath $cloudflaredBinary)
    } | Out-Null
    if (Test-Path -LiteralPath $cloudflaredBinary -PathType Leaf) {
        Copy-Item -LiteralPath $cloudflaredBinary -Destination $cloudflaredBackup -Force
    }
    $cloudflaredReplacementStarted = $true
    Install-CloudflaredBinary `
        -DestinationBinary $cloudflaredBinary `
        -Architecture $architecture `
        -TempDirectory $tempRoot `
        -SourceBinary $OfflineCloudflaredBinary

    $publicUrl = ''
    $manifestPublicUrl = ''
    $engineCommitted = $false
    $enginePrepared = $false
    $engineTransactionId = [Guid]::NewGuid().ToString('N')
    if ($RegisterStartup) {
        New-Item -ItemType Directory -Path $runtimeDir -Force | Out-Null

        $existingAuthToken = Read-ProtectedText -Path $tokenPath -Entropy 'agentdock.startup.v1'
        if ([string]::IsNullOrWhiteSpace($AuthToken)) {
            $AuthToken = $existingAuthToken
        }
        if ([string]::IsNullOrWhiteSpace($AuthToken)) {
            $AuthToken = New-AgentDockToken
        }
        if (-not [string]::Equals($AuthToken, $existingAuthToken, [StringComparison]::Ordinal)) {
            if ([string]::IsNullOrWhiteSpace($existingAuthToken)) {
                Backup-UnreadableProtectedText -Path $tokenPath
            }
            Write-ProtectedText -Path $tokenPath -Value $AuthToken -Entropy 'agentdock.startup.v1'
        }

        $oauthCredentials = Initialize-OAuthCredentials `
            -PasswordPath $oauthPasswordPath `
            -TokenSecretPath $oauthTokenSecretPath `
            -RequestedPassword $OAuthPassword `
            -RequestedTokenSecret $OAuthTokenSecret
        $OAuthPassword = $oauthCredentials.Password
        $OAuthTokenSecret = $oauthCredentials.TokenSecret
        Write-TextFile -Path $credentialOwnerSidPath -Value $taskUser.Sid

        if ($resolvedTunnelMode -ne 'none') {
            $existingServerUrl = Read-TextFile -Path $serverUrlPath
            if ([string]::IsNullOrWhiteSpace($ServerUrl)) {
                $ServerUrl = [Environment]::GetEnvironmentVariable('AGENTDOCK_SERVER_URL')
            }
            if ([string]::IsNullOrWhiteSpace($ServerUrl)) {
                $ServerUrl = $existingServerUrl
            }
            if ($resolvedTunnelMode -eq 'named' -and [string]::IsNullOrWhiteSpace($ServerUrl)) {
                $ServerUrl = Read-TextFile -Path $namedServerUrlPath
            }
            if ($resolvedTunnelMode -eq 'quick') {
                $ServerUrl = ''
            }
            if ($resolvedTunnelMode -eq 'named') {
                if ([string]::IsNullOrWhiteSpace($ServerUrl)) {
                    $ServerUrl = Read-Host 'Fixed HTTPS public origin, for example https://agent.example.com'
                }
                $ServerUrl = Normalize-ServerUrl -Value $ServerUrl
                Write-TextFile -Path $namedServerUrlPath -Value $ServerUrl

                $tunnelTokenState = Resolve-AvailableTunnelToken `
                    -TokenPath $tunnelTokenPath `
                    -RequestedToken $TunnelToken `
                    -TokenFile $TunnelTokenFile
                $existingTunnelToken = $tunnelTokenState.ExistingToken
                $TunnelToken = $tunnelTokenState.Token
                if ([string]::IsNullOrWhiteSpace($TunnelToken)) {
                    if ($InstallChannel -eq 'setup') {
                        $installErrorCode = 'tunnel-token-required'
                        throw 'The saved Cloudflare Tunnel Token is missing or unreadable. Re-enter it in Setup.'
                    }
                    $secureTunnelToken = Read-Host 'Cloudflare Tunnel Token' -AsSecureString
                    $credential = New-Object System.Management.Automation.PSCredential('token', $secureTunnelToken)
                    $TunnelToken = $credential.GetNetworkCredential().Password
                }
                if ([string]::IsNullOrWhiteSpace($TunnelToken)) {
                    throw 'A fixed Cloudflare hostname requires a Tunnel Token.'
                }
                if (-not [string]::Equals($TunnelToken, $existingTunnelToken, [StringComparison]::Ordinal)) {
                    Write-ProtectedText -Path $tunnelTokenPath -Value $TunnelToken -Entropy 'agentdock.cloudflare.tunnel.v1'
                }
            }
            Write-TextFile -Path $serverUrlPath -Value $ServerUrl
            Write-TextFile -Path $tunnelModePath -Value $resolvedTunnelMode
        } else {
            if (-not $preserveTailscale) { Write-TextFile -Path $serverUrlPath -Value '' }
            Write-TextFile -Path $tunnelModePath -Value 'none'
            Remove-Item -LiteralPath $quickTunnelUrlPath -Force -ErrorAction SilentlyContinue
        }

        $escapedBinaryPath = $destinationBinary.Replace("'", "''")
        $escapedRuntimeDir = $runtimeDir.Replace("'", "''")
        # Keep the compatibility launcher only for legacy updates and rollback. New startup entries use the WinExe tray proxy.
        $launcher = @"
`$ErrorActionPreference = 'Stop'
& '$escapedBinaryPath' service launch-core --runtime-root '$escapedRuntimeDir'
exit `$LASTEXITCODE
"@
        [IO.File]::WriteAllText($launcherPath, $launcher, $Utf8NoBom)

        $manifestPublicUrl = ''
        if ($resolvedTunnelMode -eq 'named') {
            $manifestPublicUrl = $ServerUrl
        }
        $publicUrl = $manifestPublicUrl
        if ($preserveTailscale) { $publicUrl = $preservedTailscaleOrigin }
        if ($effectivePrivilegeMode -eq 'elevated') {
            Remove-ItemProperty -LiteralPath $runKey -Name $runValueName -ErrorAction SilentlyContinue
            Enable-AgentDockTask
        } else {
            $startupCommand = "`"$destinationTrayBinary`" --start-core --runtime-root `"$runtimeDir`""
            Set-RunValue -RegistryPath $runKey -Name $runValueName -Value $startupCommand
        }
        $startupRegistrationChanged = $true
        $trayStartupCommand = "`"$destinationTrayBinary`" --background"
        Set-RunValue -RegistryPath $runKey -Name $trayRunValueName -Value $trayStartupCommand
        $trayStartupRegistrationChanged = $true

        if ($resolvedTunnelMode -ne 'none') {
            # Compatibility launcher delegates to the native Tunnel supervisor.
            # Normal startup still uses the WinExe tray proxy.
            $cloudflaredLauncher = @"
`$ErrorActionPreference = 'Stop'
& '$escapedBinaryPath' tunnel launch --runtime-root '$escapedRuntimeDir'
exit `$LASTEXITCODE
"@
            [IO.File]::WriteAllText($cloudflaredLauncherPath, $cloudflaredLauncher, $Utf8NoBom)
            [IO.File]::WriteAllText($cloudflaredStdoutLogPath, '', $Utf8NoBom)
            [IO.File]::WriteAllText($cloudflaredStderrLogPath, '', $Utf8NoBom)

            $cloudflaredStartupCommand = "`"$destinationTrayBinary`" --start-tunnel --runtime-root `"$runtimeDir`""
            Set-RunValue -RegistryPath $runKey -Name $cloudflaredRunValueName -Value $cloudflaredStartupCommand
            $tunnelStartupRegistrationChanged = $true
        } else {
            Remove-ItemProperty -LiteralPath $runKey -Name $cloudflaredRunValueName -ErrorAction SilentlyContinue
            $tunnelStartupRegistrationChanged = $true
            Write-TextFile -Path $tunnelModePath -Value 'none'
            if (-not $preserveTailscale) { Write-TextFile -Path $serverUrlPath -Value '' }
            Remove-Item -LiteralPath $quickTunnelUrlPath -Force -ErrorAction SilentlyContinue
        }
    }

    # HKCU/Task (if any) are already written. Engine owns runtime.json/skills/start.
        # committed is written only after this script finishes adapter work and calls install commit.
        # A fresh or different-version generation is published here from --payload-dir; PowerShell does not pre-commit it.
        New-Item -ItemType Directory -Path $runtimeDir -Force | Out-Null
        $engineArgs = @(
            'install',
            '--install-root', $runtimeDir,
            '--payload-dir', $extractDir,
            '--host', '127.0.0.1',
            '--port', "$Port",
            '--tunnel-mode', $resolvedTunnelMode,
            '--privilege-mode', $effectivePrivilegeMode,
            '--agentdock-home', $runtimeAgentDockHome,
            '--agentdock-default-dir', $runtimeAgentDockDefaultDir,
            '--startup-value-name', $runValueName,
            '--tray-startup-value-name', $trayRunValueName,
            '--cloudflared-startup-value-name', $cloudflaredRunValueName,
            '--channel', $InstallChannel,
            '--transaction-id', $engineTransactionId,
            '--defer-commit'
        )
        if ($effectivePrivilegeMode -eq 'elevated') {
            $engineArgs += @('--task-name', 'AgentDock')
        }
        if ((-not $RegisterStartup) -or ($InstallChannel -eq 'setup' -and -not $existingInstallDetected)) {
            # Setup always leaves Core activation to the Windows adapter below so fresh, repair,
            # and upgrade launches all happen outside the Inno RedirectionGuard process tree.
            $engineArgs += @('--no-start', '--skip-health')
        }
        if (-not [string]::IsNullOrWhiteSpace($payloadVersion)) {
            $engineArgs += @('--version', $payloadVersion)
        }
        if (-not [string]::IsNullOrWhiteSpace($coreSkillBundle)) {
            $engineArgs += @('--skill-bundle', $coreSkillBundle)
        }
        if (-not [string]::IsNullOrWhiteSpace($manifestPublicUrl)) {
            $engineArgs += @('--server-url', $manifestPublicUrl)
        }
        # Engine may replace stable shim/icon before returning an error; mark this before invocation so catch can restore them.
        $stableFilesMayBeReplaced = $true
        $engineInvoked = $true
        $engineInvocationStartedAt = [DateTime]::UtcNow
        $engineStageWatch = [Diagnostics.Stopwatch]::StartNew()
        try {
            if ($InstallChannel -eq 'setup' -and $existingInstallDetected -and $RegisterStartup) {
                # The upgrade Engine starts and verifies Core/Tunnel during its trial.
                # Use the native GUI worker while retaining the Engine transaction
                # identity and current user SID, with no interactive PowerShell task.
                $engineJson = (Invoke-SetupRuntimeProcess -FilePath $sourceBinary `
                    -Arguments (ConvertTo-NativeArguments -Values $engineArgs) `
                    -WaitForExit -PassThruOutput -TimeoutSeconds 300 | Out-String).Trim()
            } else {
                $engineErrorPath = Join-Path $tempRoot 'installer-engine-error.log'
                $engineJson = (& $sourceBinary @engineArgs 2>$engineErrorPath | Out-String).Trim()
                if ($LASTEXITCODE -ne 0) {
                    $engineDetails = (Get-Content -LiteralPath $engineErrorPath -Raw -ErrorAction SilentlyContinue)
                    $engineFailureMessage = Get-InstallerEngineFailureMessage -RuntimeRoot $runtimeDir `
                        -FallbackMessage "Installer Engine failed to write the runtime generation and manifest. $engineDetails" `
                        -NotBeforeUtc $engineInvocationStartedAt
                    throw $engineFailureMessage
                }
            }
        } finally {
            $engineStageWatch.Stop()
            [void] $setupTiming.Stages.Add([pscustomobject]@{
                stage = 'payload_write'
                started_at = $engineInvocationStartedAt.ToString('o')
                completed_at = ([DateTime]::UtcNow).ToString('o')
                duration_ms = [Int64] $engineStageWatch.ElapsedMilliseconds
            })
        }
        # Engine already left a trial. Catch must abandon even if the JSON handshake is unreadable.
        $enginePrepared = $true
        try {
            $engineResult = $engineJson | ConvertFrom-Json
        } catch {
            throw "Installer Engine returned invalid JSON: $($_.Exception.Message)"
        }
        if (-not [string]::Equals([string] $engineResult.transaction_id, $engineTransactionId, [StringComparison]::Ordinal)) {
            throw 'Installer Engine did not acknowledge the requested transaction id.'
        }
        if ($null -ne $engineResult.PSObject.Properties['timing']) {
            $engineTiming = $engineResult.timing
        }

    if (-not $RegisterStartup) {
        Remove-ItemProperty -LiteralPath $runKey -Name $trayRunValueName -ErrorAction SilentlyContinue
        $trayStartupRegistrationChanged = $true
    }

    $mustRestartExistingProcess = (-not $RegisterStartup) -and $processWasRunning

    $localMCPUrl = "http://127.0.0.1:$Port/mcp"
    Write-Host 'Core Skills were installed by the Installer Engine.'

    # Provision is complete here. A fresh Installer-owned generation must become committed before
    # the stable shim can be used for optional immediate activation; outer rollback can still abandon
    # this transaction because the committed pointer keeps the Installer transaction id.
    if ($enginePrepared -and -not $existingInstallDetected) {
        & $sourceBinary install commit --install-root $runtimeDir --runtime-root $runtimeDir --transaction-id $engineTransactionId --keep-journal 1>$null
        if ($LASTEXITCODE -ne 0) {
            throw 'Installer Engine failed to commit the fresh install transaction.'
        }
        $engineCommitted = $true
    }

    # Immediate activation is a separate phase; only a fresh standard install may defer activation,
    # because an upgrade must still be able to roll back to its prior runtime.
    $healthStatus = 'not-started'
    $engineOwnsActivation = -not ($InstallChannel -eq 'setup' -and -not $existingInstallDetected)
    try {
        if ($InstallChannel -eq 'setup' -and -not $taskState.SchedulerAvailable -and
            ($RegisterStartup -or $mustRestartExistingProcess -or $trayProcessWasRunning)) {
            throw "Windows Task Scheduler is unavailable for immediate Setup activation: $($taskState.SchedulerError)"
        }

        if ($engineOwnsActivation -and $RegisterStartup) {
            $healthStatus = 'healthy'
            if ($resolvedTunnelMode -eq 'quick') {
                # Tunnel/public readiness is a soft dependency. Record a URL only if it is already
                # available; the control panel will show eventual readiness after install/update.
                $publicUrl = Read-TextFile -Path $quickTunnelUrlPath
            } elseif ($resolvedTunnelMode -eq 'named') {
                $publicUrl = $ServerUrl
            }
        } elseif ($RegisterStartup) {
            Invoke-SetupMeasuredStage -Timing $setupTiming -Stage 'service_start' -Action {
                if ($effectivePrivilegeMode -eq 'elevated') {
                    Start-AgentDockTask -AgentDockBinary $destinationBinary -ExpectedUserSid $taskUser.Sid
                } elseif ($InstallChannel -eq 'setup') {
                    Invoke-SetupRuntimeProcess `
                        -FilePath $sourceBinary `
                        -Arguments "service start --runtime-root `"$runtimeDir`"" `
                        -WaitForExit
                } else {
                    & $destinationBinary service start --runtime-root $runtimeDir
                    if ($LASTEXITCODE -ne 0) {
                        throw "AgentDock native service start failed with exit code $LASTEXITCODE."
                    }
                }
            } | Out-Null
            Invoke-SetupMeasuredStage -Timing $setupTiming -Stage 'readiness_wait' -Action {
                Wait-AgentDockHealth -HealthPort $Port
            } | Out-Null
            $healthStatus = 'healthy'

            if ($resolvedTunnelMode -eq 'quick') {
                $publicUrl = Read-TextFile -Path $quickTunnelUrlPath
            } elseif ($resolvedTunnelMode -eq 'named') {
                $publicUrl = $ServerUrl
            }
        } elseif ($mustRestartExistingProcess) {
            Invoke-SetupMeasuredStage -Timing $setupTiming -Stage 'service_start' -Action {
                if ($InstallChannel -eq 'setup') {
                    Invoke-SetupRuntimeProcess `
                        -FilePath $sourceBinary `
                        -Arguments "service start --runtime-root `"$runtimeDir`"" `
                        -WaitForExit
                } else {
                    & $destinationBinary service start --runtime-root $runtimeDir
                    if ($LASTEXITCODE -ne 0) {
                        throw "AgentDock native service restart failed with exit code $LASTEXITCODE."
                    }
                }
            } | Out-Null
            Invoke-SetupMeasuredStage -Timing $setupTiming -Stage 'readiness_wait' -Action {
                Wait-AgentDockHealth -HealthPort $Port
            } | Out-Null
            $healthStatus = 'healthy'
        }

        if ($RegisterStartup -or $trayProcessWasRunning) {
            $activationTrayBinary = $destinationTrayBinary
            if ($enginePrepared -and -not $engineCommitted) {
                $activationTrayBinary = Join-Path $generationBootstrapDirectory 'agentdock-tray.exe'
            }
            Start-AgentDockTray -BinaryPath $activationTrayBinary
        }
    } catch {
        if ($existingInstallDetected -or $effectivePrivilegeMode -ne 'standard') {
            throw
        }

        $healthStatus = 'deferred'
        $activationWarningMessage = 'AgentDock was installed and startup was configured, but immediate runtime activation or verification did not complete. Start AgentDock from the Start menu or sign in again to retry.'
        if ([string]::IsNullOrWhiteSpace($installWarningMessage)) {
            $installWarningMessage = $activationWarningMessage
        } else {
            $installWarningMessage = ($installWarningMessage + ' ' + $activationWarningMessage).Trim()
        }
        if ([string]::IsNullOrWhiteSpace($installWarningCode)) {
            $installWarningCode = 'runtime-launch-deferred'
        } else {
            $installWarningCode = "$installWarningCode,runtime-launch-deferred"
        }
        Write-Warning "$activationWarningMessage Details: $($_.Exception.Message)"
    }

    if (Test-Path -LiteralPath $legacyManagerPath -PathType Leaf) {
        Remove-Item -LiteralPath $legacyManagerPath -Force
    }

    if ($enginePrepared) {
        $commitArgs = @('install', 'commit', '--install-root', $runtimeDir, '--runtime-root', $runtimeDir, '--transaction-id', $engineTransactionId)
        if ($healthStatus -eq 'healthy') { $commitArgs += '--require-health' }
        & $sourceBinary @commitArgs 1>$null
        if ($LASTEXITCODE -ne 0) {
            throw 'Installer Engine failed to commit the install transaction.'
        }
        $engineCommitted = $true
    }

    # Core is authoritative for install/update success. Start Tunnel only after commit and do it
    # asynchronously through the existing WinExe startup proxy so Cloudflare/network readiness
    # cannot hold the transaction or its success UI open.
    if ($RegisterStartup -and $resolvedTunnelMode -ne 'none') {
        try {
            $tunnelStartupArguments = "--start-tunnel --runtime-root `"$runtimeDir`""
            if ($InstallChannel -eq 'setup') {
                Invoke-SetupRuntimeProcess `
                    -FilePath $destinationTrayBinary `
                    -Arguments $tunnelStartupArguments
            } else {
                Start-Process `
                    -FilePath $destinationTrayBinary `
                    -ArgumentList $tunnelStartupArguments `
                    -WindowStyle Hidden | Out-Null
            }
        } catch {
            # Network/public readiness stays a soft dependency, but failure to schedule the local
            # Tunnel host is actionable. Keep Core committed and surface the degraded public state.
            $tunnelWarningMessage = 'AgentDock was installed successfully, but public access could not be started in the background. Open the control panel or sign in again to retry.'
            if ([string]::IsNullOrWhiteSpace($installWarningMessage)) {
                $installWarningMessage = $tunnelWarningMessage
            } else {
                $installWarningMessage = ($installWarningMessage + ' ' + $tunnelWarningMessage).Trim()
            }
            if ([string]::IsNullOrWhiteSpace($installWarningCode)) {
                $installWarningCode = 'tunnel-start-deferred'
            } else {
                $installWarningCode = "$installWarningCode,tunnel-start-deferred"
            }
            Write-Warning "$tunnelWarningMessage Details: $($_.Exception.Message)"
        }
        if ($resolvedTunnelMode -eq 'quick') {
            $publicUrl = Read-TextFile -Path $quickTunnelUrlPath
        } elseif ($resolvedTunnelMode -eq 'named') {
            $publicUrl = $ServerUrl
        }
    }

    $taskTransactionCommitted = $taskTransactionStarted
    if ($preserveTailscale) { $publicUrl = $preservedTailscaleOrigin }
    $publicMCPUrl = ''
    if (-not [string]::IsNullOrWhiteSpace($publicUrl)) {
        $publicMCPUrl = "$publicUrl/mcp"
    }
    try {
        Write-SetupTimingSummary -Timing $setupTiming -RuntimeRoot $runtimeDir -InstallerTiming $engineTiming
    } catch {
        Write-Warning "AgentDock installation completed, but the timing summary could not be written: $($_.Exception.Message)"
    }
    Write-InstallResult `
        -Path $ResultFile `
        -Success $true `
        -Message 'AgentDock installation completed.' `
        -InstalledVersion $Version `
        -LocalMCPUrl $localMCPUrl `
        -PublicMCPUrl $publicMCPUrl `
        -BearerToken $AuthToken `
        -OAuthLoginPassword $OAuthPassword `
        -HealthStatus $healthStatus `
        -PrivilegeMode $effectivePrivilegeMode `
        -WarningCode $installWarningCode `
        -WarningMessage $installWarningMessage

    Write-Host "AgentDock installed: $destinationBinary"
    Write-Host "Local MCP address: $localMCPUrl"
    Write-Host 'Open a new terminal if the updated user PATH is not visible yet.'
    if ($RegisterStartup) {
        Write-Host "Bearer Token: $AuthToken"
    }
    if ($resolvedTunnelMode -ne 'none') {
        Write-Host ''
        Write-Host 'AgentDock public access configured'
        Write-Host "Public mode: $resolvedTunnelMode"
        if (-not [string]::IsNullOrWhiteSpace($publicUrl)) {
            Write-Host "Public address: $publicUrl"
            Write-Host "MCP address: $publicUrl/mcp"
        } else {
            Write-Host 'Public access is starting in the background.'
            Write-Host 'Open the AgentDock control panel to view the public address when it is ready.'
        }
        Write-Host "Bearer Token: $AuthToken"
        Write-Host "OAuth login password: $OAuthPassword"
        Write-Host 'Authentication: Bearer Token and OAuth are both enabled.'
        Write-Host "cloudflared stdout log: $cloudflaredStdoutLogPath"
        Write-Host "cloudflared stderr log: $cloudflaredStderrLogPath"
        if ($resolvedTunnelMode -eq 'quick') {
            Write-Host 'The temporary address changes after cloudflared restarts.'
            Write-Host 'The control panel reports the current address after background startup completes.'
        } else {
            Write-Host 'Tunnel startup continues in the background; readiness is shown in the control panel and logs.'
            Write-Host "Cloudflare Public Hostname service target: http://127.0.0.1:$Port"
        }
    }
} catch {
    $installError = $_
    # The Engine may fail before its JSON handshake. Only rebind our own ID.
    $enginePrepared = Test-OwnInstallerTransaction
    $taskRollbackError = $null
    $rollbackError = $null
    $taskRecoveryPath = ''
    try {
        if ($generationLayoutDetected -or $enginePrepared) {
            # Target Core runs as agentdock-core.exe after both bootstrap and Update Engine.
            # Stopping the CUI shim would miss the running generation and leave the new pointer live.
            $rollbackGenerationTray = Join-Path $generationBootstrapDirectory 'agentdock-tray.exe'
            $rollbackGenerationCore = Join-Path $generationBootstrapDirectory 'agentdock-core.exe'
            [void] (Stop-AgentDockTrayForUpgrade -BinaryPath $rollbackGenerationTray)
            [void] (Stop-AgentDockForUpgrade -BinaryPath $rollbackGenerationCore)
        } else {
            if ($trayStopAttempted -or $stableFilesMayBeReplaced -or $trayStartupRegistrationChanged) {
                [void] (Stop-AgentDockTrayForUpgrade -BinaryPath $destinationTrayBinary)
            }
            if ($agentDockStopAttempted -or $stableFilesMayBeReplaced -or $startupRegistrationChanged) {
                [void] (Stop-AgentDockForUpgrade -BinaryPath $destinationBinary)
            }
        }
        if ($cloudflaredStopAttempted -or $cloudflaredReplacementStarted -or $tunnelStartupRegistrationChanged) {
            [void] (Stop-CloudflaredForUpgrade -BinaryPath $cloudflaredBinary)
        }
        if ($effectivePrivilegeMode -eq 'elevated') {
            Stop-ScheduledTask -TaskName 'AgentDock' -TaskPath '\' -ErrorAction SilentlyContinue
            Start-Sleep -Milliseconds 500
        }

        if ($enginePrepared) {
            # Restore generation bytes before outer credentials/registry. This
            # also restores same-version repair binaries from the journal.
            & $sourceBinary install restore-files --install-root $runtimeDir --runtime-root $runtimeDir `
                --transaction-id $engineTransactionId --payload-dir $extractDir 1>$null
            if ($LASTEXITCODE -ne 0) { throw "Installer Engine could not restore generation files (exit $LASTEXITCODE)." }
        }

        if ($cloudflaredReplacementStarted) {
            $cloudflaredBackupExists = Test-Path -LiteralPath $cloudflaredBackup -PathType Leaf
            if ($cloudflaredBackupExists) {
                Copy-Item -LiteralPath $cloudflaredBackup -Destination $cloudflaredBinary -Force
            }
            if (-not $cloudflaredBackupExists) {
                Remove-Item -LiteralPath $cloudflaredBinary -Force -ErrorAction SilentlyContinue
            }
        }

        if ($stableFilesMayBeReplaced) {
            $trayBackupExists = Test-Path -LiteralPath $trayBackup -PathType Leaf
            if ($trayBackupExists) {
                Copy-Item -LiteralPath $trayBackup -Destination $destinationTrayBinary -Force
            } else {
                Remove-Item -LiteralPath $destinationTrayBinary -Force -ErrorAction SilentlyContinue
            }
            $trayIconBackupExists = Test-Path -LiteralPath $trayIconBackup -PathType Leaf
            if ($trayIconBackupExists) {
                Copy-Item -LiteralPath $trayIconBackup -Destination $destinationTrayIcon -Force
            } else {
                Remove-Item -LiteralPath $destinationTrayIcon -Force -ErrorAction SilentlyContinue
            }
            $binaryBackupExists = Test-Path -LiteralPath $binaryBackup -PathType Leaf
            if ($binaryBackupExists) {
                Copy-Item -LiteralPath $binaryBackup -Destination $destinationBinary -Force
            } else {
                Remove-Item -LiteralPath $destinationBinary -Force -ErrorAction SilentlyContinue
            }
        }

        if ($rollbackStateCaptured) {
            foreach ($item in $managedRuntimeFiles) {
                Restore-FileState -Path $item.Path -Name $item.Name -BackupDirectory $runtimeBackupDir
            }

            if ($null -ne $previousRunValue) {
                Set-RunValue -RegistryPath $runKey -Name $runValueName -Value $previousRunValue
            } else {
                Remove-ItemProperty -LiteralPath $runKey -Name $runValueName -ErrorAction SilentlyContinue
            }
            if ($null -ne $previousTrayRunValue) {
                Set-RunValue -RegistryPath $runKey -Name $trayRunValueName -Value $previousTrayRunValue
            } else {
                Remove-ItemProperty -LiteralPath $runKey -Name $trayRunValueName -ErrorAction SilentlyContinue
            }
            if ($null -ne $previousTunnelRunValue) {
                Set-RunValue -RegistryPath $runKey -Name $cloudflaredRunValueName -Value $previousTunnelRunValue
            } else {
                Remove-ItemProperty -LiteralPath $runKey -Name $cloudflaredRunValueName -ErrorAction SilentlyContinue
            }
        }

        if ($taskTransactionStarted -and -not $taskTransactionCommitted) {
            try {
                $restoreTaskActionResult = Start-ElevatedAgentDockTaskAction `
                    -Action restore `
                    -BackupDirectory $taskBackupDirectory `
                    -AdminLauncherPath $sourceTrayBinary `
                    -LauncherPath '' `
                    -TaskUser $taskUser
                if (-not $restoreTaskActionResult.Started) {
                    throw "Administrator approval for AgentDock rollback was not completed: $($restoreTaskActionResult.ErrorMessage)"
                }
                if (-not $restoreTaskActionResult.Succeeded) {
                    throw $restoreTaskActionResult.ErrorMessage
                }
                $taskRestored = $true
            } catch {
                $taskRollbackError = $_
                try {
                    # Preserve the original task definition when automatic restore fails so temp cleanup does not destroy manual recovery material.
                    $taskRecoveryPath = Join-Path `
                        (Join-Path $runtimeDir 'logs\installer') `
                        ('scheduled-task-recovery-' + [Guid]::NewGuid().ToString('N'))
                    New-Item -ItemType Directory -Path $taskRecoveryPath -Force | Out-Null
                    $taskBackupStatePath = Join-Path $taskBackupDirectory 'state.json'
                    if (-not (Test-Path -LiteralPath $taskBackupStatePath -PathType Leaf)) {
                        throw "AgentDock scheduled-task backup state is missing: $taskBackupStatePath"
                    }
                    Copy-Item -LiteralPath $taskBackupStatePath -Destination $taskRecoveryPath -Force
                    $taskBackupXmlPath = Join-Path $taskBackupDirectory 'task.xml'
                    if (Test-Path -LiteralPath $taskBackupXmlPath -PathType Leaf) {
                        Copy-Item -LiteralPath $taskBackupXmlPath -Destination $taskRecoveryPath -Force
                    }
                    Write-Warning "AgentDock scheduled-task recovery files were preserved at: $taskRecoveryPath"
                } catch {
                    Write-Warning "AgentDock could not preserve scheduled-task recovery files: $($_.Exception.Message)"
                    $taskRecoveryPath = ''
                }
                Write-Warning "AgentDock scheduled-task rollback failed: $($taskRollbackError.Exception.Message)"
            }
        }

        $rollbackHealthPort = $Port
        if (Test-Path -LiteralPath $runtimeManifestPath -PathType Leaf) {
            $restoredManifest = Get-Content -LiteralPath $runtimeManifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
            if ($restoredManifest.PSObject.Properties['port']) { $rollbackHealthPort = [int]$restoredManifest.port }
        }
        $taskWillRestartAgentDock = $false
        if ($taskRestored -and $taskState.WasRunning) {
            Start-AgentDockTask -AgentDockBinary $sourceBinary -ExpectedUserSid $taskUser.Sid
            $taskWillRestartAgentDock = $true
        }
        if ($processWasRunning -and -not $taskWillRestartAgentDock) {
            if (Test-Path -LiteralPath $destinationBinary -PathType Leaf) {
                # The Engine transaction has restored the committed source generation. Wait for the
                # source Core to become healthy before confirming the outer adapter rollback.
                if ($InstallChannel -eq 'setup') {
                    Invoke-SetupRuntimeProcess `
                        -FilePath $sourceBinary `
                        -Arguments "service start --runtime-root `"$runtimeDir`"" `
                        -WaitForExit
                } else {
                    & $sourceBinary service start --runtime-root $runtimeDir
                    if ($LASTEXITCODE -ne 0) {
                        throw "AgentDock rollback service start failed with exit code $LASTEXITCODE."
                    }
                }
                Wait-AgentDockHealth -HealthPort $rollbackHealthPort
            } elseif (Test-Path -LiteralPath $launcherPath -PathType Leaf) {
                Start-AgentDockLauncher -LauncherPath $launcherPath
            }
        } elseif ($taskWillRestartAgentDock) {
            Wait-AgentDockHealth -HealthPort $rollbackHealthPort
        }
        if ($cloudflaredProcessWasRunning) {
            # Rollback success is anchored to the restored source Core + local health. Tunnel/public
            # recovery is best-effort and must not turn Cloudflare/network delay into rollback_failed.
            if (Test-Path -LiteralPath $destinationTrayBinary -PathType Leaf) {
                try {
                    $rollbackTunnelArguments = "--start-tunnel --runtime-root `"$runtimeDir`""
                    if ($InstallChannel -eq 'setup') {
                        Invoke-SetupRuntimeProcess `
                            -FilePath $destinationTrayBinary `
                            -Arguments $rollbackTunnelArguments
                    } else {
                        Start-Process `
                            -FilePath $destinationTrayBinary `
                            -ArgumentList $rollbackTunnelArguments `
                            -WindowStyle Hidden | Out-Null
                    }
                } catch {
                    # Tunnel diagnostics remain available through panel/runtime logs.
                }
            } elseif (Test-Path -LiteralPath $cloudflaredLauncherPath -PathType Leaf) {
                try {
                    Start-CloudflaredLauncher -LauncherPath $cloudflaredLauncherPath
                } catch {
                    # Legacy launcher recovery is also a soft dependency.
                }
            }
        }
        if ($trayProcessWasRunning -and (Test-Path -LiteralPath $destinationTrayBinary -PathType Leaf)) {
            Start-AgentDockTray -BinaryPath $destinationTrayBinary
        }
    } catch {
        $rollbackError = $_
        Write-Warning "AgentDock rollback failed: $($_.Exception.Message)"
    }

    if (($enginePrepared -or $engineCommitted) -and (Test-Path -LiteralPath $sourceBinary -PathType Leaf)) {
        $abandonArgs = @(
            'install', 'abandon',
            '--install-root', $runtimeDir,
            '--runtime-root', $runtimeDir
        )
        if (-not [string]::IsNullOrWhiteSpace($engineTransactionId)) {
            $abandonArgs += @('--transaction-id', $engineTransactionId)
        }
        if ($null -ne $rollbackError -or $null -ne $taskRollbackError) {
            $abandonArgs += '--rollback-failed'
        } elseif ($processWasRunning -or $taskState.WasRunning) {
            $abandonArgs += '--require-health'
        }
        & $sourceBinary @abandonArgs 1>$null
        if ($LASTEXITCODE -ne 0 -and $null -eq $rollbackError) {
            try {
                throw "Installer Engine could not record rollback state (exit $LASTEXITCODE)."
            } catch {
                $rollbackError = $_
            }
        }
    }

    $resultErrorCode = $installErrorCode
    $resultMessage = $installError.Exception.Message
    $resultErrorRecord = $installError
    if ($null -ne $taskRollbackError) {
        $resultErrorCode = 'elevated-task-rollback-failed'
        $resultMessage = "AgentDock installation failed and the previous administrator-enhanced task could not be restored automatically. Original error: $($installError.Exception.Message) Rollback error: $($taskRollbackError.Exception.Message)"
        if (-not [string]::IsNullOrWhiteSpace($taskRecoveryPath)) {
            $resultMessage += " Recovery files: $taskRecoveryPath"
        }
        $resultErrorRecord = $taskRollbackError
    } elseif ($null -ne $rollbackError) {
        $resultErrorCode = 'rollback-failed'
        $resultMessage = "AgentDock installation failed and rollback did not complete. Original error: $($installError.Exception.Message) Rollback error: $($rollbackError.Exception.Message)"
        $resultErrorRecord = $rollbackError
    }

    if ($null -ne $rollbackError -or $null -ne $taskRollbackError) {
        $preserveRecoveryFiles = $true
        Write-Warning "Recovery files retained after incomplete rollback: $tempRoot"
        $resultMessage += " Recovery files: $tempRoot"
    }

    try {
        Write-SetupTimingSummary -Timing $setupTiming -RuntimeRoot $runtimeDir -InstallerTiming $engineTiming
    } catch {
        Write-Warning "AgentDock failure timing summary could not be written: $($_.Exception.Message)"
    }

    Write-InstallResult `
        -Path $ResultFile `
        -Success $false `
        -Message $resultMessage `
        -InstalledVersion $Version `
        -LocalMCPUrl "http://127.0.0.1:$Port/mcp" `
        -PublicMCPUrl '' `
        -BearerToken '' `
        -OAuthLoginPassword '' `
        -HealthStatus 'failed' `
        -PrivilegeMode $effectivePrivilegeMode `
        -ErrorCode $resultErrorCode `
        -ErrorRecord $resultErrorRecord
    throw $installError
} finally {
    if ($DeleteTunnelTokenFile -and -not [string]::IsNullOrWhiteSpace($TunnelTokenFile)) {
        Remove-Item -LiteralPath $TunnelTokenFile -Force -ErrorAction SilentlyContinue
    }
    if (-not $preserveRecoveryFiles) {
        Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
