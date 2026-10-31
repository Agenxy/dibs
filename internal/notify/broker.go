// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// A full 32 KiB mail body can expand sixfold under JSON escaping. The v1
// legacy detail stays bounded separately; reply-capable helpers negotiate the
// larger full-text frame before any post is sent.
const brokerFrameLimit = 1024 * 1024

type brokerPost struct {
	Profile       string          `json:"profile,omitempty"`
	Version       int             `json:"v"`
	Type          string          `json:"type"`
	ID            string          `json:"id"`
	Title         string          `json:"title,omitempty"`
	Subtitle      string          `json:"subtitle,omitempty"`
	Excerpt       string          `json:"excerpt,omitempty"`
	Detail        json.RawMessage `json:"detail,omitempty"`
	Buttons       []string        `json:"buttons,omitempty"`
	Until         float64         `json:"until,omitempty"`
	TimeSensitive bool            `json:"timeSensitive,omitempty"`
	ReplyKind     string          `json:"replyKind,omitempty"`
	Choices       []string        `json:"choices,omitempty"`
}

type brokerEvent struct {
	Version int         `json:"v"`
	ID      string      `json:"id"`
	Event   string      `json:"event"`
	Action  string      `json:"action"`
	Receipt ReceiptData `json:"receipt"`
	Error   string      `json:"error"`
	Reply   int         `json:"reply"`
	Text    string      `json:"text"`
	Choice  string      `json:"choice"`
}

// These anonymous pipes are the only answer transport. There is deliberately
// no action command, socket, named pipe, file mailbox or public IPC endpoint.
// The process and pending map are derived; losing them never invents a verdict.
type nativeBroker struct {
	mu      sync.Mutex
	writeMu sync.Mutex
	input   io.WriteCloser
	pending map[string]chan brokerEvent
	done    chan struct{}
	ready   chan error
	err     error
	reply   bool
}

var nativeBrokerProcess struct {
	sync.Mutex
	broker *nativeBroker
}
var unkeyedNotification atomic.Uint64

func currentBroker(h string) (*nativeBroker, error) {
	nativeBrokerProcess.Lock()
	defer nativeBrokerProcess.Unlock()
	if b := nativeBrokerProcess.broker; b != nil {
		select {
		case <-b.done:
		default:
			return b, nil
		}
	}
	b, err := startNativeBroker(h)
	if err == nil {
		nativeBrokerProcess.broker = b
	}
	return b, err
}

func startNativeBroker(h string) (*nativeBroker, error) {
	// #nosec G204 -- bundled executable resolved next to this binary; no user argv.
	cmd := exec.Command(h, "--broker")
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	b := &nativeBroker{
		input: input, pending: make(map[string]chan brokerEvent),
		done: make(chan struct{}), ready: make(chan error, 1),
	}
	if err := cmd.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, err
	}
	go b.read(output, cmd)
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case err := <-b.ready:
		if err != nil {
			_ = input.Close()
			return nil, err
		}
		return b, nil
	case <-timer.C:
		_ = cmd.Process.Kill()
		_ = input.Close()
		return nil, fmt.Errorf("%w: notifier broker startup timed out; read the board and run dibs doctor", ErrCannotNotify)
	}
}

func (b *nativeBroker) read(output io.Reader, cmd *exec.Cmd) {
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), brokerFrameLimit+1)
	var protocolErr error
	for scanner.Scan() {
		var event brokerEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Version != 1 {
			protocolErr = errors.New("invalid notifier broker response; no answer was recorded")
			break
		}
		protocolErr = b.deliver(event)
		if protocolErr != nil {
			break
		}
	}
	b.stopped(cmd, scanner.Err(), protocolErr)
}

func (b *nativeBroker) deliver(event brokerEvent) error {
	if event.Event == "ready" {
		b.mu.Lock()
		b.reply = event.Reply == 1
		b.mu.Unlock()
		select {
		case b.ready <- nil:
		default:
		}
		return nil
	}
	if event.ID == "" && event.Event == "failed" {
		select {
		case b.ready <- fmt.Errorf("%w: %s", ErrCannotNotify, event.Error):
		default:
		}
		return nil
	}
	b.mu.Lock()
	ch := b.pending[event.ID]
	b.mu.Unlock()
	if ch == nil {
		return nil
	}
	// Overflow means the helper is misbehaving. Kill the broker and fail
	// every pending ask rather than silently drop an event for one ID.
	select {
	case ch <- event:
		return nil
	default:
		return errors.New("notifier response queue overflow; read the Dibs board")
	}
}

func (b *nativeBroker) stopped(cmd *exec.Cmd, readErr, protocolErr error) {
	if protocolErr != nil {
		_ = cmd.Process.Kill()
	}
	_ = b.input.Close()
	waitErr := cmd.Wait()
	b.mu.Lock()
	switch {
	case protocolErr != nil:
		b.err = protocolErr
	case readErr != nil:
		b.err = readErr
	default:
		b.err = fmt.Errorf("%w: native notifier broker stopped (%v); "+
			"no answer was recorded; continue on the Dibs board", ErrNoAnswer, waitErr)
	}
	err := b.err
	close(b.done)
	b.mu.Unlock()
	select {
	case b.ready <- fmt.Errorf("%w: %w; run dibs doctor", ErrCannotNotify, err):
	default:
	}
}

