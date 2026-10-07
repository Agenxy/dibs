package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func reviewProof() error {
	files := []string{
		"internal/mailhistory/query_review_test.go",
		"internal/mailhistory/page_progress_test.go",
		"internal/ledger/mail_history_canary_cost_test.go",
	}
	for _, file := range files {
		// #nosec G304 -- fixed immutable source fixture manifest.
		raw, err := os.ReadFile(filepath.Join("source", file))
		if err != nil {
			return err
		}
		// #nosec G703 -- identical tests copied to the disposable reviewed old source.
		if err := os.WriteFile(filepath.Join("old-writer", file), raw, 0o600); err != nil {
			return err
		}
	}
	cases := map[string]string{
		"TestMailHistoryNativeAdoptionSourceLimitIsPartyLocalAfterRestart": "unrelated party lost history after " +
			"adoption source overflow",
		"TestMailHistoryNativeCursorForgeryAndForeignReplayAreUniform": "forged or foreign cursor confirmed a position",
		"TestMailHistoryNativeInheritedReferenceForgeryRefusesLikeGarbage": "forged inherited reference " +
			"confirmed another position",
		"TestMailHistoryNativeOversizedContentKeepsMetadataAndLaterRows": "oversized content poisoned " +
			"metadata or later rows",
		"TestMailHistoryNativeFirstContentDeadlineKeepsMetadata": "first content deadline poisoned " +
			"authorized metadata",
		"TestHistoryPendingMetadataPreservesIssuedCursor": "unexamined pending page discarded " +
			"issued continuation",
		"TestHistoryProductionBootstrapDoesNotSerializePrivateBoard": "production bootstrap serialized " +
			"private canonical board",
	}
	for name, marker := range cases {
		got, exit, err := test("source", "^"+name+"$")
		if err != nil || exit != nil || got[name].action != "pass" {
			return fmt.Errorf("review candidate guard failed %s: %v %v", name, err, exit)
		}
		got, exit, err = test("old-writer", "^"+name+"$")
		if err != nil {
			return err
		}
		if err := intended(got, exit, name, marker); err != nil {
			return err
		}
		fmt.Printf("HISTORY_API_OLD_REVIEW source=%s test=%s intended_red=true\n", beforeWriter, name)
	}
	return nil
}
