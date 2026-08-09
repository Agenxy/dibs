package core

import (
	"errors"
	"fmt"
)

// Error is a structured, instructive API error. Hint tells a drifted agent
// the corrective call — protocol recovery is built into the error surface.
type Error struct {
	Code string `json:"code"`
	Msg  string `json:"message"`
	Hint string `json:"hint,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Msg) }

// Structured error codes (spec §10).
var (
	// The "if lost" clause used to say "register a fresh lane" — the one action
	// this codebase has measured going wrong. A fresh registration under the same
	// name makes you `yourname-2`: a second lane that cannot read the first one's
	// mail, while the board shows two healthy agents and nothing looks broken.
	// SKILLS.md spends a paragraph on that exact incident (four agents restarted,
	// four became siblings, every message sent to them beforehand stranded), and
	// the error was advising it.
	ErrBadToken = &Error{
		Code: "E_BAD_TOKEN", Msg: "unknown or missing lane token",
		Hint: "pass the token returned by register_lane. If you lost it, register " +
			"again with the SAME name and the SAME nonce — you get your own lane " +
			"back, with its mail. Registering without the nonce makes you a second " +
			"lane that cannot read the first one's mail",
	}
	ErrMustAck = &Error{
		Code: "E_MUST_ACK_BOARD", Msg: "awareness gate: board not acknowledged",
		Hint: "call ack_board() first to see what other agents are doing, then retry",
	}
	ErrMailboxFull = &Error{
		Code: "E_MAILBOX_FULL", Msg: "recipient mailbox is at capacity",
		Hint: "retry later; the recipient has unprocessed messages",
	}
	ErrLaneLimit = &Error{
		Code: "E_LANE_LIMIT", Msg: "maximum number of lanes reached",
		Hint: "wait for stale lanes to be archived or ask the human to raise limits",
	}
	ErrRateLimited = &Error{
		Code: "E_RATE_LIMITED", Msg: "rate limit exceeded",
		Hint: "back off briefly and retry",
	}
	ErrBadID = &Error{
		Code: "E_BAD_ID", Msg: "malformed blob id",
		Hint: "a blob id is 'sha256:' + 64 lowercase hex chars, exactly as returned by put_blob",
	}
	// Distinguished from ErrBadID because the two have different causes and the
	// wrong one sends you looking in the wrong place. An empty id almost always
	// means the argument never arrived — a misspelled parameter name, which
	// models do constantly (get_blob takes `blob`, and `blob_id` is the common
	// miss). Lecturing about hex format in that case is actively misleading.
	ErrNoID = &Error{
		Code: "E_BAD_ID", Msg: "no blob id given",
		Hint: "pass the id in the 'blob' argument (get_blob takes 'blob', not 'blob_id')",
	}
	ErrBadMime = &Error{
		Code: "E_BAD_MIME", Msg: "malformed or oversize mime type",
		Hint: "use a simple type/subtype such as application/json or image/png",
	}
	ErrNoBlob = &Error{
		Code: "E_NO_BLOB", Msg: "no such blob accessible to you",
		Hint: "you can fetch only blobs you created or received on a live message",
	}
	// Distinct from ErrNoBlob because the difference is the whole diagnosis.
	//
	// A blob referenced by a live message CAN still be evicted: the store cap is
	// a hard bound, and its last-resort pass drops referenced content rather
	// than exceed it. The recipient then holds a message naming a blob that is
	// gone — and used to be told "you can fetch only blobs you created or
	// received on a live message", which is precisely the rule it had satisfied.
	// An agent reading that debugs its own access assumptions, or concludes the
	// sender lied.
	//
	// Safe to distinguish: this is only ever returned to a caller that already
	// holds the reference in its own mail, so it reveals nothing (A6's oracle
	// concern is about strangers, not recipients).
	ErrBlobEvicted = &Error{
		Code: "E_BLOB_EVICTED", Msg: "that blob was evicted to keep the store under its cap",
		Hint: "your message still names it, and your access was never the problem — the " +
			"content is gone. Ask the sender to put_blob it again, or to send the data another way",
	}
	ErrQuota = &Error{
		Code: "E_QUOTA", Msg: "per-lane blob quota exceeded",
		Hint: "let old blobs age out, or attach fewer/smaller blobs",
	}
	ErrStoreFull = &Error{
		Code: "E_STORE_FULL", Msg: "blob store is full",
		Hint: "retry shortly; the store evicts unreferenced blobs under pressure",
	}
	ErrNotAdmin = &Error{
		Code: "E_NOT_ADMIN",
		Msg:  "this action needs the admin role",
		Hint: "admin can read every lane's mail, so only a human grants it: `lanes admin admin <lane>`",
	}
	ErrNotCoordinator = &Error{
		Code: "E_NOT_COORDINATOR",
		Msg:  "this action needs the coordinator role",
		Hint: "a human grants it with `lanes admin coordinator <lane>`; lanes cannot promote themselves",
	}
	ErrBlobUnavailable = &Error{
		Code: "E_BLOB_UNAVAILABLE", Msg: "blob bytes are no longer available",
		Hint: "the blob was evicted under retention bounds; ask the sender to re-put it",
	}
)

// ErrTooLargeBlob reports an oversize blob against the byte cap.
func ErrTooLargeBlob(limit int) *Error {
	return errf(
		"E_TOO_LARGE", "put a smaller blob, or use a fileref for large local files", "blob exceeds the %d-byte limit",
		limit,
	)
}

func errf(code, hint, format string, a ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...), Hint: hint}
}

func errTooLarge(what string, limit int) *Error {
	return errf("E_TOO_LARGE", "shorten the field", "%s exceeds the %d-byte limit", what, limit)
}

// ErrCursorTooOld carries the SPEC §10 checkpoint recovery protocol.
func ErrCursorTooOld(floor uint64) *Error {
	return errf("E_CURSOR_TOO_OLD",
		"call ack_board() for an atomic {board, inbox, serial} checkpoint and resume polling from its serial",
		"cursor precedes the event ring floor (%d)", floor)
}

// ErrNoMessage explains absence honestly: pruned mail is detectable via the
// recipient's truncation watermark (SPEC §8).
func ErrNoMessage(serial, truncatedBefore uint64) *Error {
	hint := "check the serial; use inbox() to list your mail"
	if serial < truncatedBefore {
		hint = "this serial precedes your truncated_before_serial watermark — the message was evicted under retention bounds"
	}
	return errf("E_NO_MESSAGE", hint, "no accessible message %d", serial)
}

// Port-level sentinels for blob storage. They live in core so the engine can
// depend on the BlobStore *port* without importing a concrete adapter.
var (
	// ErrBlobMissing: the registry says a blob exists but its bytes are gone.
	ErrBlobMissing = errors.New("blob bytes unavailable")
	// ErrBlobTooLarge: the source exceeded the byte cap during staging.
	ErrBlobTooLarge = errors.New("blob exceeds size limit")
)

// ErrWrongKind is for a serial that exists but is not the kind of thing the
// caller asked for. Names what it IS and which tool reads it, rather than
// reporting the absence of what was asked for — a serial the caller got from a
// wake nudge is not a serial they invented, and "no such message" sends them
// looking for a deletion that never happened.
func ErrWrongKind(serial uint64, lane string) *Error {
	return errf("E_NOT_A_MESSAGE",
		"read it with lane_read on lane "+lane,
		"serial %d is an announcement in lane %q, not a message — get_message reads "+
			"direct mail, lane_read reads a lane's announcements", serial, lane)
}
