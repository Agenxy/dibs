package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// The human relay: the person's own Mac, attached to a board that runs
// somewhere else. docs/NETWORK.md §8.
//
// While one is attached, mail to the person goes to it instead of to this
// machine's screen, because this machine may be a server in a room nobody is
// in. The relay shows it on the person's Mac and answers through the same op
// every other answer uses (respondAsHuman), so an agent cannot tell, or need
// to tell, which screen the answer came from.

// HumanNotice is one message for the person, as a relay shows it.
type HumanNotice struct {
	Serial  uint64               `json:"serial"`
	Type    string               `json:"type"`
	From    string               `json:"from"`
	Who     string               `json:"who,omitempty"`
	Body    string               `json:"body"`
	Choices []string             `json:"choices,omitempty"`
	Grant   string               `json:"grant,omitempty"`
	Adopt   string               `json:"adopt,omitempty"`
	Node    string               `json:"node,omitempty"`
	Cleanup *NotificationCleanup `json:"notification_cleanup,omitempty"`
}

// Privileged is whether approving this grants something: a role, a
// permission, or another agent's mail. Approving one needs a fresh signature.
func (n HumanNotice) Privileged() bool { return n.Grant != "" || n.Adopt != "" }

// relayQueue bounds what one relay may fall behind by. A relay that cannot
// keep up loses notices, never the mail: everything is still on the board.
const relayQueue = 64

type humanRelays struct {
	mu   sync.Mutex
	next int
	subs map[int]chan HumanNotice
}

// AttachHumanRelay registers a relay and returns its feed and a detach func.
func (e *Engine) AttachHumanRelay() (<-chan HumanNotice, func()) {
	r := &e.relays
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.subs == nil {
		r.subs = map[int]chan HumanNotice{}
	}
	id := r.next
	r.next++
	ch := make(chan HumanNotice, relayQueue)
	r.subs[id] = ch
	return ch, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if c, ok := r.subs[id]; ok {
			delete(r.subs, id)
			close(c)
		}
	}
}

// HumanRelays is how many relays are attached right now.
func (e *Engine) HumanRelays() int {
	e.relays.mu.Lock()
	defer e.relays.mu.Unlock()
	return len(e.relays.subs)
}

// enqueueHumanNotice hands a notice to every attached relay and counts
// those that took it. Never blocks: a full relay is skipped and says so in the log.
func (e *Engine) enqueueHumanNotice(n HumanNotice) int {
	r := &e.relays
	r.mu.Lock()
	defer r.mu.Unlock()
	took := 0
	for id, ch := range r.subs {
		select {
		case ch <- n:
			took++
		default:
			slog.Warn("a human relay is not keeping up; this notice skipped it, the mail is on the board",
				"relay", id, "msg", n.Serial)
		}
	}
	return took
}

// PendingForHuman is the person's open questions and requests, for a relay
// that has just attached: what arrived while nothing was there to show it.
func (e *Engine) PendingForHuman(ctx context.Context) ([]HumanNotice, error) {
	var out []HumanNotice
	_, err := e.query(ctx, func() core.Result {
		human := e.humanIdentityLocked()
		if human == "" {
			return nil
		}
		for _, m := range e.state.Messages {
			if m.To != human || m.Terminal() {
				continue
			}
			if m.Type != core.MsgQuestion && m.Type != core.MsgRequest {
				continue
			}
			out = append(out, e.noticeOf(m))
		}
		return nil
	})
	slices.SortFunc(out, func(a, b HumanNotice) int { return cmp.Compare(a.Serial, b.Serial) })
	return out, err
}

// noticeOf renders one stored message as a notice. On the loop.
func (e *Engine) noticeOf(m *core.Message) HumanNotice {
	n := HumanNotice{
		Serial: m.Serial, Type: m.Type, From: m.From, Body: m.Body,
		Choices: m.Choices, Grant: m.Grant, Adopt: m.Adopt,
		Node: e.state.NodeID,
	}
	if a := e.state.Agents[m.From]; a != nil {
		n.Who = whoIs(a)
	}
	return n
}

// HumanNoticeFor returns one message addressed to the person, open or not,
// so an answer can be checked against what it answers.
func (e *Engine) HumanNoticeFor(ctx context.Context, serial uint64) (HumanNotice, bool, error) {
	var (
		n     HumanNotice
		found bool
		open  bool
	)
	_, err := e.query(ctx, func() core.Result {
		m := e.state.Messages[serial]
		if m == nil || m.To == "" || m.To != e.humanIdentityLocked() {
			return nil
		}
		found, open, n = true, !m.Terminal(), e.noticeOf(m)
		return nil
	})
	if err != nil {
		return HumanNotice{}, false, err
	}
	if !found {
		return HumanNotice{}, false, ErrNotTheHumans
	}
	return n, open, nil
}

// ErrNotTheHumans is a serial that is not a message to the person.
var ErrNotTheHumans = errors.New("no message to the human has that serial")

// AnswerAsHuman records a relay's answer, synchronously, so the relay learns
// whether it landed.
func (e *Engine) AnswerAsHuman(ctx context.Context, serial uint64, disposition, body string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, token, err := e.HumanAgent(ctx)
	if err != nil {
		return fmt.Errorf("could not act as the human: %w", err)
	}
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRespond, Token: token, MsgSerial: serial,
		Disposition: disposition, Body: body,
	})
	if err != nil {
		return err
	}
	if ce, ok := res["error"].(*core.Error); ok && ce != nil {
		return ce
	}
	return nil
}
