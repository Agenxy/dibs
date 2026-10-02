package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/agenxy/dibs/internal/engine"
)

// serveTaskSubscription pushes notifications/tasks for the tracked requests
// a client named, each carrying the whole task as tasks/get would return it,
// so the host follows the work without polling. Its own stream, like the
// wake stream: one stream doing two jobs is how two of them once got each
// other's cursors.
//
// Ids this board did not issue are left out of the acknowledgment, which is
// how the extension says "not honoured". The stream ends when every task it
// follows has finished: there is nothing more it could say.
func (s *Server) serveTaskSubscription(w http.ResponseWriter, r *http.Request, req *rpcRequest, ids []string) {
	ctx := r.Context()
	follow := s.followTasks(ctx, ids)
	_, since, err := s.eng.SubscribeInfo(ctx, "")
	if err != nil {
		writeRPC(w, http.StatusOK, req.ID, nil, rpcErrFrom(err))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeRPC(w, http.StatusInternalServerError, req.ID, nil,
			&rpcError{Code: -32603, Message: "streaming not supported by this server"})
		return
	}
	// Subscribed before the current state is read, so a change between the
	// two arrives as a notification rather than falling between them.
	sub, cancel := s.eng.SubscribeTracked(since)
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	stream := sseStream{w: w, fl: flusher}
	honored := make([]string, 0, len(follow))
	for _, id := range follow {
		honored = append(honored, id)
	}
	stream.send(notification("notifications/subscriptions/acknowledged", map[string]any{
		"notifications": map[string]any{"taskIds": honored},
	}, req.ID))

	s.pushTasks(ctx, stream, req.ID, follow)
	s.pumpTasks(ctx, stream, req.ID, follow, sub)
}

func (s *Server) followTasks(ctx context.Context, ids []string) map[uint64]string {
	follow := map[uint64]string{}
	for _, id := range ids {
		if serial, ok := s.taskSerial(id); ok {
			if _, found, _ := s.eng.TrackedMessage(ctx, serial); found {
				follow[serial] = id
			}
		}
	}
	return follow
}

// pushTask returns whether a task is still worth following.
func (s *Server) pushTask(ctx context.Context, stream sseStream, subID json.RawMessage, serial uint64) bool {
	m, found, err := s.eng.TrackedMessage(ctx, serial)
	if err != nil || !found {
		return false
	}
	t := s.detailedTask(m)
	if !stream.send(notification("notifications/tasks", t, subID)) {
		return false
	}
	return t["status"] == "working"
}

func (s *Server) pushTasks(ctx context.Context, stream sseStream, subID json.RawMessage, follow map[uint64]string) {
	for serial := range follow {
		if !s.pushTask(ctx, stream, subID, serial) {
			delete(follow, serial)
		}
	}
}

func (s *Server) pumpTasks(
	ctx context.Context, stream sseStream, subID json.RawMessage, follow map[uint64]string, sub *engine.Subscription,
) {
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for len(follow) > 0 {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			// A full event buffer may have dropped the terminal update.
			// Read snapshots rather than waiting forever for another event.
			if sub.Lost() {
				s.pushTasks(ctx, stream, subID, follow)
			}
			if !stream.comment() {
				return
			}
		case ev, open := <-sub.C:
			if !open {
				return
			}
			if sub.Lost() {
				s.pushTasks(ctx, stream, subID, follow)
			}
			serial, _ := ev.Data["msg_serial"].(uint64)
			if _, ok := follow[serial]; ok && !s.pushTask(ctx, stream, subID, serial) {
				delete(follow, serial)
			}
		}
	}
}
