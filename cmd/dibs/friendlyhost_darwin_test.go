// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

func TestBridgeUsesMacFriendlyNameRatherThanNetworkKernelName(t *testing.T) {
	t.Setenv("DIBS_HOST_ID", "friendly-host-fixture")
	hostIDOnce, hostIDValue = sync.Once{}, ""
	t.Cleanup(func() { hostIDOnce, hostIDValue = sync.Once{}, "" })
	want := ""
	for _, key := range []string{"HostName", "LocalHostName", "ComputerName"} {
		out, err := exec.Command("/usr/sbin/scutil", "--get", key).Output()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			want = strings.TrimSpace(string(out))
			break
		}
	}
	kernel, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if want == "" || want == kernel {
		t.Skip("machine has no distinct friendly name; injected lookup tests cover ordering")
	}
	if got := sessionContext(false)["host"]; got != want {
		t.Fatalf("bridge host=%q want friendly %q (kernel %q)", got, want, kernel)
	}
}
