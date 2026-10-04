package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type outcomeUnit struct {
	serial     uint64
	text, body string
	at         time.Time
	blocking   bool
}

// Bounded presentations leave the unshown suffix in state for the next read.
const maxInlineOutcomes = 16

type outcomeGroup struct {
	message *core.Message
	units   []outcomeUnit
}

func (e *Engine) outcomeGroups(agent string) []outcomeGroup {
	var groups []outcomeGroup
	for _, m := range e.sentOutcomes(agent) {
		groups = append(groups, outcomeGroup{m, e.outcomeUnits(m)})
	}
	for _, m := range e.receivedReviews(agent) {
		groups = append(groups, outcomeGroup{m, e.reviewUnits(m)})
	}
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		as, bs := a.units[len(a.units)-1].serial, b.units[len(b.units)-1].serial
		if as != bs {
			return as > bs
		}
		return a.message.Serial > b.message.Serial
	})
	return groups
}

func noticeKey(agent string, serial uint64) string {
	return fmt.Sprintf("%s\x00%d", agent, serial)
}

func (e *Engine) presentUpdates(agent string, budget *int, wanted map[string]bool) ([]string, map[uint64]uint64) {
	lines, through := e.presentOutcomes(agent, budget, wanted)
	for _, n := range e.takeNotices(agent) {
		if wanted != nil && !wanted[noticeKey(agent, n.Serial)] {
			continue
		}
		if e.state != nil {
			if m := e.state.Messages[n.Msg]; m != nil && m.From == agent && m.Expecting() {
				continue // already rendered in oldest-prefix order above
			}
			if m := e.state.Messages[n.Msg]; m != nil && m.To == agent && noticeIsReview(m, n.Serial) {
				continue
			}
		}
		lines = append(lines, e.presentOtherNotice(agent, n, budget))
	}
	return lines, through
}

// The withdrawal receipt is the recipient's, and its durable Consumed bit
// still requires ack. Quoting a reason never acknowledges the withdrawal.
func (e *Engine) presentOtherNotice(agent string, n notice, budget *int) string {
	if e.state == nil {
		return n.Text
	}
	m := e.state.Messages[n.Msg]
	l := e.state.Agents[agent]
	if m == nil || l == nil || m.To != agent ||
		(l.CreatedSerial > 0 && m.Serial < l.CreatedSerial && !e.state.AdoptedFor(m, agent)) {
		return n.Text
	}
	if m.State == core.MsgStateWithdrawn {
		quote, _ := e.quoteText(m.Serial, m.WithdrawalReason, budget)
		return n.Text + ": " + quote
	}
	for _, p := range m.Progress {
		if p.Serial == n.Serial && p.Review != "" {
			quote, _ := e.quoteText(m.Serial, p.Note, budget)
			return n.Text + ": " + quote
		}
	}
	return n.Text
}

func (e *Engine) pullUpdates(l *core.Agent, now time.Time) ([]string, error) {
	budget := mailQuoteBudget
	// Pull envelopes keep their historical full bodies. Charge their digest
	// shares before adding inline outcomes, just as the hook/socket renderer.
	for _, m := range e.state.Inbox(l.ID) {
		_, _ = e.quoteText(m.Serial, m.Body, &budget)
	}
	lines, through := e.presentUpdates(l.ID, &budget, nil)
	return lines, e.consumeOutcomes(l.ID, through, now)
}

// Authoritative envelopes, not event-ring bodies or a stale cached pointer.
// Newest request first; each request's retained unread prefix is oldest first.
func (e *Engine) sentOutcomes(agent string) []*core.Message {
	if e.state == nil {
		return nil
	}
	l := e.state.Agents[agent]
	if l == nil || l.Retired() {
		return nil
	}
	var out []*core.Message
	for _, m := range e.state.Messages {
		// Preserve the old read-all meaning before the recorded upgrade epoch.
		// New bounded deliveries cannot use the board awareness watermark.
		if e.hasReadLegacyOutcome(l, m) {
			continue
		}
		if m.From == agent && m.Expecting() && m.HasUnreadOutcome() &&
			(l.CreatedSerial == 0 || m.Serial >= l.CreatedSerial) && len(e.outcomeUnits(m)) > 0 {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LatestOutcomeSerial() != out[j].LatestOutcomeSerial() {
			return out[i].LatestOutcomeSerial() > out[j].LatestOutcomeSerial()
		}
		return out[i].Serial > out[j].Serial
	})
	return out
}

func (e *Engine) hasReadLegacyOutcome(l *core.Agent, m *core.Message) bool {
	latest := m.LatestOutcomeSerial()
	if e.state.ReviewReadCutoff != 0 && latest > e.state.ReviewReadCutoff {
		return false
	}
	return l.AckedSerial >= latest ||
		(m.OutcomeReadAt != 0 && (!m.QueueDebt || m.QueueChangedSerial <= m.OutcomeReadAt))
}

