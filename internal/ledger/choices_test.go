package ledger

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A question's answers are content, and were the one piece of it left in clear.
//
// Choices become recipient-scoped state on the message, exactly like the body
// beside them, written by the same sender for the same reader. The body was
// sealed and the answers were not, so a copied ledger (a backup, a support
// bundle, a pasted repro) showed the alternatives next to ciphertext. They are
// frequently the sensitive half, because they name what was actually on the
// table. Found by a pre-release review.
func TestQuestionChoicesAreSealedInTheLedger(t *testing.T) {
	box, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	op := &core.Op{
		Kind: core.OpSendMessage, MsgType: core.MsgQuestion,
		Body:    "which way?",
		Choices: []string{"Ship it", "Hold for the audit", ""},
	}

	enc := *op // exactly what Ledger.Append does
	if err := box.EncryptOp(&enc); err != nil {
		t.Fatal(err)
	}

	for _, c := range enc.Choices {
		if c != "" && !strings.HasPrefix(c, encPrefix) {
			t.Errorf("choice %q went to disk in the clear beside a sealed body", c)
		}
	}

	// The CALLER's op must be untouched.
	//
	// Append shallow-copies the op, and a shallow copy shares the backing array
	// of every slice in it. Sealing in place would write ciphertext into the op
	// the engine still holds and core has already stored on the message, so the
	// board would render sealed strings as the answers to a live question. The
	// first version of this fix did exactly that.
	if op.Choices[0] != "Ship it" || op.Choices[1] != "Hold for the audit" {
		t.Fatalf("encrypting the ledger copy mutated the caller's op: %q", op.Choices)
	}

	if err := box.DecryptOp(&enc); err != nil {
		t.Fatal(err)
	}
	for i, want := range op.Choices {
		if enc.Choices[i] != want {
			t.Errorf("round trip lost choice %d: got %q, want %q", i, enc.Choices[i], want)
		}
	}
}

// A ledger written before choices were sealed still reads.
func TestPlaintextChoicesFromAnOlderLedgerStillOpen(t *testing.T) {
	box, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	op := &core.Op{Kind: core.OpSendMessage, Choices: []string{"Yes", "No"}}
	if err := box.DecryptOp(op); err != nil {
		t.Fatalf("plaintext choices from an older ledger failed to open: %v", err)
	}
	if op.Choices[0] != "Yes" || op.Choices[1] != "No" {
		t.Errorf("passthrough altered them: %q", op.Choices)
	}
}
