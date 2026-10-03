package engine

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestBoardUsesMacFriendlyNameRatherThanNetworkKernelName(t *testing.T) {
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
	e := New(core.NewState("friendly-host", core.DefaultLimits()), &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "local", Nonce: "friendly-host-local", Agent: &core.AgentInfo{HostID: e.HostID(), Host: kernel}}); err != nil {
		t.Fatal("setup:", err)
	}
	board, err := e.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := board["agents"].([]map[string]any)
	if len(rows) != 1 || rows[0]["host"] != want {
		t.Fatalf("friendly board host: %v, want %q (kernel %q)", rows, want, kernel)
	}
}
