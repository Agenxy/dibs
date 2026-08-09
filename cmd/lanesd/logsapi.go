package main

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/agenxy/lanes/internal/engine"
	"github.com/agenxy/lanes/internal/logs"
)

// registerLogsAPI exposes the bounded log tail. It sits behind the same auth
// gate as everything else; records are already redacted, so this cannot leak
// what the ledger encrypts.
func registerLogsAPI(mux *http.ServeMux, ring *logs.Ring) {
	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n == 0 {
			n = 200
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"records": ring.Tail(n)})
	})
}

// registerAdminAPI exposes actions only a human may take. It sits on a god-view
// path, so the auth gate requires BOTH the local secret (same-user) and the
// admin password — which is precisely why a lane can never promote itself or
// another: no lane token reaches this handler.
func registerAdminAPI(mux *http.ServeMux, eng *engine.Engine) {
	mux.HandleFunc("POST /api/admin/role", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Lane string `json:"lane"`
			Role string `json:"role"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		res, err := eng.GrantRole(r.Context(), body.Lane, body.Role)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(res)
	})

	// Closing another lane is a human's call: a crashed lane cannot close itself,
	// and no agent should be able to evict a peer. Same gate as role granting —
	// local secret AND admin password, and no lane token reaches here.
	mux.HandleFunc("POST /api/admin/prune", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Lane string `json:"lane"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		res, err := eng.Prune(r.Context(), body.Lane)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(res)
	})
}

// registerMatchStatusAPI exposes why work-overlap matching is or is not working.
//
// A separate route rather than a field on the board because the answer is often
// "matching never started", and a board payload cannot explain its own absence.
// `lanes doctor` reads this; so does anyone debugging a fleet that is quietly
// not coordinating.
func registerMatchStatusAPI(mux *http.ServeMux, eng *engine.Engine) {
	mux.HandleFunc("GET /api/hook-health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(eng.HookHealth())
	})
	mux.HandleFunc("GET /api/match-status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(eng.MatchStatus())
	})
}
