package engine

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type nonceNameRecovery struct {
	id, name string
}

// Check the name BEFORE the existing recovery register can rotate a token,
// wake a row or change a session. Only the nonce proves which identity returns.
// The historical register fold still receives its original, current name.
func (e *Engine) prepareNonceName(op *core.Op) (*nonceNameRecovery, error) {
	if op.Kind != core.OpRegister || op.HumanMint {
		return nil, nil
	}
	id := e.state.Nonces[op.Nonce]
	l := e.state.Agents[id]
	if op.Nonce == "" || l == nil {
		return nil, nil
	}
	next := op.Name
	if next == "" {
		next = l.Name
	}
	if _, err := core.AdmitNameChange(e.state, e.nameAliases, l, &core.Op{Kind: core.OpUpdate, Name: next}); err != nil {
		return nil, err
	}
	op.Name = l.Name
	return &nonceNameRecovery{id: id, name: next}, nil
}

func (e *Engine) decorateAgentNames(row map[string]any, id string) {
	if names := e.nameAliases.Names(e.state, id); len(names) > 0 {
		row["name_aliases"] = names
	}
}

// Resolve omitted descriptions before admission. An already-absent release
// that asks for no other change returns before wakeIfSleeping, so it cannot
// accidentally allocate an activation record or report a fictitious release.
func (e *Engine) prepareAliasUpdate(op *core.Op, actor *core.Agent) (core.Result, error) {
	if op.Kind != core.OpUpdate {
		return nil, nil
	}
	if op.KeepDescription {
		op.Description = actor.Description
	}
	requested := op.ReleaseNames != nil
	released, err := core.AdmitNameChange(e.state, e.nameAliases, actor, op)
	if err != nil {
		return nil, err
	}
	op.ReleaseNames = released
	if requested && len(released) == 0 && aliasReleaseOnly(op, actor) {
		return core.Result{
			"ok": true, "id": actor.ID, "name": actor.Name,
			"description": actor.Description, "name_aliases": e.nameAliases.Names(e.state, actor.ID),
			"released_names": []string{}, "changed": false, "serial": e.state.Serial,
			"names": "none of these former names is retained by you; nothing changed or was recorded",
		}, nil
	}
	return nil, nil
}

func aliasReleaseOnly(op *core.Op, actor *core.Agent) bool {
	// A bridge may repeat the very same current binding. A different or guessed
	// binding has an effect of its own and must pass through the normal update.
	sameSession := op.SessionAlias == "" || (!op.SessionGuessed && op.SessionAlias == actor.CurrentSession)
	return (op.Name == "" || op.Name == actor.Name) && op.Description == actor.Description &&
		!actor.IdentityWouldChange(op.Agent) && !op.NoProcess && !op.ReleaseSession && sameSession
}

// Both records execute on the same writer, after pre-admission and the
// existing register guards. The second op authenticates with the returned
// token and uses the ordinary update fold. A crash between them leaves the
// same identity attached under its old name; presenting the nonce retries it.
func (e *Engine) finishNonceName(plan *nonceNameRecovery, res core.Result, now time.Time) error {
	if plan == nil {
		return nil
	}
	l := e.state.Agents[plan.id]
	if l.Name != plan.name {
		token, _ := res["token"].(string)
		update := &core.Op{
			Kind: core.OpUpdate, Token: token, Name: plan.name,
			Description: l.Description, V7Semantics: true,
		}
		updated, err := e.applyAndLedger(update, now)
		if err != nil {
			return err
		}
		for _, key := range []string{"renamed_from", "address"} {
			res[key] = updated[key]
		}
	}
	res["via"], res["name"] = "nonce", l.Name
	res["serial"], res["board"] = e.state.Serial, e.decoratedBoard()
	res["name_aliases"] = e.nameAliases.Names(e.state, l.ID)
	res["name_note"] = "reattached as " + l.ID + "; the nonce recovers this identity and its mailbox. " +
		"Use the token returned here; a supplied name revises its label, and omitted name keeps it"
	return nil
}

func (e *Engine) observeNameAliases(op *core.Op, events []core.Event) {
	if e.nameAliases == nil {
		e.nameAliases = core.NewAgentNameAliases(e.state, nil)
	}
	e.nameAliases.Observe(e.state, events)
	if op.Kind == core.OpSweep || op.Kind == core.OpPrune {
		e.nameAliases.Prune(e.state)
	}
}
