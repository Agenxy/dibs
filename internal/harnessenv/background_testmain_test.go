package harnessenv

import (
	"fmt"
	"os"
	"testing"
)

// Keep every package test away from the person's real desktop lock while
// retaining the production lock resolution, acquisition and fallback decisions.
func TestMain(m *testing.M) {
	cache, err := os.MkdirTemp("", "dibs-background-cache-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	backgroundPairCacheDir = func() (string, error) { return cache, nil }
	code := m.Run()
	_ = os.RemoveAll(cache)
	os.Exit(code)
}
