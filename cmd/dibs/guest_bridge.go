package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/agenxy/dibs/internal/transport"
)

func guestBridgeArg(args []string) (string, bool, error) {
	for _, a := range args {
		if a == "--guest" || strings.HasPrefix(a, "--guest=") {
			if len(args) != 2 || args[0] != "--guest" || args[1] == "" || strings.HasPrefix(args[1], "--") {
				return "", true, errors.New("usage: dibs mcp-stdio --guest <absolute private recipe path>; " +
					"do not combine guest and local/remote-session modes")
			}
			return args[1], true, nil
		}
	}
	return "", false, nil
}

// The guest has no local board. Choose this typed transport BEFORE local
// preflight and never call the local identity/wake/secret/upgrade bootstrap.
func runGuestBridge(file string) error {
	recipe, endpoint, ca, err := readGuestRecipe(file)
	if err != nil {
		return err
	}
	client := guestClient(recipe, endpoint, ca)
	defer client.CloseIdleConnections()
	out := &syncWriter{w: bufio.NewWriter(os.Stdout)}
	defer out.flush()
	ctx, end := context.WithCancel(context.Background())
	defer end()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go endSessionWhenTheHarnessGoes(ctx, sigs, end, out)
	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	legacySession := ""
	for {
		line, rerr := readLine(in)
		if rerr != nil {
			return rerr
		}
		if line == nil || ctx.Err() != nil {
			return nil
		}
		if len(line) == 0 {
			continue
		}
		prepared, rerr := guestPrepareNonce(line, file, recipe)
		if rerr != nil {
			guestWriteReply(out, guestNonceReply(line, rerr))
			continue
		}
		line = prepared
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, recipe.Endpoint, bytes.NewReader(line))
		if rerr != nil {
			return fmt.Errorf("guest request: %w", rerr)
		}
		setGuestRouting(req, line)
		if legacySession != "" && req.Header.Get("MCP-Protocol-Version") != "2026-07-28" {
			req.Header.Set("Mcp-Session-Id", legacySession)
		}
		if session := guestForward(client, req, line, out); session != "" {
			legacySession = session
		}
	}
}

// Guest authentication has ONE source and every round trip checks the scope.
// A fresh http.Transport, not a clone of a caller-mutable DefaultTransport:
// fleet roots, environment proxies and local credential refresh do not apply.
func guestClient(recipe *guestRecipe, endpoint *url.URL, ca *x509.Certificate) *http.Client {
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	config := &tls.Config{
		RootCAs: pool, MinVersion: tls.VersionTLS13, ServerName: endpoint.Hostname(),
		VerifyConnection: func(s tls.ConnectionState) error {
			for _, chain := range s.VerifiedChains {
				if len(chain) != 2 {
					continue
				}
				pin := sha256.Sum256(chain[1].RawSubjectPublicKeyInfo)
				if hex.EncodeToString(pin[:]) == recipe.Pin {
					return nil
				}
			}
			return errors.New("guest TLS requires a leaf directly signed by the pinned guest CA; " +
				"ask the issuer, never relax verification")
		},
	}
	rt := &http.Transport{
		TLSClientConfig: config, TLSHandshakeTimeout: 5 * time.Second, IdleConnTimeout: 30 * time.Second,
		MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != endpoint.Host {
				return nil, errors.New("guest dial changed the invited endpoint; use the original private recipe")
			}
			return dialer.DialContext(ctx, "tcp6", endpoint.Host)
		},
	}
	return &http.Client{
		Timeout: 75 * time.Second,
		Transport: transport.Stamp(&guestTransport{
			next: rt, endpoint: recipe.Endpoint, credential: recipe.Key, expires: recipe.Expires,
		}),
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("guest redirects are refused; ask the issuer for a new invitation")
		},
	}
}

type guestTransport struct {
	next       http.RoundTripper
	endpoint   string
	credential string
	expires    time.Time
}

func (g *guestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL == nil || r.URL.String() != g.endpoint || r.Method != http.MethodPost {
		return nil, errors.New("guest request escaped its exact invited POST endpoint; do not forward it")
	}
	if r.Host != "" && r.Host != r.URL.Host {
		return nil, errors.New("guest HTTP Host escaped its invited endpoint; use the original private recipe")
	}
	if !time.Now().Before(g.expires) {
		return nil, errors.New("guest invitation expired; ask its issuer for a new private recipe")
	}
	if r.Header.Get("X-Dibs-Local") != "" || r.Header.Get("X-Dibs-Agent-Nonce") != "" {
		return nil, errors.New("guest request contains local-board credentials; use invitation-only guest mode")
	}
	next := r.Clone(r.Context())
	next.Header.Set("Authorization", "Bearer "+g.credential)
	return g.next.RoundTrip(next)
}

