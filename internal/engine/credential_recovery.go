package engine

import (
	"fmt"
	"log/slog"
	"sort"

	"github.com/agenxy/dibs/internal/core"
)

type credentialRecovery struct {
	index int
	ids   []string
}

// On the writer, before every ordinary nonce/session/human guard. Holding a
// credential is the evidence; the public name, host and session prove nothing.
// No candidate is tried as a register, so unselected rows are never mutated.
func (e *Engine) prepareCredentialRecovery(op *core.Op) (*credentialRecovery, error) {
	if op.RecoveryNonces == nil {
		return nil, nil
	}
	host := ""
	if op.Agent != nil {
		host = op.Agent.HostID
	}
	var winner *core.Agent
	plan := &credentialRecovery{index: -1}
	for i, nonce := range op.RecoveryNonces {
		row := e.state.Agents[e.state.Nonces[nonce]]
		if row == nil || row.Gone() || row.Agent == nil || row.Name != op.Name || host == "" ||
			e.canonicalHost(row.Agent.HostID) != host {
			continue
		}
		plan.ids = append(plan.ids, row.ID)
		if winner == nil || row.CreatedSerial < winner.CreatedSerial ||
			(row.CreatedSerial == winner.CreatedSerial && row.ID < winner.ID) {
			winner, plan.index = row, i
		}
	}
	if winner == nil {
		return nil, &core.Error{
			Code: "E_RECOVERY_UNPROVEN", Msg: "no retained credential matches an eligible identity of this name and host",
			Hint: "keep every stored credential; register with your own explicit nonce or ask the coordinator to recover the original mailbox. Do not retry with a freshly minted nonce",
		}
	}
	op.Nonce = op.RecoveryNonces[plan.index]
	op.RecoveryNonces = nil // no candidate group reaches the ledger or fold
	sort.Strings(plan.ids)
	return plan, nil
}

func (e *Engine) finishCredentialRecovery(plan *credentialRecovery, op *core.Op, res core.Result) {
	if op.Kind != core.OpRegister || res == nil {
		return
	}
	if plan != nil {
		res["recovery_nonce_index"] = plan.index
		res["recovery_ids"] = plan.ids
		if len(plan.ids) > 1 {
			id, _ := res["agent_id"].(string)
			hint := "legacy credentials remain readable; ask the coordinator to adopt or merge the newer identity into " + id
			res["recovery_note"] = hint
			slog.Info("retained credentials resolved to oldest identity", "selected", id, "identities", plan.ids, "hint", hint)
		}
		return
	}
	if res["via"] == "nonce" || op.Agent == nil || op.Agent.HostID == "" {
		return
	}
	id, _ := res["agent_id"].(string)
	var older *core.Agent
	for _, row := range e.state.Agents {
		if row.ID == id || row.Agent == nil || row.Name != op.Name ||
			(row.Status != core.StatusDormant && row.Status != core.StatusStale) ||
			e.canonicalHost(row.Agent.HostID) != op.Agent.HostID {
			continue
		}
		if older == nil || row.CreatedSerial < older.CreatedSerial ||
			(row.CreatedSerial == older.CreatedSerial && row.ID < older.ID) {
			older = row
		}
	}
	if older == nil {
		return
	}
	to := "human"
	live, dormant := e.coordinatorsByLiveness()
	if live != "" || dormant != "" {
		to = "coordinator"
	}
	body := "I lost the recovery credential for " + older.ID + "; please recover its mailbox onto my current identity " + id
	res["recovery_request"] = core.Result{"to": to, "type": "request", "adopt": older.ID, "body": body}
	res["name_note"] = fmt.Sprintf("you registered as %s; %s is %s on this host and its mail remains in its mailbox. With the token returned here, call send(to:%q, type:\"request\", adopt:%q, body:%q), then await approval. A name or claimed session does not authorize recovery", id, older.ID, older.Status, to, older.ID, body)
}
