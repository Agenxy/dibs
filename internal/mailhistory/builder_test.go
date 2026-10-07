package mailhistory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func serveHistory(t *testing.T, index *Index) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	srv.Listener = index.ServingListener(ctx, srv.Listener)
	srv.Start()
	t.Cleanup(func() { srv.Close(); cancel() })
	res, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal("setup: real serving door:", err)
	}
	_ = res.Body.Close()
	return cancel
}

func awaitHistory(t *testing.T, i *Index) Measurement {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		m := i.Measurement()
		if m.Failed {
			t.Fatal("history failed while building")
		}
		if m.Ready {
			return m
		}
		select {
		case <-deadline.C:
			t.Fatal("builder did not catch up")
		case <-tick.C:
		}
	}
}

func TestServingDoorBuildsCapturedPrefixAndOrderedLiveCommit(t *testing.T) {
	i := New()
	i.BeginReplay()
	i.mu.Lock()
	for n := uint64(1); n <= blockUnits+1; n++ {
		i.head = Record{Serial: n}
		i.capture(snapshotUnit{
			Position: position{n, 1, 0}, Metadata: Metadata{From: "sender", To: "worker", RequestPriority: "high"},
			FromCreated: 1, ToCreated: 2, AfterKnown: true,
		})
	}
	i.records = blockUnits + 1
	i.mu.Unlock()
	i.EndReplay()
	if m := i.Measurement(); m.Ready || m.Started || m.Units != 0 || m.Blocks != 0 {
		t.Fatal("replay encoded snapshots or launched the builder")
	}
	cancel := serveHistory(t, i)
	// Enter the real observer while the serving-triggered builder can be
	// consuming the replay prefix. No builder/ready flag is set by the test.
	op := &core.Op{Kind: core.OpSendMessage, AgentID: "sender"}
	st := core.NewState("fixture", core.DefaultLimits())
	st.Messages[blockUnits+2] = &core.Message{Serial: blockUnits + 2, From: "sender", To: "worker", RequestPriority: "urgent"}
	i.Observe(Record{Serial: blockUnits + 2}, Snapshot{}, st, op, nil)
	m := awaitHistory(t, i)
	if m.Units != blockUnits+2 || m.BuiltSerial != blockUnits+2 || m.QueuedBytes != 0 {
		t.Fatalf("prefix/live watermark lost: %+v", m)
	}
	i.viewMu.RLock()
	u, err := i.codec.unit(blockUnits + 1)
	i.viewMu.RUnlock()
	if err != nil || u.Metadata.RequestPriority != "urgent" || u.Position.Op != blockUnits+2 {
		t.Fatal("live commit did not follow the captured prefix", err)
	}
	cancel()
}

func TestQueueSaturationRefusesDerivedViewWithoutBlockingCoordination(t *testing.T) {
	i := New()
	i.mu.Lock()
	i.started, i.queueLimit = true, 1 // deterministic queue-capacity fixture only
	i.mu.Unlock()
	st := core.NewState("fixture", core.DefaultLimits())
	st.Messages[1] = &core.Message{Serial: 1, From: "a", To: "b"}
	i.Observe(Record{Serial: 1}, Snapshot{}, st, &core.Op{Kind: core.OpSendMessage}, nil)
	i.Observe(Record{Serial: 2}, Snapshot{}, st, &core.Op{Kind: core.OpSweep}, nil)
	if m := i.Measurement(); !m.Failed || m.Ready || m.QueuedBytes != 0 || len(st.Messages) != 1 {
		t.Fatal("saturated derived queue was silently complete or affected canonical mail")
	}
}
