package engine

import (
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type socketBatchMember struct {
	agent           *core.Agent
	mail, announced []string
	offer           socketOffer
}

// All authenticated participants share one mail-first body budget and one
// newest-request-first outcome selection. Only the exact quoted participant
// prefix can be consumed by the existing session's new-turn receipt.
func (e *Engine) socketBatchPresentation(
	agents []*core.Agent, now time.Time,
) ([]string, map[string]socketOffer) {
	budget := mailQuoteBudget
	var members []socketBatchMember
	var groups []outcomeGroup
	wanted := map[string]bool{}
	for _, a := range agents {
		if !e.socketCanPresent(a, now) {
			continue
		}
		announced, announcementKeys := e.dueAnnouncements(a.ID, now)
		_, work := e.dueSocketWaits(a, now)
		offer := socketOffer{
			canConfirm: e.socketLifecycle(a, now) == "idle",
			mail:       e.wakeKeys(a.ID, now), announcements: announcementKeys,
			notices: e.dueNoticeKeys(a.ID, now), work: work, backoff: e.socketBackoff[a.ID],
		}
		for _, key := range offer.notices {
			wanted[key] = true
		}
		members = append(members, socketBatchMember{
			agent: a, mail: e.freshMailQuotedBudget(a.ID, now, &budget), announced: announced, offer: offer,
		})
		groups = append(groups, e.outcomeGroups(a.ID)...)
	}
	updates, prefixes := e.presentGroupedOutcomes(groups, &budget, wanted)
	var texts []string
	presented := map[string]socketOffer{}
	for _, member := range members {
		a := member.agent
		notices := updates[a.ID]
		notices = append(notices, e.presentGenericUpdates(a.ID, &budget, wanted)...)
		text := ""
		if len(member.mail)+len(member.announced)+len(notices) > 0 {
			text = strings.TrimRight(hookDigest(a.ID, member.mail, member.announced, notices), "\n")
		}
		if work := e.socketWorkDigest(a, now); work != "" {
			text = strings.TrimSpace(text + "\n" + work)
		}
		// Every authenticated participant owns the shared reservation, even
		// the requester whose own mailbox currently has no presentation. Its
		// write receipt must settle/release the batch; only quoted prefixes
		// are readable through the separately recorded outcome map.
		member.offer.outcomes = prefixes[a.ID]
		presented[a.ID] = member.offer
		if text == "" {
			continue
		}
		texts = append(texts, text)
	}
	return texts, presented
}
