// Command lip-cursor-sdk-bridge is the plugin-private Cursor SDK bridge
// launcher.
//
// The connector spawns it as a direct executable next to the installed plugin,
// and it starts the packaged private Node runtime over the bridge entrypoint
// using only fixed plugin-private paths relative to its own location. It never
// runs a shell, npm, or any other package manager, never downloads anything, and
// never looks a runtime up on PATH or through the current working directory.
//
// It forwards the bridge protocol streams and the exit status unchanged, and it
// owns the runtime descendant it creates: termination is forwarded, the
// descendant is reaped, and repeated cleanup is idempotent.
//
// A missing private runtime or bridge entry is an explicit prerequisite failure.
package main

import (
	"context"
	"fmt"
	"os"
)

const (
	// exitOK is a clean runtime exit.
	exitOK = 0
	// exitPrerequisite reports an incomplete plugin-private runtime layout
	// (sysexits EX_CONFIG): nothing was started.
	exitPrerequisite = 78
	// exitStartFailed reports a private runtime that could not be started
	// (sysexits EX_SOFTWARE).
	exitStartFailed = 70
)

func main() {
	os.Exit(launchMain(context.Background(), os.Args[1:], stdioSet{in: os.Stdin, out: os.Stdout, err: os.Stderr}))
}

// launchMain is the whole launcher process: resolve, own, forward, propagate,
// and release. It returns the exit status the launcher propagates.
func launchMain(ctx context.Context, args []string, stdio stdioSet) int {
	lay, err := resolvePrivateLayout()
	if err != nil {
		fmt.Fprintln(stdio.err, err)
		return exitPrerequisite
	}

	// The Cursor SDK is operator-provisioned, so an installed tree that has not been
	// provisioned cannot serve a request. Deciding that here gives the operator the
	// provisioning command instead of a module-resolution stack from inside the
	// runtime, and it starts nothing. The bridge's own check stays as defense in depth
	// for a tree that changes after this point.
	if err := preflightSDK(lay.private); err != nil {
		fmt.Fprintln(stdio.err, err)
		return exitPrerequisite
	}

	l := newLauncher(lay, args, stdio, launcherOptions{})
	stop := installTerminationHandler(l)
	defer stop()

	if err := l.Start(ctx); err != nil {
		fmt.Fprintf(stdio.err, "lip-cursor-sdk-bridge: %v\n", err)
		if closeErr := l.Close(); closeErr != nil {
			fmt.Fprintf(stdio.err, "lip-cursor-sdk-bridge: cleanup: %v\n", closeErr)
		}
		return exitStartFailed
	}

	status := l.Wait()
	if waitErr := l.ExitError(); waitErr != nil {
		fmt.Fprintf(stdio.err, "lip-cursor-sdk-bridge: private runtime exited: %v\n", waitErr)
	}
	if closeErr := l.Close(); closeErr != nil {
		fmt.Fprintf(stdio.err, "lip-cursor-sdk-bridge: cleanup: %v\n", closeErr)
	}
	return status
}
