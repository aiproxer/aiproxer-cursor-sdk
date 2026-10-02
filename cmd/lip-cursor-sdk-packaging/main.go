// Command lip-cursor-sdk-packaging is the plugin's build-time packaging
// contract reporter and metadata renderer.
//
// scripts/package-plugin.{sh,ps1} and scripts/verify-package.{sh,ps1} drive it
// instead of restating the archive layout, the closed host manifest, or the
// release metadata shape. internal/packagelayout stays the single layout
// contract; this command is only its reporting and rendering surface. Without
// it the scripts would carry a second copy of the layout, and JSON surgery in two
// different shells would be the second copy of the manifest.
//
// Nothing in a released archive and nothing on the plugin-private run-time path
// depends on this command. It is never shipped and never invoked at run time.
package main

import (
	"flag"
	"fmt"
	"os"
)

// reportSchema versions the JSON layout report. The packaging scripts key off
// the fields, not off prose.
const reportSchema = "golip.cursorsdk.package.layout/v1"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch verb := os.Args[1]; verb {
	case "layout":
		err = runLayout(os.Args[2:])
	case "render":
		err = runRender(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "lip-cursor-sdk-packaging: unknown verb %q\n", verb)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: lip-cursor-sdk-packaging <layout|render> [flags]

  layout   print the archive layout of a platform as a JSON report
  render   write the host manifest and compatibility metadata for a staged
           install root, deriving every value from the staged tree

`)
}

// newFlagSet builds a flag set that reports a caller error instead of exiting, so
// both verbs fail with a diagnosable message.
func newFlagSet(verb string) *flag.FlagSet {
	fs := flag.NewFlagSet("lip-cursor-sdk-packaging "+verb, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// reportError prefixes a verb failure with the tool name so a packaging script
// log names the failing step.
func reportError(verb string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("lip-cursor-sdk-packaging %s: %w", verb, err)
}
