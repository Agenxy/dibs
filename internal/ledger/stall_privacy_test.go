package ledger

import (
	"path/filepath"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestStallNoticeBodyIsEncrypted(t *testing.T) {
	box, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal("setup:", err)
	}
	// Literal kind lets this guard run unchanged against the pre-feature code.
	op := &core.Op{Kind: "stall_notified", Body: "private declaration quoted in a stall"}
	plain := op.Body
	if err := box.EncryptOp(op); err != nil {
		t.Fatal(err)
	}
	if op.Body == plain {
		t.Fatal("stall notice left private declaration content in plaintext")
	}
	if err := box.DecryptOp(op); err != nil || op.Body != plain {
		t.Fatalf("decrypted notice: %q, %v", op.Body, err)
	}
}
