<#
.SYNOPSIS
    Verify an assembled Cursor SDK plugin install tree.

.DESCRIPTION
    Reports the exact files of an install tree, the checksum of every one of them,
    and the private runtime metadata, and fails when the tree does not match what
    the release claims. It checks that every required archive entry exists, that
    the file set and the checksum record agree in both directions, that every
    digest matches including the plugin-private files, that the closed host
    manifest carries the plugin's identity and export posture and claims only the
    platform the archive was assembled on, and that the shipped private runtime
    and the plugin-private bridge launcher actually run and report the SDK version
    the bridge pins.

    The archive layout is not restated here. It is read from
    cmd/lip-cursor-sdk-packaging, which reports internal/packagelayout.

    The private runtime is executed by absolute path. Nothing here requires a
    node on PATH: the private-runtime variant ships its own runtime, and a
    verification that needed a global Node would prove the opposite of what it
    claims.

    The checksums cover plugin-private files, but nothing here claims the host
    authenticates them: the host's manifest digest stays the authority for the
    outer executable only.

.PARAMETER RepoRoot
    Plugin repository root. Defaults to the parent of this script's directory.

.PARAMETER PackageRoot
    Install tree to verify. Required.

.PARAMETER ReportPath
    Write the report to this file as well as to standard output.

.PARAMETER ExpectPlatform
    Platform the install tree must have been assembled for. Defaults to the
    platform recorded in the tree; a tree for another platform cannot be verified
    here because its private runtime cannot be run natively.
#>
[CmdletBinding()]
param(
    [string]$RepoRoot = '',
    [string]$PackageRoot = '',
    [string]$ReportPath = '',
    [string]$ExpectPlatform = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# The plugin module is standalone: every go invocation in this script has to run
# with the workspace switched off, or it would answer for a developer's sibling
# checkout instead of for this repository.
$env:GOWORK = 'off'

# The install-ownership decision lives in its own helper so it can be evaluated on a
# platform that exposes no POSIX permission bits, and so one implementation of the
# rule serves every host.
. (Join-Path $PSScriptRoot 'lib/install-ownership.ps1')

$script:Findings = [System.Collections.Generic.List[string]]::new()
$script:Report = [System.Collections.Generic.List[string]]::new()

function Add-Finding([string]$Message) {
    $script:Findings.Add("verify-package: FAIL: $Message")
}

function Add-Line([string]$Message) {
    $script:Report.Add($Message)
}

# UsageError reports a usage mistake and exits with the code the shell verifier uses,
# so automation can treat the two scripts interchangeably. A thrown error would exit
# with 1 and blur a usage mistake into a verification failure.
function UsageError([string]$Message) {
    [Console]::Error.WriteLine("verify-package: $Message")
    exit 2
}

# Read-FileSHA256 digests one file.
function Read-FileSHA256([string]$Path) {
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        $bytes = [System.IO.File]::ReadAllBytes($Path)
        return ([System.BitConverter]::ToString($sha.ComputeHash($bytes)) -replace '-', '').ToLowerInvariant()
    } finally {
        $sha.Dispose()
    }
}

