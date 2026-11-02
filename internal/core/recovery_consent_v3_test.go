// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRecoveryV3PreservesEveryOtherMessageField(t *testing.T) {
	for _, proposer := range []string{"survivor", "lead"} {
		for _, field := range reflect.VisibleFields(reflect.TypeFor[Message]()) {
			t.Run(proposer+"/"+field.Name, func(t *testing.T) {
				s := recoveryState()
				m := &Message{Serial: 10, From: "peer", To: "source", Type: MsgQuestion, State: MsgStatePending}
				s.Messages[10] = m
				r := &RecoveryRequest{From: "source", Into: "survivor", Fence: "v3:sha256:" + strings.Repeat("a", 64)}
				before := s.RecoveryFence(r, proposer, 0)
				if !strings.HasPrefix(before, "v3:sha256:") {
					t.Fatal("current recorded prefix did not select v3")
				}
				reflect.ValueOf(m).Elem().FieldByIndex(field.Index).Set(recoveryFieldValue(field.Type))
				stored, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				after := s.RecoveryFence(r, proposer, 0)
				unchanged := field.Name == "ContactEscalatedAt" || field.Name == "StallNotifiedDeclaration"
				if (before == after) != unchanged {
					t.Fatalf("field %s: equal=%v want %v", field.Name, before == after, unchanged)
				}
				actual, err := json.Marshal(m)
				if err != nil || string(actual) != string(stored) {
					t.Fatal("projection mutated stored mail", err)
				}
			})
		}
	}
}

func recoveryFieldValue(typ reflect.Type) reflect.Value {
	if typ == reflect.TypeFor[time.Time]() {
		return reflect.ValueOf(time.Unix(100, 0).UTC())
	}
	v := reflect.New(typ).Elem()
	switch typ.Kind() {
	case reflect.String:
		v.SetString("changed")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint64:
		v.SetUint(1)
	case reflect.Pointer:
		v.Set(reflect.New(typ.Elem()))
	case reflect.Slice:
		v.Set(reflect.MakeSlice(typ, 1, 1))
	default:
		panic("new Message field requires a mutation value: " + typ.String())
	}
	return v
}

func TestRecoveryV3KeepsSourceCustodyAndExcludesSurvivorIncoming(t *testing.T) {
	for _, proposer := range []string{"survivor", "lead"} {
		for _, tc := range []struct {
			from, to string
			same     bool
		}{
			{"peer", "survivor", true},
			{"survivor", "peer", true},
			{"source", "survivor", false},
			{"source", "peer", false},
			{"peer", "source", false},
			{"survivor", "source", false},
		} {
			t.Run(proposer+"/"+tc.from+"-to-"+tc.to, func(t *testing.T) {
				s := recoveryState()
				r := &RecoveryRequest{From: "source", Into: "survivor", Fence: "v3:sha256:" + strings.Repeat("a", 64)}
				before := s.RecoveryFence(r, proposer, 0)
				s.Messages[10] = &Message{Serial: 10, From: tc.from, To: tc.to, Type: MsgNotify, Body: "new mail"}
				if same := before == s.RecoveryFence(r, proposer, 0); same != tc.same {
					t.Fatalf("source custody/survivor mail: same=%v want=%v", same, tc.same)
				}
			})
		}
	}
}
