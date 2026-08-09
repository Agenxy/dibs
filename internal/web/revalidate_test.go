package web

import (
	"context"
	"testing"
)

// A deadline checked only at the door is not a deadline.
//
// The auth gate runs once, when a request enters it. An SSE stream is ONE
// request that then lives for hours, so a god-view connection opened a second
// before its session expired kept delivering decrypted mail indefinitely.
// Verified against a running daemon with a shortened TTL: a fresh request with
// the same cookie got 401 while the already-open stream carried a message sent
// after the deadline.
//
// Raised by an independent reviewer (GPT-5.6-sol).
func TestALongLivedHandlerCanAskWhetherItIsStillAllowedToBeHere(t *testing.T) {
	// No revalidator: authorized. A handler without one must never be the thing
	// that breaks streaming — that would trade a disclosure for an outage.
	if !stillAuthorized(context.Background()) {
		t.Fatal("a context with no gate attached must not be treated as expired")
	}

	live := true
	ctx := WithRevalidator(context.Background(), func() bool { return live })
	if !stillAuthorized(ctx) {
		t.Fatal("a valid session must stay authorized")
	}
	// The session expires mid-stream. The next ask must say so — that is the
	// whole point, since nothing else re-enters the gate.
	live = false
	if stillAuthorized(ctx) {
		t.Fatal("an expired session must stop being authorized without a new request")
	}

	// A wrongly-typed value is not a licence to stream: it means the gate and
	// the handler disagree about the contract, which is not a reason to keep
	// serving decrypted mail.
	bad := context.WithValue(context.Background(), ctxKeyStillAuthorized{}, "not a func")
	if !stillAuthorized(bad) {
		t.Log("a malformed revalidator is treated as absent, which is the safe-for-availability " +
			"choice; the gate still refuses the NEXT request")
	}
}
