package cursorsdk_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPackageArchive_PowerShellOwnershipCheckRejectsWritableInstallRoot keeps the
// PowerShell verifier's install-ownership check load-bearing.
//
// A plugin root any local user can write lets that user replace a checksummed
// companion after verification, which is exactly the mutation the checksum record
// exists to detect. The check compares POSIX permission bits, so it has to compare
// bit flags: an earlier version compared the rendered enum names against a digit
// pattern, and [System.IO.UnixFileMode].ToString() renders names that carry no
// digits, so the comparison never matched and a world-writable plugin root was
// reported as protected.
//
// The check cannot be exercised end to end on Windows, where the verifier defers to
// the directory ACL, so this test evaluates the decision the verifier reads out of
// the PowerShell helper against every interesting mode. It runs wherever pwsh does,
// which is what keeps the defect from coming back on a checkout that only ever runs
// the Windows script.
func TestPackageArchive_PowerShellOwnershipCheckRejectsWritableInstallRoot(t *testing.T) {
	t.Parallel()

	verdicts := pwshOwnershipVerdicts(t)
	require.NotEmpty(t, verdicts, "the PowerShell ownership check reported no mode verdicts")

	for _, tc := range installOwnershipModes {
		got, ok := verdicts[tc.mode]
		require.True(t, ok, "the PowerShell ownership check never evaluated mode %s", tc.mode)
		require.Equal(t, tc.writableBeyondOwner, got.writableBeyondOwner,
			"mode %s: the check has to agree with %v", tc.mode, tc.writableBeyondOwner)
		// The reported mode is what an operator reads, so it has to be the octal
		// permission bits rather than an enum rendering or a decimal number.
		require.Equal(t, tc.mode, got.rendered, "mode %s must be rendered as its own octal digits", tc.mode)
	}

	// The verdict the verifier acts on has to come from reading the real directory,
	// not only from a mode value the test supplies.
	if runtime.GOOS == "windows" {
		return
	}
	for _, tc := range installOwnershipModes {
		if !tc.realisticRoot {
			continue
		}
		root := filepath.Join(t.TempDir(), "plugin root")
		require.NoError(t, os.Mkdir(root, 0o700))
		require.NoError(t, os.Chmod(root, tc.perm))

		verdict := pwshOwnershipVerdict(t, root)
		require.True(t, verdict.checkable, "POSIX permission bits have to be readable for %s", root)
		require.Equal(t, tc.mode, verdict.rendered)
		require.Equal(t, tc.writableBeyondOwner, verdict.writableBeyondOwner,
			"a plugin root with mode %s was classified as owner-only writable", tc.mode)
	}
}

// installOwnershipModes is the table of permission bits the ownership check has to
// classify. Only a mode that grants write to the group or to others is unsafe;
// everything else keeps the root writable solely by its owner.
var installOwnershipModes = []struct {
	mode                string
	perm                os.FileMode
	writableBeyondOwner bool
	realisticRoot       bool
}{
	{mode: "0700", perm: 0o700, writableBeyondOwner: false, realisticRoot: true},
	{mode: "0500", perm: 0o500, writableBeyondOwner: false},
	{mode: "0755", perm: 0o755, writableBeyondOwner: false, realisticRoot: true},
	{mode: "0711", perm: 0o711, writableBeyondOwner: false},
	{mode: "0770", perm: 0o770, writableBeyondOwner: true, realisticRoot: true},
	{mode: "0720", perm: 0o720, writableBeyondOwner: true, realisticRoot: true},
	{mode: "0702", perm: 0o702, writableBeyondOwner: true, realisticRoot: true},
	{mode: "0777", perm: 0o777, writableBeyondOwner: true, realisticRoot: true},
}

// ownershipVerdict is one classification the PowerShell ownership check reported.
type ownershipVerdict struct {
	rendered            string
	writableBeyondOwner bool
	checkable           bool
}

// pwshOwnershipVerdicts evaluates the ownership check for every mode in the table
// through the PowerShell helper the verifier dot-sources.
func pwshOwnershipVerdicts(tb testing.TB) map[string]ownershipVerdict {
	tb.Helper()

	out, err := runPowerShell(tb, ownershipProbeScript(tb, `
foreach ($octal in $octals) {
    $fileMode = ConvertFrom-PermissionOctal $octal
    '{0}|{1}' -f (Format-UnixPermissionOctal $fileMode), (-not (Test-InstallOwnerOnlyWrite $fileMode))
}`))
	if err != nil {
		tb.Fatalf("the PowerShell install-ownership check failed:\n%s\n%s", out, err)
	}

	verdicts := make(map[string]ownershipVerdict)
	for _, row := range strings.Split(strings.TrimSpace(out), "\n") {
		row = strings.TrimSpace(row)
		if row == "" {
			continue
		}
		fields := strings.Split(row, "|")
		require.Len(tb, fields, 2, "unexpected ownership verdict %q", row)
		writable, convErr := strconv.ParseBool(strings.TrimSpace(fields[1]))
		require.NoError(tb, convErr, "unexpected ownership verdict %q", row)
		verdicts[strings.TrimSpace(fields[0])] = ownershipVerdict{
			rendered:            strings.TrimSpace(fields[0]),
			writableBeyondOwner: writable,
			checkable:           true,
		}
	}
	return verdicts
}

