//go:build windows

package main

import "syscall"

// detachedChildAttrs is Windows' answer to leaving the process tree, and it is "there is none":
// Windows exposes no process group or session a child can be moved out of, and taskkill /T
// walks process ancestry. A descendant started here is therefore reachable, which is why the
// case that stages an escape is stated as a POSIX one.
func detachedChildAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
