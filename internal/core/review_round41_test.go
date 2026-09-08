package core

import (
	"testing"
	"time"
)

// A blob is fetchable by the recipient of a message referencing it, and that
// route did not ask whose mail the message was: below the watermark it was
// addressed to a previous occupant of the id, the replacement could not see
// it, and could still fetch its attachment by blob id. The mail fence and
// the ownership strip closed two doors; this was the third.
func TestHiddenPredecessorMailDoesNotAuthoriseItsAttachments(t *testing.T) {
	s := NewState("probe", DefaultLimits())
	now := time.Unix(1700000000, 0)
	ackReg(t, s, "alice", "tok-a", now)
	ackReg(t, s, "peer", "tok-p", now)
	blob := blobID("peer's attachment")
	putBlob(t, s, "tok-p", "peer's attachment", now)
	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tok-p", To: "alice", MsgType: MsgNotify, Body: "for the previous occupant",
		Attachments: []Attachment{{Blob: blob}},
	}, now)
	if !s.BlobAccessible(blob, "alice") {
		t.Fatal("setup: the recipient cannot fetch the attachment it was sent")
	}
	s.Agents["alice"].Status = StatusArchived
	s.Agents["alice"].ArchivedAt = now.Add(-s.Limits.ArchiveRetention - time.Hour)
	// The historical sweep keeps the mail, addressed to the id.
	mustApply(t, s, &Op{Kind: OpSweep}, now)
	mustApply(t, s, &Op{Kind: OpRegister, Name: "alice", NewToken: "tok-a2", V7Semantics: true}, now.Add(time.Hour))
	if got := s.Inbox("alice"); len(got) != 0 {
		t.Fatalf("setup: the replacement sees %d of its predecessor's messages", len(got))
	}
	if s.BlobAccessible(blob, "alice") {
		t.Fatal("a stranger registering the purged name can fetch an attachment from mail it cannot " +
			"see: the message route does not ask whose mail the message is")
	}
	if !s.BlobAccessible(blob, "peer") {
		t.Fatal("the sender, who owns the blob, lost access")
	}
}
