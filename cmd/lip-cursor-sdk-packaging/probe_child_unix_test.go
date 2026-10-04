//go:build !windows

package main

import (
	"syscall"
)

// detachedChildAttrs puts a descendant in a session of its own, which on POSIX takes it out of
// the process group the probe terminates. That is the escape the probe has to report rather
// than claim to clean up: a session leader is reached by no group signal.
func detachedChildAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
