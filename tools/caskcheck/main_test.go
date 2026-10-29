// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The archive gate checks the actual generated cask. This small independent
// cask proves that the checker discriminates through Homebrew's real loader
// and conditional step runner, rather than matching Ruby source strings.
const soundCask = `cask "dibs" do
  version "0.0.17"
  sha256 "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  url "https://example.invalid/dibs.tar.gz"
  binary "dibs"
  binary "dibd"
  on_macos do
    binary "dibs-presence"
    app "Dibs.app"
    caveats "dibs configure --service; brew upgrade dibs; dibs upgrade; Touch ID"
  end
  on_linux do
    caveats "dibs configure --service; systemctl --user enable --now dibs; notify-send; libnotify; brew upgrade dibs; dibs upgrade"
  end
  postflight_steps do
    on_macos do
      run "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "{{staged_path}}"], must_succeed: false
      run "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "{{appdir}}/Dibs.app"], must_succeed: false
    end
  end
end
`

func TestGuardsEnterHomebrewLoaderAndStepRunner(t *testing.T) {
	if _, err := exec.LookPath("brew"); err != nil {
		t.Skip("Homebrew DSL contract runs in the macOS gate; actual caskcheck never skips")
	}
	for _, tc := range []struct {
		name, before, after, want string
	}{
		{name: "sound"},
		{"legacy-flight", "postflight_steps do", "postflight do", "deprecated"},
		{"linux-app", `    app "Dibs.app"`, "  end\n  app \"Dibs.app\"\n  on_macos do", "linux selected"},
		{"linux-helper", `    binary "dibs-presence"`, "  end\n  binary \"dibs-presence\"\n  on_macos do", "linux selected"},
		{"linux-xattr", "  postflight_steps do\n", "  postflight_steps do\n    on_linux do\n      run \"/usr/bin/xattr\", args: [\"-dr\", \"com.apple.quarantine\", \"{{staged_path}}\"]\n    end\n", "linux selected"},
		{"service-call", `run "/usr/bin/xattr"`, `run "{{staged_path}}/dibs"`, "unexpected or fatal"},
		{"fatal-xattr", "must_succeed: false", "must_succeed: true", "unexpected or fatal"},
		{"missing-daemon", `  binary "dibd"`, "", "expected one dibd"},
		{"wrong-linux-advice", "notify-send; libnotify", "Touch ID", "linux caveats omit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := soundCask
			if tc.before != "" {
				if !strings.Contains(body, tc.before) {
					t.Fatal("setup: mutation has no target")
				}
				body = strings.Replace(body, tc.before, tc.after, 1)
			}
			path := filepath.Join(t.TempDir(), "dibs.rb")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			err := run(path)
			if tc.want == "" {
				if err != nil {
					t.Fatal("setup: sound cask rejected:", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected rejection %q; got %v", tc.want, err)
			}
		})
	}
}
