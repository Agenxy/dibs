package engine

import (
	"context"
	"fmt"
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
	Route      string                  `json:"route"`
	State      string                  `json:"state"`
	RelayCount int                     `json:"relay_count,omitempty"`
	Error      string                  `json:"error,omitempty"`
	Receipts   map[string]humanReceipt `json:"receipts,omitempty"`
}

type humanReceipt struct {
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
	Posted    bool   `json:"posted,omitempty"`
	Dismissed bool   `json:"dismissed,omitempty"`
}

type humanDeliveries struct {
	mu       sync.Mutex
	notifier HumanNotifier
	bySerial map[uint64]humanDelivery
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
		Type: n.Type, From: n.From, Who: n.Who, Body: n.Body,
		Choices: n.Choices, Grant: n.Grant, Adopt: n.Adopt, Serial: n.Serial,
		Receipt: func(state string) { e.recordHumanDelivery(n.Serial, "desktop", state, "") },
	})
	if err != nil {
		e.recordHumanDelivery(n.Serial, "desktop", "failed", err.Error())
		e.report(err)
		return
	}
	if a.Disposition != "" {
		e.respondAsHuman(n.Serial, a.Disposition, a.Body)
	}
}

func (e *Engine) recordHumanDelivery(serial uint64, source, state, failure string) {
	e.humanDelivery.mu.Lock()
	defer e.humanDelivery.mu.Unlock()
	d, ok := e.humanDelivery.bySerial[serial]
	if !ok {
		d = humanDelivery{Route: "relay", State: "queued"}
	}
	if d.Receipts == nil {
		d.Receipts = map[string]humanReceipt{}
	}
	r := d.Receipts[source]
	r.State, r.Error = state, failure
	r.Posted = r.Posted || state == "posted"
	r.Dismissed = r.Dismissed || state == "dismissed"
	d.Receipts[source] = r
	// Each source's last receipt is retained. A failure on one attached Mac
	// must not erase affirmative posting evidence from another.
	d.State, d.Error = "queued", ""
	for _, r := range d.Receipts {
		confirmed := r.State
		if r.Posted && deliveryRank(confirmed) < deliveryRank("posted") {
			confirmed = "posted"
		}
		if r.Dismissed {
			confirmed = "dismissed"
		}
		if deliveryRank(confirmed) > deliveryRank(d.State) ||
			(confirmed == d.State && r.Error < d.Error) {
			d.State, d.Error = confirmed, r.Error
		}
	}
	if e.humanDelivery.bySerial == nil {
		e.humanDelivery.bySerial = map[uint64]humanDelivery{}
	}
	e.humanDelivery.bySerial[serial] = d
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
	if source == "" || len(source) > 128 {
		return fmt.Errorf("invalid relay receipt source")
	}
	if state != "posted" && state != "dismissed" && state != "failed" {
		return fmt.Errorf("unsupported notification receipt %q", state)
	}
	if len(failure) > 4096 {
		return fmt.Errorf("notification error exceeds 4096 bytes")
	}
	if state == "failed" && strings.TrimSpace(failure) == "" {
		return fmt.Errorf("a failed receipt needs its error")
	}
	var invalid error
	_, err := e.query(ctx, func() core.Result {
		m := e.state.Messages[serial]
		if m == nil || m.To != e.humanIdentityLocked() {
			invalid = ErrNotTheHumans
			return nil
		}
		e.humanDelivery.mu.Lock()
		d := e.humanDelivery.bySerial[serial]
		_, known := d.Receipts[source]
		full := !known && len(d.Receipts) >= relayQueue
		e.humanDelivery.mu.Unlock()
		if full {
			invalid = fmt.Errorf("notification receipt source limit reached (64)")
			return nil
		}
		e.recordHumanDelivery(serial, source, state, failure)
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

// On the writer, after read_mail's ordinary authentication and visibility gate.
func (e *Engine) deliveryForHuman(m *core.Message) humanDelivery {
	e.humanDelivery.mu.Lock()
	d, ok := e.humanDelivery.bySerial[m.Serial]
	if d.Receipts != nil {
		copyReceipts := make(map[string]humanReceipt, len(d.Receipts))
		for source, receipt := range d.Receipts {
			copyReceipts[source] = receipt
		}
		d.Receipts = copyReceipts
	}
	e.humanDelivery.mu.Unlock()
	if !ok {
		d = humanDelivery{Route: "unknown", State: "unknown"}
	}
	// The answer is replayable evidence, unlike notification receipts.
	if m.RespondedAt != 0 {
		d.State = "answered"
		d.Error = ""
	}
	return d
}
