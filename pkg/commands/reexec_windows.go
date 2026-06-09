//go:build windows

package commands

import "fmt"

// reexecSelf is unsupported on Windows: there's no exec-replace syscall,
// and veil's self-update doesn't target Windows anyway (updateTarget
// rejects it before this is reached). Defined so the package still builds
// for GOOS=windows.
func reexecSelf(path string, args []string) error {
	return fmt.Errorf("re-exec after auto-update is not supported on Windows; re-run the command after `veil update`")
}
