package hygiene

import (
	"strings"
	"testing"
)

// The Taskfile scanner sees every form a command takes, and nothing that is
// not one: the guard used to read `cmd:` mappings alone.
func TestTheTaskfileScannerReadsEveryCommandForm(t *testing.T) {
	const sample = `version: '3'
tasks:
  a:
    desc: not a command
    cmds:
      - task: build
        vars: { X: y }
      - echo one
      - |
        echo two
        echo three
      - cmd: go run ./tools/x
      - 'go run ./tools/y {{.ARGS}}'
      - cd sub && make
  b:
    cmds:
      - defer: rm -f out
`
	got := taskfileCommands(sample)
	if len(got) != 6 {
		t.Fatalf("found %d commands, want 6 (a task: call is not one): %+v", len(got), got)
	}
	var block, conditional bool
	for _, b := range got {
		if strings.Contains(b.body, "echo two") && countStatements(b.body) == 2 {
			block = true
		}
		if strings.Contains(b.body, "&&") {
			conditional = true
		}
		if strings.Contains(b.body, "task: build") || strings.Contains(b.body, "not a command") {
			t.Errorf("a non-command was read as one: %q", b.body)
		}
	}
	if !block {
		t.Error("the `- |` block was not read as a two-statement command: that is where the " +
			"shell conditional the guard missed lived")
	}
	if !conditional {
		t.Error("the `cd x && y` scalar was not read: nine of those sat beside the block")
	}
}
