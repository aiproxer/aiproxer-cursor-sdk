<#
.SYNOPSIS
    Verify an assembled Cursor SDK plugin install tree.

.DESCRIPTION
    Reports the exact files of an install tree, the checksum of every shipped one of
    them, and the private runtime metadata, and fails when the tree does not match
    what the release claims. It checks that every required archive entry exists, that
    the shipped file set and the checksum record agree in both directions, that every
    shipped digest matches including the plugin-private files, that the closed host
    manifest carries the plugin's identity and export posture and claims only the
    platform the archive was assembled on, and that the shipped private runtime and
    the plugin-private bridge launcher actually run.

    The Cursor SDK is proprietary and is not redistributed, so an installed tree
    carries it only after the operator provisions it against the runtime the archive
    ships. Two tree states decide what a run means, and both are checked in full:

      -TreeState installed (default) an installed tree. The operator-provisioned
        dependency tree is outside the shipped checksum record - the plugin
        authenticates what it ships, the operator authenticates what they provisioned -
        so its files are neither "present but not listed" nor digested here. What has
        to hold instead is the SDK requirement: the tree resolves the pinned SDK
        version, or the run fails with the exact provisioning command.

      -TreeState shipped a released archive nobody has provisioned yet. It must contain
        no third-party package code at all, because shipping the SDK or its dependency
        closure would assert a redistribution right nobody has verified.

    The archive layout is not restated here. It is read from
    cmd/lip-cursor-sdk-packaging, which reports internal/packagelayout.

    The private runtime is executed by absolute path. Nothing here requires a
    node on PATH: the private-runtime variant ships its own runtime, and a
    verification that needed a global Node would prove the opposite of what it
    claims.

    The checksums cover the shipped plugin-private files, but nothing here claims the
    host authenticates them: the host's manifest digest stays the authority for the
    outer executable only.

.PARAMETER PackageRoot
    Install tree to verify. Required.

.PARAMETER ReportPath
    Write the report to this file as well as to standard output.

.PARAMETER ExpectPlatform
    Platform the install tree must have been assembled for. Defaults to the
    platform recorded in the tree; a tree for another platform cannot be verified
    here because its private runtime cannot be run natively.

.PARAMETER TreeState
    installed (default) or shipped. See the description above.
#>
[CmdletBinding()]
param(
    [string]$PackageRoot = '',
    [string]$ReportPath = '',
    [string]$ExpectPlatform = '',
    [string]$TreeState = ''
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

# Get-PackageVersion reads the version one package manifest declares. It reads the file
# rather than importing the package: the fact is the metadata, and importing the SDK is
# the thing the provisioning check exists to avoid.
function Get-PackageVersion([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return '' }
    try {
        return [string](Get-JsonField (Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json) 'version')
    } catch {
        return ''
    }
}

# Get-PinnedSDKVersion reads the exact SDK version the shipped bridge manifest pins.
function Get-PinnedSDKVersion([string]$Path, [string]$PackageName) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return '' }
    $dependencies = Get-JsonField (Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json) 'dependencies'
    if (-not $dependencies) { return '' }
    return [string](Get-JsonField $dependencies $PackageName)
}

# Get-SamePath compares two filesystem paths without caring about case or the
# Windows extended-length prefix.
function Test-SamePath([string]$Left, [string]$Right) {
    $a = $Left -replace '^\\\\\?\\', ''
    $b = $Right -replace '^\\\\\?\\', ''
    if (-not $b) { return $false }
    return [string]::Equals([System.IO.Path]::GetFullPath($a), [System.IO.Path]::GetFullPath($b), [System.StringComparison]::OrdinalIgnoreCase)
}

# The repository is the parent of this script's directory, and there is no way to
# substitute another one. The layout contract and release.yaml below are inputs to the
# verdict, so an option that replaced them would let whoever passed it decide what an
# install tree is - the same hole as asking the working directory, with a switch. The
# verifier ships inside the repository it needs (this script, release.yaml and
# cmd/lip-cursor-sdk-packaging), so there is no caller the default does not already
# serve; a tree that lives outside a checkout is what -PackageRoot is for.
$RepoRoot = (Resolve-Path -LiteralPath (Split-Path -Parent $PSScriptRoot)).Path
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

