package mcp

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/selfupdate"
)

// Keep framing validation before compatibility admission: an invalid envelope
// gets the existing framing error, not a version error or a notification drop.
func refuseRPCAdmission(w http.ResponseWriter, r *http.Request, req *rpcRequest) bool {
	if err := validEnvelope(req); err != nil {
		writeRPC(w, http.StatusOK, req.ID, nil, err)
		return true
	}
	refusal := invitedGuestContract(r.Context(), r.Header)
	if refusal == nil {
		return false
	}
	if req.ID == nil {
		// A notification has no response channel. Refuse before dispatch,
		// without fabricating an error envelope or leaking body/credentials.
		slog.Debug("invited notification refused", "reason", "guest compatibility floor")
		w.WriteHeader(http.StatusAccepted)
	} else {
		writeRPC(w, http.StatusOK, req.ID, nil, refusal)
	}
	return true
}

// This is per-request and invitation-only. It confers no authority; publicGate
// must already have authenticated the invitation. In particular discovery and
// modern tools/call need no initialize, so no session may cache a passed floor.
func invitedGuestContract(ctx context.Context, headers http.Header) *rpcError {
	if _, invited := engine.InvitationFrom(ctx); !invited || selfupdate.GuestSupportingMinimum == "" {
		return nil
	}
	if _, err := selfupdate.ReleaseForTag(selfupdate.GuestSupportingMinimum); err != nil {
		return &rpcError{
			Code: -32000, Message: "this board was built with an invalid guest contract floor",
			Data: map[string]any{
				"code": "E_GUEST_CONTRACT",
				"hint": "ask the issuer to repair this board build's compiled guest minimum; " +
					"no guest release or recipe edit can satisfy an invalid floor",
			},
		}
	}
	versions := headers.Values(selfupdate.GuestVersionHeader)
	version := ""
	if len(versions) == 1 {
		version = versions[0]
	}
	if err := selfupdate.CheckGuestBridgeVersion(version, selfupdate.GuestSupportingMinimum); err != nil {
		return &rpcError{
			Code: -32000,
			Message: "guest bridge is missing, not a stable release, " +
				"or older than this board's compiled contract floor",
			Data: map[string]any{
				"code": "E_GUEST_VERSION",
				"hint": "ask the issuer for a private recipe for verified tag " + selfupdate.GuestSupportingMinimum +
					" or newer; install that stable guest release and restart the bridge, not the local board",
			},
		}
	}
	return nil
}
