// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import "github.com/agenxy/dibs/internal/core"

// The direct path refuses empty adoption. Request approval has the same check,
// except a recorded transfer satisfying this exact request can approve without
// another move. Without the check the second door ledgered an empty rescue and
// told the requester to read mail it received none of. Authority and own-session
// guards run before this decision; Admit then validates the selected receipt.
func (e *Engine) prepareAdoptionApproval(op *core.Op) error {
	if !op.AdoptAuthorised {
		return nil
	}
	from := e.adoptionSourceOf(op)
	if from == "" {
		return nil
	}
	err := e.refuseEmptyAdoption(from)
	if err == nil {
		return nil
	}
	prior := e.state.CompletedAdoptionOf(e.state.Messages[op.MsgSerial])
	if prior == 0 {
		return err
	}
	op.PriorAdoptionSerial = prior
	return nil
}
