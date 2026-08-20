//go:build !unix

package main

import "errors"

// reexec is unix-only: replacing a process image in place is what keeps the
// harness's pipes attached, and there is no equivalent elsewhere. Dibs supports
// macOS and Linux (README §Platform), so this is a compile-time completeness
// stub rather than a gap.
func reexec(string, []string) error {
	return errors.New("in-place upgrade is not supported on this platform")
}
