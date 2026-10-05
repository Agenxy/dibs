package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/notify"
)

// HumanNotifier is the local desktop boundary. Configure before starting Run.
// A nil notifier uses this machine's notification implementation.
type HumanNotifier interface {
	Available() bool
	Ask(humanask.Message) (humanask.Answer, error)
}

type humanDelivery struct {
	notify.Presentation
	Posted     bool                    `json:"posted"`
	Route      string                  `json:"route"`
	State      string                  `json:"state"`
	RelayCount int                     `json:"relay_count,omitempty"`
	Error      string                  `json:"error,omitempty"`
	Receipts   map[string]humanReceipt `json:"receipts,omitempty"`
	Cleanup    *notify.Cleanup         `json:"notification_cleanup,omitempty"`
}

type humanReceipt struct {
	label             string           // per-message label; the enrolled device key stays internal
	State             string           `json:"state"`
	Error             string           `json:"error,omitempty"`
	Posted            bool             `json:"posted,omitempty"`
	Dismissed         bool             `json:"dismissed,omitempty"`
	Cleanup           *notify.Cleanup  `json:"notification_cleanup,omitempty"`
	Settings          *notify.Settings `json:"settings"`
	InterruptionLevel string           `json:"interruption_level,omitempty"`
	Shown             string           `json:"shown"`
	Hint              string           `json:"hint,omitempty"`
}

type humanDeliveries struct {
	mu       sync.Mutex
	notifier HumanNotifier
	bySerial map[uint64]humanDelivery
	cleanup  chan NotificationCleanup
}

// SetHumanNotifier supplies a desktop implementation; relay dispatch is unchanged.
func (e *Engine) SetHumanNotifier(n HumanNotifier) {
	e.humanDelivery.mu.Lock()
	defer e.humanDelivery.mu.Unlock()
	e.humanDelivery.notifier = n
}

func (e *Engine) localHumanNotifier() (func() bool, func(humanask.Message) (humanask.Answer, error)) {
	e.humanDelivery.mu.Lock()
	n := e.humanDelivery.notifier
	e.humanDelivery.mu.Unlock()
	if n != nil {
		return n.Available, n.Ask
	}
	return notify.Available, humanask.Ask
}

// On the writer. Nonblocking relay enqueue IS the route decision; counting
// attachments first races detach/full queues and reports a route we did not use.
func (e *Engine) dispatchHuman(res core.Result) {
	serial, ok := res["msg_serial"].(uint64)
	if !ok {
		return
	}
	m := e.state.Messages[serial]
	if m == nil {
		return
	}
	if res["deduplicated"] == true {
		d := e.deliveryForHuman(m)
		res["human_route"], res["human_relay_count"] = d.Route, d.RelayCount
		return // a retry reads the original decision, never posts another alert
	}
	n := e.noticeOf(m)
	count := e.enqueueHumanNotice(n)
	d := humanDelivery{Route: "relay", State: "queued", RelayCount: count}
	available, ask := e.localHumanNotifier()
	if count == 0 {
		d.Route, d.State = "desktop", "pending"
		if !available() {
			d.Route, d.State = "none", "unavailable"
		}
	}
	e.humanDelivery.mu.Lock()
	if e.humanDelivery.bySerial == nil {
		e.humanDelivery.bySerial = map[uint64]humanDelivery{}
	}
	// Bounded by retained mail, and pruned on every dispatch.
	for old := range e.humanDelivery.bySerial {
		if e.state.Messages[old] == nil {
			delete(e.humanDelivery.bySerial, old)
		}
	}
	if d.Route == "desktop" {
		d.Receipts = map[string]humanReceipt{"desktop": {State: "pending", label: "desktop"}}
	}
	e.humanDelivery.bySerial[serial] = d
	e.humanDelivery.mu.Unlock()
	res["human_route"], res["human_relay_count"] = d.Route, count
	if d.Route == "none" {
		res["notified"] = false
		res["notify_hint"] = "mail is on the board; no desktop route is available. " +
			"Enroll and run dibs human-relay on your Mac, or enable this machine's desktop notifications."
		return
	}
	if d.Route == "desktop" {
		go e.askHumanDesktop(n, ask)
	}
}

