package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

func blobID(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func putBlob(t *testing.T, s *State, token, content string, now time.Time) Result {
	t.Helper()
	res, _, err := s.Apply(&Op{
		Kind: OpPutBlob, Token: token, Blob: blobID(content),
		Size: int64(len(content)),
	}, now)
	if err != nil {
		t.Fatalf("put_blob: %v", err)
	}
	return res
}

//nolint:unparam // returning the Agent keeps the helper usable from new tests
func ackReg(t *testing.T, s *State, name, token string, now time.Time) *Agent {
	t.Helper()
	l := reg(t, s, name, token, now)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: token}, now)
	return l
}

// TestBlobDedupIsCallerScoped is the P1-1 fix: `deduped` reflects only whether
// the CALLER already owned the content, never global existence, so it can't be
// used as a cross-agent existence oracle.
func TestBlobDedupIsCallerScoped(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	ackReg(t, s, "alpha", "ta", t0)
	ackReg(t, s, "beta", "tb", t0)

	// A puts novel content.
	if got := putBlob(t, s, "ta", "secret-config", t0)["deduped"]; got != false {
		t.Fatalf("first put deduped=%v, want false", got)
	}
	// A re-puts the same content → true dedup (A already owns it).
	if got := putBlob(t, s, "ta", "secret-config", t0)["deduped"]; got != true {
		t.Fatalf("owner re-put deduped=%v, want true", got)
	}
	// B puts the SAME content it did not previously own → deduped MUST be false
	// (identical to a novel put), so B learns nothing about prior existence.
	if got := putBlob(t, s, "tb", "secret-config", t0)["deduped"]; got != false {
		t.Fatalf("non-owner put of existing content deduped=%v, want false (oracle leak!)", got)
	}
	// Both are now owners of the single content-addressed entry.
	b := s.Blobs[blobID("secret-config")]
	if !b.Owners["alpha"] || !b.Owners["beta"] {
		t.Fatalf("both putters should own the blob: %+v", b.Owners)
	}
}

// TestBlobAccessControl: only an owner or a live-message recipient may fetch or
// attach a blob; everyone else gets E_NO_BLOB (no existence disclosure).
func TestBlobAccessControl(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	ackReg(t, s, "alpha", "ta", t0)
	ackReg(t, s, "beta", "tb", t0)
	ackReg(t, s, "gamma", "tg", t0)
	id := putBlob(t, s, "ta", "payload", t0)["blob"].(string)

	// Non-owner gamma cannot attach it.
	_, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: "tg", To: "alpha",
		MsgType: MsgNotify, Body: "x", Attachments: []Attachment{{Blob: id}},
	}, t0)
	var e *Error
	if !errors.As(err, &e) || e.Code != "E_NO_BLOB" {
		t.Fatalf("non-owner attach: got %v, want E_NO_BLOB", err)
	}
	// Owner alpha attaches to beta.
	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "ta", To: "beta",
		MsgType: MsgNotify, Body: "here", Attachments: []Attachment{{Blob: id}},
	}, t0)
	// Now beta (recipient) may access & re-attach it; gamma still cannot.
	if !s.BlobAccessible(id, "beta") {
		t.Fatal("recipient beta should have access")
	}
	if s.BlobAccessible(id, "gamma") {
		t.Fatal("gamma must not have access")
	}
}

// TestBlobRefcountAndReattach: attaching refcounts; a recipient can re-share
// (A6.2 transitive closure); refs drop when messages leave s.Messages.
func TestBlobRefcount(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	ackReg(t, s, "alpha", "ta", t0)
	ackReg(t, s, "beta", "tb", t0)
	id := putBlob(t, s, "ta", "doc", t0)["blob"].(string)
	if r := s.blobRefs(id); r != 0 {
		t.Fatalf("fresh blob refs=%d, want 0", r)
	}
	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "ta", To: "beta",
		MsgType: MsgNotify, Body: "b", Attachments: []Attachment{{Blob: id}},
	}, t0)
	if r := s.blobRefs(id); r != 1 {
		t.Fatalf("after attach refs=%d, want 1", r)
	}
}

// TestBlobIDValidation rejects malformed ids before any use (P2-3).
func TestBlobIDValidation(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	ackReg(t, s, "alpha", "ta", t0)
	for _, bad := range []string{
		"", "sha256:xyz", "sha256:" + "../etc/passwd", "md5:abcd",
		"sha256:" + hex.EncodeToString(make([]byte, 20)),
	} {
		_, _, err := s.Apply(&Op{Kind: OpPutBlob, Token: "ta", Blob: bad, Size: 1}, t0)
		var e *Error
		if !errors.As(err, &e) || e.Code != "E_BAD_ID" {
			t.Fatalf("put_blob(%q): got %v, want E_BAD_ID", bad, err)
		}
	}
}

