package core

import (
	"testing"
	"time"
)

// A sweep written before v0.0.7 purged the row and kept its blob ownership,
// which replay must preserve. A replacement registering the name then
// fenced the mail and still held every blob the predecessor had put. A
// fresh row has put nothing, so registration strips the id from every blob.
func TestARegistrationAfterAHistoricalPurgeDoesNotInheritAttachments(t *testing.T) {
	s := NewState("probe", DefaultLimits())
	now := time.Unix(1700000000, 0)
	ackReg(t, s, "alice", "tok-a", now)
	ackReg(t, s, "peer", "tok-p", now)
	blob := blobID("alice's attachment")
	putBlob(t, s, "tok-a", "alice's attachment", now)
	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tok-a", To: "peer", MsgType: MsgNotify, Body: "see attached",
		Attachments: []Attachment{{Blob: blob}},
	}, now)
	s.Agents["alice"].Status = StatusArchived
	s.Agents["alice"].ArchivedAt = now.Add(-s.Limits.ArchiveRetention - time.Hour)
	// The historical sweep: no PurgeMail flag, as every sweep before v0.0.7.
	mustApply(t, s, &Op{Kind: OpSweep}, now)
	if s.Agents["alice"] != nil {
		t.Fatal("setup: the sweep did not purge alice")
	}
	if b := s.Blobs[blob]; b == nil || !b.Owners["alice"] {
		t.Fatal("setup: the historical sweep no longer keeps ownership, so nothing below is contested")
	}
	// A stranger takes the name today.
	mustApply(t, s, &Op{Kind: OpRegister, Name: "alice", NewToken: "tok-a2", V7Semantics: true}, now.Add(time.Hour))
	if s.BlobAccessible(blob, "alice") {
		t.Fatal("a stranger registering the purged name can fetch the predecessor's attachment: " +
			"the historical purge kept the ownership and registration left it")
	}
	if !s.BlobAccessible(blob, "peer") {
		t.Fatal("the recipient of the live message referencing the blob lost access")
	}
}
