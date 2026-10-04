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

      -TreeState shipped a released archive nobody has provisioned yet. It must carry
        no Cursor SDK and nothing that exists only to satisfy it, because shipping the
        SDK or its dependency closure would assert a redistribution right nobody has
        verified. The scope of that rule is the Cursor SDK and its closure, not the
        whole tree: the archive does ship third-party package code, the private runtime's
        own bundled npm, and that is expected rather than a finding.

    The archive layout is not restated here. It is read from
    cmd/lip-cursor-sdk-packaging, which reports internal/packagelayout.

    The private runtime is executed by absolute path. Nothing here requires a
    node on PATH: the private-runtime variant ships its own runtime, and a
    verification that needed a global Node would prove the opposite of what it
    claims.

    Every one of those executions is bounded. The staged runtime and the staged launcher
    are executables this project ships but cannot vouch for at verification time, so one
    that never answers has to end the run with a finding naming it rather than leave the
    gate reporting nothing at all. The bound lives in
    cmd/lip-cursor-sdk-packaging, not here: how long a shipped executable is given to
    answer is part of the verdict, and an input to the verdict is not an input to a
    script. Giving up on the launcher takes the runtime it owns with it, so a bounded wait
    never trades a hang for a leak.

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

# Join-TreePath builds a path under the install root from a name the tree supplied -
# a checksum record entry, or a name read back off disk.
#
# It is deliberately not Join-Path. Join-Path is a provider cmdlet, and a provider path
# on POSIX normalises a backslash into a separator, so a shipped file whose name contains
# one - a backslash is an ordinary character in a POSIX file name - is rewritten into a
# path through a directory that does not exist. The record then disagrees with the tree in
# both directions at once: the real file reads as unlisted, and the listed one as missing.
# [System.IO.Path]::Combine joins in the platform's own API and leaves every byte of the
# name alone, which is the same reason the separator rewrite in Get-RelativeFiles is
# restricted to Windows.
function Join-TreePath([string]$Root, [string]$Rel) {
    return [System.IO.Path]::Combine($Root, $Rel)
}

