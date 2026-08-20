package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// rpcNoHeader sends a request the way a STDIO client must: the protocol version
// in _meta and no MCP-Protocol-Version header, because that transport has none.
//
// The existing rpc() helper sets the header, which is why every conformance
// test passed while the stdio transport was non-conformant: the helper could do
// something the real client cannot.
func rpcNoHeader(t *testing.T, srv *httptest.Server, body string) map[string]any {
	t.Helper()
	hreq, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader([]byte(body)))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// A stdio client asking for 2026-07-28 must be SERVED 2026-07-28.
//
// THE BUG THIS CATCHES. The era was selected from the `MCP-Protocol-Version`
// HTTP header alone. 2026-07-28 is stateless: the version travels in `_meta` on
// every request, and stdio has no headers at all. So every stdio client that
// asked for the modern era got a result with no `resultType`, which the
// revision requires on every result, and a conformant client is entitled to
// reject it.
//
// Measured against a live daemon before the fix, with the real Codex Desktop
// binary 0.148.0-alpha.9 over the stdio bridge:
//
//	server/discover  client=codex-mcp-client  _meta protocolVersion=2026-07-28
//	server/discover  client=codex-mcp-client  (retried)
//	initialize       client=codex/1           <- gave up, fell back to legacy
//	notifications/initialized
//	tools/call ...                            <- every real call on 2025
//
// That reads as "the client does not support 2026 yet". It was us: the client
// asked correctly, twice, and we answered in the wrong era both times.
//
// The same daemon answered CORRECTLY over HTTP, because there the header is
// present. One transport conformant and the other not, from one line, with the
// tests exercising only the conformant one: the drift class this repository is
// most expensive at.
func TestStdioClientAskingFor2026IsServed2026(t *testing.T) {
	srv, cancel := newServer(t)
	defer cancel()

	// Exactly what a stdio client sends: the version in _meta, no header,
	// because the transport has none to set.
	out := rpcNoHeader(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{`+
		`"io.modelcontextprotocol/protocolVersion":"2026-07-28",`+
		`"io.modelcontextprotocol/clientInfo":{"name":"probe","version":"1"}}}}`)
	res, _ := out["result"].(map[string]any)
	if res == nil {
		t.Fatalf("setup: no result in %v", out)
	}

	if got, _ := res["resultType"].(string); got != "complete" {
		t.Errorf("resultType = %q, want \"complete\". A client that asked for "+
			"2026-07-28 in _meta was answered in the legacy era because it could "+
			"not set an HTTP header it has no transport for. Conformant clients "+
			"reject this and fall back to 2025, which is exactly what Codex does, "+
			"and it looks from the outside like the client lacking 2026 support",
			got)
	}
}

// The legacy era must NOT be stamped, and that restriction is load-bearing.
//
// Deployed TypeScript and Rust SDKs strict-validate results and reject unknown
// keys, so stamping `resultType` on a 2025 answer breaks every client hosts
// actually use today. The fix above widens where the modern era is DETECTED; it
// must not widen who gets stamped.
func TestALegacyClientIsStillNotStamped(t *testing.T) {
	srv, cancel := newServer(t)
	defer cancel()

	for _, tc := range []struct{ name, body string }{
		{"no version anywhere", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`},
		{"legacy version in _meta", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{` +
			`"io.modelcontextprotocol/protocolVersion":"2025-11-25"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := rpcNoHeader(t, srv, tc.body)
			res, _ := out["result"].(map[string]any)
			if res == nil {
				t.Fatalf("setup: no result in %v", out)
			}
			if _, present := res["resultType"]; present {
				t.Errorf("a legacy result was stamped with resultType: strict SDKs " +
					"reject unknown keys, so this breaks the clients every host uses " +
					"today in order to satisfy one that none of them are yet")
			}
		})
	}
}

// A version we do not serve must be REFUSED, however the client stated it.
//
// The same header-only blindness as above, one layer down, and it survived the
// first fix: `tagResult` was corrected to read `_meta`, but the version
// VALIDATION still looked only at `MCP-Protocol-Version`. So over HTTP an
// unsupported version got `-32022`, and over stdio the request was served
// silently as though we had agreed to it.
//
// That is worse than a wrong answer. The 2026 backward-compatibility rules
// turn on which error a probe gets: a client that receives a RECOGNIZED modern
// error such as UnsupportedProtocolVersionError "MUST NOT fall back to
// initialize" and instead picks from the advertised list, while anything
// unrecognized sends it back to the legacy handshake. Answering an impossible
// version with success tells the client we agreed, and every later result is a
// version mismatch neither side is checking.
//
// server/discover is deliberately exempt: it is the negotiation call, and the
// spec accepts a DiscoverResult carrying supportedVersions as a valid answer to
// a version we do not serve. That is the friendlier of the two permitted
// behaviours, so it stays.
func TestAnUnsupportedVersionIsRefusedOverStdioToo(t *testing.T) {
	srv, cancel := newServer(t)
	defer cancel()

	for _, method := range []string{"tools/list", "resources/list"} {
		t.Run(method, func(t *testing.T) {
			out := rpcNoHeader(t, srv, `{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":{"_meta":{`+
				`"io.modelcontextprotocol/protocolVersion":"2027-01-01"}}}`)
			e, _ := out["error"].(map[string]any)
			if e == nil {
				t.Fatalf("a version we do not serve was accepted silently over stdio: "+
					"the client believes we agreed to 2027-01-01, and every result "+
					"after this is an unchecked version mismatch (got %v)", out["result"])
			}
			if code, _ := e["code"].(float64); int(code) != errUnsupportedProtocolVersion {
				t.Errorf("code = %v, want %d. The backward-compatibility rules turn on "+
					"this exact code: a RECOGNIZED modern error keeps the client on the "+
					"modern path, anything else sends it back to initialize",
					e["code"], errUnsupportedProtocolVersion)
			}
		})
	}

	// The negotiation call still answers, so a client can learn what we do serve.
	out := rpcNoHeader(t, srv, `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{`+
		`"io.modelcontextprotocol/protocolVersion":"2027-01-01"}}}`)
	res, _ := out["result"].(map[string]any)
	if res == nil || res["supportedVersions"] == nil {
		t.Errorf("server/discover refused to advertise its versions to a client asking "+
			"for one we lack, leaving it no way to pick a mutually supported one: %v", out)
	}
}
