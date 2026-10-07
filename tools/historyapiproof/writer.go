package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func oldWriterProof() error {
	file := "internal/mailhistory/query_writer_cost_test.go"
	// #nosec G304 -- fixed native regression fixture from immutable source.
	raw, err := os.ReadFile(filepath.Join("source", file))
	if err != nil {
		return err
	}
	// #nosec G703 -- identical fixture enters the named disposable old checkout.
	if err := os.WriteFile(filepath.Join("old-writer", file), raw, 0o600); err != nil {
		return err
	}
	cases := map[string]string{
		"TestMailHistoryNativeNonMailWriterDoesNotCopyLiveMailbox": "non-mail writer copied unrelated live mailbox",
		"TestMailHistoryNativeCheckpointCopiesOwnMailboxOnly":      "checkpoint copied unrelated board mail",
		"TestMailHistoryNativeFailedBuilderStopsWriterSnapshots":   "failed history kept copying writer metadata",
	}
	for name, marker := range cases {
		got, exit, err := test("old-writer", "^"+name+"$")
		if err != nil {
			return err
		}
		if err := intended(got, exit, name, marker); err != nil {
			return err
		}
		fmt.Printf("HISTORY_API_OLD_WRITER source=%s test=%s intended_red=true\n", beforeWriter, name)
	}
	return nil
}
