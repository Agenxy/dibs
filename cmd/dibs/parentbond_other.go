//go:build !darwin && !linux

package main

import "context"

// watchParent falls back to noticing reparenting. Dibs supports macOS and Linux
// (README §Platform); this keeps the guarantee meaningful anywhere else it is
// built rather than silently absent.
func watchParent(ctx context.Context, ppid int) { pollParent(ctx, ppid) }
