package remap

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// The test binary doubles as a stand-in `remap` answering as remap 0.2.0
// did on 2026-09-13, so the parsing is exercised against real envelopes.
func TestMain(m *testing.M) {
	if os.Getenv("DIBS_TEST_AS_REMAP") != "" {
		os.Exit(fakeRemap(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeRemap(args []string) int {
	if len(args) < 2 || args[0] != "--json" {
		return 2
	}
	w := func(s string) { _, _ = os.Stdout.WriteString(s) }
	switch args[1] {
	case "status":
		if os.Getenv("DIBS_TEST_REMAP_EMPTY") != "" {
			w(`{"command":"status","ok":true,"result":{"kind":"status","value":null},"schema":"remap.cli/v1"}`)
			return 0
		}
		w(`{"command":"status","ok":true,"result":{"kind":"status","value":{"daemon_version":"0.2.0","enabled_count":2,"mapping_count":2,"revision":22,"schema_version":1}},"schema":"remap.cli/v1"}`)
	case "get":
		if os.Getenv("DIBS_TEST_REMAP_EMPTY") != "" || len(args) < 3 || args[2] != "dibs" {
			w(`{"command":"get","ok":true,"result":{"kind":"mapping","value":null},"schema":"remap.cli/v1"}`)
			return 0
		}
		w(`{"command":"get","ok":true,"result":{"kind":"mapping","value":{"enabled":true,"host_policy":"use-upstream","pattern":"dibs","target":"http://127.0.0.1:4777/","target_kind":"http","updated_revision":23}},"schema":"remap.cli/v1"}`)
	case "set":
		if os.Getenv("DIBS_TEST_REMAP_EMPTY") != "" {
			w(`{"command":"set","ok":true,"result":{"kind":"mapping","value":null},"schema":"remap.cli/v1"}`)
			return 0
		}
		if os.Getenv("DIBS_TEST_REMAP_DISABLED") != "" {
			w(`{"command":"set","ok":true,"result":{"kind":"mapping","value":{"enabled":false,"host_policy":"use-upstream","pattern":"` + args[2] + `","target":"` + args[3] + `","target_kind":"http","updated_revision":24}},"schema":"remap.cli/v1"}`)
			return 0
		}
		if os.Getenv("DIBS_TEST_REMAP_OTHER") != "" {
			w(`{"command":"set","ok":true,"result":{"kind":"mapping","value":{"enabled":true,"host_policy":"use-upstream","pattern":"other","target":"http://127.0.0.1:1/","target_kind":"http","updated_revision":24}},"schema":"remap.cli/v1"}`)
			return 0
		}
		if len(args) >= 4 && args[3] == "conflict" {
			w(`{"schema":"remap.cli/v1","ok":false,"command":"set","error":{"code":"E_REVISION_CONFLICT","message":"the registry is at revision 13, not the expected revision 12","hint":"read current mappings, reconsider the change, and use the new revision","retryable":false}}`)
			return 1
		}
		w(`{"command":"set","ok":true,"result":{"kind":"mapping","value":{"enabled":true,"host_policy":"use-upstream","pattern":"` + args[2] + `","target":"` + args[3] + `","target_kind":"http","updated_revision":24}},"schema":"remap.cli/v1"}`)
	default:
		return 2
	}
	return 0
}

func useFake(t *testing.T) {
	t.Helper()
	old := Command
	Command = os.Args[0]
	t.Setenv("DIBS_TEST_AS_REMAP", "1")
	t.Cleanup(func() { Command = old })
}

func TestMappingsAreReadFromRemapsOwnEnvelopes(t *testing.T) {
	useFake(t)
	ctx := context.Background()
	rev, err := Status(ctx)
	if err != nil || rev != 22 {
		t.Fatalf("Status = %d %v", rev, err)
	}
	m, err := Get(ctx, "dibs")
	if err != nil || m == nil || m.Target != "http://127.0.0.1:4777/" || !m.Enabled {
		t.Errorf("Get(dibs) = %+v %v", m, err)
	}
	if m, err := Get(ctx, "nothing"); err != nil || m != nil {
		t.Errorf("Get(nothing) = %+v %v, want no mapping and no error: an absent name is an answer", m, err)
	}
	if err := Set(ctx, "board", "http://127.0.0.1:4777/"); err != nil {
		t.Errorf("Set = %v", err)
	}
}

func TestRemapsRefusalsArriveWithTheirCodeAndHint(t *testing.T) {
	useFake(t)
	err := Set(context.Background(), "board", "conflict")
	var re *Error
	if !errors.As(err, &re) || re.Code != "E_REVISION_CONFLICT" || !strings.Contains(re.Hint, "reconsider") {
		t.Errorf("Set on a conflict = %v, want Remap's own code and hint", err)
	}
	for _, name := range []string{"--data-dir", "", "a b", "http://x"} {
		if _, err := Get(context.Background(), name); err == nil || !strings.Contains(err.Error(), "not a name") {
			t.Errorf("Get(%q) = %v, want it refused before argv", name, err)
		}
	}
}

// An ok envelope is not a registration: Set requires the mapping Remap says
// it holds, and it must be the one asked for; a result of the wrong kind, or
// none, is refused for every command but Get.
func TestAnOkEnvelopeAroundTheWrongThingIsNotSuccess(t *testing.T) {
	useFake(t)
	ctx := context.Background()
	t.Setenv("DIBS_TEST_REMAP_EMPTY", "1")
	if err := Set(ctx, "board", "http://127.0.0.1:4777/"); err == nil {
		t.Error("Set accepted an ok envelope with no mapping in it")
	}
	if _, err := Status(ctx); err == nil {
		t.Error("Status accepted an ok envelope with no status in it")
	}
	if m, err := Get(ctx, "board"); err != nil || m != nil {
		t.Errorf("Get on an empty result = %+v %v, want no mapping and no error", m, err)
	}
	t.Setenv("DIBS_TEST_REMAP_EMPTY", "")
	t.Setenv("DIBS_TEST_REMAP_OTHER", "1")
	if err := Set(ctx, "board", "http://127.0.0.1:4777/"); err == nil || !strings.Contains(err.Error(), "not the") {
		t.Errorf("Set accepted a mapping for another name: %v", err)
	}
	t.Setenv("DIBS_TEST_REMAP_OTHER", "")
	t.Setenv("DIBS_TEST_REMAP_DISABLED", "1")
	if err := Set(ctx, "board", "http://127.0.0.1:4777/"); err == nil || !strings.Contains(err.Error(), "does not route") {
		t.Errorf("Set accepted a disabled mapping as a registration: %v", err)
	}
	// Inputs are bounded before anything is started.
	if _, err := Get(ctx, strings.Repeat("a", 300)); err == nil || !strings.Contains(err.Error(), "not a name") {
		t.Errorf("an oversized name reached argv: %v", err)
	}
	if err := Set(ctx, "board", "http://"+strings.Repeat("a", 600)); err == nil || !strings.Contains(err.Error(), "not a target") {
		t.Errorf("an oversized target reached argv: %v", err)
	}
}

func TestNotInstalledIsItsOwnAnswer(t *testing.T) {
	old := Command
	Command = "/nonexistent/remap-" + t.Name()
	t.Cleanup(func() { Command = old })
	if _, err := Status(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Status without the binary = %v", err)
	}
}
