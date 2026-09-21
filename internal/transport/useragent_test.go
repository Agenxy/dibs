package transport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every request this project makes says what it is.
//
// They all went out as `Go-http-client/1.1`: in a hub's access log one
// machine's bridge was indistinguishable from another's hook, from a
// shipment, and from any other Go program on the network, and the version
// a caller is running, which is the first thing worth knowing about it,
// was not there to read.
func TestEveryRequestNamesTheProductAndItsVersion(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	res, err := Client(nil).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if !strings.HasPrefix(got, "dibs/") {
		t.Fatalf("a request went out as %q: a log full of those cannot tell one caller "+
			"from another", got)
	}
	if got != UserAgent() {
		t.Fatalf("the wire said %q and UserAgent() says %q: two answers to one question", got, UserAgent())
	}
	if !strings.Contains(got, "/") || !strings.Contains(got, "(") {
		t.Fatalf("user agent %q carries no version or platform", got)
	}
}

// A caller that set its own is left alone, and the request it handed over
// is not modified: net/http requires a RoundTripper to leave its argument
// intact, and a shared request would otherwise race.
func TestAStatedUserAgentIsKept(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "something-else/1")
	res, err := Client(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if got != "something-else/1" {
		t.Fatalf("the caller's own user agent became %q", got)
	}

	// And the stamp does not write into the caller's request.
	plain, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = Client(nil).Do(plain)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if h := plain.Header.Get("User-Agent"); h != "" {
		t.Fatalf("the transport modified the request it was handed: it now carries %q", h)
	}
}
