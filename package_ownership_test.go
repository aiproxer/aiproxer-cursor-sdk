package cursorsdk_test

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// requirePowerShell skips a case that cannot run without the PowerShell host, rather
// than failing on a machine that has none.
func requirePowerShell(tb testing.TB) {
	tb.Helper()

	if _, err := exec.LookPath("pwsh"); err != nil {
		tb.Skipf("this case needs the PowerShell host: %v", err)
	}
}

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
		require.NotNil(t, got.writableBeyondOwner, "mode %s was evaluated, so it has an answer", tc.mode)
		require.Equal(t, tc.writableBeyondOwner, *got.writableBeyondOwner,
			"mode %s: the check has to agree with %v", tc.mode, tc.writableBeyondOwner)
		// The reported mode is what an operator reads, so it has to be the octal
		// permission bits rather than an enum rendering or a decimal number.
		require.Equal(t, tc.mode, got.rendered, "mode %s must be rendered as its own octal digits", tc.mode)
	}

	// The verdict the verifier acts on has to come from reading the real directory,
	// not only from a mode value the test supplies.
	if runtime.GOOS == "windows" {
		// Windows exposes ownership through the directory ACL, which the host owns, so
		// there is nothing to measure. "Not machine-checkable" has to read as that:
		// reporting it as owner-only-write would be a statement no check produced.
		unmeasurable := pwshOwnershipVerdict(t, filepath.Join(t.TempDir(), "plugin root"))
		require.False(t, unmeasurable.checkable,
			"a Windows install root has no POSIX permission bits to read")
		require.Nil(t, unmeasurable.writableBeyondOwner,
			"an unmeasurable root must not report itself as checked and safe")
		require.Empty(t, unmeasurable.rendered,
			"an unmeasurable root has no permission bits to render")
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
		require.NotNil(t, verdict.writableBeyondOwner, "a measurable root has an answer")
		require.Equal(t, tc.writableBeyondOwner, *verdict.writableBeyondOwner,
			"a plugin root with mode %s was classified as owner-only writable", tc.mode)
	}
}

// TestPackageArchive_VerifierFailsOnAWritableBeyondOwnerInstallRoot keeps the
// load-bearing half of the ownership fix under test.
//
// The check above evaluates the helper; it never runs the verifier. Deleting the FAIL
// branch in scripts/verify-package.ps1 therefore left every Windows test green, and a
// Windows-only checkout proved nothing about the wiring. What has to hold is that the
// verifier reads the verdict at all: a root that is writable beyond its owner becomes a
// finding that names the remedy, and a root that is owner-only writable is reported as
// such instead.
//
// The assertion is the difference between the two verdicts, not the exit code of
// either. The substituted root is deliberately empty, so both runs also report every
// required archive entry as missing, and a non-zero exit proves nothing about the
// ownership branch; and the report prints the verdict as a status line either way, so
// a neutered branch still contains its own text. What a neutered branch removes is one
// finding, so the writable root has to produce exactly one finding more than the
// owner-only root, and that finding has to be the ownership one. Deleting the
// Add-Finding call in scripts/verify-package.ps1 makes the two sets identical and fails
// this test.
//
// Only the verdict is substituted. The verifier is the shipped script, copied verbatim;
// what it dot-sources is replaced by a stub. No option and no environment variable can
// reach the shipped verifier's decision, because none exists.
func TestPackageArchive_VerifierFailsOnAWritableBeyondOwnerInstallRoot(t *testing.T) {
	t.Parallel()
	requirePowerShell(t)

	cases := []struct {
		name     string
		mode     string
		writable bool
	}{
		{name: "writable_beyond_owner", mode: "0777", writable: true},
		{name: "owner_only_write", mode: "0700", writable: false},
	}

	findings := make(map[string][]string, len(cases))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "plugin install root with spaces")
			require.NoError(t, os.MkdirAll(root, 0o755))

			report, _ := runVerifyScriptImplFrom(t, "ps1", substitutedVerifierRepo(t, tc.mode, tc.writable),
				"", root, nil, nil)

			// The report has to carry the verdict in both directions, so the case that
			// passes proves the branch is driven by the verdict rather than always
			// firing.
			require.Contains(t, report, "install ownership: "+tc.mode,
				"the verifier has to report the verdict it read:\n%s", report)
			if !tc.writable {
				require.Contains(t, report, "owner-only write required",
					"an owner-only writable root is not a finding:\n%s", report)
				require.NotContains(t, report, "writable beyond its owner",
					"an owner-only writable root must not fail:\n%s", report)
			}
			findings[tc.name] = verifyFindings(report)
		})
	}

	ownerOnly, writable := findings["owner_only_write"], findings["writable_beyond_owner"]
	added := findingsOnlyIn(writable, ownerOnly)
	require.Len(t, added, 1,
		"a root writable beyond its owner has to add exactly one finding over an owner-only writable root; "+
			"the FAIL branch in scripts/verify-package.ps1 decides it.\nowner-only: %v\nwritable: %v",
		ownerOnly, writable)

	ownershipFinding := added[0]
	require.Contains(t, ownershipFinding, "verify-package: FAIL: install root",
		"the finding the verdict added has to be the verifier's own: %q", ownershipFinding)
	require.Contains(t, ownershipFinding, "is writable beyond its owner",
		"the finding has to name the verdict: %q", ownershipFinding)
	require.Contains(t, ownershipFinding, "protected plugin root",
		"the finding has to name the operator remedy: %q", ownershipFinding)
}

