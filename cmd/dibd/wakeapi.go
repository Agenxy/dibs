package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/agenxy/dibs/internal/engine"
)

// registerWakeAPI is the other half of a wake on another machine: the host's
// bridge reports how the command it ran came out, and lists what is attached.
//
//	POST /api/wake-result   {id, host, ok, detail}   → {accepted}
//	GET  /api/hosts                                  → {hosts: [{host, harnesses, since}], self: [id…]}
//
// Both take the board's secret, the same credential that opened the stream
// the request arrived on. A report names a request id the hub handed out and
// is accepted once; a report for a request nobody is waiting on is refused so
// a bridge that lost track learns it rather than believing it was heard.
func registerWakeAPI(mux *http.ServeMux, eng *engine.Engine, secret string) {
	authed := func(r *http.Request) bool {
		if h := r.Header.Get("X-Dibs-Local"); h != "" && subtle.ConstantTimeCompare([]byte(h), []byte(secret)) == 1 {
			return true
		}
		scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
		return found && strings.EqualFold(scheme, "Bearer") && subtle.ConstantTimeCompare([]byte(token), []byte(secret)) == 1
	}
	mux.HandleFunc("POST /api/wake-result", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if !authed(r) {
			refuse(w, http.StatusUnauthorized, "a wake result needs the board's secret")
			return
		}
		var res engine.WakeResult
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&res); err != nil {
			refuse(w, http.StatusBadRequest, "the wake result did not parse: "+err.Error())
			return
		}
		if res.ID == 0 || res.Host == "" {
			refuse(w, http.StatusBadRequest, "a wake result names the request id and the host that ran it")
			return
		}
		if !eng.ReportWakeResult(res) {
			refuse(w, http.StatusNotFound, "no wake is waiting on that report: the hub gave up on it, "+
				"or the id is not one it handed out")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true})
	})
	mux.HandleFunc("GET /api/hosts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if !authed(r) {
			refuse(w, http.StatusUnauthorized, "listing attached hosts needs the board's secret")
			return
		}
		hosts := eng.HostBridges()
		if hosts == nil {
			hosts = []engine.HostBridgeInfo{}
		}
		// AND WHICH IDS THIS DAEMON READS AS ITSELF. A bridge is a route
		// for another machine's agents and never for a local one, and the
		// hub's own [wake.exec] is the reverse, so doctor cannot say which
		// agents are covered without drawing the same line the waker does.
		// It used to draw it from the board's host id alone, which is the
		// wrong answer on a machine with aliases or a pre-Supgang node id.
		_ = json.NewEncoder(w).Encode(map[string]any{"hosts": hosts, "self": eng.SelfHostIDs()})
	})
}