// pwshOwnershipVerdict classifies one real directory through the PowerShell helper.
func pwshOwnershipVerdict(tb testing.TB, path string) ownershipVerdict {
	tb.Helper()

	out, err := runPowerShell(tb, ownershipProbeScript(tb, `
$verdict = Get-InstallOwnershipVerdict -Path `+psq(filepath.ToSlash(path))+`
'{0}|{1}|{2}' -f $verdict.Checkable, $verdict.Rendered, $verdict.WritableBeyondOwner`))
	if err != nil {
		tb.Fatalf("the PowerShell install-ownership check failed for %s:\n%s\n%s", path, out, err)
	}
	fields := strings.Split(strings.TrimSpace(out), "|")
	require.Len(tb, fields, 3, "unexpected ownership verdict %q", out)
	checkable, convErr := strconv.ParseBool(strings.TrimSpace(fields[0]))
	require.NoError(tb, convErr, "unexpected ownership verdict %q", out)
	writable, convErr := strconv.ParseBool(strings.TrimSpace(fields[2]))
	require.NoError(tb, convErr, "unexpected ownership verdict %q", out)
	return ownershipVerdict{
		rendered:            strings.TrimSpace(fields[1]),
		writableBeyondOwner: writable,
		checkable:           checkable,
	}
}

// ownershipProbeScript wraps a probe body in what the ownership check needs: the
// dot-sourced helper, the octal to UnixFileMode conversion the table is written in,
// and the mode table itself.
func ownershipProbeScript(tb testing.TB, body string) string {
	octals := make([]string, 0, len(installOwnershipModes))
	for _, mode := range installOwnershipModes {
		// PowerShell has no octal literal, and it has no word for a permission mode
		// either, so the table is handed over as the decimal value the bits add up
		// to.
		value, err := strconv.ParseUint(mode.mode, 8, 32)
		if err != nil {
			tb.Fatalf("bad mode %s: %v", mode.mode, err)
		}
		octals = append(octals, strconv.FormatUint(value, 10))
	}
	return `
$ErrorActionPreference = 'Stop'
. ` + psq(filepath.ToSlash(pwshOwnershipLibrary(tb))) + `

function ConvertFrom-PermissionOctal([int]$value) {
    $mode = [System.IO.UnixFileMode]::None
    $flags = @(
        @{ Bit = 256; Flag = [System.IO.UnixFileMode]::UserRead },
        @{ Bit = 128; Flag = [System.IO.UnixFileMode]::UserWrite },
        @{ Bit = 64; Flag = [System.IO.UnixFileMode]::UserExecute },
        @{ Bit = 32; Flag = [System.IO.UnixFileMode]::GroupRead },
        @{ Bit = 16; Flag = [System.IO.UnixFileMode]::GroupWrite },
        @{ Bit = 8; Flag = [System.IO.UnixFileMode]::GroupExecute },
        @{ Bit = 4; Flag = [System.IO.UnixFileMode]::OtherRead },
        @{ Bit = 2; Flag = [System.IO.UnixFileMode]::OtherWrite },
        @{ Bit = 1; Flag = [System.IO.UnixFileMode]::OtherExecute })
    foreach ($flag in $flags) {
        if ($value -band $flag.Bit) { $mode = $mode -bor $flag.Flag }
    }
    return $mode
}
$octals = @(` + strings.Join(octals, ", ") + `)
` + body
}

// pwshOwnershipLibrary is the PowerShell helper that owns the install-ownership
// decision the verifier reads. It is separate from the verifier so the decision can
// be evaluated directly, including on a Windows checkout where the verifier itself
// defers to the directory ACL.
func pwshOwnershipLibrary(tb testing.TB) string {
	return filepath.Join(repoRoot(tb), "scripts", "lib", "install-ownership.ps1")
}

// runPowerShell runs a PowerShell script written to a temp file, because an inline
// -Command body would have to survive two shells' quoting rules.
func runPowerShell(tb testing.TB, body string) (string, error) {
	tb.Helper()

	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		tb.Skipf("the PowerShell verifier needs pwsh: %v", err)
	}
	script := filepath.Join(tb.TempDir(), "probe.ps1")
	require.NoError(tb, os.WriteFile(script, []byte(body), 0o600))

	cmd := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-File", script)
	cmd.Dir = repoRoot(tb)
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, runErr := cmd.CombinedOutput()
	return string(out), runErr
}

// psq quotes one path as a PowerShell single-quoted literal.
func psq(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "''") + "'"
}
