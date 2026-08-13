package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// helpPass runs `dibs <verb> --help` for EVERY verb the binary has, and the
// flag-error path for one, checking that neither names a product this is not.
//
// The existing checks each name one command, so they cover exactly the outputs
// somebody thought to list. `usage: agents <verb>` shipped on every subcommand's
// --help and on every bad-flag error, past all of them, because no check ran a
// per-verb help at all: the string lives in one shared helper that no single
// named check goes through.
//
// The verbs come from the binary's own dispatch table rather than a list here,
// so a verb added later is covered without anybody remembering to add it.
func helpPass() (int, error) {
	verbs, err := verbTable()
	if err != nil {
		return 0, err
	}
	// The bare word is not the bug: this output legitimately says "agents can
	// never promote themselves". What must not appear is a stale name in
	// COMMAND position, so the pattern requires a real verb after it, taken
	// from the same dispatch table. A looser matcher failed two verbs on their
	// prose, which is how a check earns the reputation that gets it deleted.
	var names []string
	for v := range verbs {
		names = append(names, regexp.QuoteMeta(v))
	}
	stale := regexp.MustCompile(`(?m)^(?:usage: )?\s*(agents|lanes) (` + strings.Join(names, "|") + `)\b`)

	failed := 0
	for verb := range verbs {
		out, _ := exec.Command("bin/dibs", verb, "--help").CombinedOutput() // #nosec G204 -- from our own dispatch table
		if m := stale.FindStringSubmatch(string(out)); m != nil {
			failed++
			fmt.Printf("  FAIL `dibs %s --help` calls the binary %q\n%s\n",
				verb, m[1]+" "+m[2], indent(string(out)))
		}
	}

	// The other half of the same helper: a flag it does not take. This is the
	// path that told users to run `agents <verb> --help`.
	out, _ := exec.Command("bin/dibs", "board", "-nosuchflag").CombinedOutput()
	if m := stale.FindStringSubmatch(string(out)); m != nil {
		failed++
		fmt.Printf("  FAIL a bad flag calls the binary %q in its correction:\n%s\n",
			m[1]+" "+m[2], indent(string(out)))
	}

	if failed == 0 {
		fmt.Printf("  ok   %d verbs' --help and the bad-flag correction name dibs\n", len(verbs))
	}
	return failed, nil
}
