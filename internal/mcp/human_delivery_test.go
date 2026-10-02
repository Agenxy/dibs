package mcp

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/ledger"
)

type desktopFixture struct {
	available bool
	ask       func(humanask.Message) (humanask.Answer, error)
}

func (d desktopFixture) Available() bool                                 { return d.available }
func (d desktopFixture) Ask(m humanask.Message) (humanask.Answer, error) { return d.ask(m) }

func TestHumanDeliveryThroughActualMCP(t *testing.T) {
	for _, route := range []string{"relay", "desktop", "none"} {
		t.Run(route, func(t *testing.T) {
			srv, eng, _ := newServerWithEngine(t)
			asked, release := make(chan humanask.Message, 1), make(chan struct{})
			t.Cleanup(func() { close(release) })
			eng.SetHumanNotifier(desktopFixture{available: route == "desktop", ask: func(m humanask.Message) (humanask.Answer, error) {
				asked <- m
				<-release
				return humanask.Answer{}, nil
			}})
			if route == "relay" {
				_, detach := eng.AttachHumanRelay()
				t.Cleanup(detach)
			}
			r := toolCall(t, srv, "register", map[string]any{"name": "sender", "nonce": "delivery-fixture"})
			token, ok := r["token"].(string)
			if !ok {
				t.Fatalf("setup: %v", r)
			}
			sent := toolCall(t, srv, "send", map[string]any{"token": token, "to": "human", "type": "question", "body": "approval route fixture"})
			if sent["human_route"] != route {
				t.Fatalf("route: %v", sent)
			}
			serial, ok := sent["msg_serial"].(float64)
			if !ok {
				t.Fatalf("send: %v", sent)
			}
			read := func() map[string]any {
				r := toolCall(t, srv, "read_mail", map[string]any{"token": token, "msg_serial": serial})
				d, ok := r["human_delivery"].(map[string]any)
				if !ok {
					t.Fatalf("delivery missing: %v", r)
				}
				return d
			}
			d := read()
			if d["route"] != route {
				t.Fatalf("read route: %v", d)
			}
			if route == "none" {
				if d["state"] != "unavailable" || sent["notify_hint"] == nil {
					t.Fatalf("none: %v %v", sent, d)
				}
				return
			}
			if route == "desktop" {
				select {
				case m := <-asked:
					m.Receipt("posted")
				case <-time.After(3 * time.Second):
					t.Fatal("send did not dispatch")
				}
			} else {
				if sent["human_relay_count"] != float64(1) || d["state"] != "queued" {
					t.Fatalf("relay: %v %v", sent, d)
				}
				if err := eng.ReportHumanDelivery(context.Background(), uint64(serial), "relay-1", "posted", ""); err != nil {
					t.Fatal(err)
				}
				if err := eng.ReportHumanDelivery(context.Background(), uint64(serial), "relay-2", "failed", "other Mac offline"); err != nil {
					t.Fatal(err)
				}
			}
			if d := read(); d["state"] != "posted" {
				t.Fatalf("posted: %v", d)
			}
			if err := eng.AnswerAsHuman(context.Background(), uint64(serial), "answer", "yes"); err != nil {
				t.Fatal(err)
			}
			if d := read(); d["state"] != "answered" {
				t.Fatalf("answer: %v", d)
			}
		})
	}
}

func TestFailedHumanDesktopIsVisibleToTheMCPCaller(t *testing.T) {
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger"), "delivery-errors", box)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(core.NewState("delivery-errors", core.DefaultLimits()), led, nil)
	eng.SetHumanNotifier(desktopFixture{available: true, ask: func(humanask.Message) (humanask.Answer, error) {
		return humanask.Answer{}, errors.New("fixture notifier crash")
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	srv := httptest.NewServer(New(eng))
	t.Cleanup(func() { srv.Close(); cancel(); <-done; _ = led.Close() })
	reg := toolCall(t, srv, "register", map[string]any{"name": "sender", "nonce": "failure-sender"})
	token, ok := reg["token"].(string)
	if !ok {
		t.Fatalf("register: %v", reg)
	}
	events, unsubscribe := eng.Subscribe(0)
	defer unsubscribe()
	sent := toolCall(t, srv, "send", map[string]any{"token": token, "to": "human", "type": "request", "body": "fixture only"})
	serial, ok := sent["msg_serial"].(float64)
	if !ok {
		t.Fatalf("send: %v", sent)
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Type != "message.sent" || event.Agent != "dibs" {
				continue
			}
			read := toolCall(t, srv, "read_mail", map[string]any{"token": token, "msg_serial": serial})
			d := asMap(read["human_delivery"])
			if d["state"] != "failed" || d["error"] != "fixture notifier crash" {
				t.Fatalf("failure invisible: %v", read)
			}
			return
		case <-timer.C:
			t.Fatal("failure was not reported")
		}
	}
}

func TestFullHumanRelayFallsBackWithoutBlockingMCP(t *testing.T) {
	srv, eng, _ := newServerWithEngine(t)
	eng.SetHumanNotifier(desktopFixture{available: false})
	_, detach := eng.AttachHumanRelay()
	defer detach()
	reg := toolCall(t, srv, "register", map[string]any{"name": "sender", "nonce": "full-relay-fixture"})
	token, ok := reg["token"].(string)
	if !ok {
		t.Fatalf("register: %v", reg)
	}
	for i := range 64 {
		if i%16 == 0 {
			worker := "queue-worker-" + strconv.Itoa(i/16)
			reg := toolCall(t, srv, "register", map[string]any{"name": worker, "nonce": worker})
			var ok bool
			token, ok = reg["token"].(string)
			if !ok {
				t.Fatalf("worker setup: %v", reg)
			}
		}
		res, err := eng.Do(context.Background(), &core.Op{
			Kind: core.OpSendMessage, Token: token,
			To: "human", MsgType: core.MsgNotify, Body: "fill queue fixture"})
		if err != nil || res["error"] != nil || res["human_route"] != "relay" {
			t.Fatalf("queue setup %d failed: %v %v", i, res, err)
		}
	}
	sent := toolCall(t, srv, "send", map[string]any{"token": token, "to": "human", "type": "question", "body": "overflow fixture"})
	if sent["human_route"] != "none" || sent["human_relay_count"] != float64(0) {
		t.Fatalf("full relay was reported as a working route: %v", sent)
	}
}
