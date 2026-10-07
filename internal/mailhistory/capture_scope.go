package mailhistory

import "github.com/agenxy/dibs/internal/core"

type mailScope uint8

const (
	scopeNone mailScope = iota
	scopePoint
	scopeListed
	scopeMailbox
	scopeFull
)

// The classification is exhaustive and guarded against every core op kind.
// Unknown future kinds conservatively capture all mail until classified.
func scopeOf(kind string) (mailScope, bool) {
	switch kind {
	case core.OpSweep, core.OpPrune, core.OpPruneOwn,
		core.OpMergeAgents, core.OpAdoptAgent, core.OpSignOff,
		core.OpInitializeReviewRead:
		return scopeFull, true
	case core.OpAckBoard, core.OpSendMessage, core.OpRespond,
		core.OpQueueUpdate, core.OpWithdrawMessage:
		return scopeMailbox, true
	case core.OpAckMessage, core.OpOutcomeRead, core.OpContactEscalate:
		return scopePoint, true
	case core.OpMarkDelivered:
		return scopeListed, true
	case core.OpActivityCheckpoint, core.OpAppRestartObserved, core.OpBindSession,
		core.OpClaim, core.OpClaimCoordinator, core.OpClearSlot,
		core.OpContactNotified, core.OpContactResolved, core.OpForceRelease,
		core.OpGrantPermission, core.OpGrantRole, core.OpHeartbeat,
		core.OpHostRenamed, core.OpPutBlob, core.OpReadAppRestart,
		core.OpRegister, core.OpRelease, core.OpRelocate,
		core.OpRelocateByHuman, core.OpResume, core.OpRevokePermission,
		core.OpSetRestartSetting, core.OpSetSlot, core.OpSpaceAck,
		core.OpSpaceAdmit, core.OpSpaceAnnounce, core.OpSpaceClose,
		core.OpSpaceEvict, core.OpSpaceExclusive, core.OpSpaceForceRelease,
		core.OpSpaceJoin, core.OpSpaceLeave, core.OpSpaceMerge,
		core.OpSpaceOpen, core.OpSpacePost, core.OpSpaceRetitle,
		core.OpSpaceSubscribe, core.OpUpdate, core.OpVouchChild,
		core.OpWake:
		return scopeNone, true
	default:
		return scopeFull, false
	}
}

func (s *Snapshot) prepare(scope mailScope) {
	// A prior sweep must not make every later small op clear a board-sized map.
	if scope == scopeNone || len(s.Mail) > 256 {
		s.Mail = nil
	} else {
		clear(s.Mail)
	}
	if len(s.Authors) > 8 {
		s.Authors = nil
	} else {
		clear(s.Authors)
	}
	if scope != scopeNone && s.Mail == nil {
		s.Mail = make(map[uint64]Metadata)
	}
	if s.Authors == nil {
		s.Authors = make(map[string]Author)
	}
	s.all, s.newMessage, s.prepared = scope == scopeFull, 0, true
}

func (s Snapshot) captureMessage(st *core.State, m *core.Message) {
	if m == nil {
		return
	}
	s.Mail[m.Serial] = stateMetadata(st, m)
	s.captureAuthor(st.Agents[m.From])
	s.captureAuthor(st.Agents[m.To])
}

func (s *Snapshot) captureMailbox(st *core.State, op *core.Op, actor *core.Agent) {
	if op.Kind == core.OpAckBoard {
		if actor != nil {
			for _, m := range st.Inbox(actor.ID) {
				s.captureMessage(st, m)
			}
		}
		return
	}
	recipient := op.To
	if m := st.Messages[op.MsgSerial]; m != nil {
		recipient = m.To
	}
	if op.Kind == core.OpSendMessage {
		s.newMessage = st.Serial + 1
	}
	for _, m := range st.Messages {
		if m.To == recipient {
			s.captureMessage(st, m)
		}
	}
}
