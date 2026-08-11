package core

import "testing"

// An absent blob id and a malformed one have different causes, and the wrong
// message sends you looking in the wrong place. get_blob takes `blob`; `blob_id`
// is the common miss, and models get argument names wrong constantly: a
// misspelled name arrives as an EMPTY id, where a lecture about hex format is
// actively misleading.
func TestEmptyBlobIDIsNotReportedAsMalformed(t *testing.T) {
	if ErrNoID.Msg == ErrBadID.Msg {
		t.Fatal("the two blob-id errors must not share a message")
	}
	for _, c := range []struct{ name, hint string }{
		{"absent", ErrNoID.Hint},
		{"malformed", ErrBadID.Hint},
	} {
		if c.hint == "" {
			t.Errorf("%s: hint must say what to do", c.name)
		}
	}
	// The absent-id hint has to name the argument, because naming it is the
	// whole point: it is what turns a dead end into a one-line fix.
	if !contains(ErrNoID.Hint, "blob_id") || !contains(ErrNoID.Hint, "'blob'") {
		t.Errorf("absent-id hint must name both the right and wrong argument, got %q", ErrNoID.Hint)
	}
	// Both stay E_BAD_ID: the code is a stable contract for callers that switch
	// on it, and splitting it would be a breaking change for a wording fix.
	if ErrNoID.Code != ErrBadID.Code {
		t.Errorf("the error CODE must stay stable, got %q vs %q", ErrNoID.Code, ErrBadID.Code)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
