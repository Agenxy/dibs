// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package harnessenv

import "testing"

// Real kernel ancestry and argv, through the bridge's actual DetectContext
// entry. A cloud exec-server is not established by UUID shape or a setter.
func TestDetectCloudExecutionServerThroughRealAncestry(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"cloud", []string{"exec-server", "--remote", "https://fixture.invalid/api"}, "chatgpt-cloud-exec"},
		{"local-executor", []string{"exec-server"}, ChatGPTApp},
		{"local-app-server", []string{"app-server"}, ChatGPTApp},
		{"remote-is-not-enough", []string{"app-server", "--remote", "https://fixture.invalid/api"}, ChatGPTApp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bridge, _ := nativeAppProcessesWithArgs(t, "codex", append([]string{"--"}, tc.args...))
			got, err := DetectContext(t.Context(), bridge.PID)
			if err != nil || got != tc.want {
				t.Fatalf("measured bridge origin=%q err=%v, want %q", got, err, tc.want)
			}
		})
	}
}
