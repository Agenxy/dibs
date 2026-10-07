package mailhistory

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Classification is a static property. A new core operation must make an
// explicit mail-scope decision before it can pass the repository gate.
func TestEveryCoreOpHasExplicitHistoryCaptureScope(t *testing.T) {
	files, err := filepath.Glob("../core/*.go")
	if err != nil || len(files) == 0 {
		t.Fatal("setup: core sources:", err)
	}
	count := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal("setup: core syntax:", err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			spec, ok := node.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for n, name := range spec.Names {
				if !strings.HasPrefix(name.Name, "Op") {
					continue
				}
				if n >= len(spec.Values) {
					t.Fatalf("operation %s has no explicit literal", name.Name)
				}
				literal, ok := spec.Values[n].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatalf("operation %s is not an explicit string literal", name.Name)
				}
				kind, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatal("setup: operation literal:", err)
				}
				count++
				if _, known := scopeOf(kind); !known {
					t.Errorf("core %s=%q has no explicit history capture scope", name.Name, kind)
				}
			}
			return true
		})
	}
	if count < 50 {
		t.Fatal("setup: incomplete operation inventory:", count)
	}
}

func scopeApply(t *testing.T, st *core.State, op *core.Op) core.Result {
	t.Helper()
	res, _, err := st.Apply(op, time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal("setup: real fold:", op.Kind, err)
	}
	return res
}

func scopeState(t *testing.T) *core.State {
	t.Helper()
	st := core.NewState("scope-fixture", core.DefaultLimits())
	for _, id := range []string{"sender", "a", "b"} {
		scopeApply(t, st, &core.Op{Kind: core.OpRegister, Name: id, NewToken: id + "-token", Nonce: id + "-nonce", PID: 1, AgentKind: core.KindPersistent})
		scopeApply(t, st, &core.Op{Kind: core.OpAckBoard, Token: id + "-token"})
	}
	for n := 0; n < 20; n++ {
		for _, to := range []string{"a", "b"} {
			scopeApply(t, st, &core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: to, MsgType: core.MsgNotify, Body: "live scope evidence"})
		}
	}
	return st
}

// Compare the whole canonical board after the actual fold. The captured set
// must cover every changed pre-existing message, and the new-send marker must
// cover a birth. Narrowing is checked against state, never a guessed event list.
func assertScopeCoverage(t *testing.T, st *core.State, op *core.Op, want int) {
	t.Helper()
	previous := make(map[uint64]Metadata, len(st.Messages))
	for n, m := range st.Messages {
		previous[n] = stateMetadata(st, m)
	}
	var scratch Snapshot
	before := Capture(st, op, &scratch)
	if len(before.Mail) != want {
		t.Fatalf("scope copied %d rows, wanted %d", len(before.Mail), want)
	}
	scopeApply(t, st, op)
	for n, old := range previous {
		m := st.Messages[n]
		if m != nil && stateMetadata(st, m) == old {
			continue
		}
		if got, ok := before.Mail[n]; !ok || got != old {
			t.Errorf("changed message %d was absent or wrong in snapshot", n)
		}
	}
	for n := range st.Messages {
		if _, existed := previous[n]; !existed && !before.all && before.newMessage != n {
			t.Errorf("new message %d was outside scoped after-set", n)
		}
	}
}

func TestCaptureScopesCoverActualCanonicalChanges(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		assertScopeCoverage(t, scopeState(t), &core.Op{Kind: core.OpActivityCheckpoint, Token: "sender-token"}, 0)
	})
	t.Run("mailbox", func(t *testing.T) {
		assertScopeCoverage(t, scopeState(t), &core.Op{Kind: core.OpAckBoard, Token: "a-token"}, 20)
	})
	t.Run("send-birth", func(t *testing.T) {
		assertScopeCoverage(t, scopeState(t), &core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "a", MsgType: core.MsgNotify, Body: "birth"}, 20)
	})
	t.Run("point", func(t *testing.T) {
		assertScopeCoverage(t, scopeState(t), &core.Op{Kind: core.OpAckMessage, Token: "a-token", MsgSerial: 7}, 1)
	})
	t.Run("listed", func(t *testing.T) {
		assertScopeCoverage(t, scopeState(t), &core.Op{Kind: core.OpMarkDelivered, MsgSerials: []uint64{7, 9}}, 2)
	})
	t.Run("full", func(t *testing.T) {
		st := scopeState(t)
		scopeApply(t, st, &core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "a", MsgType: core.MsgQuestion, Body: "must expire on departure"})
		assertScopeCoverage(t, st, &core.Op{Kind: core.OpSignOff, Token: "a-token"}, 41)
	})
}
