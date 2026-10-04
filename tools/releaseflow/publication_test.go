package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestProductionValidateRequiresFullPublicationEvidence(t *testing.T) {
	c := fixture(t)
	c.phase = "validate"
	err := execute(context.Background(), c, command)
	if err == nil || !strings.Contains(err.Error(), "full-publication") {
		t.Fatalf("candidate without real full-publication proof accepted: %v", err)
	}
	if git(t, "tag", "--list") != "" || git(t, "ls-remote", "--tags", "origin") != "" {
		t.Fatal("missing proof created a tag")
	}
}

func TestCommitTagRequiresFullPublicationEvidenceAgain(t *testing.T) {
	c := fixture(t)
	c.phase = "commit-tag"
	if err := localTag(context.Background(), c, command); err != nil {
		t.Fatal(err)
	}
	r := proofFixture(c, false)
	r.Tree = git(t, "rev-parse", "HEAD^{tree}")
	r.TagOID = git(t, "rev-parse", "refs/tags/v"+c.version)
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(c.receiptPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	err = execute(context.Background(), c, command)
	if err == nil || !strings.Contains(err.Error(), "full-publication") {
		t.Fatalf("commit-tag without refreshed publication proof accepted: %v", err)
	}
	if git(t, "ls-remote", "--tags", "origin") != "" {
		t.Fatal("missing proof pushed a remote tag")
	}
}

func TestUnknownPublicationTargetNeverReachesASubprocess(t *testing.T) {
	c := fixture(t)
	c.phase, c.target = "validate", "https://example.invalid/other/repo"
	run := func(context.Context, []string, string, ...string) ([]byte, error) {
		t.Fatal("untrusted target reached a subprocess")
		return nil, nil
	}
	if err := execute(context.Background(), c, run); err == nil {
		t.Fatal("arbitrary repository accepted as publication target")
	}
}

func TestUnboundRehearsalTargetNeverReachesASubprocess(t *testing.T) {
	c := fixture(t)
	c.phase, c.target = "full-publication", "rehearsal"
	run := func(context.Context, []string, string, ...string) ([]byte, error) {
		t.Fatal("unapproved scratch target reached a subprocess")
		return nil, nil
	}
	if err := execute(context.Background(), c, run); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("unnamed rehearsal target did not refuse explicitly: %v", err)
	}
}

func TestDiscoveryNegativeControlNeverRunsInProduction(t *testing.T) {
	c := fixture(t)
	c.phase, c.negativeControl = "validate", true
	run := func(context.Context, []string, string, ...string) ([]byte, error) {
		t.Fatal("negative control reached production")
		return nil, nil
	}
	if err := execute(context.Background(), c, run); err == nil {
		t.Fatal("negative control accepted for production")
	}
}