// Version/extension evidence belongs to THIS request. Do not make a task
// declaration up from a previous initialize or reconstruct opaque task IDs.
func setGuestRouting(r *http.Request, line []byte) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	var m struct {
		Method string `json:"method"`
		Params struct {
			TaskID string         `json:"taskId"`
			Meta   map[string]any `json:"_meta"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &m) != nil {
		return
	}
	r.Header.Set("Mcp-Method", m.Method)
	if v, ok := m.Params.Meta["io.modelcontextprotocol/protocolVersion"].(string); ok {
		r.Header.Set("MCP-Protocol-Version", v)
	}
	switch m.Method {
	case "tasks/get", "tasks/update", "tasks/cancel":
		r.Header.Set("Mcp-Name", m.Params.TaskID)
	}
}

// Filling only the recovery credential preserves the invitation server's
// identity policy: no pid/session/host/app/local-token fields are injected.
func guestRecoveryNonce(line []byte, nonce string) []byte {
	if nonce == "" {
		return line
	}
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(line))
	d.UseNumber()
	if d.Decode(&m) != nil || m["method"] != "tools/call" {
		return line
	}
	p, ok := m["params"].(map[string]any)
	if !ok || p["name"] != "register" {
		return line
	}
	a, ok := p["arguments"].(map[string]any)
	if !ok {
		return line
	}
	if _, given := a["nonce"]; given {
		return line
	}
	a["nonce"] = nonce
	b, err := json.Marshal(m)
	if err != nil {
		return line
	}
	return b
}

func guestForward(client *http.Client, req *http.Request, line []byte, out *syncWriter) string {
	if startupRequest(line) {
		ctx, cancel := context.WithTimeout(req.Context(), startupGrace)
		defer cancel()
		req = req.WithContext(ctx)
	}
	resp, err := guestDoWithGrace(client, req, line)
	if err != nil {
		guestWriteReply(out, guestTransportReply(line, err))
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	// Same-origin redirects are refusals too, including a JSON-shaped body.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		guestWriteReply(out, guestTransportReply(line, errors.New("guest endpoint returned a redirect; "+
			"request a new invitation")))
		return ""
	}
	const maxReply = 96 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReply+1))
	if err != nil || len(body) > maxReply {
		guestWriteReply(out, guestTransportReply(line, errors.New("guest reply was truncated or exceeded its bound; "+
			"the call's outcome is unknown")))
		return ""
	}
	body = bytes.TrimSpace(body)
	if reply, refused := notAReply(line, resp.StatusCode, body); refused {
		guestWriteReply(out, guestHint(reply))
		return ""
	}
	if !guestValidReply(line, body) {
		guestWriteReply(out, guestTransportReply(line, fmt.Errorf(
			"guest endpoint returned HTTP %d without the expected JSON-RPC reply; "+
				"verify the invitation before repeating a mutation", resp.StatusCode)))
		return ""
	}
	guestWriteReply(out, body)
	if methodOf(line) == "initialize" {
		return resp.Header.Get("Mcp-Session-Id")
	}
	return ""
}

func guestValidReply(line, body []byte) bool {
	if len(body) == 0 {
		return bytes.Equal(idOf(line), []byte("null")) // notifications need no reply
	}
	var rpc struct {
		Version string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	return json.Unmarshal(body, &rpc) == nil && rpc.Version == "2.0" &&
		bytes.Equal(rpc.ID, idOf(line)) && (len(rpc.Result) > 0) != (len(rpc.Error) > 0)
}

func guestWriteReply(out *syncWriter, reply []byte) {
	if len(reply) != 0 {
		out.line(reply)
	} // notifications get NO synthesized line
}

// This grace retries only never-accepted connections, at the SAME endpoint.
// No boardNow, local config refresh, DNS/Supgang discovery or CA reload.
func guestDoWithGrace(client *http.Client, req *http.Request, body []byte) (*http.Response, error) {
	grace := upgradeGrace
	if startupRequest(body) {
		grace = startupGrace
	}
	deadline := time.Now().Add(grace)
	for {
		// #nosec G704 -- guestTransport checks the exact operator-invited POST URL and Host on EVERY
		// round trip; guestClient dials only that literal IPv6, ignores proxies and refuses all redirects.
		resp, err := client.Do(req)
		if err == nil || !dialFailed(err) || !time.Now().Before(deadline) {
			return resp, err
		}
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(150 * time.Millisecond):
		}
		next := req.Clone(req.Context())
		next.Body = io.NopCloser(bytes.NewReader(body))
		req = next
	}
}

func guestTransportReply(line []byte, err error) []byte {
	return guestHint(unreachableReply(line, err))
}

// Rewrite only bridge-generated diagnostic hints, never a genuine server's
// RPC response. Running doctor/upgrade on a guest would affect the wrong board.
func guestHint(reply []byte) []byte {
	if len(reply) == 0 {
		return nil
	}
	var m map[string]any
	decoder := json.NewDecoder(bytes.NewReader(reply))
	decoder.UseNumber()
	if decoder.Decode(&m) != nil {
		return reply
	}
	e, _ := m["error"].(map[string]any)
	d, _ := e["data"].(map[string]any)
	if d != nil {
		h, _ := d["hint"].(string)
		if strings.Contains(h, "dibs doctor") || strings.Contains(h, "dibs upgrade") {
			d["hint"] = "the guest endpoint refused or was unreachable before accepting this call; " +
				"ask the issuer to check its listener and live invitation, never use a local-board secret or relax TLS"
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return reply
	}
	return b
}