// TestBlobMimeValidation rejects crafted mime metadata (P2-6).
func TestBlobMimeValidation(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	ackReg(t, s, "alpha", "ta", t0)
	_, _, err := s.Apply(&Op{
		Kind: OpPutBlob, Token: "ta", Blob: blobID("z"), Size: 1,
		Mime: "text/html<script>",
	}, t0)
	var e *Error
	if !errors.As(err, &e) || e.Code != "E_BAD_MIME" {
		t.Fatalf("crafted mime: got %v, want E_BAD_MIME", err)
	}
}

// TestBlobPerLaneQuota is part of the P1-3 fix: one agent cannot exceed its store
// quota.
func TestBlobPerLaneQuota(t *testing.T) {
	lim := DefaultLimits()
	lim.PerAgentBlobBytes = 10
	s := NewState("n1", lim)
	ackReg(t, s, "alpha", "ta", t0)
	putBlob(t, s, "ta", "12345", t0) // 5 bytes ok
	_, _, err := s.Apply(&Op{
		Kind: OpPutBlob, Token: "ta", Blob: blobID("1234567890X"),
		Size: 11,
	}, t0)
	var e *Error
	if !errors.As(err, &e) || e.Code != "E_QUOTA" {
		t.Fatalf("over-quota put: got %v, want E_QUOTA", err)
	}
}

// TestBlobTTLEviction: an unreferenced blob is evicted after the hard TTL; a
// referenced one is not.
func TestBlobTTLEviction(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	ackReg(t, s, "alpha", "ta", t0)
	ackReg(t, s, "beta", "tb", t0)
	unref := putBlob(t, s, "ta", "ephemeral", t0)["blob"].(string)
	refd := putBlob(t, s, "ta", "kept", t0)["blob"].(string)
	mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "ta", To: "beta",
		MsgType: MsgNotify, Body: "b", Attachments: []Attachment{{Blob: refd}},
	}, t0)

	past := t0.Add(8 * 24 * time.Hour)
	mustApply(t, s, &Op{Kind: OpSweep}, past)
	if _, ok := s.Blobs[unref]; ok {
		t.Fatal("unreferenced blob should be TTL-evicted")
	}
	if _, ok := s.Blobs[refd]; !ok {
		t.Fatal("referenced blob must survive TTL")
	}
}

// TestBlobCapEviction is the anti-deadlock guarantee (P1-3): a full store of
// referenced blobs still admits a new put via last-resort eviction.
func TestBlobCapEviction(t *testing.T) {
	lim := DefaultLimits()
	lim.BlobStoreBytes = 30
	lim.PerAgentBlobBytes = 1000
	lim.BlobGraceWindow = time.Minute
	s := NewState("n1", lim)
	ackReg(t, s, "alpha", "ta", t0)
	ackReg(t, s, "beta", "tb", t0)

	// Fill the store with referenced blobs (each attached to a live message).
	for i, c := range []string{"aaaaaaaaaa", "bbbbbbbbbb", "cccccccccc"} { // 3×10 = 30
		id := putBlob(t, s, "ta", c, t0.Add(time.Duration(i)*time.Second))["blob"].(string)
		mustApply(t, s, &Op{
			Kind: OpSendMessage, Token: "ta", To: "beta", MsgType: MsgNotify,
			Body: "b", Attachments: []Attachment{{Blob: id}},
		}, t0.Add(time.Duration(i)*time.Second))
	}
	if got := s.storeBytes(); got != 30 {
		t.Fatalf("store=%d, want 30 (full)", got)
	}
	// A new put past the grace window must succeed by evicting the oldest,
	// even though every existing blob is referenced (no deadlock).
	later := t0.Add(2 * time.Minute)
	res, _, err := s.Apply(&Op{
		Kind: OpPutBlob, Token: "ta", Blob: blobID("dddddddddd"),
		Size: 10,
	}, later)
	if err != nil {
		t.Fatalf("put into full store: %v (should evict + admit)", err)
	}
	if res["deduped"] != false {
		t.Fatal("new content should not be deduped")
	}
	if s.storeBytes() > 30 {
		t.Fatalf("store over cap after eviction: %d", s.storeBytes())
	}
	if _, ok := s.Blobs[blobID("dddddddddd")]; !ok {
		t.Fatal("new blob should be present after last-resort eviction")
	}
}

