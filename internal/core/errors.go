package core

import (
	"errors"
	"fmt"
)

// Error is a structured, instructive API error. Hint tells a drifted agent
// the corrective call: protocol recovery is built into the error surface.
type Error struct {
	Code string `json:"code"`
	Msg  string `json:"message"`
	Hint string `json:"hint,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Msg) }

// Structured error codes (spec §10).
var (
	// The "if lost" clause used to say "register a fresh agent": the one action
	// this codebase has measured going wrong. A fresh registration under the same
	// name makes you `yourname-2`: a second agent that cannot read the first one's
	// mail, while the board shows two healthy agents and nothing looks broken.
	// SKILLS.md spends a paragraph on that exact incident (four agents restarted,
	// four became siblings, every message sent to them beforehand stranded), and
	// the error was advising it.
	ErrBadToken = &Error{
		Code: "E_BAD_TOKEN", Msg: "unknown or missing agent token",
		Hint: "pass the token returned by register. If you lost it, register " +
			"again with the SAME name and the SAME nonce: you get your own agent " +
			"back, with its mail. Registering without the nonce makes you a second " +
			"agent that cannot read the first one's mail",
	}
	ErrMustAck = &Error{
		Code: "E_MUST_ACK_BOARD", Msg: "awareness gate: board not acknowledged",
		Hint: "call check_in() first to see what other agents are doing, then retry",
	}
	ErrMailboxFull = &Error{
		Code: "E_MAILBOX_FULL", Msg: "recipient mailbox is at capacity",
		Hint: "retry later; the recipient has unprocessed messages",
	}
	ErrAgentLimit = &Error{
		Code: "E_AGENT_LIMIT", Msg: "maximum number of agents reached",
		Hint: "wait for stale agents to be archived or ask the human to raise limits",
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
	// means the argument never arrived: a misspelled parameter name, which
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
	// gone, and used to be told "you can fetch only blobs you created or
	// received on a live message", which is precisely the rule it had satisfied.
	// An agent reading that debugs its own access assumptions, or concludes the
	// sender lied.
	//
	// Safe to distinguish: this is only ever returned to a caller that already
	// holds the reference in its own mail, so it reveals nothing (A6's oracle
	// concern is about strangers, not recipients).
	ErrBlobEvicted = &Error{
		Code: "E_BLOB_EVICTED", Msg: "that blob was evicted to keep the store under its cap",
		Hint: "your message still names it, and your access was never the problem: the " +
			"content is gone. Ask the sender to put_blob it again, or to send the data another way",
	}
	ErrQuota = &Error{
		Code: "E_QUOTA", Msg: "per-agent blob quota exceeded",
		Hint: "let old blobs age out, or attach fewer/smaller blobs",
	}
	ErrStoreFull = &Error{
		Code: "E_STORE_FULL", Msg: "blob store is full",
		Hint: "retry shortly; the store evicts unreferenced blobs under pressure",
	}
	// ErrNoCoordinator answers mail addressed to a role nobody holds.
	//
	// Silence would be worse than an error: the sender would believe it had
	// asked somebody, and wait out a deadline against a mailbox that does not
	// exist.
	ErrNoCoordinator = &Error{
		Code: "E_NO_COORDINATOR",
		Msg:  "nobody holds the coordinator role on this board",
		Hint: "address the human instead: the board row marked `human: true`. If you " +
			"need the role yourself, ask them for it with " +
			"send(to: <that row>, type: \"request\", grant: \"coordinator\")",
	}

	// ErrGrantNeedsHuman refuses a role request addressed to anything but the
	// person. Two agents exchanging requests and approving each other's is
	// self-promotion with one extra participant.
	ErrGrantNeedsHuman = &Error{
		Code: "E_GRANT_NEEDS_HUMAN",
		Msg:  "a role can only be requested from the human",
		Hint: "address it to the board row marked `human: true`. If no row is marked, " +
			"nobody has opened the board on this machine yet and there is nobody to " +
			"ask: send without `grant` and say what you need, or drop it",
	}
	ErrNotAdmin = &Error{
		Code: "E_NOT_ADMIN",
		Msg:  "this action needs the admin role",
		Hint: "admin reads every agent's mail, so unlike coordinator it is never " +
			"granted by approving a notification: send(to: the row marked " +
			"`human: true`, type: \"request\", body: what you need it for) to ask, and " +
			"they run `dibs admin admin <agent>` themselves, on their own machine",
	}
	ErrNotCoordinator = &Error{
		Code: "E_NOT_COORDINATOR",
		Msg:  "this action needs the coordinator role",
		// The corrective call an AGENT can make, first.
		//
		// This named a command only a person can run, at a terminal the agent is
		// not sitting at, which reads as "you cannot do this" rather than as a
		// route. An agent that needs the role has an ordinary way to ask for it:
		// the board carries the human as a row like any other, and a `request`
		// to them raises a notification with Approve on it. Measured on a real
		// board: nobody could broadcast because the coordinator was an agent
		// nobody could log back into, and no hint said what to do next.
		Hint: "ask, and their yes IS the grant: send(to: the board row marked " +
			"`human: true`, type: \"request\", grant: \"coordinator\", body: what you " +
			"need it for). That raises a notification with Approve on it; pressing it " +
			"promotes you and the answer comes back as an ordinary response. You still " +
			"cannot promote yourself: only they can press it",
	}
	ErrBlobUnavailable = &Error{
		Code: "E_BLOB_UNAVAILABLE", Msg: "blob bytes are no longer available",
		Hint: "the blob was evicted under retention bounds; ask the sender to re-put it",
	}
)

// ReportHint is for errors that mean DIBS misbehaved, rather than errors that
// name a corrective call the agent can make.
//
// Every error above tells a drifted agent what to do instead, which is the
// honesty rule in AGENTS.md. An unstructured error has no such answer: nothing
// the agent does differently will help, and it is the only witness to what it
// called and what came back. That is worth a report, and an agent that hit it
// is better placed to write one than anybody who reads the ledger afterwards.
//
// Deliberately NOT attached to the errors above. An agent told to file an issue
// every time it drifts would file issues about its own mistakes, and a tracker
// full of "I forgot to check_in" buries the defects that are real. Asking the
// human first is not deference for its own sake: they know whether this machine's
// work is something they want described in public.
const ReportHint = "this looks like a defect in Dibs rather than something you did, " +
	"and there is no corrective call to make. Ask your human whether to report it at " +
	"https://github.com/Agenxy/dibs/issues with the tool you called, the arguments, and " +
	"what you expected. If they are happy for you to go further, a fix is welcome too: " +
	"AGENTS.md is the map and contributions from agents are read the same as anyone's"

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
		"call check_in() for an atomic {board, inbox, serial} checkpoint and resume polling from its serial",
		"cursor precedes the event ring floor (%d)", floor)
}

// ErrNoMessage explains absence honestly: pruned mail is detectable via the
// recipient's truncation watermark (SPEC §8).
func ErrNoMessage(serial, truncatedBefore uint64) *Error {
	hint := "check the serial; use inbox() to list your mail"
	if serial < truncatedBefore {
		hint = "this serial precedes your truncated_before_serial watermark: the message was evicted under retention bounds"
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
// reporting the absence of what was asked for: a serial the caller got from a
// wake nudge is not a serial they invented, and "no such message" sends them
// looking for a deletion that never happened.
func ErrWrongKind(serial uint64, agent string) *Error {
	return errf("E_NOT_A_MESSAGE",
		"read it with read_space on agent "+agent,
		"serial %d is an announcement in agent %q, not a message: read_mail reads "+
			"direct mail, read_space reads an agent's announcements", serial, agent)
}
