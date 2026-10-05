package engine

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/harnessenv"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "dibs-engine-codex-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("CODEX_HOME", dir); err != nil {
		panic(err)
	}
	guard := harnessenv.Shower{
		Holds: func(string) bool { panic("unfaked app access in engine test") },
		Open:  func([]string) error { panic("unfaked app access in engine test") },
	}
	harnessenv.RealShower = guard
	shower = &guard
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestAppGuardRejectsUnfakedAccess(t *testing.T) {
	if route := os.Getenv("DIBS_TEST_UNFAKED_APP"); route != "" {
		if route == "package" {
			shower.Holds("fixture")
		} else {
			_ = harnessenv.RealShower.Open([]string{"/nonexistent/dibs-test-app-guard"})
		}
		t.Fatal("unfaked app guard returned")
	}
	for _, route := range []string{"package", "real"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestAppGuardRejectsUnfakedAccess$") // this test binary only
		cmd.Env = append(os.Environ(), "DIBS_TEST_UNFAKED_APP="+route)
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "unfaked app access in engine test") {
			t.Fatalf("guard %s did not reject accidental app access: %v %s", route, err, out)
		}
	}
}
