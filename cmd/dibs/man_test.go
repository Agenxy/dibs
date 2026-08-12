package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every verb the dispatch accepts must have an entry on the man page.
//
// The page is generated from the usage text precisely so the two cannot
// drift, and this is the test that turns the drift into a build failure
// rather than a page that quietly stops mentioning a verb. The verbs are read
// out of main.go's case labels, the same source
// TestEverySubcommandIsInTheUsageText reads, so the check holds against the
// dispatch itself.
func TestEveryDispatchedVerbHasAManPageEntry(t *testing.T) {
	verbs := dispatchedVerbs(t)
	page, err := manPage("2026-08-11")
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range verbs {
		if !strings.Contains(page, ".It Cm "+verb) {
			t.Errorf("`dibs %s` is dispatched but has no entry on the man page:\n"+
				"  a man page that skips a verb documents a different program", verb)
		}
	}
}

// Every environment variable the help names must appear on the page, for the
// same reason as the verbs: the page is read while something is broken, and a
// variable it does not know about is a fix it cannot offer.
func TestEveryEnvironmentVariableIsOnTheManPage(t *testing.T) {
	page, err := manPage("2026-08-11")
	if err != nil {
		t.Fatal(err)
	}
	vars := regexp.MustCompile(`DIBS_[A-Z_]+`).FindAllString(usage, -1)
	if len(vars) < 3 {
		t.Fatalf("found only %d DIBS_* variables in the usage text: the env line "+
			"changed shape and this test is checking almost nothing", len(vars))
	}
	for _, v := range vars {
		if !strings.Contains(page, v) {
			t.Errorf("%s is in the usage text but not on the man page", v)
		}
	}
}

// The page must satisfy mandoc's linter, because that is the bar the mdoc
// ecosystem holds pages to and the one stated when this page was asked for.
// Machines without mandoc skip rather than fail: their absence says nothing
// about the page.
func TestManPageLintsCleanUnderMandoc(t *testing.T) {
	bin, err := exec.LookPath("mandoc")
	if err != nil {
		t.Skip("mandoc is not installed here")
	}
	page, err := manPage("2026-08-11")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "dibs.1")
	if err := os.WriteFile(path, []byte(page), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "-Tlint", path).CombinedOutput()
	if err != nil || len(out) > 0 {
		t.Fatalf("mandoc -Tlint is not clean: %v\n%s", err, out)
	}
}

// The date has to come from the flag when one is given, or the release's
// promise that two builds of one commit produce one page is quietly false.
func TestManPageDateComesFromTheFlag(t *testing.T) {
	for flagValue, want := range map[string]string{
		"2026-01-02":           ".Dd January 2, 2026",
		"2026-01-02T15:04:05Z": ".Dd January 2, 2026",
	} {
		page, err := manPage(flagValue)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(page, want) {
			t.Errorf("manPage(%q) does not carry %q", flagValue, want)
		}
	}
	if _, err := manPage("last tuesday"); err == nil {
		t.Error("a date the parser cannot read must refuse, not silently become today")
	}
}
