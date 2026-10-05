package main

import (
	"bytes"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// All cases enter at unit generation or upgrade planning. None manually sets
// policy drift: doing that would miss the production wire that detects it.
func policyUpgradeFixture(t *testing.T, extra string) (*plan, string, []byte) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir := filepath.Join(home, "board")
	bin := filepath.Join(home, "dibd")
	if err := os.WriteFile(bin, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(home, "Library", "LaunchAgents", "org.agenxy.dibs.plist")
	if err := os.MkdirAll(filepath.Dir(unit), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(fmt.Sprintf(`<plist version="1.0"><dict>
<key>Label</key><string>org.agenxy.dibs</string>
<key>ProgramArguments</key><array><string>%s</string><string>-dir</string><string>%s</string></array>
%s
</dict></plist>`, html.EscapeString(bin), html.EscapeString(dir), extra))
	if err := os.WriteFile(unit, body, 0o640); err != nil {
		t.Fatal(err)
	}
	p := &plan{dir: dir, installed: bin, serving: true, checked: "v0.0.12"}
	p.findDrift()
	if p.unit != unit || p.unitWrong {
		t.Fatalf("setup: matching correctly pinned unit was not found: %+v", p)
	}
	return p, unit, body
}

func TestLaunchPolicyGeneratedStandard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeLaunchAgent("/bin/dibd", home); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "org.agenxy.dibs.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("<key>ProcessType</key><string>Standard</string>")) {
		t.Fatal("generated daemon retains Background throttle")
	}
}

func TestLaunchPolicyUpgradeRetainsCustomBytes(t *testing.T) {
	for _, policy := range []string{"Background", "Back&#103;round"} {
		t.Run(policy, func(t *testing.T) {
			extra := `<key>EnvironmentVariables</key><dict><key>ProcessType</key><string>Background</string><key>CUSTOM</key><string>a&amp;b</string></dict>
<!-- operator tuning --> <key>Nice</key><integer>7</integer>
<key>ProcessType</key><string>` + policy + `</string>`
			p, unit, before := policyUpgradeFixture(t, extra)
			if err := p.preflight(); err != nil {
				t.Fatal(err)
			}
			if _, err := p.reconcile(); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(unit)
			if err != nil {
				t.Fatal(err)
			}
			want := bytes.Replace(before, []byte("<key>ProcessType</key><string>"+policy+"</string>\n</dict>"), []byte("<key>ProcessType</key><string>Standard</string>\n</dict>"), 1)
			if bytes.Equal(want, before) {
				t.Fatal("setup: expected replacement did not match")
			}
			if !bytes.Equal(want, after) {
				t.Fatalf("policy-only upgrade did not preserve all other bytes:\n%s", after)
			}
			backup, err := os.ReadFile(unit + ".replaced")
			if err != nil || !bytes.Equal(backup, before) {
				t.Fatalf("original unit not retained: %v", err)
			}
			st, err := os.Stat(unit)
			if err != nil || st.Mode().Perm() != 0o640 {
				t.Fatalf("unit mode changed: %v", err)
			}
			p.findDrift()
			if !p.nothingToDo(buildInfo{Version: "v0.0.12"}) {
				t.Fatal("migrated same-build unit keeps demanding a restart")
			}
		})
	}
}

func TestLaunchPolicySameBuildStillNeedsMigration(t *testing.T) {
	p, _, _ := policyUpgradeFixture(t, `<key>ProcessType</key><string>Background</string>`)
	if p.nothingToDo(buildInfo{Version: "v0.0.12"}) {
		t.Fatal("same-build Background policy incorrectly treated as no work")
	}
}

func TestLaunchPolicyRefusesMalformedBeforeStop(t *testing.T) {
	for _, extra := range []string{
		`<key>ProcessType</key><string>Background</string><key>ProcessType</key><string>Standard</string>`,
		`<key>ProcessType</key><integer>3</integer>`,
		`<key>ProcessType</key><string>Background</string><key>dangling</key>`,
	} {
		t.Run(extra, func(t *testing.T) {
			p, unit, before := policyUpgradeFixture(t, extra)
			if err := p.preflight(); err == nil || !strings.Contains(err.Error(), "nothing has been stopped") {
				t.Fatalf("invalid unit passed preflight: %v", err)
			}
			after, err := os.ReadFile(unit)
			if err != nil || !bytes.Equal(after, before) {
				t.Fatal("refusal changed original")
			}
		})
	}
}

func TestLaunchPolicyRefusesChangedUnit(t *testing.T) {
	p, unit, before := policyUpgradeFixture(t, `<key>ProcessType</key><string>Background</string>`)
	changed := append(before, []byte("<!-- edited concurrently -->")...)
	if err := os.WriteFile(unit, changed, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := p.preflight(); err == nil {
		t.Fatal("unit changed after planning but preflight passed")
	}
	if _, err := p.reconcile(); err == nil {
		t.Fatal("unit changed after planning was clobbered")
	}
	after, err := os.ReadFile(unit)
	if err != nil || !bytes.Equal(changed, after) {
		t.Fatal("concurrent edit lost")
	}
}

func TestLaunchPolicyRefusesUnwritableBackupBeforeStop(t *testing.T) {
	p, unit, _ := policyUpgradeFixture(t, `<key>ProcessType</key><string>Background</string>`)
	if err := os.Mkdir(unit+".replaced", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := p.preflight(); err == nil {
		t.Fatal("known backup failure passed preflight")
	}
}

// These are positive controls: old code already leaves explicit policies and
// another board alone. They must stay green on the old-code proof.
func TestLaunchPolicyOperatorAndOtherBoardControls(t *testing.T) {
	for _, policy := range []string{"Standard", "Adaptive", "Interactive", ""} {
		extra := `<key>EnvironmentVariables</key><dict><key>ProcessType</key><string>Background</string></dict>`
		if policy != "" {
			extra += `<key>ProcessType</key><string>` + policy + `</string>`
		}
		p, unit, before := policyUpgradeFixture(t, extra)
		if err := p.preflight(); err != nil {
			t.Fatal(err)
		}
		if _, err := p.reconcile(); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(unit)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("operator policy changed")
		}
		p.dir += "-other"
		p.unit = ""
		p.findDrift()
		if p.unit != "" {
			t.Fatal("another board's unit selected")
		}
		if _, err := p.reconcile(); err != nil {
			t.Fatal(err)
		}
	}
}
