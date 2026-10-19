// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestLiveBoardReplyProjectionPreservesBytesAndFold(t *testing.T) {
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	e := New(core.NewState("reply-canary", core.DefaultLimits()), &memLedger{}, deadProber{})
	empty := []uint64{}
	type step struct {
		name string
		op   core.Op
	}
	steps := []step{
		{"fresh", core.Op{Kind: core.OpRegister, Name: "alpha", Nonce: "alpha-nonce", NewToken: "alpha-token", AgentKind: core.KindPersistent, SessionID: "s1", V7Semantics: true}},
		{"register-retry", core.Op{Kind: core.OpRegister, Name: "alpha", Nonce: "alpha-nonce", NewToken: "ignored", AgentKind: core.KindPersistent, SessionID: "s1", V7Semantics: true}},
		{"ack", core.Op{Kind: core.OpAckBoard, Token: "alpha-token"}},
		{"checkpoint-mail", core.Op{Kind: core.OpActivityCheckpoint, Token: "alpha-token", MailboxSerials: &empty}},
		{"checkpoint-legacy", core.Op{Kind: core.OpActivityCheckpoint, Token: "alpha-token"}},
		{"dormant", core.Op{Kind: core.OpSweep, StaleAgents: []string{"alpha"}}},
		{"register-recovery", core.Op{Kind: core.OpRegister, Name: "alpha", Nonce: "alpha-nonce", NewToken: "alpha-token-2", AgentKind: core.KindPersistent, SessionID: "s2", V7Semantics: true}},
		{"resume", core.Op{Kind: core.OpResume, Nonce: "alpha-nonce", ResumeID: "resume-a", NewToken: "alpha-token-3", SessionID: "s3", V7Semantics: true}},
		{"resume-retry", core.Op{Kind: core.OpResume, Nonce: "alpha-nonce", ResumeID: "resume-a", NewToken: "ignored", SessionID: "s3", V7Semantics: true}},
		{"resume-next", core.Op{Kind: core.OpResume, Nonce: "alpha-nonce", ResumeID: "resume-b", NewToken: "alpha-token-4", SessionID: "s4", V7Semantics: true}},
		{"superseded", core.Op{Kind: core.OpResume, Nonce: "alpha-nonce", ResumeID: "resume-a", NewToken: "ignored", SessionID: "s3", V7Semantics: true}},
		{"session-retry", core.Op{Kind: core.OpRegister, Name: "alpha", NewToken: "ignored", SessionID: "s4", V7Semantics: true}},
		{"second-agent", core.Op{Kind: core.OpRegister, Name: "beta", NewToken: "beta-token", SessionID: "beta-s", V7Semantics: true}},
		{"second-ack", core.Op{Kind: core.OpAckBoard, Token: "beta-token"}},
		{"ordinary-send", core.Op{Kind: core.OpSendMessage, Token: "alpha-token-4", To: "beta", MsgType: core.MsgQuestion, Body: "canary evidence"}},
		{"session-reattach", core.Op{Kind: core.OpRegister, Name: "beta", NewToken: "beta-return", SessionID: "beta-s", V7Semantics: true}},
	}
	// Fold hashes recorded on unmodified main 066a64c remain unchanged. Reply
	// hashes include the supported-merge sessions and possible_forks additions;
	// a full payload comparison with main 22ec1f1 found only those new fields.
	// Everything else, including the checkpoint assertion, remains byte-exact.
	want := map[string][2]string{
		"session-reattach":  {"ea41d707ecef9a4fcae139000613a15f42fa6ac7832cb6cdbc4cbb63c5defe22", "95beb7f6b6176a11a1fc2afffd23e08e8349e13272742f2ad98e174432e750cd"},
		"fresh":             {"0b72ef21291e6be89d67d82711c888c32aaaafbae585bac795763d3acc0a93ab", "fb004d16856ab620f28405fbd5f73df8d73ff19da14a2e5266afda5d7f0485ad"},
		"register-retry":    {"8ef4e04a095df31c3d69a8282aaaa4ea7b31c020dc989ae3778e354ef4dcabd3", "fb004d16856ab620f28405fbd5f73df8d73ff19da14a2e5266afda5d7f0485ad"},
		"ack":               {"275dfa23f310a2b7451000ab934550492f2bff519bbd37e06f10bae731556eba", "b67600c226c7d76efd869de5a580031f14515bf7422ce04ea2555f97004f27f8"},
		"checkpoint-mail":   {"88cfa6f5f30db627d34bb0b18b2bb4d8ad2361fd60ecb9ed61eebfc6048b594a", "04a0043ca5704e188a3b145f2437ca0bb85ede84da06f968b74052e0ddcd43c8"},
		"checkpoint-legacy": {"4062edaf750fb8074e7e83e0c9028c94e32468a8b6f1614774328ef045150f93", "87d8b3e8a771f336f43a2485a843782483e4be7e213e7646768f5c7e70692ca8"},
		"dormant":           {"1bff1ce9494f80e149182ba3b174ede892b39b886812687bf4b5b98a801532db", "4aad0f00cb2a3377cbc054803bb01b62232764eb47aa86c50f84412e980157e0"},
		"register-recovery": {"f1e9a76b2f194e3e9ed10c2be686623859612abbbd9db54bbc35ebaba2499bfe", "e9a3a3cdbcf57be0145c04619b618aaa9df92f81e88d6b00cd7ac5b5cb7f0b60"},
		"resume":            {"c928f16d203376d605288112e8d89f160f7f14f069d4c0003218ab4d8533908b", "8e610dab0622279d532fafc069402d003f98841a247d4c4a7e793db92a94d0d4"},
		"resume-retry":      {"3e57fa1ad9f26333f976eb0fd30661ce514f31fb489dd89443139c1de1b14733", "8e610dab0622279d532fafc069402d003f98841a247d4c4a7e793db92a94d0d4"},
		"resume-next":       {"cafdb24c70d1fb416111434e3284406b90fe76efc65d43b98772b365f035c7f9", "0787366c37374d8f1c733d49df67f9a45953967641b4ee62f6805540e2f607f9"},
		"superseded":        {"8af232467d3e1981cfb33b9963dd21ea485b69d944ef0002bf886fd604ce82d8", "0787366c37374d8f1c733d49df67f9a45953967641b4ee62f6805540e2f607f9"},
		"session-retry":     {"5d26e9742d928f6c04821f3532320aae3d8bf0a426a566f2c1e8f4e84e5285f1", "b7a5caa8eb798eaa746e23f2acbab6efb7128a55a8da7d2934aed7c865ceaa05"},
		"second-agent":      {"78b4eeb352e4efdd1f25c9af99e75080477ccce12615fc8b4caac9e56bf11f5b", "2a9a5a748e02a32e367ed2619beaa2bbe08f3f089dfc815903aee9e57a1c5b41"},
		"second-ack":        {"bc4d6db61d63cd99be37e565d992651e1ab47b3e1c8544522ecce0cc1af97b94", "2328d958944e28e7b894a902cb14eba9be52ca61c8ac82e5288e91d1ce3a78b0"},
		"ordinary-send":     {"2d95506714a5eaaa814988990761c303349737f0e142a1eefa422c8a8a38deab", "aa1f2f12c015020005c9fa62989fd7ca5f67f39357523e8ded7c68dc490fffe1"},
	}
	for n, entry := range steps {
		at := now.Add(time.Duration(n) * time.Second)
		res, err := e.applyAndLedger(&entry.op, at)
		if err != nil {
			t.Fatalf("setup %s: %v", entry.name, err)
		}
		if board, ok := res["board"].(map[string]any); ok &&
			(entry.op.Kind == core.OpAckBoard || entry.op.Kind == core.OpActivityCheckpoint) {
			actor := e.state.AgentByToken(entry.op.Token)
			if actor == nil {
				t.Fatal("setup: checkpoint caller absent")
			}
			for _, row := range board["agents"].([]map[string]any) {
				if row["id"] == actor.ID {
					stamp, ok := row["last_coordination_at"].(time.Time)
					if !ok || !stamp.Equal(at) {
						t.Fatalf("%s shows the caller before its checkpoint", entry.name)
					}
					// Only this timestamp is normalized: post-Apply truth replaces
					// the old caller timestamp. No peer field is ignored.
					row["last_coordination_at"] = time.Time{}
				}
			}
		}
		resultHash := boardReplyHash(t, res)
		foldHash := boardReplyHash(t, []any{e.state, e.ring})
		t.Logf("BOARD_REPLY_CANARY %s %s %s", entry.name, resultHash, foldHash)
		expected, ok := want[entry.name]
		if !ok {
			t.Fatalf("missing pre-change canary: %s", entry.name)
		}
		if expected[0] != resultHash || expected[1] != foldHash {
			t.Fatalf("%s changed live reply bytes or canonical state/events", entry.name)
		}
	}
}

func boardReplyHash(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal("setup: canonical canary encoding", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
