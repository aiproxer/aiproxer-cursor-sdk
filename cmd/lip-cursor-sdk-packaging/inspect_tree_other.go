//go:build !linux

package main

import "os"

// Windows ACLs are not machine-checked; other platforms fail closed.
func ownerUID(os.FileInfo) (uint32, bool) { return 0, false }
