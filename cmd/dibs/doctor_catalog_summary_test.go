// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/procexec"
)

func TestCatalogDoctorProcess(_ *testing.T) {
	if os.Getenv("DIBS_TEST_CATALOG_DOCTOR_HELPER") != "1" {
		return
	}
	_, _ = fmt.Fprintln(os.Stdout, "READY")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestDoctorGroupsUnknownBridgeCatalogs(t *testing.T) {
	start := harnessenv.ProcessStart(os.Getpid())
	if start == "" {
		t.Skip("process incarnation inventory unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	cmd := procexec.Command(ctx, os.Args[0], "-test.run=^TestCatalogDoctorProcess$")
	cmd.Env = append(os.Environ(), "DIBS_TEST_CATALOG_DOCTOR_HELPER=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); cancel(); _ = cmd.Wait() })
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "READY\n" {
		t.Fatalf("setup: child not ready: %q %v", line, err)
	}
	childStart := harnessenv.ProcessStart(cmd.Process.Pid)
	if childStart == "" {
		t.Fatal("setup: ready child has no process incarnation")
	}
	b := &boardView{HostID: hostID(), BridgeToolCatalogs: []engine.BridgeToolCatalog{
		{PID: os.Getpid(), StartedAt: start, CurrentHash: "current", Agents: []string{"alpha", "shared"}},
		{PID: cmd.Process.Pid, StartedAt: childStart, CurrentHash: "current", Agents: []string{"beta", "shared"}},
	}}
	var warnings []string
	checkBridgeToolCatalogs(b, func(what, fix string) { warnings = append(warnings, what+" "+fix) })
	if len(warnings) != 1 {
		t.Fatalf("unknown catalogs must produce one actionable summary, got %v", warnings)
	}
	for _, text := range []string{"2 connected session bridges", "alpha", "beta", "shared", "clears as these bridges restart onto this build"} {
		if !strings.Contains(warnings[0], text) {
			t.Errorf("summary lacks %q: %s", text, warnings[0])
		}
	}
	if strings.Count(warnings[0], "shared") != 1 {
		t.Fatal("shared agent label repeated in summary")
	}
	if strings.Contains(warnings[0], "start a new chat") {
		t.Fatal("pre-catalog bridges presented as a manual chat-restart task")
	}
}
