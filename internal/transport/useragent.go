package transport

import (
	"net/http"
	"runtime"

	"github.com/agenxy/dibs/internal/build"
)

// A process that does not say what it is appears in every log it touches as
// `Go-http-client/1.1`.
//
// Dibs is a fleet tool: a hub's access log is one of the few places an
// operator can see which machines are talking to it, and every request from
// every bridge, hook and shipment arrived anonymous and identical to any
// other Go program on the network. The daemon's own log said no more. So
// every request this project makes carries the product, its version and the
// platform, which is what a commercial client has always done and what makes
// a log readable at all.
//
// The version is the linker's, so a hub can see which build a bridge is on
// without asking it: that is the first question after "who is this".
func userAgent() string {
	return "dibs/" + build.Version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
}

// UserAgent is what this build sends. Exported so a test, and `dibs doctor`,
// can state it rather than reconstruct it.
func UserAgent() string { return userAgent() }

// stamping is a RoundTripper that names this product on every request it
// carries, and changes nothing else.
type stamping struct{ base http.RoundTripper }

func (s stamping) RoundTrip(req *http.Request) (*http.Response, error) {
	base := s.base
	if base == nil {
		base = http.DefaultTransport
	}
	// A CALLER'S OWN HEADER WINS, and the request is not mutated: a
	// RoundTripper must not modify what it was handed (net/http says so),
	// and two clients sharing one request would otherwise race.
	if req.Header.Get("User-Agent") != "" {
		return base.RoundTrip(req)
	}
	clone := req.Clone(req.Context())
	clone.Header.Set("User-Agent", userAgent())
	return base.RoundTrip(clone)
}

// Stamp wraps a transport so its requests say who is calling. nil means the
// default transport, which is the common case.
func Stamp(base http.RoundTripper) http.RoundTripper { return stamping{base: base} }

// Client is an http.Client that names this product, for the many places that
// used http.DefaultClient and inherited an anonymous one.
func Client(c *http.Client) *http.Client {
	if c == nil {
		return &http.Client{Transport: Stamp(nil)}
	}
	c.Transport = Stamp(c.Transport)
	return c
}
