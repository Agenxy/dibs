package engine

import (
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/notify"
)

// Off the writer: Focus observations may read files and probe the notifier.
func (e *Engine) humanPresentation() notify.Presentation {
	e.humanDelivery.mu.Lock()
	n := e.humanDelivery.notifier
	e.humanDelivery.mu.Unlock()
	if probe, ok := n.(interface{ Presentation() notify.Presentation }); ok {
		return probe.Presentation()
	}
	return notify.FocusPresentation()
}

func (e *Engine) setHumanPresentation(serial uint64, p notify.Presentation, posting bool) humanDelivery {
	e.humanDelivery.mu.Lock()
	defer e.humanDelivery.mu.Unlock()
	d := e.humanDelivery.bySerial[serial]
	// A later send-response snapshot must not replace posting-time evidence.
	if posting || !d.Posted {
		d.Presentation = p
	}
	if posting {
		d.Posted = true
	}
	e.humanDelivery.bySerial[serial] = d
	if d.Receipts != nil {
		copyReceipts := make(map[string]humanReceipt, len(d.Receipts))
		for key, receipt := range d.Receipts {
			copyReceipts[key] = receipt
		}
		d.Receipts = copyReceipts
	}
	return d
}

func (e *Engine) humanSendPresentation(res core.Result) {
	serial, ok := res["msg_serial"].(uint64)
	if !ok {
		return
	}
	d := e.setHumanPresentation(serial, e.humanPresentation(), false)
	res["human_delivery"] = d
	res["notify_hint"] = "The notification is pending until the notifier confirms posting. The request stays pending; do not assume it was seen. Check read_mail for receipts."
	if d.Posted {
		res["notify_hint"] = "macOS accepted posting; this does not confirm the person saw it. The request stays pending until answered."
	}
	if d.Reason != "" {
		res["notify_hint"] = d.Reason + " Check read_mail for posting receipts."
	}
}