# The tree states are part of the layout contract too, so this script does not spell a
# state of its own: an unrecognized request is a usage error naming the states the
# archive contract knows, so automation can tell it from a verification failure.
if (-not $TreeState) { $TreeState = 'installed' }
$knownStates = @($hostLayout.tree_states)
if ($knownStates -notcontains $TreeState) {
    UsageError "-TreeState $TreeState is not one of: $($knownStates -join ', ')"
}
$shippedTree = $TreeState -eq 'shipped'

Add-Line "install root: $PackageRoot"
Add-Line "tree state: $TreeState"

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

# 2. Checksum record and file set. The record covers shipped files only, so the shipped
#    side still has to agree in both directions - an unlisted shipped file is
#    unaccounted-for content, a listed file that is gone is a broken install - while the
#    operator-provisioned tree is explicitly outside it: the plugin authenticates what
#    it ships and the operator authenticates what they provisioned. The provisioned
#    prefix is the whole of that scope, and it comes from the layout contract.
$checksumsPath = Join-Path $PackageRoot $layout.checksums
$listed = [ordered]@{}
$checksumOrder = [System.Collections.Generic.List[string]]::new()
$provisionedPrefix = [string]$layout.provisioned_prefix
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
        # A record line for a provisioned file would claim the plugin authenticated the
        # operator's own npm resolution, which is the one claim the record scope does
        # not make.
        if ($rel.StartsWith($provisionedPrefix, [System.StringComparison]::Ordinal)) {
            Add-Finding "checksum record lists the operator-provisioned ${rel}: $($layout.checksums) covers shipped files only; the plugin authenticates what it ships, the operator authenticates what they provisioned"
        }
        $listed[$rel] = $digest
        $checksumOrder.Add($rel)
    }
}