# Get-RelativeFiles returns every file under a root as a slash-separated relative
# path. The result is wrapped in an array at the call site because PowerShell
# unrolls a returned collection, and an empty install tree must not become $null.
function Get-RelativeFiles([string]$Root) {
    $files = [System.Collections.Generic.List[string]]::new()
    foreach ($full in [System.IO.Directory]::EnumerateFiles($Root, '*', [System.IO.SearchOption]::AllDirectories)) {
        $files.Add(([System.IO.Path]::GetRelativePath($Root, $full)).Replace('\', '/'))
    }
    return $files.ToArray()
}

# Get-ReleaseScalar reads one flat scalar out of release.yaml.
function Get-ReleaseScalar([string]$ReleaseFile, [string]$Key) {
    foreach ($line in Get-Content -LiteralPath $ReleaseFile) {
        if ($line -match "^$([regex]::Escape($Key)):\s*(.+?)\s*$") { return $Matches[1] }
    }
    return ''
}

# Invoke-Probe runs one packaged executable by absolute path and captures its
# output and exit status. It is used for the private runtime and the launcher; a
# probe that cannot run is a finding, never a skip.
function Invoke-Probe([string]$FilePath, [string[]]$Arguments) {
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $lines = & $FilePath @Arguments 2>&1 | ForEach-Object { $_.ToString() }
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    return [pscustomobject]@{ ExitCode = $code; Output = ($lines -join "`n") }
}

# Get-JsonField reads one field of a JSON object without failing when it is absent.
function Get-JsonField($Record, [string]$Name) {
    $property = $Record.PSObject.Properties[$Name]
    if (-not $property) { return '' }
    return $property.Value
}

# Get-SamePath compares two filesystem paths without caring about case or the
# Windows extended-length prefix.
function Test-SamePath([string]$Left, [string]$Right) {
    $a = $Left -replace '^\\\\\?\\', ''
    $b = $Right -replace '^\\\\\?\\', ''
    if (-not $b) { return $false }
    return [string]::Equals([System.IO.Path]::GetFullPath($a), [System.IO.Path]::GetFullPath($b), [System.StringComparison]::OrdinalIgnoreCase)
}

if (-not $RepoRoot) { $RepoRoot = Split-Path -Parent $PSScriptRoot }
$RepoRoot = (Resolve-Path -LiteralPath $RepoRoot).Path
if (-not $PackageRoot) { UsageError '-PackageRoot is required' }
if (-not (Test-Path -LiteralPath $PackageRoot -PathType Container)) {
    UsageError "$PackageRoot is not an install directory"
}
$PackageRoot = (Resolve-Path -LiteralPath $PackageRoot).Path

$envParts = ((& go env GOOS GOARCH) -join ' ').Trim() -split '\s+'
$hostPlatform = "$($envParts[0])/$($envParts[1])"

# The fixed metadata file names do not vary by platform, so the host layout report
# locates them; the archive's own platform claim decides which report applies.
#
# The report has to come from the repository being verified. Asking for it relative to
# the current working directory would let the caller's directory decide what an install
# tree is: one report names the required entries, the checksum record and its
# separator, the manifest digest authority, the plugin-private prefix, and the runtime
# and launcher probes, so a directory carrying its own cmd/lip-cursor-sdk-packaging
# would make a tampered tree verify clean. -C is the switch the shell verifier's
# `cd "$repo_root"` corresponds to.
$hostLayout = (& go -C $RepoRoot run ./cmd/lip-cursor-sdk-packaging layout -platform $hostPlatform) | ConvertFrom-Json

$recordPath = Join-Path $PackageRoot $hostLayout.compatibility
$script:Record = $null
$archivePlatform = ''
if (Test-Path -LiteralPath $recordPath) {
    $script:Record = Get-Content -LiteralPath $recordPath -Raw | ConvertFrom-Json
    $archivePlatform = [string](Get-JsonField $script:Record 'platform')
}
if ($ExpectPlatform) { $archivePlatform = $ExpectPlatform }
if (-not $archivePlatform) { $archivePlatform = $hostPlatform }
if ($archivePlatform -ne $hostPlatform) {
    throw "verify-package: this install tree claims $archivePlatform but verification runs $hostPlatform; an archive can only be validated natively on its own platform"
}
$layout = $hostLayout

Add-Line "install root: $PackageRoot"

# Protected install ownership: a plugin root any local user can write lets that user
# replace a checksummed companion after verification, which is exactly the mutation
# the checksum record exists to detect. Windows exposes this through the directory
# ACL rather than through permission bits, and the host owns the plugin root's ACL,
# so this script reports the requirement rather than guessing at it.
$ownership = Get-InstallOwnershipVerdict -Path $PackageRoot
if (-not $ownership.Checkable) {
    Add-Line 'install ownership: not machine-checkable here (Windows ACL); install into a protected plugin root writable only by its owner'
} elseif ($ownership.WritableBeyondOwner) {
    Add-Finding "install root $PackageRoot is writable beyond its owner (mode $($ownership.Rendered)); reinstall into a protected plugin root so no local user can replace a checksummed companion"
    Add-Line "install ownership: $($ownership.Rendered) (FAILED: writable beyond its owner)"
} else {
    Add-Line "install ownership: $($ownership.Rendered) (owner-only write required)"
}

# 1. Required archive entries. A missing entry is an explicit prerequisite failure
#    that names the packaged location and the operator remedy.
$present = [System.Collections.Generic.List[string]]::new()
foreach ($rel in $layout.required_entries) {
    $full = Join-Path $PackageRoot ($rel -replace '/', [string][System.IO.Path]::DirectorySeparatorChar)
    if (Test-Path -LiteralPath $full) {
        $present.Add([string]$rel)
        continue
    }
    Add-Finding "required archive entry is missing: $rel; reinstall the Cursor plugin package from a complete archive"
}

# 2. Checksum record and file set. The record has to describe exactly the files
#    present: an unlisted file is unaccounted-for content, a listed file that is
#    gone is a broken install.
$checksumsPath = Join-Path $PackageRoot $layout.checksums
$listed = [ordered]@{}
$checksumOrder = [System.Collections.Generic.List[string]]::new()
if (-not (Test-Path -LiteralPath $checksumsPath)) {
    Add-Finding "checksum record is missing: $($layout.checksums); reinstall the Cursor plugin package from a complete archive"
} else {
    foreach ($line in Get-Content -LiteralPath $checksumsPath) {
        if (-not $line.Trim()) { continue }
        $parts = $line -split [regex]::Escape([string]$layout.checksum_separator), 2
        if ($parts.Count -ne 2) {
            Add-Finding "malformed checksum line: $line"
            continue
        }
        $digest = $parts[0].Trim()
        $rel = $parts[1].Trim()
        if ($digest -notmatch '^[0-9a-f]{64}$') {
            Add-Finding "malformed checksum digest for $rel"
            continue
        }
        if ($listed.Contains($rel)) {
            Add-Finding "duplicate checksum line for $rel"
            continue
        }
        $listed[$rel] = $digest
        $checksumOrder.Add($rel)
    }
}

$onDisk = @(Get-RelativeFiles $PackageRoot)
$mismatched = 0
foreach ($rel in $onDisk) {
    if ($rel -eq $layout.checksums) { continue }
    if (-not $listed.Contains($rel)) {
        Add-Finding "file is present but not listed in $($layout.checksums): $rel; the archive does not account for this file"
        continue
    }
    $actual = Read-FileSHA256 (Join-Path $PackageRoot ($rel -replace '/', [string][System.IO.Path]::DirectorySeparatorChar))
    if ($actual -ne $listed[$rel]) {
        $mismatched++
        Add-Finding "checksum mismatch for ${rel}: recorded $($listed[$rel]), found $actual; reinstall the Cursor plugin package"
    }
}
foreach ($rel in $checksumOrder) {
    if ($onDisk -notcontains $rel) {
        Add-Finding "file listed in $($layout.checksums) is missing: ${rel}; reinstall the Cursor plugin package"
    }
}

$privateCount = @($checksumOrder | Where-Object { $_.StartsWith([string]$layout.private_prefix) }).Count
$variant = if ($script:Record) { [string](Get-JsonField $script:Record 'packaging_variant') } else { '' }
$externalNode = if ($script:Record) { [bool](Get-JsonField $script:Record 'external_node_required') } else { $null }
Add-Line "plugin: $(Get-ReleaseScalar (Join-Path $RepoRoot 'release.yaml') 'plugin_id')"
Add-Line "platform: $($layout.platform) (natively assembled and validated)"
Add-Line "packaging variant: $(if ($variant) { $variant } else { '(no release metadata)' })"
Add-Line "external node required: $(if ($null -eq $externalNode) { 'unknown' } elseif ($externalNode) { 'yes' } else { 'no' })"
Add-Line "node on PATH: not required; $($layout.private_runtime) is the only runtime this tree starts"
Add-Line "files: $($onDisk.Count) present, $($listed.Count) checksummed, $mismatched checksum mismatch(es)"
Add-Line "plugin-private files checksummed: $privateCount of $($listed.Count) under $($layout.private_prefix)"
Add-Line ('checksums: {0} (sha256, <digest>{1}<install-root-relative path>)' -f $layout.checksums, $layout.checksum_separator)
Add-Line "host digest authority: $($layout.manifest) sha256 covers $($layout.outer_executable) only; nothing here claims the host authenticates companion files"

# 3. Closed host manifest: identity, export posture, and the platform claim.
$manifestPath = Join-Path $PackageRoot $layout.manifest
if (Test-Path -LiteralPath $manifestPath) {
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    $outerExe = Join-Path $PackageRoot ($layout.outer_executable -replace '/', [string][System.IO.Path]::DirectorySeparatorChar)
    $release = Join-Path $RepoRoot 'release.yaml'

    $exeRel = [string](Get-JsonField $manifest 'executable')
    if ($exeRel -ne $layout.outer_executable) {
        Add-Finding "manifest executable is ${exeRel} but this archive stages $($layout.outer_executable)"
    }
    if ((Test-Path -LiteralPath $outerExe)) {
        $actual = Read-FileSHA256 $outerExe
        if ([string](Get-JsonField $manifest 'sha256') -ne $actual) {
            Add-Finding "manifest sha256 does not match $($layout.outer_executable): the host would reject this install"
        } else {
            Add-Line "outer executable sha256: $actual"
        }
    }

    $claimed = @((Get-JsonField $manifest 'platforms') | ForEach-Object { "$((Get-JsonField $_ 'os'))/$((Get-JsonField $_ 'arch'))" })
    if ($claimed.Count -ne 1 -or $claimed[0] -ne $layout.platform) {
        Add-Finding "manifest platforms claim $($claimed -join ', ') but this archive was assembled for $($layout.platform): only the natively validated platform may be claimed"
    }

    foreach ($field in @('plugin_id', 'version', 'build_id')) {
        $manifestValue = [string](Get-JsonField $manifest $field)
        $releaseValue = Get-ReleaseScalar $release $field
        if ($releaseValue -and $manifestValue -ne $releaseValue) {
            Add-Finding "manifest ${field} is $manifestValue but release.yaml declares $releaseValue"
        }
    }

    $exports = @(Get-JsonField $manifest 'exports')
    if ($exports.Count -ne 1) {
        Add-Finding "manifest declares $($exports.Count) exports; exactly one is expected"
    } else {
        # The declared trust boundary is load-bearing: local-only access, one
        # process per instance, static credentials, and the agent runtime
        # execution class are what the host policy and the connector rely on.
        $posture = [ordered]@{
            credential_mode = 'static'
            access_scope    = 'local_only'
            process_sharing = 'per_instance'
            execution_class = 'agent_runtime'
        }
        foreach ($field in $posture.Keys) {
            $got = [string](Get-JsonField $exports[0] $field)
            if ($got -ne $posture[$field]) {
                Add-Finding "manifest export $field is '$got' but the plugin declares '$($posture[$field])'"
            }
        }
        Add-Line "export posture: $($posture.Values -join ', ')"
    }

    # The release metadata records the digest of the manifest bytes this archive carries.
    # It is recorded from the staged tree, so it is checked here rather than printed: a
    # record that describes a different manifest than the one beside it is not evidence
    # about this archive, and a recorded digest nothing ever compared is provenance
    # decoration. A digest the record does not carry reads as not recorded, which is what
    # the shell verifier's json_digest decides too.
    $recordedManifest = if ($script:Record) { [string](Get-JsonField $script:Record 'manifest_sha256') } else { '' }
    if ($recordedManifest -match '^[0-9a-f]{64}$') {
        $actual = Read-FileSHA256 $manifestPath
        if ($recordedManifest -ne $actual) {
            Add-Finding "release metadata records manifest_sha256 $recordedManifest but the staged $($layout.manifest) hashes to $actual; the record does not describe this archive"
        } else {
            Add-Line "manifest sha256 (recorded, matches): $actual"
        }
    }
}

# 4. Release metadata, and the private runtime and bridge that actually run.
if ($script:Record) {
    $record = $script:Record
    $sdkVersion = [string](Get-JsonField $record 'cursor_sdk_version')
    $pinnedVersion = [string](Get-JsonField $record 'cursor_sdk_pinned_version')
    if ($pinnedVersion -and $sdkVersion -and $sdkVersion -ne $pinnedVersion) {
        Add-Finding "release metadata records SDK $sdkVersion but the bridge pins $pinnedVersion"
    }
    Add-Line "sdk version (staged production tree): $sdkVersion (pinned $pinnedVersion)"
    Add-Line "node engine required: $(Get-JsonField $record 'bridge_node_engine')"
    Add-Line "private runtime source: $(Get-JsonField $record 'private_runtime_source')"
    Add-Line "tested host artifact sha256: $(if (Get-JsonField $record 'tested_host_artifact_sha256') { Get-JsonField $record 'tested_host_artifact_sha256' } else { '(not certified in this archive)' })"

    $privateRuntime = Join-Path $PackageRoot ($layout.private_runtime -replace '/', [string][System.IO.Path]::DirectorySeparatorChar)
    $launcher = Join-Path $PackageRoot ($layout.launcher -replace '/', [string][System.IO.Path]::DirectorySeparatorChar)

    # The same applies to the runtime digest: it is recorded from the staged executable,
    # so it is compared against that executable rather than reported as provenance.
    $recordedRuntime = [string](Get-JsonField $record 'private_runtime_sha256')
    if ($recordedRuntime -match '^[0-9a-f]{64}$' -and (Test-Path -LiteralPath $privateRuntime)) {
        $actual = Read-FileSHA256 $privateRuntime
        if ($recordedRuntime -ne $actual) {
            Add-Finding "release metadata records private_runtime_sha256 $recordedRuntime but the staged $($layout.private_runtime) hashes to $actual; the record does not describe this archive"
        } else {
            Add-Line "private runtime sha256 (recorded, matches): $actual"
        }
    }

    if ((Test-Path -LiteralPath $privateRuntime) -and (Test-Path -LiteralPath $launcher)) {
        $selfProbe = Invoke-Probe $privateRuntime @('-p', 'process.execPath')
        $resolved = if (Test-SamePath $selfProbe.Output.Trim() $privateRuntime) { 'yes' } else { 'no' }
        Add-Line "private runtime: $privateRuntime"
        Add-Line "private runtime resolves to itself: $resolved"
        if ($resolved -ne 'yes') {
            Add-Finding "the staged private runtime resolved to '$($selfProbe.Output.Trim())' instead of $privateRuntime"
        }
        $versionProbe = Invoke-Probe $privateRuntime @('--version')
        if ($versionProbe.ExitCode -ne 0) {
            Add-Finding "the staged private runtime did not run: $($versionProbe.Output)"
        }
        Add-Line "private runtime version: $($versionProbe.Output.Trim()) (recorded $(Get-JsonField $record 'private_runtime_version'))"
        $versions = Invoke-Probe $privateRuntime @('-p', 'JSON.stringify(process.versions)')
        if ($versions.ExitCode -eq 0) {
            $bundled = ($versions.Output | ConvertFrom-Json).PSObject.Properties |
                Where-Object { $_.Name -in @('node', 'icu', 'openssl', 'uv', 'zlib') } |
                Sort-Object Name | ForEach-Object { "$($_.Name)=$($_.Value)" }
            Add-Line "private runtime bundled components: $($bundled -join ' ')"
        }

        # The launcher starts the packaged runtime and runs the bridge's own doctor,
        # which resolves the SDK version from the staged production dependency tree.
        $doctor = Invoke-Probe $launcher @('doctor')
        if ($doctor.ExitCode -ne 0) {
            Add-Finding "the private bridge launcher did not pass doctor through the private runtime: $($doctor.Output)"
        }
        Add-Line "bridge doctor (launcher -> private runtime -> bridge entry): $($doctor.Output.Trim())"
    } else {
        Add-Finding "the private runtime or the private bridge launcher is missing, so no runtime metadata could be probed"
    }
}

# The notice count is a collection count: under Set-StrictMode a scalar result has no
# Count property, so a license directory holding fewer than two notices threw here and
# discarded the whole report, findings included.
Add-Line "licenses: $(@(Get-ChildItem -LiteralPath (Join-Path $PackageRoot $layout.licenses_dir) -File -ErrorAction SilentlyContinue).Count) notices under $($layout.licenses_dir)/"
Add-Line '--- files ---'
foreach ($rel in $checksumOrder) {
    Add-Line "$($listed[$rel])$($layout.checksum_separator)$rel"
}
Add-Line '--- end of files ---'

$lines = $script:Report -join "`n"
if ($ReportPath) {
    Set-Content -LiteralPath $ReportPath -Value $lines -Encoding utf8NoBOM
}
Write-Output $lines
foreach ($finding in $script:Findings) { Write-Output $finding }
if ($script:Findings.Count -gt 0) {
    Write-Output "verify-package: $($script:Findings.Count) finding(s); the install tree does not match the release it claims"
    exit 1
}
Write-Output 'verify-package: ok'
exit 0