func (e *Engine) askHumanDesktop(n HumanNotice, ask func(humanask.Message) (humanask.Answer, error)) {
	a, err := ask(humanask.Message{
		Type: n.Type, From: n.From, FromName: n.FromName, Who: n.Who, Body: n.Body,
		Choices: n.Choices, Grant: n.Grant, Adopt: n.Adopt, AdoptName: n.AdoptName,
		Serial: n.Serial, Node: n.Node,
		Receipt: func(state string) {
			if state == "posted" {
				e.setHumanPresentation(n.Serial, e.humanPresentation(), true)
			}
			e.recordDesktopDelivery(n.Serial, state, "")
			if state == "posted" {
				e.cleanupLateHumanPost(n.Serial)
			}
		},
		DeliveryReceipt: func(data notify.ReceiptData) {
			if data.State == "posted" {
				e.setHumanPresentation(n.Serial, e.humanPresentation(), true)
			}
			e.recordHumanReceipt(n.Serial, "desktop", data, "")
			if data.State == "posted" {
				e.cleanupLateHumanPost(n.Serial)
			}
		},
	})
	if err != nil {
		e.recordDesktopDelivery(n.Serial, "failed", err.Error())
		e.report(err)
		return
	}
	if a.Disposition != "" {
		e.respondAsHuman(n.Serial, a.Disposition, a.Body)
	}
}

func (e *Engine) recordDesktopDelivery(serial uint64, state, failure string) {
	e.recordHumanReceipt(serial, "desktop", notify.ReceiptData{State: state}, failure)
}

func (e *Engine) recordHumanReceipt(serial uint64, source string, data notify.ReceiptData, failure string) bool {
	data = data.Normalized()
	state := data.State
	e.humanDelivery.mu.Lock()
	defer e.humanDelivery.mu.Unlock()
	if e.humanDelivery.bySerial == nil {
		e.humanDelivery.bySerial = map[uint64]humanDelivery{}
	}
	d, ok := e.humanDelivery.bySerial[serial]
	if !ok {
		d = humanDelivery{Route: "relay", State: "queued"}
	}
	if d.Receipts == nil {
		d.Receipts = map[string]humanReceipt{}
	}
	r, known := d.Receipts[source]
	if !known {
		if len(d.Receipts) >= relayQueue {
			return false
		}
		r.label = receiptLabel(source, d.Receipts)
	}
	if strings.HasPrefix(state, "cleanup_") {
		r.Cleanup = &notify.Cleanup{State: strings.TrimPrefix(state, "cleanup_"), BestEffort: true, Error: failure}
		d.Receipts[source] = r
		if source == "desktop" {
			d.Cleanup = r.Cleanup
		}
		e.humanDelivery.bySerial[serial] = d
		return true
	}
	r.State, r.Error = state, failure
	if data.Settings != nil {
		r.Settings = data.Settings
	}
	if data.InterruptionLevel == "active" || data.InterruptionLevel == "timeSensitive" {
		r.InterruptionLevel = data.InterruptionLevel
	}
	r.Shown = "unconfirmed"
	r.Hint = r.Settings.Hints(r.InterruptionLevel == "timeSensitive")
	r.Posted = r.Posted || state == "posted"
	d.Posted = d.Posted || r.Posted
	r.Dismissed = r.Dismissed || state == "dismissed"
	d.Receipts[source] = r
	recomputeHumanDelivery(&d)
	e.humanDelivery.bySerial[serial] = d
	return true
}

// Each source's last receipt is retained. A failure on one attached Mac
// must not erase affirmative posting evidence from another.
func recomputeHumanDelivery(d *humanDelivery) {
	d.State, d.Error = "queued", ""
	for _, r := range d.Receipts {
		confirmed := confirmedReceiptState(r)
		if deliveryRank(confirmed) > deliveryRank(d.State) ||
			(confirmed == d.State && r.Error < d.Error) {
			d.State, d.Error = confirmed, r.Error
		}
	}
}

