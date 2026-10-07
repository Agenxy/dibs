// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestUncommittedRegistrationReleasesItsRequestHold(t *testing.T) {
	for _, path := range []string{"commit", "direct"} {
		for _, refusal := range []string{"before-enqueue", "invitation", "identity"} {
			t.Run(path+"/"+refusal, func(t *testing.T) {
				store := newPausedReconcileStore(t)
				e, token, ctx := transferEngineFixture(t, store)
				t.Cleanup(store.resume)
				size := int64(8)
				identity, err := e.AuthorizeUpload(ctx, token, hashHex([]byte("12345678")), &size)
				if err != nil {
					t.Fatal(err)
				}
				blob, _, err := store.Put([]byte("12345678"), 8)
				if err != nil {
					t.Fatal(err)
				}
				call := ctx
				var unblock func()
				switch refusal {
				case "before-enqueue":
					// Make enqueue impossible, not a race between two ready
					// select cases with an already-canceled context.
					entered, proceed, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
					go func() {
						defer close(finished)
						_, _ = e.query(ctx, func() core.Result {
							close(entered)
							<-proceed
							return nil
						})
					}()
					unblock = func() { close(proceed); <-finished }
					defer unblock()
					select {
					case <-entered:
					case <-time.After(2 * time.Second):
						t.Fatal("writer did not enter blocking setup")
					}
					var cancel context.CancelFunc
					call, cancel = context.WithCancel(ctx)
					cancel()
				case "invitation":
					// Refused before CommitTransfer's closure is entered.
					call = WithInvitation(ctx, Invitation{IssuedBy: "missing-issuer"})
				case "identity":
					identity.Created++
					token = "invalid-token"
				}
				if path == "commit" {
					_, err = e.CommitTransfer(call, identity, blob, 8, "text/plain")
				} else {
					_, err = e.Do(call, &core.Op{Kind: core.OpPutBlob, Token: token, Blob: blob, Size: 8})
				}
				if err == nil || (refusal == "before-enqueue" && !errors.Is(err, context.Canceled)) {
					t.Fatalf("refusal setup did not fail as expected: %v", err)
				}
				store.Release(blob) // the caller still owns its original hold
				if refusal != "before-enqueue" {
					// Receipt can race the deferred writer cleanup. A subsequent
					// actual query observes completion before checking for leaks.
					if _, err := e.query(ctx, func() core.Result { return nil }); err != nil {
						t.Fatal(err)
					}
				}
				if n, err := store.Store.Reconcile(map[string]bool{}); err != nil || n != 1 {
					t.Fatalf("uncommitted request leaked protection: removed %d: %v", n, err)
				}
			})
		}
	}
}
