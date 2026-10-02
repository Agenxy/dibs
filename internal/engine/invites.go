package engine

import (
	"context"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Invitation is transport-established authority. It never travels in an op:
// ingress checks restrict NEW calls, not the fold of accepted history.
type Invitation struct {
	Name, AgentID, Token string
	IssuedBy             string
	IssuerCreated        uint64
	IssuerClosed         uint64
}
type invitationKey struct{}

// InvitationHostPrefix is transport-established locality, never client input.
const InvitationHostPrefix = "invite:"

// InvitationHost scopes an invited agent's absolute paths to its own host.
func InvitationHost(name string) string { return InvitationHostPrefix + name }

func invitedAgent(a *core.Agent) bool {
	return a != nil && a.Agent != nil && strings.HasPrefix(a.Agent.HostID, InvitationHostPrefix)
}

func (e *Engine) invitedOperation(op *core.Op) bool {
	return (op.Agent != nil && strings.HasPrefix(op.Agent.HostID, InvitationHostPrefix)) ||
		invitedAgent(e.state.AgentByToken(op.Token))
}

// WithInvitation carries verified public-listener authority, never client metadata.
func WithInvitation(ctx context.Context, invite Invitation) context.Context {
	return context.WithValue(ctx, invitationKey{}, invite)
}

// InvitationFrom returns the request's transport-established invitation.
func InvitationFrom(ctx context.Context) (Invitation, bool) {
	i, ok := ctx.Value(invitationKey{}).(Invitation)
	return i, ok
}

// WithInvitationToken narrows an authenticated invitation to a supplied agent token.
func WithInvitationToken(ctx context.Context, token string) context.Context {
	i, ok := InvitationFrom(ctx)
	if !ok {
		return ctx
	}
	i.Token = token
	return WithInvitation(ctx, i)
}

func inviteRefusal(why string) error {
	return &core.Error{
		Code: "E_INVITE_SCOPE", Msg: why,
		Hint: "use the invitation's own name, token and nonce; ask the operator for a separate invite for another agent",
	}
}

// admitInvitation runs on the writer loop immediately before the operation or
// read. Checking before queueing would race another registration or token move.
func (e *Engine) admitInvitation(i *Invitation, op *core.Op) error {
	if i == nil {
		return nil
	}
	if !e.inviteIssuerCurrent(i.IssuedBy, i.IssuerCreated, i.IssuerClosed) {
		return inviteRefusal("the invitation's issuer closed or was removed; its children are revoked")
	}
	if op != nil && (op.Kind == core.OpRegister || op.Kind == core.OpResume) {
		return e.admitInviteRegistration(i, op)
	}
	token := i.Token
	if op != nil {
		token = op.Token
	}
	// A tokenless query is the public board or metadata, not a private read.
	if op == nil && token == "" {
		return nil
	}
	l := e.state.AgentByToken(token)
	if !e.ownsInvitation(i, l) {
		return inviteRefusal("token does not resolve to the invited agent")
	}
	if op != nil && op.Kind == core.OpUpdate && op.Name != "" && op.Name != i.Name {
		return inviteRefusal("an invited agent cannot rename its reserved identity")
	}
	return nil
}

// InviteIssuer is the authenticated issuer's current, ledger-derived identity.
// A token rotation changes none of these; an explicit close changes Closed.
type InviteIssuer struct {
	ID          string
	Created     uint64
	Closed      uint64
	Coordinator bool
}

// InvitationIssuer authenticates a private-listener caller and refuses cloud
// recursion even if the human later gives an invited agent a staff role.
func (e *Engine) InvitationIssuer(ctx context.Context, token string) (InviteIssuer, error) {
	res, err := e.query(ctx, func() core.Result {
		a, refused := e.authRead(token, time.Now())
		if refused != nil {
			return refused
		}
		if invitedAgent(a) {
			return core.Result{"error": inviteRefusal("invited agents cannot issue further invitations")}
		}
		return core.Result{"issuer": InviteIssuer{
			ID: a.ID, Created: a.CreatedSerial,
			Closed: e.inviteClosed[a.ID], Coordinator: a.IsCoordinator(),
		}}
	})
	if err != nil {
		return InviteIssuer{}, err
	}
	i, _ := res["issuer"].(InviteIssuer)
	return i, nil
}

func (e *Engine) inviteIssuerCurrent(id string, created, closed uint64) bool {
	if id == core.HumanActor && created == 0 {
		return true
	}
	a := e.state.Agents[id]
	return a != nil && a.Status != core.StatusClosed && a.CreatedSerial == created && e.inviteClosed[id] == closed
}

// InvitationIssuerCurrent checks access at the HTTP gate, and admitInvitation
// repeats the same decision on the writer immediately before the actual call.
func (e *Engine) InvitationIssuerCurrent(ctx context.Context, id string, created, closed uint64) (bool, error) {
	res, err := e.query(ctx, func() core.Result { return core.Result{"live": e.inviteIssuerCurrent(id, created, closed)} })
	if err != nil {
		return false, err
	}
	live, _ := res["live"].(bool)
	return live, nil
}

// InvitationHasAgent protects retained mailboxes (and an in-flight first
// binding) from access-configuration GC. Paths and directories are irrelevant.
func (e *Engine) InvitationHasAgent(ctx context.Context, name, bound string) (bool, error) {
	res, err := e.query(ctx, func() core.Result {
		for _, a := range e.state.Agents {
			if a.ID == bound || a.ID == name || a.Name == name {
				return core.Result{"present": true}
			}
		}
		return core.Result{"present": false}
	})
	if err != nil {
		return false, err
	}
	present, _ := res["present"].(bool)
	return present, nil
}

// rebuildInviteClosures consumes the FULL replay history, not the bounded
// event ring. Only rows still in state need an index: creation serial prevents
// an old invite attaching to a reused address after purge.
func (e *Engine) rebuildInvitationHistory(history [][]core.Event) {
	if len(history) > 0 {
		e.rebuildInviteClosures(history[0])
	} else {
		e.rebuildInviteClosures(nil)
	}
}

func (e *Engine) rebuildInviteClosures(history []core.Event) {
	e.inviteClosed = map[string]uint64{}
	e.noteInviteClosures(history)
}

func (e *Engine) noteInviteClosures(evs []core.Event) {
	for _, ev := range evs {
		if ev.Type == "agent.purged" {
			delete(e.inviteClosed, ev.Agent)
		}
		// Space closes also use agent.closed, with agent_id naming the space
		// and Agent naming its director. Closing a space is not closing its issuer.
		if ev.Type != "agent.closed" || ev.Data["agent_id"] != nil {
			continue
		}
		if a := e.state.Agents[ev.Agent]; a != nil && a.CreatedSerial <= ev.Serial {
			if e.inviteClosed == nil {
				e.inviteClosed = map[string]uint64{}
			}
			e.inviteClosed[ev.Agent] = max(e.inviteClosed[ev.Agent], ev.Serial)
		}
	}
}

func (e *Engine) ownsInvitation(i *Invitation, l *core.Agent) bool {
	return l != nil && l.Name == i.Name && l.Agent != nil && l.Agent.HostID == InvitationHost(i.Name) &&
		(i.AgentID == "" || l.ID == i.AgentID) && l.ID != e.humanIdentityLocked()
}

func (e *Engine) inviteIdentityAvailable(i *Invitation, nonceID string) bool {
	for _, l := range e.state.Agents {
		if l.ID == nonceID || l.ID == i.AgentID || l.Name == i.Name || l.ID == i.Name {
			if l.ID != nonceID || !e.ownsInvitation(i, l) {
				return false
			}
		}
	}
	return true
}

func (e *Engine) admitInviteRegistration(i *Invitation, op *core.Op) error {
	if op.Kind == core.OpRegister && op.Name != i.Name {
		return inviteRefusal("register must use the invitation's exact name")
	}
	if op.Nonce == "" {
		return inviteRefusal("an invited agent requires a retained random nonce to prevent sibling registrations")
	}
	id := e.state.Nonces[op.Nonce]
	if i.AgentID != "" && id != i.AgentID {
		return inviteRefusal("recover the invite's bound agent with its original nonce")
	}
	if !e.inviteIdentityAvailable(i, id) {
		return inviteRefusal("that name or nonce is held by another identity; no sibling was created")
	}
	if op.Kind == core.OpResume && id == "" {
		return inviteRefusal("resume requires the invitation's existing nonce")
	}
	if e.inviteReservedName(i.Name, i.IssuedBy) {
		return inviteRefusal("invites cannot register a privileged name")
	}
	return nil
}

func (e *Engine) inviteReservedName(name, issuer string) bool {
	if e.privilegedName(name) || name == "human" || name == "dibd" || name == "coordinator" || name == "admin" {
		return true
	}
	if !strings.HasPrefix(name, "dibs-") {
		return false
	}
	parent := e.state.Agents[issuer]
	return parent == nil || invitedAgent(parent) || !strings.HasPrefix(name, parent.ID+"-")
}

// InviteNameAvailable checks both names and stable addresses. It is rechecked
// by admitInvitation on registration, so a local registration cannot race it.
func (e *Engine) InviteNameAvailable(ctx context.Context, name, bound, issuer string) error {
	_, err := e.query(ctx, func() core.Result {
		if e.inviteReservedName(name, issuer) {
			return core.Result{"error": inviteRefusal("that name is privileged or reserved")}
		}
		for _, a := range e.state.Agents {
			if a.Name == name || a.ID == name {
				if bound == "" || !e.ownsInvitation(&Invitation{Name: name, AgentID: bound}, a) {
					return core.Result{"error": inviteRefusal("that name already belongs to an agent")}
				}
			}
		}
		return core.Result{}
	})
	return err
}

// InviteChildPrefixAvailable prevents a broad issuer prefix from squatting on
// another existing agent's narrower namespace. Coordinators use named issuance.
func (e *Engine) InviteChildPrefixAvailable(ctx context.Context, issuer, name string) error {
	_, err := e.query(ctx, func() core.Result {
		for id := range e.state.Agents {
			if id != issuer && strings.HasPrefix(name, id+"-") {
				return core.Result{"error": inviteRefusal("child name enters another agent's ID namespace")}
			}
		}
		return core.Result{}
	})
	return err
}
