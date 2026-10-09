// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package codexipc

import (
	"context"
	"errors"
	"testing"

	"github.com/agenxy/dibs/internal/testcodexipc"
)

func TestNativeOneInputUsesAppOwnerAndState(t *testing.T) {
	for _, mode := range []string{"idle", "active"} {
		t.Run(mode, func(t *testing.T) {
			s := testcodexipc.Start(t, mode, nil)
			r, err := Deliver(context.Background(), testcodexipc.Thread, "Dibs: new request from reviewer.")
			if err != nil {
				t.Fatal(err)
			}
			method, disposition, version := "thread-follower-start-turn", "started", float64(2)
			if mode == "active" {
				method, disposition, version = "thread-follower-steer-turn", "steered", 1
			}
			if r.Disposition != disposition || r.TurnID == "" {
				t.Fatalf("receipt: %+v", r)
			}
			calls := s.Inputs()
			if len(calls) != 1 {
				t.Fatalf("inputs: %d", len(calls))
			}
			f := calls[0]
			if f["method"] != method || f["version"] != version || f["targetClientId"] != "owner" || f["sourceClientId"] != "dibs-client" {
				t.Fatalf("envelope: %+v", f)
			}
			p := f["params"].(map[string]any)
			if p["conversationId"] != testcodexipc.Thread {
				t.Fatal("wrong thread")
			}
			if mode == "idle" {
				turn := p["turnStart"].(map[string]any)
				if turn["context"].(map[string]any)["inheritThreadSettings"] != true {
					t.Fatal("settings not inherited")
				}
				p = turn["request"].(map[string]any)
				if p["threadId"] != testcodexipc.Thread {
					t.Fatal("wrong turn thread")
				}
			}
			input := p["input"].([]any)[0].(map[string]any)
			if input["type"] != "text" || input["text"] != "Dibs: new request from reviewer." || len(input["text_elements"].([]any)) != 0 {
				t.Fatalf("input: %+v", input)
			}
		})
	}
}

func TestNativeRefusalAndUnknownNeverSubmitTwice(t *testing.T) {
	for _, mode := range []string{"no-owner", "changed", "disconnect", "refused", "wrong-owner", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			s := testcodexipc.Start(t, mode, nil)
			_, err := Deliver(context.Background(), testcodexipc.Thread, "private notice")
			if err == nil {
				t.Fatal("bad native outcome accepted")
			}
			if errors.Is(err, ErrNoOwner) != (mode == "no-owner") {
				t.Fatalf("wrong cold classification: %v", err)
			}
			want := 1
			if mode == "no-owner" || mode == "changed" || mode == "unsupported" {
				want = 0
			}
			if got := len(s.Inputs()); got != want {
				t.Fatalf("submissions %d, want %d", got, want)
			}
		})
	}
}

func TestNativeRechecksAfterAppSnapshot(t *testing.T) {
	s := testcodexipc.Start(t, "idle", nil)
	r, err := DeliverFresh(context.Background(), testcodexipc.Thread, func() (string, error) { return "", nil })
	if err != nil || r.Disposition != "settled" || len(s.Inputs()) != 0 {
		t.Fatalf("stale input: %+v %v %d", r, err, len(s.Inputs()))
	}
}
