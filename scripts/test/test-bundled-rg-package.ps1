#requires -Version 7.0
[CmdletBinding()]
param([Parameter(Mandatory=$true)][string] $CacheDirectory)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$prepare=Join-Path $PSScriptRoot '..\..\packaging\windows\prepare-bundled-rg.ps1'
$spec=Get-Content (Join-Path $PSScriptRoot '..\..\internal\bundledrg\windows-amd64.json') -Raw | ConvertFrom-Json
$source=Join-Path $CacheDirectory $spec.asset
if (-not(Test-Path -LiteralPath $source)) {throw 'Required official archive fixture is missing; packaging tests do not skip.'}
$root=Join-Path ([IO.Path]::GetTempPath()) ('agentdock-rg-package-test-'+[guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $root | Out-Null
# A cache-only test must never accidentally download or invoke another process.
function Invoke-WebRequest {throw 'Network is forbidden in this packaging regression.'}
function Expect-Failure([scriptblock]$Action,[string]$Message) {
    try {& $Action | Out-Null} catch {
        if ($_.Exception.Message -notlike ('*'+$Message+'*')) {throw "Unexpected failure: $($_.Exception.Message)"}
        return
    }
    throw "Expected packaging failure containing: $Message"
}
try {
    $good=Join-Path $root 'good'
    & $prepare -Destination $good -CacheDirectory $CacheDirectory | Out-Null
    & $prepare -Destination $good -VerifyOnly | Out-Null
    if ((Get-FileHash (Join-Path $good 'rg.exe')).Hash.ToLowerInvariant() -ne $spec.files[0].sha256) {throw 'Prepared executable does not match the official pin.'}
    foreach ($case in @('tampered-archive','partial-archive')) {
        $cache=Join-Path $root $case
        New-Item -ItemType Directory -Path $cache | Out-Null
        $bytes=[IO.File]::ReadAllBytes($source)
        if($case -eq 'partial-archive') {$bytes=$bytes[0..127]} else {$bytes[-1]=$bytes[-1] -bxor 1}
        [IO.File]::WriteAllBytes((Join-Path $cache $spec.asset),$bytes)
        $target=Join-Path $root ($case+'-target')
        Expect-Failure {& $prepare -Destination $target -CacheDirectory $cache} 'Cached ripgrep archive'
        if(Test-Path $target){throw 'Rejected archive produced a component directory.'}
    }
    foreach ($case in @('changed-executable','wrong-architecture','missing-licence','changed-licence')) {
        $target=Join-Path $root $case
        Copy-Item -LiteralPath $good -Destination $target -Recurse
        switch($case) {
            'changed-executable' {$path=Join-Path $target 'rg.exe'; $bytes=[IO.File]::ReadAllBytes($path); $bytes[-1]=$bytes[-1] -bxor 1; [IO.File]::WriteAllBytes($path,$bytes)}
            'wrong-architecture' {$path=Join-Path $target 'rg.exe'; $bytes=[IO.File]::ReadAllBytes($path); $offset=[BitConverter]::ToInt32($bytes,0x3c); $bytes[$offset+4]=0x64; $bytes[$offset+5]=0xaa; [IO.File]::WriteAllBytes($path,$bytes)}
            'missing-licence' {Remove-Item -LiteralPath (Join-Path $target 'LICENSE-MIT')}
            'changed-licence' {[IO.File]::WriteAllText((Join-Path $target 'COPYING'),'changed')}
        }
        $message=if($case -eq 'missing-licence') {'LICENSE-MIT'} elseif($case -eq 'changed-licence') {'Incomplete or redirected'} else {'SHA-256 mismatch'}
        Expect-Failure {& $prepare -Destination $target -VerifyOnly} $message
    }
    Write-Host 'Bundled ripgrep packaging: 8 cache-only acceptance/rejection checks passed; 0 skips.'
} finally {
    Remove-Item -LiteralPath $root -Recurse -Force
}
