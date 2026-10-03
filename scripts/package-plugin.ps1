<#
.SYNOPSIS
    Assemble the plugin's native release archive and its installable tree.

.DESCRIPTION
    Builds the outer plugin executable and the plugin-private bridge launcher for
    the host platform, builds the production JavaScript out of the source tree,
    stages the metadata the bridge needs to resolve and verify the SDK version - the
    package manifest and the lockfile that pins it - plus the private Node runtime
    together with that runtime's own bundled npm, renders the closed host manifest and
    the release metadata, records a checksum over every archive file including the
    plugin-private ones, and emits a per-platform archive with its own digest.

    The archive stages NO Cursor SDK package code. The Cursor SDK is proprietary and is
    not redistributed, so neither it nor its dependency closure is shipped: the operator
    provisions that tree once, out of band, with the runtime this archive carries. The
    archive does ship third-party package code - the private runtime's own bundled npm
    and the dependencies npm bundles with it - because the provisioning command has to run
    without a global Node or a global package manager. That code is MIT and its notices
    are collected from the staged runtime's own LICENSE into the archive's notice directory.

    The archive layout is not restated here. It is read from
    cmd/lip-cursor-sdk-packaging, which reports internal/packagelayout, so the
    packager, the verifier, and the private launcher cannot disagree about it.

    npm, Node, and Go are build-time tools here. Nothing the archive needs at run
    time is looked up on PATH, through a shell, or through a package manager.

    The archive is assembled for this machine's platform only. Cross-compiling a
    platform and claiming it would be an unvalidated claim, so -Platform refuses
    anything but the host platform.

.PARAMETER RepoRoot
    Plugin repository root. Defaults to the parent of this script's directory.

.PARAMETER OutDir
    Directory that receives the install tree and the archive. Defaults to
    <RepoRoot>/dist.

.PARAMETER NodeDist
    Official Node distribution zip or extracted directory to stage as the private
    runtime. Its license notice ships with the archive. Preferred over a runtime
    picked from PATH because a distribution is the artifact whose provenance is
    known.

.PARAMETER NodeRuntime
    Private Node runtime executable to stage when no distribution is given. Its
    LICENSE is collected from the same directory or a parent of it. Defaults to
    LIP_PACKAGE_NODE_RUNTIME, then to the node on PATH at build time.

.PARAMETER Platform
    os/arch to assemble. Must be the host platform.
