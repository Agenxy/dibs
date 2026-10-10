// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"strconv"
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
		announced, announcementKeys := e.socketAnnouncements(a.ID, now)
		offer := socketOffer{
			canConfirm: e.socketLifecycle(a, now) == "idle",
			mail:       e.unwrittenSocketKeys("mail:", e.wakeKeys(a.ID, now)), announcements: announcementKeys,
			notices: e.unwrittenSocketKeys("notice:", e.dueNoticeKeys(a.ID, now)),
		}
		for _, key := range offer.notices {
			wanted[key] = true
		}
		mail, fyis := e.socketMailPresentation(a.ID, now, offer.mail, &budget)
		offer.fyis = fyis
		members = append(members, socketBatchMember{
			agent: a, mail: mail, announced: announced, offer: offer,
		})
		groups = append(groups, e.outcomeGroups(a.ID)...)
	}
	selectSocketNoticeKeys(groups, wanted)
	updates, prefixes := e.presentGroupedOutcomes(groups, &budget, wanted)
	var texts []string
	presented := map[string]socketOffer{}
	for _, member := range members {
		a := member.agent
		notices := updates[a.ID]
		notices = append(notices, e.presentGenericUpdates(a.ID, &budget, wanted)...)
		text := ""
		if len(member.mail)+len(member.announced)+len(notices) > 0 {
			text = strings.TrimRight(e.mailDigest(a.ID, member.mail, member.announced, notices), "\n")
		}
		// Every authenticated participant owns the shared reservation, even
		// the requester whose own mailbox currently has no presentation. Its
		// write receipt must settle/release the batch; only quoted prefixes
		// are readable through the separately recorded outcome map.
		member.offer.outcomes = prefixes[a.ID]
		member.offer.notices = selectedSocketNoticeKeys(member.offer.notices, wanted)
		presented[a.ID] = member.offer
		if text == "" {
			continue
		}
		texts = append(texts, text)
	}
	return texts, presented
}

func (e *Engine) socketMailLines(agent string, now time.Time, keys []string, budget *int) []string {
	lines, _ := e.socketMailPresentation(agent, now, keys, budget)
	return lines
}

func (e *Engine) socketMailPresentation(
	agent string, now time.Time, keys []string, budget *int,
) ([]string, []fyiPresentation) {
	wanted := map[uint64]bool{}
	for _, key := range keys {
		_, raw, _ := strings.Cut(key, "\x00")
		serial, _ := strconv.ParseUint(raw, 10, 64)
		wanted[serial] = true
	}
	return e.mailPresentationForBudget(agent, now, wanted, budget)
}

func (e *Engine) socketAnnouncements(agent string, now time.Time) ([]string, []string) {
	lines, keys := e.dueAnnouncements(agent, now)
	var out, selected []string
	for i, key := range keys {
		if !e.socketWritten["announcement:"+key] {
			out, selected = append(out, lines[i]), append(selected, key)
		}
	}
	return out, selected
}

// The formatter's bounded selection is also the write-receipt selection.
// Keep skipped older units in the groups so an unconfirmed earlier write
// cannot be promoted to durable read evidence by a newer partial quote.
func selectSocketNoticeKeys(groups []outcomeGroup, wanted map[string]bool) {
	sortOutcomeGroups(groups)
	count := 0
	for _, group := range groups {
		for _, unit := range group.units {
			key := noticeKey(group.agent, unit.serial)
			if !wanted[key] {
				continue
			}
			if count >= maxInlineOutcomes {
				delete(wanted, key)
			} else {
				count++
			}
		}
	}
}

func selectedSocketNoticeKeys(keys []string, wanted map[string]bool) []string {
	var out []string
	for _, key := range keys {
		if wanted[key] {
			out = append(out, key)
		}
	}
	return out
}
