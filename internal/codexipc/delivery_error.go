// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package codexipc

import "errors"

// ErrRefused is a matched refusal from the selected owner, not an unknown
// result. It carries no private app error body.
var ErrRefused = errors.New("native app refused the request")

// deliveryError records whether any input-frame byte crossed the socket.
// Initialization, discovery and following are not agent input. A partial
// input write is conservatively submitted: it must never cause a blind retry.
type deliveryError struct {
	err       error
	submitted bool
}

func (e *deliveryError) Error() string { return e.err.Error() }
func (e *deliveryError) Unwrap() error { return e.err }

// BeforeInput proves that this attempt wrote no input-frame bytes. Callers
// may retry within their existing bounds, but may not choose the cold route.
func BeforeInput(err error) bool {
	var e *deliveryError
	return errors.As(err, &e) && !e.submitted
}
