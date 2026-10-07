// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// transferAdmission owns the reply even when its caller cancels after sending.
// A lost reply must not strand a reservation the manager never received.
func (e *Engine) transferAdmission(ctx context.Context, fn func() core.Result) (core.Result, error) {
	req := request{fn: fn, reply: make(chan reply, 1)}
	if i, ok := InvitationFrom(ctx); ok {
		req.invite = &i
	}
	select {
	case e.ops <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-req.reply:
		return r.res, r.err
	case <-ctx.Done():
		go func() { //nolint:gosec // G118: this cleanup MUST outlive the cancelled authorization request
			r := <-req.reply // the writer accepted this request and always replies
			if i, ok := r.res["identity"].(TransferIdentity); ok && i.Reservation != 0 {
				cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = e.ReleaseTransfer(cleanup, i.Reservation)
			}
		}()
		return nil, ctx.Err()
	}
}

func (e *Engine) stagingDiskSpace(reserved int64) error {
	if e.blobs == nil {
		return nil // adapters are attached before production listeners start
	}
	free, err := e.blobs.FreeBytes()
	if err != nil || reserved < 0 {
		return &core.Error{
			Code: "E_STAGING_SPACE", Msg: "cannot measure staging filesystem space",
			Hint: "check the blob directory and free disk space, then retry",
		}
	}
	// Reserve cipher overhead (one 32-byte tag/header per 64 KiB package),
	// envelopes and a 64 MiB ledger safety margin. Counting already-written
	// staged bytes again is deliberately conservative, not a live-registry cap.
	need := uint64(reserved)
	need += need/1024 + 64*1024*1024
	if free < need {
		return &core.Error{
			Code: "E_STAGING_SPACE", Msg: "insufficient disk space for staging and ledger safety margin",
			Hint: "free disk space on the blob filesystem or request a smaller transfer",
		}
	}
	return nil
}
