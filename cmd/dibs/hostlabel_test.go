package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/agenxy/dibs/internal/hostname"
	"github.com/agenxy/dibs/internal/mcp"
)

// The label decision also preserves the old kernel spelling if an identity
// provider ever returns unknown. This does not pretend disk failure does so.
func TestUnknownHostIDLabelKeepsKernelComparisonEvidence(t *testing.T) {
	want, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if got := bridgeHostLabel(""); got != want {
		t.Fatalf("unknown ID changed legacy host evidence: %q want %q", got, want)
	}
}

func TestRegistrationCarriesHostIDEvenWhenItCannotBePublished(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(dir, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_HOST_ID", "")
	t.Setenv("PATH", t.TempDir()) // no Supgang lookup can supply a disk identity
	hostIDOnce, hostIDValue = sync.Once{}, ""
	t.Cleanup(func() { hostIDOnce, hostIDValue = sync.Once{}, "" })
	id := hostID()
	if id == "" {
		t.Fatal("setup: publication failure unexpectedly returned unknown")
	}
	if _, err := os.Stat(filepath.Join(dir, "host_id")); err == nil {
		t.Fatal("setup: identity was published under an ordinary file")
	}
	enrichRegister([]byte(`{"method":"initialize","params":{"clientInfo":{"name":"host-label-fixture","version":"1"}}}`))
	var msg map[string]any
	line := enrichRegister([]byte(`{"method":"tools/call","params":{"name":"register","arguments":{"name":"worker"},"_meta":{"com.dibs/session":"host-label-fixture"}}}`))
	if err := json.Unmarshal(line, &msg); err != nil {
		t.Fatal(err)
	}
	params := msg["params"].(map[string]any)
	meta := params["_meta"].(map[string]any)
	args := params["arguments"].(map[string]any)
	if meta[mcp.HostMetaKey] != id || args["host"] != hostname.Name() {
		t.Fatalf("real registration lost identity or display name: %v", params)
	}
}
