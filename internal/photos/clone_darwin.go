//go:build darwin

package photos

import "golang.org/x/sys/unix"

// cloneFile is clonefile(2): a copy-on-write clone on APFS.
func cloneFile(src, dst string) error { return unix.Clonefile(src, dst, 0) }
