package core

import (
	"testing"
	"time"
)

// A purge drops the row's mail and retires its outgoing mail, and left every
// blob it had put naming its id as an owner. Ownership is an authorisation
// on its own, so a stranger registering the purged name inherited the
// predecessor's attachments for as long as a peer's message kept the blob
// alive. The purge strips the id from every blob it owned.
func TestAPurgeStripsThePurgedAgentsBlobOwnership(t *testing.T) {
	s := NewState("probe", DefaultLimits())
	now := time.Unix(1700000000, 0)
	ackReg(t, s, "alice", "tok-a", now)
	ackReg(t, s, "peer", "tok-p", now)
	blob := blobID("alice's attachment")
	putBlob(t, s, "tok-a", "alice's attachment", now)
	// Referenced by a peer's live message, so the blob outlives alice.
	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tok-a", To: "peer", MsgType: MsgNotify, Body: "see attached",
		Attachments: []Attachment{{Blob: blob}},
	}, now)
	if !s.BlobAccessible(blob, "alice") {
		t.Fatal("setup: the owner cannot fetch its own blob")
	}
	s.Agents["alice"].Status = StatusArchived
	s.Agents["alice"].ArchivedAt = now.Add(-s.Limits.ArchiveRetention - time.Hour)
	mustApply(t, s, &Op{Kind: OpSweep, PurgeMail: true, V7Semantics: true}, now)
	if s.Agents["alice"] != nil {
		t.Fatal("setup: the sweep did not purge alice")
	}
	if s.Blobs[blob] == nil {
		t.Fatal("setup: the blob did not survive the purge, so nothing below is contested")
	}
	// A stranger takes the name and inherits the id.
	regPersistent(t, s, "alice", "tok-a2", "n-alice-2-0123456789", now.Add(time.Hour))
	if s.BlobAccessible(blob, "alice") {
		t.Fatal("a stranger registering the purged name can fetch the predecessor's attachment: " +
			"the purge left the id as the blob's owner")
	}
	// The recipient of the live message still can, as A6 says.
	if !s.BlobAccessible(blob, "peer") {
		t.Fatal("the recipient of a live message referencing the blob lost access")
	}
}
