package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The client every credential-bearing request goes through says who is
// calling.
//
// daemonClient is the one funnel in this binary (trust.go), so stamping it
// covers the bridge, the hooks, watch, monitor, await, the admin routes and
// the doctor probes at once. Without it a hub's log recorded fifteen kinds
// of caller as one anonymous Go program. Checked on the wire rather than by
// reading the constructor, because a transport that is wrapped and then
// replaced reads the same.
func TestTheDaemonClientNamesItselfOnTheWire(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := daemonClient(2 * time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if !strings.HasPrefix(got, "dibs/") {
		t.Fatalf("the daemon client called out as %q: every request this binary makes is "+
			"anonymous in the log of whatever it called", got)
	}
}