$onDisk = @(Get-RelativeFiles $PackageRoot)
$mismatched = 0
$provisionedCount = 0
foreach ($rel in $onDisk) {
    if ($rel -eq $layout.checksums) { continue }
    if ($rel.StartsWith($provisionedPrefix, [System.StringComparison]::Ordinal)) {
        # Outside the shipped record by design, so it is counted and reported rather
        # than digested. In a shipped archive its presence at all is the finding: the
        # archive must contain no third-party package code.
        $provisionedCount++
        if ($shippedTree) {
            Add-Finding "shipped archive contains the third-party package code ${rel}: the Cursor SDK is not redistributed, so the archive ships no $($layout.bridge_modules); the operator provisions it against the shipped runtime with: $($layout.sdk_provision_command)"
        }
        continue
    }
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
    if ($rel.StartsWith($provisionedPrefix, [System.StringComparison]::Ordinal)) { continue }
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
Add-Line "files: $($onDisk.Count) present ($($onDisk.Count - $provisionedCount) shipped, $provisionedCount operator-provisioned), $($listed.Count) checksummed, $mismatched checksum mismatch(es)"
Add-Line "plugin-private files checksummed: $privateCount of $($listed.Count) under $($layout.private_prefix)"
Add-Line ('checksums: {0} (sha256, <digest>{1}<install-root-relative path>)' -f $layout.checksums, $layout.checksum_separator)
Add-Line "checksum record scope: shipped files only; $provisionedPrefix is operator-provisioned and outside the record, so the plugin authenticates what it ships and the operator authenticates what they provisioned"
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

# 4. The SDK requirement, the release metadata, and the private runtime and bridge that
#    actually run.
#
#    The shipped bridge manifest is the authority for the pinned SDK version: the
#    release metadata is cross-checked against it rather than trusted on its own, because
#    the manifest is a shipped file the record cannot contradict. What the tree has to
#    resolve is the operator's own installation, which is outside the shipped record, so
#    it is checked as a requirement and never as a digest.
$sdkPackageName = [string]$layout.sdk_package_name
$requiredVersion = Get-PinnedSDKVersion (Join-Path $PackageRoot $layout.bridge_package_json) $sdkPackageName
if (-not $requiredVersion) {
    Add-Finding "the shipped $($layout.bridge_package_json) pins no $sdkPackageName version, so no SDK requirement can be verified; reinstall the Cursor plugin package from a complete archive"
    $requiredVersion = '(unknown)'
}
$provisionedVersion = Get-PackageVersion (Join-Path $PackageRoot $layout.sdk_package_json)

if ($script:Record) {
    $record = $script:Record
    $recordedRequired = [string](Get-JsonField $record 'cursor_sdk_required_version')
    if ($recordedRequired -and $recordedRequired -ne $requiredVersion) {
        Add-Finding "release metadata records cursor_sdk_required_version $recordedRequired but the shipped $($layout.bridge_package_json) pins $requiredVersion; the record does not describe this archive"
    }
    if ([bool](Get-JsonField $record 'cursor_sdk_bundled')) {
        Add-Finding "release metadata records cursor_sdk_bundled true: the Cursor SDK is proprietary and is not redistributed, so no archive bundles it"
    }
    Add-Line "node engine required: $(Get-JsonField $record 'bridge_node_engine')"
    Add-Line "private runtime source: $(Get-JsonField $record 'private_runtime_source')"
    Add-Line "tested host artifact sha256: $(if (Get-JsonField $record 'tested_host_artifact_sha256') { Get-JsonField $record 'tested_host_artifact_sha256' } else { '(not certified in this archive)' })"
}

Add-Line "sdk provisioning command: $($layout.sdk_provision_command)"
if ($shippedTree) {
    Add-Line "sdk: not redistributed, required $requiredVersion at run time, provisioned: no (operator-provisioned; $($layout.private_npm_cli) ships so the command above needs no global npm)"
} elseif (-not $provisionedVersion) {
    Add-Finding "the Cursor SDK is not provisioned: $($layout.sdk_package_json) not found in this installed tree, and this archive ships no $sdkPackageName; required version $requiredVersion. Provision it once with: $($layout.sdk_provision_command)"
} elseif ($provisionedVersion -ne $requiredVersion) {
    Add-Finding "the provisioned Cursor SDK is $provisionedVersion but the shipped $($layout.bridge_package_json) pins $requiredVersion; provision the pinned version with: $($layout.sdk_provision_command)"
} else {
    Add-Line "sdk: required $requiredVersion, provisioned $provisionedVersion"
}

if ($script:Record) {
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
        # which resolves the installed SDK. That is an installed-tree question: on a
        # shipped archive the bridge cannot serve a request until the operator provisions
        # it, and the SDK requirement above is the finding that says so.
        if ($shippedTree) {
            Add-Line "bridge doctor: not run; a shipped archive cannot serve a request until the operator provisions $($layout.bridge_modules)"
        } else {
            $doctor = Invoke-Probe $launcher @('doctor')
            if ($doctor.ExitCode -ne 0) {
                Add-Finding "the private bridge launcher did not pass doctor through the private runtime: $($doctor.Output)"
            }
            Add-Line "bridge doctor (launcher -> private runtime -> bridge entry): $($doctor.Output.Trim())"
        }
    } else {
        Add-Finding "the private runtime or the private bridge launcher is missing, so no runtime metadata could be probed"
    }
}

# The notice count is a collection count: under Set-StrictMode a scalar result has no
# Count property, so a license directory holding fewer than two notices threw here and
# discarded the whole report, findings included.
#
# An empty directory is a finding, not a reported count of zero. The archive is
# required to carry the runtime's license and provenance notices; the packager fails
# without them, and a verifier that only prints how many it found would report a tree
# carrying none as evidence that it looked.
$licenseCount = @(Get-ChildItem -LiteralPath (Join-Path $PackageRoot $layout.licenses_dir) -File -ErrorAction SilentlyContinue).Count
Add-Line "licenses: $licenseCount notices under $($layout.licenses_dir)/"
if ($licenseCount -eq 0) {
    Add-Finding "$($layout.licenses_dir) carries no license or provenance notice; an archive that redistributes a private Node runtime has to ship its notices"
}
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