func (e *Engine) outcomeUnits(m *core.Message) []outcomeUnit {
	if m.State == core.MsgStateWithdrawn {
		return nil
	}
	var units []outcomeUnit
	if verdictEvent(m.State) != "" && (m.OutcomeReadAt == 0 || m.RespondedAt > m.OutcomeReadAt) {
		units = append(units, outcomeUnit{
			serial: m.RespondedAt, text: e.outcomeHeader(m),
			body: outcomeWords(m.Response, m.Deliverable), at: m.TerminalAt, blocking: true,
		})
	}
	for _, p := range m.Progress {
		if p.Review != "" || p.Serial <= m.OutcomeReadAt {
			continue
		}
		text := fmt.Sprintf("%s reports progress on your request (msg %d)", m.To, m.Serial)
		if p.Milestone > 0 && p.Milestone <= len(m.Milestones) {
			text += fmt.Sprintf(": milestone %d of %d, %q", p.Milestone, len(m.Milestones), m.Milestones[p.Milestone-1])
		}
		units = append(units, outcomeUnit{
			serial: p.Serial, text: text,
			body: outcomeWords(p.Note, p.Artifact), at: p.At,
		})
	}
	if m.QueueDebt && m.QueueChangedSerial > m.OutcomeReadAt && m.QueueChangedSerial != m.RespondedAt {
		units = append(units, outcomeUnit{
			serial: m.QueueChangedSerial, at: m.QueueChangedAt,
			text: fmt.Sprintf("Queue position or priority changed for request %d: position %d, priority %s",
				m.Serial, e.state.QueuePosition(m), m.EffectivePriority()),
		})
	}
	if m.From == m.To {
		units = append(units, e.reviewUnits(m)...)
	}
	sort.Slice(units, func(i, j int) bool { return units[i].serial < units[j].serial })
	return units
}

func outcomeWords(body, artifact string) string {
	if artifact != "" {
		body += " Delivered at " + artifact
	}
	return strings.Join(strings.Fields(body), " ")
}

func (e *Engine) outcomeHeader(m *core.Message) string {
	verb := map[string]string{
		core.MsgStateApproved: "APPROVED your request", core.MsgStateQueued: "queued your request",
		core.MsgStateDenied: "DENIED your request", core.MsgStateDeclined: "declined to answer",
		core.MsgStateAnswered: "answered your question", core.MsgStateDone: "reports your request DONE",
	}[m.State]
	text := fmt.Sprintf("%s %s (msg %d)", m.To, verb, m.Serial)
	if m.State == core.MsgStateQueued {
		text += fmt.Sprintf(" at #%d; accepted for later, not started", e.state.QueuePosition(m))
	}
	if m.State == core.MsgStateApproved && m.Grant != "" {
		text += approvedGrantBriefing(m.Grant)
	}
	if m.State == core.MsgStateApproved && m.Adopt != "" {
		text += fmt.Sprintf(": %q's mail is now delivered to you; call inbox", m.Adopt)
	}
	return text
}

func approvedGrantBriefing(grant string) string {
	if grant == core.PermRelocate {
		return ": you may now move a closed agent to another environment with relocate. " +
			"Calls that failed with E_NOT_PERMITTED will work now"
	}
	return ": you now hold the " + grant + " role. " + staffBriefing(grant)
}

// One shared body budget. A partial quote leaves this request's watermark
// alone beyond its last complete unit: a pointer is not evidence of reading.
func (e *Engine) presentOutcomes(
	agent string, budget *int, wanted map[string]bool,
) (lines []string, through map[uint64]uint64) {
	through = map[uint64]uint64{}
	for _, group := range e.outcomeGroups(agent) {
		if len(lines) >= maxInlineOutcomes {
			break
		}
		quoted, prefix := e.presentOutcomeGroup(agent, group, budget, wanted, maxInlineOutcomes-len(lines))
		lines = append(lines, quoted...)
		if prefix != 0 {
			through[group.message.Serial] = prefix
		}
	}
	return lines, through
}

func (e *Engine) presentOutcomeGroup(
	agent string, group outcomeGroup, budget *int, wanted map[string]bool, limit int,
) (lines []string, through uint64) {
	blocked := false
	for _, u := range group.units {
		if len(lines) >= limit {
			break
		}
		if wanted != nil && !wanted[noticeKey(agent, u.serial)] {
			blocked = true // never read through an unquoted earlier unit
			continue
		}
		quote, full := e.quoteOutcome(group.message.Serial, u.body, budget, blocked)
		lines = append(lines, u.text+": "+quote)
		if full {
			through = u.serial
		} else {
			blocked = true
		}
	}
	return lines, through
}

func (e *Engine) quoteOutcome(serial uint64, body string, budget *int, blocked bool) (string, bool) {
	quote, full := "", false
	if !blocked {
		quote, full = e.quoteText(serial, body, budget)
	}
	if quote == "" && !full {
		quote = fmt.Sprintf("read_mail(%d) has the response", serial)
	}
	return quote, full
}

func (e *Engine) consumeOutcomes(agent string, through map[uint64]uint64, now time.Time) error {
	l := e.state.Agents[agent]
	if l == nil {
		return nil
	}
	serials := make([]uint64, 0, len(through))
	for serial := range through {
		serials = append(serials, serial)
	}
	sort.Slice(serials, func(i, j int) bool { return serials[i] < serials[j] })
	for _, serial := range serials {
		// This is the already-authorized own-session/pull path. Engine ingress
		// strips AgentID from caller ops; only this trusted path supplies it.
		if _, err := e.applyAndLedger(&core.Op{
			Kind: core.OpOutcomeRead, AgentID: l.ID,
			MsgSerial: serial, OutcomeThroughSerial: through[serial],
		}, now); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) quoteText(serial uint64, body string, budget *int) (string, bool) {
	if !e.mailBodies() {
		return "", false
	}
	body = strings.Join(strings.Fields(body), " ")
	if body == "" {
		return "", true
	}
	if *budget <= 0 {
		return "", false
	}
	room := min(*budget, mailQuoteEach)
	n := len([]rune(body))
	*budget -= min(room, n)
	if n > room {
		return fmt.Sprintf("%q (trimmed; read_mail(%d) for the rest).", trimRunes(body, room), serial), false
	}
	return fmt.Sprintf("%q.", body), true
}