# Get-RelativeFiles returns every file under a root as a slash-separated relative
# path. The result is wrapped in an array at the call site because PowerShell
# unrolls a returned collection, and an empty install tree must not become $null.
#
# The separator is rewritten only where it is a separator. A backslash is an ordinary
# character in a POSIX file name, and the record spells every path with forward
# slashes - so rewriting one on POSIX turns a shipped file called "f\g" into a path
# through a directory called "f", which is a different file that the tree does not have.
# The reported name then disagrees with the record in both directions at once: the real
# file reads as unlisted, and the listed one as missing. GetRelativePath already uses the
# platform separator, so there is nothing to rewrite anywhere but Windows.
function Get-RelativeFiles([string]$Root) {
    $files = [System.Collections.Generic.List[string]]::new()
    $rewriteSeparator = [System.IO.Path]::DirectorySeparatorChar -eq '\'
    foreach ($full in [System.IO.Directory]::EnumerateFiles($Root, '*', [System.IO.SearchOption]::AllDirectories)) {
        $rel = [System.IO.Path]::GetRelativePath($Root, $full)
        if ($rewriteSeparator) { $rel = $rel.Replace('\', '/') }
        $files.Add($rel)
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

# Invoke-Probe runs one packaged executable by absolute path under a bound the packaging
# tool owns, and captures what it said and what it did. It is used for the private runtime and
# the launcher; a probe that cannot run is a finding, never a skip.
#
# Subject names what is being probed. It is used only when the executable left its output open,
# because that is a statement about something the verifier cannot clean up and has to name.
#
# The bound is neither an argument here nor an option of this script. How long a staged
# executable is given to answer is part of the verdict, so the tool holds the bound and reports
# the one it applied, and a finding quotes that number rather than one written down here that
# could drift away from the bound that was enforced.
function Invoke-Probe([string]$Subject, [string]$FilePath, [string[]]$Arguments) {
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        # The probed command's own output is the probe's stdout and the probe's report is its
        # stderr, so the two are captured apart: merging them would fold the report into an
        # answer this script is about to print into a verdict line.
        $statusPath = Join-Path $script:ToolDir 'probe-status'
        $output = (& $script:PackagingTool probe $FilePath @Arguments 2> $statusPath) -join "`n"
        $status = [string](Get-Content -LiteralPath $statusPath -Raw)
    } finally {
        $ErrorActionPreference = $previous
    }

    # An executable that settled but left its output open left a process behind holding the
    # stream this run was reading. Terminating the tree is what released it where the tree could
    # be reached; a descendant that left the tree cannot be reached from here at all, so what
    # happened is reported rather than described as a cleanup.
    $drained = (Get-ProbeField $status 'drained') -eq 'true'
    if (-not $drained) {
        Add-Finding "$Subject left its output open when the probe gave up on it after $(Get-ProbeField $status 'deadline') (probe cleanup incomplete); a descendant that leaves the staged executable's process tree cannot be terminated here, so its remaining output was abandoned"
    }

    return [pscustomobject]@{
        Output   = $output
        ExitCode = Get-ProbeField $status 'exit'
        TimedOut = (Get-ProbeField $status 'timed_out') -eq 'true'
        Drained  = $drained
        Deadline = Get-ProbeField $status 'deadline'
    }
}

# Get-ProbeField reads one field of the probe status line, which is a fixed set of
# key=value tokens. The statuses stay strings so a line that carries none of them reads as
# the absence of an answer rather than as a successful one.
function Get-ProbeField([string]$Status, [string]$Name) {
    foreach ($token in ($Status -split '\s+')) {
        if (-not $token) { continue }
        $parts = $token -split '=', 2
        if ($parts.Count -eq 2 -and $parts[0] -eq $Name) { return $parts[1] }
    }
    return ''
}

# New-ToolDir creates the directory this run's private copy of the packaging tool is built
# into. It is a fresh directory per run on purpose: the tool is compiled from the repository
# being verified, so a copy left behind by an earlier run could answer for a different tree.
function New-ToolDir {
    $dir = Join-Path ([System.IO.Path]::GetTempPath()) ('lip-cursor-sdk-packaging-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $dir -Force | Out-Null
    return $dir
}

# Remove-ToolDir discards this run's private copy of the packaging tool.
function Remove-ToolDir {
    if ($script:ToolDir -and (Test-Path -LiteralPath $script:ToolDir)) {
        Remove-Item -LiteralPath $script:ToolDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# Get-JsonField reads one field of a JSON object without failing when it is absent.
function Get-JsonField($Record, [string]$Name) {
    $property = $Record.PSObject.Properties[$Name]
    if (-not $property) { return '' }
    return $property.Value
}

# Get-RecordList reads one array field of the release record as strings, so a list the
# packager recorded can be reported one element at a time instead of as raw JSON. An
# absent or empty array reads as no elements, which is how a caller tells "nothing
# recorded" from "no such field".
function Get-RecordList($Record, [string]$Name) {
    $value = Get-JsonField $Record $Name
    if (-not $value) { return @() }
    return @($value | ForEach-Object { [string]$_ })
}

# Add-RecordList reports each element of a record's array field, prefixed, or one report
# line when the field holds nothing.
function Add-RecordList($Record, [string]$Name, [string]$Prefix, [string]$Fallback) {
    $elements = @(Get-RecordList $Record $Name)
    if ($elements.Count -eq 0) {
        Add-Line $Fallback
        return
    }
    foreach ($element in $elements) {
        Add-Line "$Prefix$element"
    }
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
    # A side that was never reported is not the current directory. A bounded probe of a
    # runtime that never answers reports nothing at all, and resolving that against the
    # process's working directory would answer for wherever this script happens to run.
    if (-not $a -or -not $b) { return $false }
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

# The packaging tool answers the layout report and owns every probe bound, so it is built
# once here and used directly. `go run` would compile it again for each question, and a
# probe this script drove itself would be a probe whose bound the script could also choose.
$script:ToolDir = New-ToolDir
$packagingTool = Join-Path $script:ToolDir 'lip-cursor-sdk-packaging'
if ($envParts[0] -eq 'windows') { $packagingTool += '.exe' }
& go -C $RepoRoot build -o $packagingTool ./cmd/lip-cursor-sdk-packaging
if ($LASTEXITCODE -ne 0) {
    Remove-ToolDir
    throw "verify-package: go build ./cmd/lip-cursor-sdk-packaging exited $LASTEXITCODE"
}
$script:PackagingTool = $packagingTool

# Everything from here to the end of this script runs with the tool in place, and the tool is
# this run's private copy of it. The finally is what discards that copy on every path out
# that unwinds - a missing prerequisite, a malformed record, an unexpected failure - because a
# verifier that leaves a compiled executable in the temporary directory has made that
# directory part of what a later run can find. The two explicit Remove-ToolDir calls at the
# bottom cover what finally does not: PowerShell does not run a finally block for exit.
try {

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
$hostLayout = (& $packagingTool layout -platform $hostPlatform) | ConvertFrom-Json

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
    $full = Join-TreePath $PackageRoot ($rel -replace '/', [string][System.IO.Path]::DirectorySeparatorChar)
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
# Two different lookups answer two different questions here, and only one of them was linear.
#
# The record's own loop asks, per listed path, whether the tree still has it, and -contains
# scanned the whole $onDisk array every time: that is the quadratic one, and a set answers it
# once per path. Its comparer is the case-insensitive one -contains already used, because on a
# case-insensitive filesystem a difference in case is not a different path and reporting one
# would be a false alarm rather than a broken install.
#
# The other side - the file's own digest against the record - already went through $listed,
# which is an ordered dictionary and so a keyed lookup rather than a scan. Nothing about it is
# quadratic, and this set does not change that.
$onDiskSet = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
foreach ($onDiskRel in $onDisk) { [void]$onDiskSet.Add($onDiskRel) }

$mismatched = 0
$provisionedCount = 0
foreach ($rel in $onDisk) {
    if ($rel -eq $layout.checksums) { continue }
    if ($rel.StartsWith($provisionedPrefix, [System.StringComparison]::Ordinal)) {
        # Outside the shipped record by design, so it is counted and reported rather
        # than digested. In a shipped archive its presence at all is the finding: the
        # archive must carry no Cursor SDK and nothing that exists only to satisfy it.
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
    $actual = Read-FileSHA256 (Join-TreePath $PackageRoot ($rel -replace '/', [string][System.IO.Path]::DirectorySeparatorChar))
    if ($actual -ne $listed[$rel]) {
        $mismatched++
        Add-Finding "checksum mismatch for ${rel}: recorded $($listed[$rel]), found $actual; reinstall the Cursor plugin package"
    }
}
foreach ($rel in $checksumOrder) {
    if ($rel.StartsWith($provisionedPrefix, [System.StringComparison]::Ordinal)) { continue }
    if (-not $onDiskSet.Contains($rel)) {
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
    $outerExe = Join-TreePath $PackageRoot ($layout.outer_executable -replace '/', [string][System.IO.Path]::DirectorySeparatorChar)
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

    # Source identity, host contract pins, platform evidence, and the certification
    # posture are printed as the record states them, including when they are absent. An
    # auditor has to be able to read an archive's evidence off this report instead of
    # inferring it from a build log, and an absent fact has to read as absent rather than
    # as a line this script chose not to print. These are the same statements the shell
    # verifier prints, in the same wording, so a reader of either report reads the same
    # verdict.
    $sourceRevision = [string](Get-JsonField $record 'source_revision')
    Add-Line "source revision: $(if ($sourceRevision) { $sourceRevision } else { 'not established; this record names no source revision' })"
    Add-Line "source stamp evidence: $(Get-JsonField $record 'source_stamp_evidence')"
    # The recorded cleanliness is tri-state and the three answers stay three answers: a
    # dirty tree, a clean tree, and a state nothing established. An absent field is the
    # third one, and reporting it as "no" would turn a missing measurement into a clean
    # build claim in the one place an operator would read it as one.
    $sourceModifiedProperty = $record.PSObject.Properties['source_modified']
    if ($sourceModifiedProperty -and $sourceModifiedProperty.Value -eq $true) {
        Add-Line 'source modified: yes (built from a work tree with uncommitted changes)'
    } elseif ($sourceModifiedProperty -and $sourceModifiedProperty.Value -eq $false) {
        Add-Line 'source modified: no (the build tree had no uncommitted changes)'
    } else {
        Add-Line 'source modified: unknown (not established: no source identity in this record, or the build tree state could not be read)'
    }
    Add-Line "host contracts pinned: $(Get-JsonField $record 'published_root_module') $(Get-JsonField $record 'host_contract_root_version'), $(Get-JsonField $record 'published_acp_module') $(Get-JsonField $record 'host_contract_acp_version')"
    Add-RecordList $record 'declared_platforms' 'declared platform: ' 'declared platforms: none recorded'
    Add-RecordList $record 'declared_platforms_not_assembled' 'declared platform this archive is not evidence for: ' 'declared platforms this archive is not evidence for: none; this archive is the only declared platform'

    # The certification posture is printed with the reason an uncertified record carries,
    # and the artifacts a certified one names. An uncertified record with no reason is a
    # finding rather than a printed blank, and a certified record that names no artifact
    # is one too: either state with nothing behind it is a claim the record cannot support.
    $certification = [string](Get-JsonField $record 'host_certification_state')
    $certificationReason = [string](Get-JsonField $record 'host_certification_reason')
    $artifacts = @(Get-RecordList $record 'tested_host_artifacts')
    switch ($certification) {
        'certified' {
            Add-Line 'host certification: certified against the host artifacts listed below'
            if ($artifacts.Count -eq 0) {
                Add-Finding 'release metadata declares host_certification certified but records no tested_host_artifacts; a certification with no artifact behind it is a claim, not evidence'
            }
        }
        'uncertified' {
            Add-Line 'host certification: uncertified (no host artifact was certified against)'
            if (-not $certificationReason) {
                Add-Finding 'release metadata declares host_certification uncertified with no host_certification_reason; an uncertified record has to say why'
            }
        }
        '' {
            Add-Finding 'release metadata records no host_certification_state; an archive has to declare whether it was certified against a host artifact'
        }
        default {
            Add-Finding "release metadata declares host certification '$certification'; the postures are certified and uncertified"
        }
    }
    if ($certificationReason) {
        Add-Line "host certification reason: $certificationReason"
    }
    if ($artifacts.Count -eq 0) {
        Add-Line 'tested host artifacts: none recorded'
    } else {
        foreach ($artifact in $artifacts) {
            Add-Line "tested host artifact sha256: $artifact"
        }
    }

    # What the record says about verification of this package. The packager writes the
    # record before verification can run, so the recorded state is not-performed and the
    # runs that have to be performed are named instead. This report is the evidence those
    # runs produce; a passing run never rewrites the record to claim it passed.
    $performed = if ([bool](Get-JsonField $record 'package_verification_performed')) { 'yes' } else { 'no' }
    Add-Line "package verification recorded in this archive: $(Get-JsonField $record 'package_verification_state') (performed: $performed)"
    Add-Line "package verification shipped-tree run: $(Get-JsonField $record 'package_verification_shipped_command')"
    Add-Line "package verification installed-tree run: $(Get-JsonField $record 'package_verification_installed_command')"
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
    $privateRuntime = Join-TreePath $PackageRoot ($layout.private_runtime -replace '/', [string][System.IO.Path]::DirectorySeparatorChar)
    $launcher = Join-TreePath $PackageRoot ($layout.launcher -replace '/', [string][System.IO.Path]::DirectorySeparatorChar)

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
        $selfProbe = Invoke-Probe 'the staged private runtime' $privateRuntime @('-p', 'process.execPath')
        $resolved = if (Test-SamePath $selfProbe.Output.Trim() $privateRuntime) { 'yes' } else { 'no' }
        Add-Line "private runtime: $privateRuntime"
        Add-Line "private runtime resolves to itself: $resolved"
        if ($selfProbe.TimedOut) {
            # A runtime that never answers has not resolved to itself; saying so would
            # report the symptom, while the bound it broke is what an operator acts on.
            Add-Finding "the staged private runtime did not answer its own path within $($selfProbe.Deadline) (probe timed out); a runtime that never answers is not a working private runtime"
        } elseif ($resolved -ne 'yes') {
            Add-Finding "the staged private runtime resolved to '$($selfProbe.Output.Trim())' instead of $privateRuntime"
        }
        $versionProbe = Invoke-Probe 'the staged private runtime' $privateRuntime @('--version')
        if ($versionProbe.TimedOut) {
            Add-Finding "the staged private runtime did not answer --version within $($versionProbe.Deadline) (probe timed out); a runtime that never answers is not a working private runtime"
        } elseif ($versionProbe.ExitCode -ne '0') {
            Add-Finding "the staged private runtime did not run: $($versionProbe.Output)"
        }
        Add-Line "private runtime version: $($versionProbe.Output.Trim()) (recorded $(Get-JsonField $record 'private_runtime_version'))"
        $versions = Invoke-Probe 'the staged private runtime' $privateRuntime @('-p', 'JSON.stringify(process.versions)')
        if ($versions.TimedOut) {
            Add-Finding "the staged private runtime did not report its bundled components within $($versions.Deadline) (probe timed out)"
        }
        # A staged executable is not vouched for, so its answer to the component probe is read
        # as data and not as something that can be assumed to parse. A runtime that answered
        # with anything but a component list is a finding, not a verifier that stops with a
        # parser error and reports nothing at all.
        #
        # "A component list" means what the probe asked for: a JSON object, from a command
        # that answered successfully. Any other JSON value parses just as cleanly and is just
        # as useless as a component report, and the other verifier says so too - the rule is
        # stated once, in the same words, on both sides.
        $components = $null
        if ($versions.ExitCode -eq '0' -and ([string]$versions.Output).Trim()) {
            try { $components = $versions.Output | ConvertFrom-Json } catch { $components = $null }
        }
        if ($components -is [pscustomobject]) {
            $bundled = $components.PSObject.Properties |
                Where-Object { $_.Name -in @('node', 'icu', 'openssl', 'uv', 'zlib') } |
                Sort-Object Name | ForEach-Object { "$($_.Name)=$($_.Value)" }
            Add-Line "private runtime bundled components: $($bundled -join ' ')"
        } elseif (-not $versions.TimedOut) {
            Add-Finding "the staged private runtime answered the bundled-components probe with $($versions.Output) rather than a component list; an executable that cannot report what it carries is not a working private runtime"
        }

        # The launcher starts the packaged runtime and runs the bridge's own doctor, which
        # resolves the installed SDK. That is an installed-tree question: on a shipped
        # archive the bridge cannot serve a request until the operator provisions it,
        # and the SDK requirement above is the finding that says so.
        if ($shippedTree) {
            Add-Line "bridge doctor: not run; a shipped archive cannot serve a request until the operator provisions $($layout.bridge_modules)"
        } else {
            $doctor = Invoke-Probe 'the private bridge launcher' $launcher @('doctor')
            if ($doctor.TimedOut) {
                Add-Finding "the private bridge launcher did not pass doctor through the private runtime within $($doctor.Deadline) (probe timed out); a launcher that never answers has no runtime behind it"
            } elseif ($doctor.ExitCode -ne '0') {
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
} finally {
    Remove-ToolDir
}

Write-Output $lines
foreach ($finding in $script:Findings) { Write-Output $finding }
if ($script:Findings.Count -gt 0) {
    Write-Output "verify-package: $($script:Findings.Count) finding(s); the install tree does not match the release it claims"
    exit 1
}
Write-Output 'verify-package: ok'
exit 0
