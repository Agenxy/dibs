package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("DIBS_TEST_WRONG_SIGNED_ID") == "1" && filepath.Base(os.Args[0]) == "codesign" {
		args := append([]string(nil), os.Args[1:]...)
		for i := range args {
			if args[i] == "--identifier" && i+1 < len(args) {
				args[i+1] = "org.agenxy.dibs.wrong"
			}
		}
		cmd := exec.Command("/usr/bin/codesign", args...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestReleaseRefusesActualPostSignMismatch(t *testing.T) {
	t.Setenv(identityEnv, "")
	t.Setenv(keychainEnv, "")
	t.Setenv("DIBS_TEST_WRONG_SIGNED_ID", "1")
	bin := t.TempDir()
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "codesign"), data, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	app := signerBundle(t, "org.agenxy.dibs")
	signErr := signOne(app)
	out, err := exec.Command("/usr/bin/codesign", "-dv", app).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Identifier=org.agenxy.dibs.wrong\n") {
		t.Fatalf("negative setup did not actually mis-sign the fixture: %v %s", err, out)
	}
	if signErr == nil || !strings.Contains(signErr.Error(), "signed notifier identifier") {
		t.Fatalf("real codesign produced a wrong identifier, but packaging was not refused: %v", signErr)
	}
}

// The production signer signs a real Mach-O bundle. No manual flag setter or
// two-constant comparison can prove the identity on the packaged artifact.
func TestReleasedNotifierHasCoherentActualIdentity(t *testing.T) {
	t.Setenv(identityEnv, "")
	t.Setenv(keychainEnv, "")
	app := signerBundle(t, "org.agenxy.dibs")
	if err := signOne(app); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/usr/bin/codesign", "-dv", app).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "\nIdentifier=org.agenxy.dibs\n") {
		t.Fatalf("actual signed artifact disagrees with bundle: %v %s", err, out)
	}
	if out, err = exec.Command("/usr/bin/codesign", "--verify", "--strict", app).CombinedOutput(); err != nil {
		t.Fatalf("signed fixture invalid: %v %s", err, out)
	}
}

func TestReleaseRefusesDifferentNotifierBundleIdentity(t *testing.T) {
	t.Setenv(identityEnv, "")
	t.Setenv(keychainEnv, "")
	app := signerBundle(t, "org.agenxy.dibs.other")
	before, err := os.ReadFile(filepath.Join(app, "Contents", "MacOS", "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	if err := signOne(app); err == nil || !strings.Contains(err.Error(), "bundle identifier") {
		t.Fatalf("disagreement must refuse before signing: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(app, "Contents", "MacOS", "fixture"))
	if err != nil || string(before) != string(after) {
		t.Fatal("refusal modified the executable")
	}
}

func TestReleasedDaemonAndNotifierHaveDistinctActualIdentities(t *testing.T) {
	t.Setenv(identityEnv, "")
	t.Setenv(keychainEnv, "")
	app := signerBundle(t, "org.agenxy.dibs")
	daemon := filepath.Join(t.TempDir(), "dibd")
	data, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(daemon, data, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{daemon, app} {
		if err := signOne(path); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("/usr/bin/codesign", "--verify", "--strict", path).CombinedOutput(); err != nil {
			t.Fatalf("actual signed fixture invalid: %v %s", err, out)
		}
	}
	readID := func(path string) string {
		out, err := exec.Command("/usr/bin/codesign", "-dv", path).CombinedOutput()
		if err != nil {
			t.Fatalf("reading actual identity: %v %s", err, out)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if id, ok := strings.CutPrefix(line, "Identifier="); ok {
				return id
			}
		}
		t.Fatalf("no actual identity: %s", out)
		return ""
	}
	daemonID, notifierID := readID(daemon), readID(app)
	if daemonID != "org.agenxy.dibs.daemon" || notifierID != "org.agenxy.dibs" || daemonID == notifierID {
		t.Fatalf("actual daemon/notifier identities must be distinct and coherent: %q / %q", daemonID, notifierID)
	}
}

func signerBundle(t *testing.T, id string) string {
	t.Helper()
	app := filepath.Join(t.TempDir(), "Dibs.app")
	bin := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "fixture"), data, 0o700); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>` + id + `</string><key>CFBundleExecutable</key><string>fixture</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	return app
}
