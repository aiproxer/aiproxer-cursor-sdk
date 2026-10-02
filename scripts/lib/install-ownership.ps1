<#
.SYNOPSIS
    Classify the ownership of a plugin install root.

.DESCRIPTION
    A plugin install root that any local user can write lets that user replace a
    checksummed companion after verification, which is exactly the mutation the
    checksum record exists to detect. This helper owns that decision so the verifier
    only has to report it.

    The decision compares POSIX permission bits as bit flags.
    [System.IO.UnixFileMode] renders as enum names ("OtherExecute, OtherWrite, ..."),
    never as digits, so a digit pattern matched against that rendering never matches
    anything and every root reads as protected. Format-UnixPermissionOctal is the
    only rendering here, and it is used for the operator-facing report, never for the
    comparison.

    The decision lives outside scripts/verify-package.ps1 so it can be evaluated on a
    platform whose filesystem exposes no permission bits at all: on Windows the
    verifier reports the ACL requirement instead of guessing at it, and the predicate
    below is still what the POSIX implementation uses.
#>

# Get-UnixDirectoryMode returns the POSIX permission bits of a directory, or $null
# when this platform does not expose them. The API arrived with .NET 6 and is
# unsupported on Windows, so either a missing member or a platform refusal reports no
# mode rather than guessing at one. The caller has already established that the
# directory exists, so nothing else can be lost here.
function Get-UnixDirectoryMode([string]$Path) {
    try {
        return [System.IO.File]::GetUnixFileMode($Path)
    } catch {
        return $null
    }
}

# Test-InstallOwnerOnlyWrite reports whether a permission mode keeps write access
# with the owner alone. Group-write or world-write is not owner-only.
function Test-InstallOwnerOnlyWrite([System.IO.UnixFileMode]$Mode) {
    if (($Mode -band [System.IO.UnixFileMode]::GroupWrite) -ne 0) { return $false }
    if (($Mode -band [System.IO.UnixFileMode]::OtherWrite) -ne 0) { return $false }
    return $true
}

# Format-UnixPermissionOctal renders permission bits the way chmod and stat -c '%a'
# do, so the report reads the same on both verifier implementations.
function Format-UnixPermissionOctal([System.IO.UnixFileMode]$Mode) {
    $value = [int]$Mode
    $groups = @(
        @{ Bits = @(@(256, 4), @(128, 2), @(64, 1)) },
        @{ Bits = @(@(32, 4), @(16, 2), @(8, 1)) },
        @{ Bits = @(@(4, 4), @(2, 2), @(1, 1)) })
    $digits = ''
    foreach ($group in $groups) {
        $digit = 0
        foreach ($bit in $group.Bits) {
            if ($value -band $bit[0]) { $digit += $bit[1] }
        }
        $digits += "$digit"
    }
    return '0' + $digits
}

# Get-InstallOwnershipVerdict classifies one install root: whether the permission
# bits are machine-checkable here, how they read, and whether the root is writable
# beyond its owner.
function Get-InstallOwnershipVerdict([string]$Path) {
    $mode = Get-UnixDirectoryMode $Path
    $checkable = $null -ne $mode
    $rendered = ''
    $writable = $false
    if ($checkable) {
        $rendered = Format-UnixPermissionOctal $mode
        $writable = -not (Test-InstallOwnerOnlyWrite $mode)
    }
    return [pscustomobject]@{
        Checkable           = $checkable
        Mode                = $mode
        Rendered            = $rendered
        WritableBeyondOwner = $writable
    }
}