// findingsOnlyIn returns the findings that appear in have but not in without, so a
// verdict can be isolated from the findings both runs share. Order is not evidence:
// the verifier reports its checks in a fixed order, not in verdict order.
func findingsOnlyIn(have, without []string) []string {
	shared := make(map[string]struct{}, len(without))
	for _, finding := range without {
		shared[finding] = struct{}{}
	}
	var only []string
	for _, finding := range have {
		if _, ok := shared[finding]; !ok {
			only = append(only, finding)
		}
	}
	return only
}

// verifyFindings returns the verifier's FAIL lines, which are what decide its exit
// status. The report also prints the install-ownership verdict as a status line, so
// only these lines are evidence that a branch produced a finding.
func verifyFindings(report string) []string {
	var findings []string
	for _, raw := range strings.Split(report, "\n") {
		if line := strings.TrimSpace(raw); strings.HasPrefix(line, "verify-package: FAIL:") {
			findings = append(findings, line)
		}
	}
	return findings
}

// substitutedVerifierRepo gives the shipped verifier a repository to resolve its own
// layout contract from, with the install-ownership library replaced.
//
// The verifier takes no repository override on purpose: the contract and release.yaml
// are inputs to its verdict, so a caller-supplied one would let the caller decide what
// an install tree is. Deciding a verdict from a temporary directory therefore means
// giving the script a temporary repository, which this builds by mirroring the sources
// the layout report reads and substituting only the ownership helper.
func substitutedVerifierRepo(tb testing.TB, mode string, writableBeyondOwner bool) string {
	tb.Helper()

	root := tb.TempDir()
	mirrorRepo(tb, repoRoot(tb), root)

	// PowerShell has no bareword boolean: an expression that reads `true` yields null
	// rather than a verdict, so a substituted library that wanted a measured answer
	// would hand the verifier none.
	stub := fmt.Sprintf(`# Substituted by TestPackageArchive_VerifierFailsOnAWritableBeyondOwnerInstallRoot.
# The verifier script is the shipped one; only the verdict it reads changes here.
function Get-InstallOwnershipVerdict([string]$Path) {
    return [pscustomobject]@{
        Checkable = $true
        Mode = $null
        Rendered = %s
        WritableBeyondOwner = $%s
    }
}
`, psq(mode), boolPowerShellLiteral(writableBeyondOwner))
	require.NoError(tb, os.WriteFile(filepath.Join(root, "scripts", "lib", "install-ownership.ps1"), []byte(stub), 0o644))
	return root
}

// mirrorRepo copies a repository without the parts a layout report never reads: the
// git history, the bridge workspace, and any installed npm tree, which is tens of
// megabytes of symlinks. Everything else is copied, so a packaging command that grows
// an import of another internal package still compiles here rather than failing the
// case for a reason of its own.
func mirrorRepo(tb testing.TB, src, dst string) {
	tb.Helper()

	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		if mirroredAway(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFileMode(path, target)
	})
	require.NoError(tb, err)
}

// mirroredAway reports the top-level and nested directories a mirror leaves out.
func mirroredAway(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if parts[0] == ".git" || parts[0] == "dist" || parts[0] == "bridge-node" {
		return true
	}
	return slices.Contains(parts, "node_modules")
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
//
// writableBeyondOwner is a tri-state, because the helper's is: nil is "not
// machine-checkable here", which is a different statement from a measured false.
type ownershipVerdict struct {
	rendered            string
	writableBeyondOwner *bool
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
			writableBeyondOwner: &writable,
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
# The helper reports an unmeasurable root as $null, which has to survive as its own
# answer rather than arriving as a measured false.
$writable = if ($null -eq $verdict.WritableBeyondOwner) { 'unknown' } else { "$($verdict.WritableBeyondOwner)" }
'{0}|{1}|{2}' -f $verdict.Checkable, $verdict.Rendered, $writable`))
	if err != nil {
		tb.Fatalf("the PowerShell install-ownership check failed for %s:\n%s\n%s", path, out, err)
	}
	fields := strings.Split(strings.TrimSpace(out), "|")
	require.Len(tb, fields, 3, "unexpected ownership verdict %q", out)
	checkable, convErr := strconv.ParseBool(strings.TrimSpace(fields[0]))
	require.NoError(tb, convErr, "unexpected ownership verdict %q", out)

	var writable *bool
	switch token := strings.TrimSpace(fields[2]); token {
	case "unknown":
		// No measurement, so no answer.
	case "True", "False":
		measured, parseErr := strconv.ParseBool(token)
		require.NoError(tb, parseErr, "unexpected ownership verdict %q", out)
		writable = &measured
	default:
		tb.Fatalf("unexpected ownership verdict %q", out)
	}
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

// boolPowerShellLiteral spells a Go boolean the way PowerShell spells one. PowerShell has
// no bareword boolean: an expression that reads `true` yields null rather than a verdict,
// so a substituted library that wanted a measured answer would hand the verifier none.
func boolPowerShellLiteral(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