#>
[CmdletBinding()]
param(
    [string]$RepoRoot = '',
    [string]$OutDir = '',
    [string]$NodeDist = '',
    [string]$NodeRuntime = '',
    [string]$Platform = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# The plugin module is standalone: every go invocation in this script has to run with
# the workspace switched off, or it would answer for a developer's sibling checkout
# instead of for this repository. It is set before the first one, because a build id,
# a layout report, or a rendered manifest produced inside a workspace is not this
# repository's.
$env:GOWORK = 'off'

# Fail aborts packaging with a diagnosable message.
function Fail([string]$Message) {
    throw "package-plugin: $Message"
}

# Invoke-Tool runs one build-time tool and captures its combined output and exit
# status. Stderr is merged so a tool reporting progress there stays visible in the
# packaging log, and a non-zero status becomes a packaging failure.
function Invoke-Tool {
    param(
        [Parameter(Mandatory = $true)][string]$Command,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [string]$WorkingDirectory = ''
    )

    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        if ($WorkingDirectory) { Push-Location -LiteralPath $WorkingDirectory }
        try {
            $lines = & $Command @Arguments 2>&1 | ForEach-Object { $_.ToString() }
            $code = $LASTEXITCODE
        } finally {
            if ($WorkingDirectory) { Pop-Location }
        }
    } finally {
        $ErrorActionPreference = $previous
    }

    $output = ($lines -join "`n")
    if ($code -ne 0) {
        Fail "$Command $($Arguments -join ' ') exited $code`n$output"
    }
    return $output
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

# Get-FileSHA256Map digests every file under a root, keyed by the slash-separated
# install-root-relative path. Reading the bytes directly keeps a tree with
# thousands of files practical.
function Get-FileSHA256Map([string]$Root, [string]$ExcludeRelative) {
    $map = [ordered]@{}
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        foreach ($full in [System.IO.Directory]::EnumerateFiles($Root, '*', [System.IO.SearchOption]::AllDirectories)) {
            $rel = [System.IO.Path]::GetRelativePath($Root, $full).Replace('\', '/')
            if ($rel -eq $ExcludeRelative) { continue }
            $bytes = [System.IO.File]::ReadAllBytes($full)
            $map[$rel] = ([System.BitConverter]::ToString($sha.ComputeHash($bytes)) -replace '-', '').ToLowerInvariant()
        }
    } finally {
        $sha.Dispose()
    }
    return $map
}

# Get-ReleaseScalar reads one flat scalar out of release.yaml. The file is a flat
# key set; anything richer belongs to the metadata renderer.
function Get-ReleaseScalar([string]$ReleaseFile, [string]$Key) {
    foreach ($line in Get-Content -LiteralPath $ReleaseFile) {
        if ($line -match "^$([regex]::Escape($Key)):\s*(.+?)\s*$") { return $Matches[1] }
    }
    return ''
}

# Get-LayoutReport reads the archive layout contract for one platform.
function Get-LayoutReport([string]$Repo, [string]$RequestPlatform) {
    $output = Invoke-Tool -Command 'go' `
        -Arguments @('run', './cmd/lip-cursor-sdk-packaging', 'layout', '-platform', $RequestPlatform) `
        -WorkingDirectory $Repo
    try {
        return $output | ConvertFrom-Json
    } catch {
        Fail "cannot read the archive layout report for ${RequestPlatform}: $($_.Exception.Message)"
    }
}

# Get-BridgeRelativePath reduces one bridge layout path to the path relative to the
# bridge package directory. The staged source tree uses the same relative paths, so
# deriving them here keeps the entry directory, the build output directory, and the
# modules directory named by the layout contract alone.
function Get-BridgeRelativePath($Layout, [string]$Rel) {
    $prefix = "$($Layout.bridge_package_dir)/"
    if (-not $Rel.StartsWith($prefix, [System.StringComparison]::Ordinal)) {
        Fail "layout path $Rel is not inside $($Layout.bridge_package_dir)"
    }
    return $Rel.Substring($prefix.Length)
}

# Resolve-NodeDistRoot accepts an extracted Node distribution either directly or
# under the single top-level directory an official archive unpacks into. The
# Windows distribution keeps its runtime at the root, the POSIX one in bin/.
function Resolve-NodeDistRoot([string]$Dir, [string]$RuntimeFileName) {
    $candidates = @($Dir)
    $candidates += @(Get-ChildItem -LiteralPath $Dir -Directory -Force -ErrorAction SilentlyContinue |
        ForEach-Object { $_.FullName })
    foreach ($candidate in $candidates) {
        if (Resolve-NodeDistRuntime -Root $candidate -RuntimeFileName $RuntimeFileName) { return $candidate }
    }
    Fail "no Node distribution found under $Dir"
}

# Resolve-NodeDistRuntime locates the runtime executable inside a distribution root.
function Resolve-NodeDistRuntime([string]$Root, [string]$RuntimeFileName) {
    foreach ($rel in @($RuntimeFileName, (Join-Path 'bin' $RuntimeFileName))) {
        $candidate = Join-Path $Root $rel
        if (Test-Path -LiteralPath $candidate -PathType Leaf) { return $candidate }
    }
    return ''
}

# Find-NodeLicense locates the license text that ships with a Node executable. A
# runtime staged without its notices is not releasable, so an absent license is a
# failure rather than a warning.
function Find-NodeLicense([string]$Executable) {
    $dir = Split-Path -Parent $Executable
    for ($depth = 0; $depth -lt 3; $depth++) {
        foreach ($name in @('LICENSE', 'LICENSE.txt')) {
            $candidate = Join-Path $dir $name
            if (Test-Path -LiteralPath $candidate) { return $candidate }
        }
        $parent = Split-Path -Parent $dir
        if ($parent -eq $dir) { break }
        $dir = $parent
    }
    Fail "no Node LICENSE found next to $Executable or its parents; a staged private runtime must ship its license notices"
}

# Get-JsonField reads one field of a JSON object without failing when it is absent.
function Get-JsonField($Record, [string]$Name) {
    if (-not $Record) { return '' }
    $property = $Record.PSObject.Properties[$Name]
    if (-not $property) { return '' }
    return $property.Value
}

# Resolve-PrivateRuntimeSource decides which Node runtime to stage and returns its
# executable, the license text that ships with it, a provenance label, the kind of
# source it came from, and any temporary directory the caller has to remove. The kind
# is recorded in the release metadata because the three sources are not the same claim:
# only an official distribution is a copy of a distribution, and a runtime staged from
# the build machine's PATH has to say so inside the archive.
function Resolve-PrivateRuntimeSource([string]$Dist, [string]$Runtime, [string]$RuntimeFileName) {
    if ($Dist) {
        if (-not (Test-Path -LiteralPath $Dist)) { Fail "-NodeDist $Dist does not exist" }
        $label = Split-Path -Leaf (Resolve-Path -LiteralPath $Dist)
        if ((Get-Item -LiteralPath $Dist).PSIsContainer) {
            $root = Resolve-NodeDistRoot $Dist $RuntimeFileName
            return [pscustomobject]@{
                Executable = Resolve-NodeDistRuntime -Root $root -RuntimeFileName $RuntimeFileName
                License    = Find-NodeLicense (Resolve-NodeDistRuntime -Root $root -RuntimeFileName $RuntimeFileName)
                Label      = $label
                Kind       = 'official-distribution'
                TempDir    = ''
            }
        }
        $scratch = Join-Path ([System.IO.Path]::GetTempPath()) ('node-dist-' + [guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $scratch | Out-Null
        try {
            # Each platform publishes its official distribution as a zip or a
            # tarball, so both are accepted rather than one being assumed.
            if ($Dist.EndsWith('.zip', [System.StringComparison]::OrdinalIgnoreCase)) {
                [System.IO.Compression.ZipFile]::ExtractToDirectory((Resolve-Path -LiteralPath $Dist).Path, $scratch)
            } else {
                Invoke-Tool -Command 'tar' -Arguments @('-xf', (Resolve-Path -LiteralPath $Dist).Path, '-C', $scratch) | Out-Null
            }
        } catch {
            Remove-Item -Recurse -Force -LiteralPath $scratch -ErrorAction SilentlyContinue
            throw
        }
        $root = Resolve-NodeDistRoot $scratch $RuntimeFileName
        return [pscustomobject]@{
            Executable = Resolve-NodeDistRuntime -Root $root -RuntimeFileName $RuntimeFileName
            License    = Find-NodeLicense (Resolve-NodeDistRuntime -Root $root -RuntimeFileName $RuntimeFileName)
            Label      = $label
            Kind       = 'official-distribution'
            TempDir    = $scratch
        }
    }

    $candidate = $Runtime
    $kind = 'supplied-runtime'
    if (-not $candidate) { $candidate = $env:LIP_PACKAGE_NODE_RUNTIME }
    if (-not $candidate) {
        $onPath = Get-Command -Name 'node' -CommandType Application -ErrorAction SilentlyContinue |
            Select-Object -First 1
        if (-not $onPath) {
            Fail 'no private Node runtime source: pass -NodeDist <distribution>, -NodeRuntime <executable>, or set LIP_PACKAGE_NODE_RUNTIME'
        }
        $candidate = $onPath.Source
        $kind = 'path-fallback'
    }
    if (-not (Test-Path -LiteralPath $candidate)) { Fail "private Node runtime $candidate does not exist" }
    $resolved = (Resolve-Path -LiteralPath $candidate).Path
    return [pscustomobject]@{
        Executable = $resolved
        License    = Find-NodeLicense $resolved
        Label      = Split-Path -Leaf $resolved
        Kind       = $kind
        TempDir    = ''
    }
}

# Write-LockedInventory records what the shipped lockfile pins and which half of it the
# provisioning command actually installs. The archive stages no Cursor SDK package code,
# so this is the inventory of what the operator's npm will resolve rather than of what
# the archive carries: naming the pinned versions is still evidence, and stating the
# attribution next to it is what keeps it honest.
#
# An npm lockfile carries a "" key for the project root, which ConvertFrom-Json
# rejects as a property name and only accepts as a hashtable key.
function Write-LockedInventory {
    param(
        [string]$LockFile,
        [string]$ManifestFile,
        [System.Collections.Generic.List[string]]$Lines
    )

    $lock = Get-Content -LiteralPath $LockFile -Raw | ConvertFrom-Json -AsHashtable
    $manifest = Get-Content -LiteralPath $ManifestFile -Raw | ConvertFrom-Json -AsHashtable
    $packages = $lock['packages']
    if ($null -eq $packages) { $packages = @{} }
    $root = $packages['']
    if ($null -eq $root) { $root = @{} }

    # npm resolves a dependency from the dependent's own directory and then from each
    # enclosing one, so the installed set is the closure of the root dependencies under
    # that rule. Everything else the lockfile pins is development-only and --omit=dev
    # leaves it out of an install tree.
    $depDir = Get-LockfileDependencyDir $packages
    if (-not $depDir) { throw "the lockfile pins no packages" }

    $installed = [System.Collections.Generic.HashSet[string]]::new()
    $queue = [System.Collections.Generic.Queue[object]]::new()
    foreach ($name in @(Get-ManifestNames $root)) {
        $queue.Enqueue(@('', $name))
    }
    while ($queue.Count -gt 0) {
        $entry = $queue.Dequeue()
        $from = [string]$entry[0]
        $key = Resolve-LockEntry -Packages $packages -DependencyDir $depDir -From $from -Name ([string]$entry[1])
        if (-not $key -or -not $installed.Add($key)) { continue }
        foreach ($dependency in @(Get-ManifestNames $packages[$key])) {
            $queue.Enqueue(@($key, $dependency))
        }
    }

    $write = {
        param($keys)
        $Lines.Add('| package | pinned version |')
        $Lines.Add('| --- | --- |')
        foreach ($key in @($keys | Sort-Object)) {
            $version = [string]$packages[$key]['version']
            $Lines.Add("| $key | $(if ($version) { $version } else { '(not declared)' }) |")
        }
    }
    & $write @($installed | Where-Object { $packages.ContainsKey($_) })
    $devOnly = @($packages.Keys | Where-Object { $_ -ne '' -and -not $installed.Contains($_) })
    if ($devOnly.Count -gt 0) {
        $Lines.Add('')
        $Lines.Add('- Development-only packages the lockfile pins and `--omit=dev` leaves out:')
        $Lines.Add('')
        & $write $devOnly
    }

    $overrides = $manifest['overrides']
    $named = @(if ($overrides) { @($overrides.Keys | Sort-Object) } else { @() })
    $Lines.Add('')
    if ($named.Count -eq 0) {
        $Lines.Add('- The shipped bridge manifest declares no npm overrides.')
        return
    }
$rendered = ($named | ForEach-Object { "$_ $($overrides[$_])" }) -join ', '
    # Concatenated rather than interpolated: a backtick is an escape character inside a
    # double-quoted PowerShell string, so an inline one would be swallowed.
    $Lines.Add('- Security overrides the shipped bridge manifest declares, honored through npm `overrides`: ' +
        "$rendered.")
}

# Get-LockfileDependencyDir is the dependency directory name a lockfile keys its
# packages by, read out of those keys rather than written here so this script never
# carries a second copy of the layout it stages.
function Get-LockfileDependencyDir($Packages) {
    foreach ($key in $Packages.Keys) {
        if ($key -eq '') { continue }
        return $key.Split('/')[0]
    }
    return ''
}

# Resolve-LockEntry finds the lockfile key npm would install one dependency to: the
# dependent's own dependency directory first, then each enclosing one, then the root.
# Get-ManifestNames returns the dependency and optional-dependency names one lockfile
# entry declares, as an array even when it declares none, so a caller never asks a
# missing hashtable for its keys.
function Get-ManifestNames($Entry) {
    $names = @()
    if ($null -eq $Entry) { return $names }
    if ($Entry['dependencies']) { $names += @($Entry['dependencies'].Keys) }
    if ($Entry['optionalDependencies']) { $names += @($Entry['optionalDependencies'].Keys) }
    return @($names | Where-Object { $null -ne $_ } | ForEach-Object { [string]$_ })
}

function Resolve-LockEntry {
    param(
        [hashtable]$Packages,
        [string]$DependencyDir,
        [string]$From,
        [string]$Name
    )

    $base = $From
    while ($true) {
        $candidate = if ($base) { "$base/$DependencyDir/$Name" } else { "$DependencyDir/$Name" }
        if ($Packages.ContainsKey($candidate)) { return $candidate }
        if (-not $base) { return '' }
        $cut = $base.LastIndexOf("/$DependencyDir/")
        $base = if ($cut -lt 0) { '' } else { $base.Substring(0, $cut) }
    }
}

# Write-ThirdPartyNotices records, factually, what the archive redistributes, what it
# deliberately does not, and who owns what. It states the licenses the redistributed
# components declare, it asserts no redistribution right nobody has confirmed, and it
# says plainly that the provisioned dependency tree is the operator's own resolution
# rather than this project's artifact.
function Write-ThirdPartyNotices {
    param(
        [string]$Path,
        [string]$LockFile,
        [string]$SDKPackageName,
        [string]$BridgeManifest,
        [string]$ModulesDir,
        [string]$PrivateNPMRoot,
        [string]$PrivateRuntime,
        [string]$NodeVersion,
        [string]$NodeSourceKind,
        [string]$NodeSource,
        [string]$Platform,
        [string]$ProvisionCommand
    )

    $lines = [System.Collections.Generic.List[string]]::new()
    $lines.Add('# Third-party notices')
    $lines.Add('')
    $lines.Add('This file records what the plugin archive redistributes. It is evidence, not a')
    $lines.Add('license grant: the licenses below are the ones the redistributed components')
    $lines.Add('declare, and no redistribution right is asserted here.')
    $lines.Add('')
    $lines.Add("Platform: $Platform")
    $lines.Add("Private Node runtime: $NodeVersion (source kind: $NodeSourceKind, source: $NodeSource)")
    $lines.Add('')
    $lines.Add('## Private Node runtime')
    $lines.Add('')
    if ($NodeSourceKind -ne 'official-distribution') {
        $lines.Add("- This runtime was staged from the build machine's toolchain, not from an official")
        $lines.Add('  Node distribution, and no distribution digest backs it. Re-stage with')
        $lines.Add('  -NodeDist <official archive> before publishing so the provenance of the shipped')
        $lines.Add('  runtime can be checked against the release SHASUMS256.txt.')
    }
    $lines.Add('- Node.js is MIT licensed. The distribution LICENSE staged in this archive holds')
    $lines.Add('  the Node.js license grant together with the notices for the components Node')
    $lines.Add('  bundles (ICU, OpenSSL, c-ares, libuv, and the rest). See the staged distribution LICENSE.')
    $lines.Add('- The runtime ships with its own bundled npm, and so does this archive, at')
    # ${...} rather than a bare $PrivateNPMRoot: PowerShell reads a colon straight
    # after a variable name as part of that name and rejects the reference.
    $lines.Add("  ${PrivateNPMRoot}: that is what the provisioning command below runs, so an")
    $lines.Add('  operator needs neither a global Node nor a global package manager.')
    $lines.Add('- That npm tree, together with the third-party packages npm bundles inside it, IS')
    $lines.Add('  third-party package code shipped by this archive, and it is the only such code')
    $lines.Add('  here. It is MIT licensed under the same Node distribution license staged in this')
    $lines.Add('  archive, whose notices cover those bundled components.')
    $lines.Add('- Components the staged runtime reports about itself:')
    $versions = (Invoke-Tool -Command $PrivateRuntime -Arguments @('-p', 'JSON.stringify(process.versions)')) | ConvertFrom-Json
    foreach ($entry in ($versions.PSObject.Properties | Sort-Object Name)) {
        $lines.Add("  - $($entry.Name) $($entry.Value)")
    }
    $lines.Add('')
    $lines.Add('## Cursor SDK: not redistributed, operator-provisioned')
    $lines.Add('')
    $lines.Add("- $SDKPackageName is proprietary. Its LICENSE.md states that use is subject to")
    $lines.Add("  Cursor's Terms of Service (https://cursor.com/terms-of-service) and it grants no")
    $lines.Add('  redistribution right. Its platform package additionally ships bundled native')
    $lines.Add('  binaries (rg and cursandbox) whose own license texts the package does not')
    $lines.Add('  redistribute.')
    $lines.Add("- This archive therefore contains no $SDKPackageName and none of its dependency")
    $lines.Add('  closure. The scope of that claim is the Cursor SDK and its closure: the')
    $lines.Add('  third-party package code this archive does ship is the npm tree named above, and')
    $lines.Add('  nothing else. The checksums in this archive cover the shipped files only.')
    $lines.Add("- The operator obtains the SDK themselves, accepting Cursor's terms, and")
    $lines.Add('  provisions it once with the runtime this archive ships:')
    $lines.Add('')
    $lines.Add("      $ProvisionCommand")
    $lines.Add('')
    $lines.Add('- npm specifically, not any package manager: `overrides` semantics differ')
    $lines.Add('  across package managers and the security baseline below depends on that')
    $lines.Add('  override being honored. A tree provisioned with another package manager is')
    $lines.Add('  unsupported.')
    $lines.Add('- Provisioning is an install-time operator step. The plugin never installs')
    $lines.Add('  anything, runs no package manager, and downloads nothing.')
    $lines.Add("- Provisioning writes $ModulesDir in the install tree.")
    $lines.Add('- The provisioned tree is operator-attributable: it is resolved by the')
    $lines.Add("  operator's npm from the shipped lockfile, it is outside this project's")
    $lines.Add('  checksum record, and no notice in this archive covers it.')
    $lines.Add('')
    $lines.Add('## Locked dependency closure (pinned, not shipped)')
    $lines.Add('')
    $lines.Add('- What the shipped lockfile pins for the operator to provision:')
    $lines.Add('')
    Write-LockedInventory -LockFile $LockFile -ManifestFile $BridgeManifest -Lines $lines
    $lines.Add('')
    $lines.Add('')
    $lines.Add('## Plugin')
    $lines.Add('')
    $lines.Add('- The plugin sources are MIT licensed (see the staged plugin LICENSE). Derived')
    $lines.Add('  connector code originates from an Apache-2.0 project; see PROVENANCE.md in the')
    $lines.Add('  source repository.')

    Set-Content -LiteralPath $Path -Value ($lines -join "`n") -Encoding utf8NoBOM
}

try { [System.IO.Compression.ZipFile] | Out-Null } catch {
    Add-Type -AssemblyName System.IO.Compression.FileSystem
}

$sep = [string][System.IO.Path]::DirectorySeparatorChar
if (-not $RepoRoot) { $RepoRoot = Split-Path -Parent $PSScriptRoot }
$RepoRoot = (Resolve-Path -LiteralPath $RepoRoot).Path
if (-not $OutDir) { $OutDir = Join-Path $RepoRoot 'dist' }

$envParts = (Invoke-Tool -Command 'go' -Arguments @('env', 'GOOS', 'GOARCH') -WorkingDirectory $RepoRoot) -split '\s+'
$envParts = @($envParts | Where-Object { $_ })
$hostPlatform = "$($envParts[0])/$($envParts[1])"
if ($Platform -and $Platform -ne $hostPlatform) {
    Fail "refusing to assemble $Platform on ${hostPlatform}: cross-compilation is not native validation, and an unvalidated platform claim is not a truthful one. Run this script on $Platform to produce that archive."
}

$layout = Get-LayoutReport $RepoRoot $hostPlatform
$exeSuffix = [string]$layout.exe_suffix
# The private runtime file name comes from the archive contract, so the staged
# runtime is whatever the launcher will look for on this platform.
$runtimeFileName = ([string]$layout.private_runtime).Split('/')[-1]

$releaseFile = Join-Path $RepoRoot 'release.yaml'
$pluginId = Get-ReleaseScalar $releaseFile 'plugin_id'
$version = Get-ReleaseScalar $releaseFile 'version'
$buildId = Get-ReleaseScalar $releaseFile 'build_id'
if (-not $pluginId) { Fail 'release.yaml has no plugin_id' }
if (-not $version) { Fail 'release.yaml has no version' }
if (-not $buildId) { Fail 'release.yaml has no build_id' }

# The archive name carries the platform as a filename-safe tag; the archive keeps a
# single install root at its top level so an operator unpacks one directory into the
# host plugin root.
$platformTag = ([string]$layout.platform).Replace('/', '-')
$base = "cursorsdk-$version-$platformTag"
$outRoot = [System.IO.Path]::GetFullPath($OutDir)
New-Item -ItemType Directory -Path $outRoot -Force | Out-Null
$installRoot = Join-Path $outRoot $base
$staging = Join-Path $outRoot ('.staging-' + $base)
$runtimeTemp = ''
if (Test-Path -LiteralPath $staging) { Remove-Item -Recurse -Force -LiteralPath $staging }
New-Item -ItemType Directory -Path $staging | Out-Null

try {
    # 1. Native executables. CGO off and a trimmed build id keep the outer
    #    executable reproducible across packaging runs, which is what makes the
    #    manifest digest a meaningful authority for the outer process.
    $outerExe = Join-Path $staging ($layout.outer_executable -replace '/', $sep)
    $launcher = Join-Path $staging ($layout.launcher -replace '/', $sep)
    New-Item -ItemType Directory -Path (Split-Path -Parent $outerExe) -Force | Out-Null
    New-Item -ItemType Directory -Path (Split-Path -Parent $launcher) -Force | Out-Null
    $env:CGO_ENABLED = '0'
    $outerCommand = Get-ReleaseScalar $releaseFile 'command'
    if (-not $outerCommand) { Fail 'release.yaml has no command' }
    Invoke-Tool -Command 'go' -Arguments @('build', '-trimpath', '-ldflags=-buildid=', '-o', $outerExe, $outerCommand) -WorkingDirectory $RepoRoot | Out-Null
    Invoke-Tool -Command 'go' -Arguments @('build', '-trimpath', '-ldflags=-buildid=', '-o', $launcher, ('./cmd/' + [string]$layout.launcher_name)) -WorkingDirectory $RepoRoot | Out-Null

    # 2. Production JavaScript. The dev toolchain stays in the source tree and never
    #    enters the archive; only the built output does.
    $bridgeSource = Join-Path $RepoRoot 'bridge-node'
    Invoke-Tool -Command 'npm' -Arguments @('ci', '--no-audit', '--no-fund') -WorkingDirectory $bridgeSource | Out-Null
    Invoke-Tool -Command 'npm' -Arguments @('run', 'build') -WorkingDirectory $bridgeSource | Out-Null

    # 3. Plugin-private bridge tree. The manifest and the lockfile ship because they are
    #    what the operator provisions against: the manifest pins the SDK version the
    #    bridge verifies at run time, and the lockfile pins the exact closure and the
    #    undici security override. The dependency closure itself is NOT staged - the
    #    Cursor SDK is proprietary and is not redistributed - so the archive needs no
    #    package manager of its own to run.
    $bridgeDir = Join-Path $staging ($layout.bridge_package_dir -replace '/', $sep)
    New-Item -ItemType Directory -Path $bridgeDir -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $bridgeSource (Split-Path -Leaf ([string]$layout.bridge_package_json))) `
        -Destination (Join-Path $staging ($layout.bridge_package_json -replace '/', $sep))
    Copy-Item -LiteralPath (Join-Path $bridgeSource (Split-Path -Leaf ([string]$layout.bridge_package_lock))) `
        -Destination (Join-Path $staging ($layout.bridge_package_lock -replace '/', $sep))

    $bridgeDist = Join-Path $staging ($layout.bridge_dist -replace '/', $sep)
    New-Item -ItemType Directory -Path $bridgeDist -Force | Out-Null
    $bridgeDistSource = Join-Path $bridgeSource ((Get-BridgeRelativePath $layout $layout.bridge_dist) -replace '/', $sep)
    Get-ChildItem -LiteralPath $bridgeDistSource |
        Copy-Item -Destination $bridgeDist -Recurse -Force
    $bridgeEntry = Join-Path $staging ($layout.bridge_entry -replace '/', $sep)
    New-Item -ItemType Directory -Path (Split-Path -Parent $bridgeEntry) -Force | Out-Null
    $bridgeEntrySource = Join-Path $bridgeSource ((Get-BridgeRelativePath $layout $layout.bridge_entry) -replace '/', $sep)
    Copy-Item -LiteralPath $bridgeEntrySource -Destination $bridgeEntry -Force

    # 4. Private Node runtime plus the notices that have to travel with it.
    $licensesDir = Join-Path $staging $layout.licenses_dir
    New-Item -ItemType Directory -Path $licensesDir -Force | Out-Null
    $runtimeSource = Resolve-PrivateRuntimeSource -Dist $NodeDist -Runtime $NodeRuntime -RuntimeFileName $runtimeFileName
    $runtimeTemp = $runtimeSource.TempDir
    $privateRuntime = Join-Path $staging ($layout.private_runtime -replace '/', $sep)
    New-Item -ItemType Directory -Path (Split-Path -Parent $privateRuntime) -Force | Out-Null
    Copy-Item -LiteralPath $runtimeSource.Executable -Destination $privateRuntime -Force
    # The launcher starts the staged runtime as a direct process, without a shell, so
    # the execute bit is what makes the archive serve a request at all. Windows has no
    # permission bit to grant and needs none; the same script runs under PowerShell on
    # POSIX, where it does.
    if (-not $IsWindows) {
        [System.IO.File]::SetUnixFileMode($privateRuntime,
            [System.IO.UnixFileMode]::UserRead -bor [System.IO.UnixFileMode]::UserWrite -bor
            [System.IO.UnixFileMode]::UserExecute -bor [System.IO.UnixFileMode]::GroupRead -bor
            [System.IO.UnixFileMode]::GroupExecute -bor [System.IO.UnixFileMode]::OtherRead -bor
            [System.IO.UnixFileMode]::OtherExecute)
    }
    Copy-Item -LiteralPath $runtimeSource.License -Destination (Join-Path $licensesDir 'nodejs-LICENSE') -Force
    Copy-Item -LiteralPath (Join-Path $RepoRoot 'LICENSE') -Destination (Join-Path $licensesDir 'plugin-LICENSE') -Force
    $nodeVersion = (Invoke-Tool -Command $privateRuntime -Arguments @('--version')).Trim()

    # The runtime ships with its own npm, and so does this archive. Without it the
    # provisioning command would need a global package manager, which is the
    # prerequisite the private runtime exists to remove. It is resolved from the same
    # installation the runtime came from, so an archive carries the npm that belongs to
    # its runtime rather than whatever npm happens to be nearest on the build machine.
    $npmRel = ([string]$layout.private_npm_root).Substring(([string]$layout.private_runtime_dir).Length + 1)
    $npmSource = ''
    foreach ($npmRoot in @((Split-Path -Parent $runtimeSource.Executable),
            (Split-Path -Parent (Split-Path -Parent $runtimeSource.Executable)))) {
        $candidate = Join-Path $npmRoot ($npmRel -replace '/', $sep)
        if (Test-Path -LiteralPath $candidate -PathType Container) { $npmSource = $candidate; break }
    }
    if (-not $npmSource) {
        Fail "no bundled npm found at $npmRel beside $($runtimeSource.Executable); stage an official Node distribution with -NodeDist so the archive ships the npm that provisions the Cursor SDK"
    }
    $npmTarget = Join-Path $staging ($layout.private_npm_root -replace '/', $sep)
    New-Item -ItemType Directory -Path (Split-Path -Parent $npmTarget) -Force | Out-Null
    Copy-Item -LiteralPath $npmSource -Destination $npmTarget -Recurse -Force

    # 5. Closed host manifest and release metadata, both derived from the staged
    #    tree rather than described independently of it. The renderer refuses to describe
    #    a tree carrying the Cursor SDK dependency closure, so a packager that staged one
    #    fails here rather than publishing a bundle it must not ship.
    $exeDigest = Read-FileSHA256 $outerExe
    Invoke-Tool -Command 'go' -Arguments @(
        'run', './cmd/lip-cursor-sdk-packaging', 'render',
        '-repo', $RepoRoot, '-staging', $staging, '-platform', $layout.platform,
        '-exe-sha256', $exeDigest,
        '-node-source-kind', $runtimeSource.Kind, '-node-source', $runtimeSource.Label
    ) -WorkingDirectory $RepoRoot | Out-Null

    Write-ThirdPartyNotices -Path (Join-Path $licensesDir 'THIRD-PARTY-NOTICES.md') `
        -LockFile (Join-Path $staging ($layout.bridge_package_lock -replace '/', $sep)) `
        -BridgeManifest (Join-Path $staging ($layout.bridge_package_json -replace '/', $sep)) `
        -SDKPackageName ([string]$layout.sdk_package_name) `
        -ModulesDir ([string]$layout.bridge_modules) `
        -PrivateNPMRoot ([string]$layout.private_npm_root) `
        -PrivateRuntime $privateRuntime `
        -NodeVersion $nodeVersion -NodeSourceKind $runtimeSource.Kind `
        -NodeSource $runtimeSource.Label -Platform $layout.platform `
        -ProvisionCommand ([string]$layout.sdk_provision_command)

    # 6. Checksums over every archive file, plugin-private files included. The
    #    record cannot cover itself, so it is written last. The order is ordinal by
    #    path, which is what LC_ALL=C sort produces in the shell packager: a
    #    culture-aware sort would order the record differently on the two platforms
    #    for the same tree.
    $digests = Get-FileSHA256Map -Root $staging -ExcludeRelative $layout.checksums
    $orderedPaths = [System.Collections.Generic.List[string]]::new()
    foreach ($key in $digests.Keys) { $orderedPaths.Add($key) }
    $orderedPaths.Sort([System.StringComparer]::Ordinal)
    $checksumLines = foreach ($rel in $orderedPaths) {
        "$($digests[$rel])$($layout.checksum_separator)$rel"
    }
    Set-Content -LiteralPath (Join-Path $staging $layout.checksums) -Value ($checksumLines -join "`n") -Encoding utf8NoBOM

    # 7. Publish the install tree, then the per-platform archive.
    if (Test-Path -LiteralPath $installRoot) { Remove-Item -Recurse -Force -LiteralPath $installRoot }
    Move-Item -LiteralPath $staging -Destination $installRoot

    $archivePath = Join-Path $outRoot ($base + $(if ($exeSuffix) { '.zip' } else { '.tar.gz' }))
    if (Test-Path -LiteralPath $archivePath) { Remove-Item -Force -LiteralPath $archivePath }
    if ($exeSuffix) {
        [System.IO.Compression.ZipFile]::CreateFromDirectory(
            $installRoot, $archivePath, [System.IO.Compression.CompressionLevel]::Optimal, $true)
    } else {
        $tar = Join-Path ([System.IO.Path]::GetTempPath()) ($base + '.tar')
        Invoke-Tool -Command 'tar' -Arguments @('-czf', $tar, '-C', (Split-Path -Parent $installRoot), $base) | Out-Null
        Move-Item -Force -LiteralPath $tar $archivePath
    }
    $archiveDigest = Read-FileSHA256 $archivePath
    Set-Content -LiteralPath ($archivePath + '.sha256') `
        -Value ("$archiveDigest$($layout.checksum_separator)" + (Split-Path -Leaf $archivePath)) -Encoding utf8NoBOM

    Write-Output "plugin_id: $pluginId"
    Write-Output "plugin_version: $version"
    Write-Output "build_id: $buildId"
    Write-Output "platform: $($layout.platform)"
    Write-Output "packaging_variant: private-runtime"
    Write-Output "out_dir: $outRoot"
    Write-Output "install_root: $installRoot"
    Write-Output "archive: $archivePath"
    Write-Output "archive_sha256: $archiveDigest"
    Write-Output "node_version: $nodeVersion"
    Write-Output "node_source_kind: $($runtimeSource.Kind)"
    Write-Output "node_source: $($runtimeSource.Label)"
    Write-Output "cursor_sdk_bundled: no (operator-provisioned; $($layout.sdk_package_name) is not redistributed)"
    Write-Output "sdk_provision_command: $($layout.sdk_provision_command)"
    Write-Output "file_count: $($digests.Count + 1)"
} finally {
    if (Test-Path -LiteralPath $staging) { Remove-Item -Recurse -Force -LiteralPath $staging }
    if ($runtimeTemp -and (Test-Path -LiteralPath $runtimeTemp)) {
        Remove-Item -Recurse -Force -LiteralPath $runtimeTemp -ErrorAction SilentlyContinue
    }
}
