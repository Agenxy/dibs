//go:build !darwin

package paths

func nativeSpelling(p string) string { return p }
