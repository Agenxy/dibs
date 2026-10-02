package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSharedConfigurationReadersDoNotSerializeOrAdmitAWriter(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.lock")
	open := func() *os.File {
		t.Helper()
		f, err := os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	a, b, writer := open(), open(), open()
	if err := LockShared(a, false); err != nil {
		t.Fatal(err)
	}
	if err := LockShared(b, false); err != nil {
		t.Fatal("second reader serialized:", err)
	}
	if err := LockExclusive(writer, false); !LockHeldElsewhere(err) {
		t.Fatalf("writer admitted among readers: %v", err)
	}
	Unlock(a)
	Unlock(b)
	if err := LockExclusive(writer, false); err != nil {
		t.Fatal(err)
	}
	if err := LockShared(a, false); !LockHeldElsewhere(err) {
		t.Fatalf("reader admitted during writer: %v", err)
	}
}