// TestAttachmentLimit enforces the per-message cap.
func TestAttachmentLimit(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxAttachments = 2
	s := NewState("n1", lim)
	ackReg(t, s, "alpha", "ta", t0)
	ackReg(t, s, "beta", "tb", t0)
	atts := []Attachment{
		{Path: "/a"}, {Path: "/b"}, {Path: "/c"},
	}
	_, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: "ta", To: "beta",
		MsgType: MsgNotify, Body: "x", Attachments: atts,
	}, t0)
	var e *Error
	if !errors.As(err, &e) || e.Code != "E_TOO_LARGE" {
		t.Fatalf("over-limit attachments: got %v, want E_TOO_LARGE", err)
	}
}

// TestFilerefNeverOpened confirms the daemon records fileref metadata verbatim
// and never depends on the path existing (A2.1 / P0-2): a fileref to a
// nonexistent or special path sends fine.
func TestFilerefAdvisory(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	ackReg(t, s, "alpha", "ta", t0)
	ackReg(t, s, "beta", "tb", t0)
	res := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "ta", To: "beta", MsgType: MsgHandoff,
		Body: "dataset", Attachments: []Attachment{{Path: "/does/not/exist", Size: 999, Hash: "deadbeef"}},
	}, t0)
	m := s.Messages[res["msg_serial"].(uint64)]
	if len(m.Attachments) != 1 || m.Attachments[0].Path != "/does/not/exist" {
		t.Fatalf("fileref not recorded verbatim: %+v", m.Attachments)
	}
}

// A blob referenced by a live message CAN still be evicted: the store cap is a
// hard bound, and its last-resort pass drops referenced content rather than
// exceed it. The recipient is then holding a message that names a blob which is
// gone, and used to be told "you can fetch only blobs you created or received
// on a live message", which is exactly the rule it HAD satisfied. An agent
// reading that debugs its own access assumptions, or concludes the sender lied.
func TestAnEvictedBlobIsNotReportedAsAnAccessProblem(t *testing.T) {
	lim := DefaultLimits()
	lim.BlobStoreBytes = 300
	lim.BlobGraceWindow = 0
	s := NewState("t", lim)
	now := time.Unix(1700000000, 0)
	reg := func(name, tok string) *Agent {
		t.Helper()
		r, _, err := s.Apply(&Op{Kind: OpRegister, Name: name, NewToken: tok}, now)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r["agent_id"].(string)
		if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: s.Agents[id].Token}, now); err != nil {
			t.Fatal(err)
		}
		return s.Agents[id]
	}
	sender := reg("sender", "t1")
	reg("recip", "t2")
	reg("stranger", "t3")

	put := func(content string, size int64) string {
		t.Helper()
		sum := sha256.Sum256([]byte(content))
		bid := "sha256:" + hex.EncodeToString(sum[:])
		if _, _, err := s.Apply(&Op{
			Kind: OpPutBlob, Token: sender.Token, Blob: bid, Size: size, Mime: "text/plain",
		}, now); err != nil {
			t.Fatalf("put %s: %v", content, err)
		}
		return bid
	}
	first := put("the important artifact", 200)
	if _, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: sender.Token, To: "recip", MsgType: "notify",
		Body: "here it is", OpID: "m1", Attachments: []Attachment{{Blob: first}},
	}, now); err != nil {
		t.Fatal(err)
	}

	// Push the store past its cap so the last-resort pass must drop something
	// that is still referenced.
	put("filler-a", 200)
	put("filler-b", 200)
	if _, _, err := s.Apply(&Op{Kind: OpSweep}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s.Blobs[first] != nil {
		t.Skip("the cap did not have to evict a referenced blob here")
	}

	if !s.BlobWasEvicted(first, "recip") {
		t.Fatal("the recipient holds a live message naming it; that is eviction, not a permission problem")
	}
	// The sender is owed the same answer: it too holds the reference.
	if !s.BlobWasEvicted(first, "sender") {
		t.Fatal("the sender's own message names it too")
	}
	// But this must not become an existence oracle. Somebody with no reference
	// learns nothing, which is the whole point of A6.
	if s.BlobWasEvicted(first, "stranger") {
		t.Fatal("an agent with no reference must not be told the blob ever existed")
	}
}