func receiptLabel(source string, receipts map[string]humanReceipt) string {
	if source == "desktop" {
		return "desktop"
	}
	count := len(receipts)
	if _, desktop := receipts["desktop"]; desktop {
		count--
	}
	return "relay-" + strconv.Itoa(count+1)
}

func confirmedReceiptState(r humanReceipt) string {
	if r.Dismissed {
		return "dismissed"
	}
	if r.Posted && deliveryRank(r.State) < deliveryRank("posted") {
		return "posted"
	}
	return r.State
}

func deliveryRank(state string) int {
	switch state {
	case "dismissed":
		return 3
	case "posted":
		return 2
	case "failed":
		return 1
	default:
		return 0
	}
}

// ReportHumanDelivery records a relay's receipt, outside the fold. The HTTP
// caller authenticates the relay session; this boundary validates its subject.
func (e *Engine) ReportHumanDelivery(ctx context.Context, serial uint64, source, state, failure string) error {
	return e.ReportHumanReceipt(ctx, serial, source, notify.ReceiptData{State: state}, failure)
}

// ReportHumanReceipt carries the posting source's versioned native observation.
// Unknown metadata never erases OS acceptance or answers/grants human mail.
func (e *Engine) ReportHumanReceipt(
	ctx context.Context, serial uint64, source string, data notify.ReceiptData, failure string,
) error {
	state := data.State
	if err := validateHumanReceipt(source, state, failure); err != nil {
		return err
	}
	var invalid error
	_, err := e.query(ctx, func() core.Result {
		m := e.state.Messages[serial]
		if m == nil || m.To != e.humanIdentityLocked() {
			invalid = ErrNotTheHumans
			return nil
		}
		if !e.recordHumanReceipt(serial, source, data, failure) {
			invalid = fmt.Errorf("notification receipt source limit reached (64)")
			return nil
		}
		if state == "posted" && humanDecision(m) {
			e.requestHumanCleanup([]uint64{serial})
		}
		return core.Result{"ok": true}
	})
	if err != nil {
		return err
	}
	if invalid == nil && state == "failed" {
		go e.report(fmt.Errorf("human relay notification failed: %s", failure))
	}
	return invalid
}

func validateHumanReceipt(source, state, failure string) error {
	if source == "" || source == "desktop" || len(source) > 128 {
		return fmt.Errorf("invalid relay receipt source")
	}
	if state != "posted" && state != "dismissed" && state != "failed" &&
		state != "cleanup_requested" && state != "cleanup_failed" && state != "cleanup_unsupported" {
		return fmt.Errorf("unsupported notification receipt %q", state)
	}
	if len(failure) > 4096 {
		return fmt.Errorf("notification error exceeds 4096 bytes")
	}
	failureState := state == "failed" || state == "cleanup_failed" || state == "cleanup_unsupported"
	if failureState && strings.TrimSpace(failure) == "" {
		return fmt.Errorf("a failed receipt needs its error")
	}
	return nil
}

// On the writer, after read_mail's ordinary authentication and visibility gate.
func (e *Engine) deliveryForHuman(m *core.Message) humanDelivery {
	e.humanDelivery.mu.Lock()
	d, ok := e.humanDelivery.bySerial[m.Serial]
	if d.Receipts != nil {
		copyReceipts := make(map[string]humanReceipt, len(d.Receipts))
		for _, receipt := range d.Receipts {
			receipt.Settings = receipt.Settings.Normalized()
			receipt.Shown = "unconfirmed"
			receipt.Hint = receipt.Settings.Hints(m.Type == core.MsgRequest || m.Type == core.MsgQuestion)
			copyReceipts[receipt.label] = receipt
		}
		d.Receipts = copyReceipts
	}
	e.humanDelivery.mu.Unlock()
	if !ok {
		d = humanDelivery{Route: "unknown", State: "unknown"}
	}
	// The answer is replayable evidence, unlike notification receipts.
	if m.RespondedAt != 0 {
		d.State = m.State
		d.Error = ""
	}
	return d
}
