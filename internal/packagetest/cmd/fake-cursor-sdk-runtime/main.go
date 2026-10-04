// Command fake-cursor-sdk-runtime is a deterministic stand-in for the two
// plugin-private executables the packaging verifier probes: the private Node
// runtime it stages as private/node/node[.exe] and the bridge launcher it stages
// as private/bridge/lip-cursor-sdk-bridge[.exe].
//
// It answers the four questions the verifier asks of them and nothing else, so the
// verifier's own probe handling - the bound, the exit status, the captured output,
// and what it does with a runtime that never answers - is checkable without a
// JavaScript toolchain, a network, or an assembled archive.
//
// The role is decided by the command line, because the command line is all the
// verifier has:
//
//	-p process.execPath      the private runtime's identity probe
//	-p JSON.stringify(...)   the private runtime's component probe
//	--version                the private runtime's version probe
//	doctor                   the bridge launcher's doctor probe
//
// LIP_FAKE_PACKAGE_RUNTIME_HANG replaces that answer with a staged failure for one probe,
// which is what a replaced or wedged executable looks like from outside. Each entry is
// "<name>:<behaviour>", where the name is execpath, version, components, doctor, runtime (any of
// the three runtime probes), launcher, or any, and the behaviour is hang or fail:<status>.
//
// LIP_FAKE_PACKAGE_RUNTIME_STREAM names a file this process leaves a descendant holding its
// output stream open on. The descendant outlives this process, so the stream stays open after
// it exits: that is what a launcher leaking the runtime it owns looks like from outside.
//
// LIP_FAKE_PACKAGE_RUNTIME_TREE names a file a descendant of this process writes long after
// this process would have answered, so a caller can observe whether the tree it started was
// terminated with it.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	// fakeVersion is the private runtime version this stand-in reports.
	fakeVersion = "22.17.0"
	// descendantLife is how long a descendant this stand-in leaves behind stays alive.
	//
	// It is short on purpose. A descendant that outlives its case is a descendant that is
	// still running when the case's own files are cleaned up, and a staged executable that
	// is still running cannot be deleted on Windows - so a long-lived descendant turns a
	// passing case into a failing one for a reason that has nothing to do with the verdict.
	// It is still far longer than any probe in these cases takes, which is the only thing it
	// has to be.
	descendantLife = 6 * time.Second
	// markAfter is the verb a recording descendant runs under, and holdStream the one a
	// stream-holding descendant runs under.
	markAfter  = "mark-after"
	holdStream = "hold-stream"
)

// The four probes the verifiers ask of the two staged executables.
const (
	probeExecPath   = "execpath"
	probeVersion    = "version"
	probeComponents = "components"
	probeDoctor     = "doctor"
)

func main() {
	argv := os.Args[1:]
	// The descendant modes come first, because a descendant is this same binary and has to
	// be able to run without doing any of what the probes ask of the staged executable.
	switch {
	case len(argv) == 3 && argv[0] == markAfter:
		recordLongevity(argv[1], argv[2])
	case len(argv) == 2 && argv[0] == holdStream:
		holdOpen(argv[1])
	}
	probe := probedName(argv)
	if probe == "" {
		fmt.Fprintf(os.Stderr, "fake-runtime: unsupported probe %q\n", strings.Join(argv, " "))
		os.Exit(2)
	}

	// Whatever this process was asked to leave behind is started before it answers, so a
	// staged failure leaves the same wreckage behind it as an answered probe would. A case
	// that names no marker gets no descendant, so nothing is left running behind a verdict.
	if staged := stagedFailure(probe); staged != nil {
		if marker := os.Getenv("LIP_FAKE_PACKAGE_RUNTIME_TREE"); marker != "" {
			startDescendant(markAfter, marker, strconv.Itoa(int(descendantLife/time.Second)))
		}
		staged()
		return
	}
	// An answered probe leaves only the descendant a case asked for by name, because a
	// case that is about what the verifier terminates has no use for one on a probe that
	// answered and exited cleanly.
	if marker := os.Getenv("LIP_FAKE_PACKAGE_RUNTIME_STREAM"); marker != "" {
		startDescendant(holdStream, marker)
	}
	answerProbe(probe)
}

