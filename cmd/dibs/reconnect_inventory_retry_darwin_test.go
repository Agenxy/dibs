// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/build"
	"github.com/agenxy/dibs/internal/procexec"
	"github.com/agenxy/dibs/internal/testexec"
)

// The private observer stalls only the daemon's second real inventory until
// its production context expires. The first establishes the old app cohort.
// Run the ORIGINAL real-bridge contract unchanged and require its actual
// reconnect receipt, rather than calling AppReconnected or a setter by hand.
func TestReconnectReceiptSurvivesOneInventoryTimeout(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	path := filepath.Join(root, "internal", "harnessenv", "harnessenv.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source = append(source, []byte(reconnectInventoryTimeoutObserver)...)
	dir := t.TempDir()
	replacement := filepath.Join(dir, "observed_harnessenv.go")
	if err := os.WriteFile(replacement, source, 0o600); err != nil {
		t.Fatal(err)
	}
	inventoryPath := filepath.Join(root, "internal", "harnessenv", "incarnation.go")
	inventorySource, err := os.ReadFile(inventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	const originalInventory = "return appProcessPass(ctx)"
	if strings.Count(string(inventorySource), originalInventory) != 1 {
		t.Fatal("setup: production ancestry inventory edge changed")
	}
	observedInventory := strings.Replace(
		string(inventorySource), originalInventory, "return reconnectInventoryObserved(ctx)", 1)
	inventoryReplacement := filepath.Join(dir, "observed_incarnation.go")
	if err := os.WriteFile(inventoryReplacement, []byte(observedInventory), 0o600); err != nil {
		t.Fatal(err)
	}
	replacements := map[string]string{path: replacement, inventoryPath: inventoryReplacement}
	raw, err := json.Marshal(map[string]any{"Replace": replacements})
	if err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(overlay, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "observed-reconnect.test")
	buildCtx, stopBuild := context.WithTimeout(t.Context(), 90*time.Second)
	defer stopBuild()
	compile := procexec.Command(buildCtx, "go", "test", "-c", "-race", "-overlay="+overlay,
		"-ldflags=-X github.com/agenxy/dibs/internal/build.Version="+build.Version, "-o", binary, "./cmd/dibs")
	compile.Dir = root
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("setup: build observed bridge/daemon: %v\n%s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(binary, "--dibs-test-prepare"))
	receipt := filepath.Join(dir, "inventory-timeout-receipt")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := procexec.Command(ctx, binary, "-test.run=^TestAppReconnectRecoversMailBeforeAModelCall/lost-queue-unavailable-observer$", "-test.v")
	cmd.Env = append(os.Environ(), "DIBS_TEST_RECONNECT_INVENTORY_RECEIPT="+receipt)
	out, runErr := cmd.CombinedOutput()
	proof, err := os.ReadFile(receipt)
	if err != nil || string(proof) != "entered\ncontext deadline exceeded\n" {
		t.Fatalf("setup: controlled inventory did not expire through its real context: %q %v\n%s", proof, err, out)
	}
	if runErr != nil || !strings.Contains(string(out), "--- PASS: TestAppReconnectRecoversMailBeforeAModelCall") {
		t.Fatalf("one expired inventory lost the actual bridge reconnect receipt: %v\n%s", runErr, out)
	}
}

// Observe only the real ancestry inventory door. App-epoch inventories have a
// different caller and must not consume the controlled first/second ordering.
const reconnectInventoryTimeoutObserver = `
func reconnectInventoryObserved(ctx context.Context) (*processPass,error) {
 receipt:=os.Getenv("DIBS_TEST_RECONNECT_INVENTORY_RECEIPT")
 if receipt!="" && os.Getenv("DIBS_TEST_STARTUP_BRIDGE")!="1" {
  base:=filepath.Join(os.Getenv("DIBS_DIR"),"reconnect-inventory-first")
  first,err:=os.OpenFile(base,os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600)
  if err==nil { first.Close() } else {
   second,e:=os.OpenFile(base+"-second",os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600)
   if e==nil {
    second.Close()
    f,e:=os.OpenFile(receipt,os.O_CREATE|os.O_WRONLY,0600)
    if e!=nil {panic(e)}
    f.WriteString("entered\n")
    <-ctx.Done()
    f.WriteString(ctx.Err().Error()+"\n")
    f.Close()
    return nil,ctx.Err()
   }
  }
 }
 return appProcessPass(ctx)
}
`
