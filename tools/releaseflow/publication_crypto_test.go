package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/selfupdate"
)

// Opt-in, offline REAL crypto negative control. The fixture is an unchanged
// published production checksums+bundle, NOT a forged positive scratch receipt.
// No network, signing credential or production policy override is used.
func TestPublicationRealCryptoRejectsProductionIdentityAndTamperedBytes(t *testing.T) {
	dir := os.Getenv("DIBS_TEST_REHEARSAL_CRYPTO_FIXTURE")
	if dir == "" {
		t.Skip("set path to independently retained published checksums.txt and bundle; no network in CI")
	}
	body, err := os.ReadFile(filepath.Join(dir, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := os.ReadFile(filepath.Join(dir, "checksums.txt.bundle"))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 || len(bundle) == 0 {
		t.Fatal("real fixture setup is empty")
	}
	// Assert the measurement setup: real unchanged production bytes MUST pass
	// the existing production verifier before a scratch identity refusal counts.
	if _, err = selfupdate.VerifyReleaseEvidence(context.Background(), "v0.0.9", body, bundle); err != nil {
		t.Fatalf("real production fixture did not verify before negative controls: %v", err)
	}
	for _, tampered := range []bool{false, true} {
		t.Run(map[bool]string{false: "production identity", true: "payload digest"}[tampered], func(t *testing.T) {
			c := fixture(t)
			c.phase = "validate"
			f := newPublicationFixture(t, &c, command)
			f.body = append([]byte(nil), body...)
			f.signature = bundle
			if tampered {
				f.body[0] ^= 1
			}
			err := execute(context.Background(), c, command)
			if err == nil {
				t.Fatal("real verifier accepted production proof as scratch")
			}
			message := strings.ToLower(err.Error())
			t.Log(err)
			if tampered {
				// Actual cosign 3.1.3 validates the signature against the changed
				// message before its identity constraint. It reports a cryptographic
				// signature mismatch, not a literal "digest mismatch" diagnostic.
				if !strings.Contains(message, "failed to verify signature: could not verify message: invalid signature when validating asn.1 encoded signature") {
					t.Fatalf("tampering did not reach exact real content-signature refusal: %v", err)
				}
			} else {
				if !strings.Contains(message, "identity") && !strings.Contains(message, "subject") {
					t.Fatalf("real identity check not reached; parse/root failure is not proof: %v", err)
				}
			}
			if git(t, "tag", "--list") != "" || git(t, "ls-remote", "--tags", "origin") != "" {
				t.Fatal("real crypto refusal created a tag")
			}
		})
	}
}
