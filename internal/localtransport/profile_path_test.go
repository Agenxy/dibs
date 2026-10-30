// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package localtransport

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmptyAndRelativeProfilesRefuseBeforeCreationOrDial(t *testing.T) {
	dir := privateDir(t)
	if err := CheckDir(dir, false); err != nil {
		t.Fatal("setup: absolute private profile rejected:", err)
	}
	t.Chdir(dir)
	for _, path := range []string{"", ".", "new-relative-profile"} {
		t.Run(path, func(t *testing.T) {
			assert := func(err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), "nonempty absolute data-directory") {
					t.Fatalf("ambiguous profile did not receive the pre-I/O corrective: %v", err)
				}
			}
			assert(CheckDir(path, true))
			conn, err := Dial(context.Background(), path)
			if conn != nil {
				_ = conn.Close()
			}
			assert(err)
			req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/mcp", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Dibs-Local", "fixture-secret-never-sent")
			resp, err := RoundTripper(path, Local, nil).RoundTrip(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			assert(err)
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "new-relative-profile")); !os.IsNotExist(err) {
		t.Fatalf("refusal created a relative profile: %v", err)
	}
}