func (b *nativeBroker) send(frame brokerPost) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(data) > brokerFrameLimit {
		return errors.New("native notification frame exceeds 1 MiB; read the Dibs board")
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	_, err = b.input.Write(append(data, '\n'))
	return err
}

func brokerNotification(ctx context.Context, h string, post brokerPost, receipt DeliveryReceipt) (string, error) {
	event, err := brokerExchange(ctx, h, post, receipt)
	return event.Action, err
}

func brokerExchange(ctx context.Context, h string, post brokerPost, receipt DeliveryReceipt) (brokerEvent, error) {
	if ctx.Err() != nil {
		return brokerEvent{}, nil
	}
	b, err := currentBroker(h)
	if err != nil {
		return brokerEvent{}, err
	}
	b.mu.Lock()
	supported := b.reply
	b.mu.Unlock()
	if post.ReplyKind != "" && !supported {
		return brokerEvent{}, ErrReplyUnavailable
	}
	if ctx.Err() != nil {
		return brokerEvent{}, nil
	}
	if post.ID == "" {
		post.ID = "dibs.msg.local-" + strconv.Itoa(os.Getpid()) + "." + strconv.FormatUint(unkeyedNotification.Add(1), 10)
	}
	post.Version, post.Type = 1, "post"
	responses := make(chan brokerEvent, 8)
	b.mu.Lock()
	if b.pending[post.ID] != nil {
		b.mu.Unlock()
		return brokerEvent{}, errors.New("this native notification is already pending; continue on the Dibs board")
	}
	b.pending[post.ID] = responses
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.pending, post.ID); b.mu.Unlock() }()
	if err := b.send(post); err != nil {
		return brokerEvent{}, err
	}
	return awaitBrokerReply(ctx, b, post, responses, receipt)
}

func awaitBrokerReply(
	ctx context.Context, b *nativeBroker, post brokerPost, responses <-chan brokerEvent, receipt DeliveryReceipt,
) (brokerEvent, error) {
	for {
		event, err := b.next(ctx, post.ID, responses)
		if errors.Is(err, context.Canceled) {
			return brokerEvent{}, nil
		}
		if err != nil {
			return brokerEvent{}, err
		}
		if event.Event == "reply" {
			if post.ReplyKind == "" {
				return brokerEvent{}, errors.New("typed reply was not negotiated; no verdict was recorded; read the Dibs board")
			}
			if err := validatePromptReply(post, event); err != nil {
				return brokerEvent{}, err
			}
			return event, nil
		}
		choice, finished, err := consumeBrokerEvent(event, post, receipt)
		if finished {
			event.Action = choice
			return event, err
		}
	}
}

func consumeBrokerEvent(event brokerEvent, post brokerPost, receipt DeliveryReceipt) (string, bool, error) {
	switch event.Event {
	case "posted":
		if receipt != nil {
			event.Receipt.State = "posted"
			receipt(event.Receipt.Normalized())
		}
		return "", len(post.Buttons) == 0, nil
	case "dismissed":
		if receipt != nil {
			receipt(ReceiptData{State: "dismissed"})
		}
		return "", true, nil
	case "action":
		if event.Text != "" || event.Choice != "" {
			return "", true, errors.New("button action carried unnegotiated text; no verdict was recorded; read the Dibs board")
		}
		if slices.Contains(post.Buttons, event.Action) {
			return event.Action, true, nil
		}
	case "expired":
		return "", true, nil
	case "failed":
		return "", true, fmt.Errorf("%w: %s", ErrCannotNotify, event.Error)
	}
	return "", false, nil
}

func (b *nativeBroker) next(ctx context.Context, id string, responses <-chan brokerEvent) (brokerEvent, error) {
	select {
	case event := <-responses:
		return event, nil
	case <-ctx.Done():
		_ = b.send(brokerPost{Version: 1, Type: "cancel", ID: id})
		return brokerEvent{}, context.Canceled
	case <-b.done:
		// A child can post then die before its parent is scheduled. Drain parsed
		// evidence in wire order before reporting death; never erase a receipt.
		select {
		case event := <-responses:
			return event, nil
		default:
		}
		b.mu.Lock()
		err := b.err
		b.mu.Unlock()
		return brokerEvent{}, err
	}
}

func makeBrokerPost(id, title, subtitle, excerpt string, detail *AskDetail, buttons []string, urgent bool) brokerPost {
	post := brokerPost{ID: id, Title: title, Subtitle: subtitle, Excerpt: excerpt, Buttons: buttons, TimeSensitive: urgent}
	if detail == nil {
		detail = &AskDetail{Sender: title, Body: excerpt, Deadline: time.Now().Add(timeout)}
	}
	post.Detail = encodeAskDetail(*detail)
	until := askUntil(detail.Deadline, time.Now())
	post.Until = float64(until.Unix()) + float64(until.Nanosecond())/float64(time.Second)
	return post
}

// Existing-owner cleanup uses the same private command pipe. Starting a
// transient app for ordinary withdrawal would reintroduce another bundle
// instance exactly while other asks remain live. No owner is started here.
func cleanupThroughBroker(ids []string) (bool, error) {
	nativeBrokerProcess.Lock()
	b := nativeBrokerProcess.broker
	nativeBrokerProcess.Unlock()
	if b == nil {
		return false, nil
	}
	select {
	case <-b.done:
		return false, nil
	default:
	}
	for _, id := range ids {
		if err := b.send(brokerPost{Version: 1, Type: "cancel", ID: id}); err != nil {
			return true, err
		}
	}
	return true, nil
}
