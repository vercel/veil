//go:build !windows

package commands

import (
	"os"
	"syscall"
)

// reexecSelf replaces the current process image with the binary at path,
// invoked with the same args and the current environment. On success it
// does not return (the running process becomes the new binary); it errors
// only if the exec syscall itself fails.
func reexecSelf(path string, args []string) error {
	return syscall.Exec(path, args, os.Environ())
}