// probedName names which of the four probes this process was asked, which is what both the
// staged failure and the answer are keyed on. It is the only thing the verifier addresses these
// two executables by.
func probedName(argv []string) string {
	switch {
	case len(argv) == 2 && argv[0] == "-p" && argv[1] == "process.execPath":
		return probeExecPath
	case len(argv) == 2 && argv[0] == "-p" && argv[1] == "JSON.stringify(process.versions)":
		return probeComponents
	case len(argv) == 1 && argv[0] == "--version":
		return probeVersion
	case len(argv) == 1 && argv[0] == "doctor":
		return probeDoctor
	default:
		return ""
	}
}

// stagedFailure is the answer the environment asked this probe to give instead of the real one.
// A nil result means this probe answers for itself.
//
// Each entry is "<name>:<behaviour>". The name matches the probe itself, or runtime for any of
// the private runtime's three, launcher for the bridge launcher, or any for all four.
func stagedFailure(probe string) func() {
	for _, entry := range strings.Split(os.Getenv("LIP_FAKE_PACKAGE_RUNTIME_HANG"), ",") {
		name, behaviour, ok := strings.Cut(entry, ":")
		if !ok || !probeNameMatches(name, probe) {
			continue
		}
		switch {
		case behaviour == "hang":
			return hang
		case strings.HasPrefix(behaviour, "fail:"):
			return failing(behaviour)
		default:
			return func() {
				fmt.Fprintf(os.Stderr, "fake-runtime: unknown staged failure %q\n", entry)
				os.Exit(2)
			}
		}
	}
	return nil
}

// probeNameMatches reports whether a staged-failure entry applies to the probe being answered.
func probeNameMatches(name, probe string) bool {
	switch name {
	case "any":
		return true
	case "runtime":
		return probe == probeExecPath || probe == probeVersion || probe == probeComponents
	case "launcher":
		return probe == probeDoctor
	default:
		return name == probe
	}
}

// hang answers nothing and never exits, which is what a replaced or wedged private runtime
// looks like from outside.
func hang() {
	time.Sleep(time.Hour)
	os.Exit(0)
}

// failing answers with a diagnostic and exits with the staged status, so a caller can see
// both the reason the executable gave and the status it left behind.
func failing(behaviour string) func() {
	status, err := strconv.Atoi(strings.TrimPrefix(behaviour, "fail:"))
	return func() {
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake-runtime: %s is not an exit status\n", behaviour)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "fake-runtime: staged failure")
		os.Exit(status)
	}
}

// answerProbe answers the probe the verifier asked.
func answerProbe(probe string) {
	switch probe {
	case probeExecPath:
		self, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake-runtime: resolve own path: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(self)
	case probeVersion:
		fmt.Println("v" + fakeVersion)
	case probeComponents:
		// The component names the verifier reports, in the shape it reads them out of:
		// one JSON object of name/value pairs.
		fmt.Printf(`{"node":%q,"acorn":"8.14.0","uv":"1.51.0","zlib":"1.3.0.1-motley",`+
			`"openssl":"3.0.15+quic","modules":"127","icu":"75.1"}`+"\n", fakeVersion)
	case probeDoctor:
		fmt.Println("doctor: ok")
	}
	os.Exit(0)
}

// startDescendant starts a long-lived descendant of this process. It outlives this process by
// design, and it keeps this process's output streams open, because a caller that terminates
// only the process it started would leave it running, which is what recording itself reports.
func startDescendant(mode string, args ...string) {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake-runtime: resolve own path: %v\n", err)
		os.Exit(1)
	}
	child := exec.Command(self, append([]string{mode}, args...)...)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "fake-runtime: start descendant: %v\n", err)
		os.Exit(1)
	}
}

// holdOpen keeps this process's output stream open for longer than any caller would wait, and
// records that it got there.
func holdOpen(marker string) {
	_ = os.WriteFile(marker, []byte("stream held\n"), 0o600)
	time.Sleep(descendantLife)
	os.Exit(0)
}

// recordLongevity writes the marker once the delay has passed, which is only reached by a
// descendant that outlived every process that could have terminated it.
func recordLongevity(marker, seconds string) {
	wait, err := strconv.Atoi(seconds)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake-runtime: %s is not a number of seconds\n", seconds)
		os.Exit(2)
	}
	time.Sleep(time.Duration(wait) * time.Second)
	_ = os.WriteFile(marker, []byte("descendant survived\n"), 0o600)
	os.Exit(0)
}
