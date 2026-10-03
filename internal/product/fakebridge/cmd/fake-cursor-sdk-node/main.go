// Command fake-cursor-sdk-node is a deterministic stand-in for the plugin's
// private Node runtime (packaged as private/node/node[.exe]).
//
// It reads a JSON script from the bridge entry file it is handed as its first
// argument, records the argv it actually received, and then behaves like the
// configured mode. It performs no network, npm, or Cursor SDK work, so launcher
// lifecycle tests stay hermetic and never need a JavaScript toolchain.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product/fakebridge"
)

// childFlag makes this binary re-enter as a long-lived descendant, which is how
// tests prove that a runtime descendant is reachable through the launcher's own
// process tree.
const childFlag = "--fake-node-child"

// script is the JSON contract written to the fake bridge entry file.
type script struct {
	// Mode is "echo" (forward stdin to stdout, then exit), "hold" (forward
	// stdin to stdout and keep running after stdin EOF), or "bridge" (run the
	// in-process NDJSON fake bridge).
	Mode string `json:"mode"`
	// Exit is the process exit status used by the echo mode.
	Exit int `json:"exit"`
	// ExitNow exits before forwarding anything.
	ExitNow bool `json:"exitNow"`
	// ArgvLog receives the received argv, one entry per line.
	ArgvLog string `json:"argvLog"`
	// SelfLog receives this process's own resolved executable path, which proves
	// which runtime binary actually ran.
	SelfLog string `json:"selfLog"`
	// PIDFile receives this process's own pid.
	PIDFile string `json:"pidFile"`
	// ChildPIDFile receives the pid of a spawned descendant.
	ChildPIDFile string `json:"childPidFile"`
	// SpawnChild starts a long-lived descendant before serving.
	SpawnChild bool `json:"spawnChild"`
	// FlushedFile, when set, is written by the hold mode once all of stdin has
	// been forwarded to stdout. It is the barrier a caller needs before it tears
	// the runtime down, because forwarding is otherwise invisible from outside.
	FlushedFile string `json:"flushedFile"`
}

func main() {
	args := os.Args[1:]
	if len(args) >= 2 && args[0] == childFlag {
		if err := writeLines(args[1], []string{strconv.Itoa(os.Getpid())}); err != nil {
			fmt.Fprintf(os.Stderr, "fake-node: write child pid: %v\n", err)
			os.Exit(2)
		}
		blockForever()
		return
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "fake-node: no bridge entry argument")
		os.Exit(2)
	}

	raw, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake-node: cannot read bridge entry: %v\n", err)
		os.Exit(2)
	}
	var sc script
	if err := json.Unmarshal(raw, &sc); err != nil {
		fmt.Fprintf(os.Stderr, "fake-node: invalid bridge entry: %v\n", err)
		os.Exit(2)
	}
	if sc.ArgvLog != "" {
		if err := writeLines(sc.ArgvLog, append([]string{args[0]}, args[1:]...)); err != nil {
			fmt.Fprintf(os.Stderr, "fake-node: write argv log: %v\n", err)
			os.Exit(2)
		}
	}
	if sc.SelfLog != "" {
		self, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake-node: resolve own path: %v\n", err)
			os.Exit(2)
		}
		if err := writeLines(sc.SelfLog, []string{self}); err != nil {
			fmt.Fprintf(os.Stderr, "fake-node: write self log: %v\n", err)
			os.Exit(2)
		}
	}
	if sc.PIDFile != "" {
		if err := writeLines(sc.PIDFile, []string{strconv.Itoa(os.Getpid())}); err != nil {
			fmt.Fprintf(os.Stderr, "fake-node: write pid file: %v\n", err)
			os.Exit(2)
		}
	}
	if sc.SpawnChild {
		if err := spawnChild(sc.ChildPIDFile); err != nil {
			fmt.Fprintf(os.Stderr, "fake-node: spawn descendant: %v\n", err)
			os.Exit(2)
		}
	}

	if sc.ExitNow {
		os.Exit(sc.Exit)
	}
	switch sc.Mode {
	case "bridge":
		h := fakebridge.New(fakebridge.DefaultScript())
		runErr := h.Run(os.Stdin, os.Stdout)
		if text := h.StderrText(); text != "" {
			fmt.Fprintln(os.Stderr, text)
		}
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "fake-node: %v\n", runErr)
			os.Exit(1)
		}
		if code, ok := h.ExitCode(); ok {
			os.Exit(code)
		}
	case "hold":
		go func() {
			_, _ = io.Copy(os.Stdout, os.Stdin)
			// Forwarding ends at stdin EOF, after the last write reached the
			// runtime's stdout pipe. A caller that tears the runtime down needs to
			// know that happened before it starts the teardown, so the optional
			// barrier file is written here and never before.
			if sc.FlushedFile != "" {
				_ = os.WriteFile(sc.FlushedFile, []byte("flushed\n"), 0o644)
			}
		}()
		blockForever()
	case "echo":
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			fmt.Fprintf(os.Stderr, "fake-node: forward stdin: %v\n", err)
		}
	default:
		fmt.Fprintf(os.Stderr, "fake-node: unknown mode %q\n", sc.Mode)
		os.Exit(2)
	}
	os.Exit(sc.Exit)
}

// spawnChild starts a long-lived descendant of this runtime process. The
// descendant inherits this process group, exactly like a real runtime's
// auxiliary children, so a test can prove whether a tree kill reaches it.
func spawnChild(pidFile string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, childFlag, pidFile)
	if err := cmd.Start(); err != nil {
		return err
	}
	// The descendant outlives this process by design: the caller kills the
	// whole tree, and this process never waits for it.
	return nil
}

func blockForever() {
	for {
		time.Sleep(time.Hour)
	}
}

func writeLines(path string, lines []string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	return nil
}
