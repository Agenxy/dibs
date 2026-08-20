package core

import "testing"

// Two different messages must not share an op_id digest.
//
// THE COLLISION THIS CATCHES. The digest appended choices and then attachment
// fields with nothing marking where one list ended, so these two are the same
// bytes:
//
//	choices ["x", "", "/tmp/a", ""]  with no attachments
//	choices ["x"]                    with attachment {path: "/tmp/a"}
//
// A retry of the second against the first is answered ok:true,
// deduplicated:true, and what is stored is the first: one message offering four
// answers, the other offering one answer and a file reference. Found by a
// pre-release review.
func TestTwoDifferentMessagesDigestDifferently(t *testing.T) {
	a := &Op{
		To: "peer", MsgType: MsgQuestion, Body: "which?",
		Choices: []string{"x", "", "/tmp/a", ""},
	}
	b := &Op{
		To: "peer", MsgType: MsgQuestion, Body: "which?",
		Choices:     []string{"x"},
		Attachments: []Attachment{{Path: "/tmp/a"}},
	}

	if sendDigest(a) == sendDigest(b) {
		t.Error("a four-choice question and a one-choice question carrying a file " +
			"share a digest. A retry of one is answered as a duplicate of the other, " +
			"reports success, and stores neither what the caller sent nor anything " +
			"they can see is wrong")
	}

	// The fields that decide what an attachment IS are part of it too.
	same := func() *Op {
		return &Op{
			To: "peer", MsgType: MsgNotify, Body: "here",
			Attachments: []Attachment{{Path: "/tmp/a", Size: 10, Mime: "text/plain"}},
		}
	}
	other := same()
	other.Attachments[0].Mime = "application/x-executable"
	if sendDigest(same()) == sendDigest(other) {
		t.Error("two attachments differing only in what they claim to be share a digest")
	}
	bigger := same()
	bigger.Attachments[0].Size = 999
	if sendDigest(same()) == sendDigest(bigger) {
		t.Error("two attachments differing only in size share a digest")
	}

	// And a genuine retry still deduplicates, or the feature is gone. Built
	// twice into named values: staticcheck reads `f(x()) != f(x())` as an
	// expression compared with itself, which is exactly what it looks like and
	// not what it means here.
	first, retry := sendDigest(same()), sendDigest(same())
	if first != retry {
		t.Error("the same message digests differently twice: no retry can ever be " +
			"recognised, which is what op_id exists for")
	}
}
