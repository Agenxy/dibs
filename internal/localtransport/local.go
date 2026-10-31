// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package localtransport authenticates a local HTTP peer before any request
// bytes leave the process. It is deliberately outside the replayable core.
package localtransport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Kind states which authority the caller is addressing, independently of a
// browser listen address. A loopback URL can also name an SSH forward.
type Kind string

// Target kinds and the fixed, private-profile endpoint name.
const (
	Local      Kind = "local"
	HTTPS      Kind = "https"
	SocketName      = "dibd.sock"
)

// Parse refuses unknown policies rather than making them a plaintext fallback.
func Parse(value string) (Kind, error) {
	switch k := Kind(value); k {
	case Local, HTTPS:
		return k, nil
	default:
		return "", fmt.Errorf("client transport %q is unknown: choose local for this profile's IPC socket "+
			"or https for a remote board", value)
	}
}

// Path is the fixed endpoint inside the selected board directory.
func Path(dir string) string { return filepath.Join(dir, SocketName) }

// UnsupportedHint is also used on Windows, where Unix mode bits are not an ACL.
const UnsupportedHint = "owner-authenticated local IPC is unavailable on this platform; " +
	"use an explicitly selected https client transport and the board's verified certificate, " +
	"never plaintext local credentials"

// MissingHint does not suggest retrying against a listener whose identity has
// not been established. HTTPS must extend to the board, even through SSH.
const MissingHint = "start or upgrade dibd for this data directory; for a remote board use " +
	"client_transport = \"https\" (or DIBS_TRANSPORT=https), an https:// address and the board's trusted CA, " +
	"including through an HTTPS SSH forward; plaintext TCP fallback is refused"

type (
	localKey struct{}
	auditKey struct{}
)

// PeerAuditToken is kernel connection evidence, never a request header. Empty
// on transports/platforms that cannot attest a native notification peer.
func PeerAuditToken(ctx context.Context) []byte {
	token, _ := ctx.Value(auditKey{}).([]byte)
	return append([]byte(nil), token...)
}

// IsLocal is kernel-derived connection evidence, never a request header.
func IsLocal(ctx context.Context) bool {
	v, _ := ctx.Value(localKey{}).(bool)
	return v
}

// ConnContext is installed only on a daemon's local listener. Verification
// precedes net/http reading application bytes; an unverified peer is closed.
func ConnContext(ctx context.Context, conn net.Conn) context.Context {
	if _, ok := conn.(*net.UnixConn); !ok {
		return ctx
	}
	if err := verifyPeer(conn); err != nil {
		_ = conn.Close()
		return ctx
	}
	ctx = context.WithValue(ctx, localKey{}, true)
	if token := peerAuditToken(conn); len(token) == 32 {
		ctx = context.WithValue(ctx, auditKey{}, token)
	}
	return ctx
}

// RoundTripper returns a transport for one explicitly selected profile. The
// logical HTTP authority is retained for origin/ticket checks; local bytes use
// plain HTTP exclusively over the owner-authenticated Unix connection.
func RoundTripper(dir string, kind Kind, base *http.Transport) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport.(*http.Transport)
	}
	rt := base.Clone()
	rt.Proxy = nil
	if kind == Local {
		rt.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return Dial(ctx, dir)
		}
		// A caller may have selected HTTPS for its browser address. Local IPC
		// has its own authenticated connection and does not negotiate TCP TLS.
		rt.DialTLSContext = nil
	}
	return &roundTripper{dir: dir, kind: kind, next: rt}
}

type roundTripper struct {
	dir  string
	kind Kind
	next *http.Transport
}

func (t *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.User != nil || req.URL.Host == "" {
		return nil, errors.New("refusing credentials for an ambiguous HTTP authority; name the board host without userinfo")
	}
	switch t.kind {
	case HTTPS:
		if req.URL.Scheme != "https" {
			return nil, refuse(fmt.Errorf("remote coordination requires verified HTTPS: %s", MissingHint))
		}
		if t.next.TLSClientConfig != nil && t.next.TLSClientConfig.InsecureSkipVerify {
			return nil, refuse(errors.New("remote coordination refuses unverified TLS; trust the board's CA or key pin"))
		}
		return t.next.RoundTrip(req)
	case Local:
		// Validate even on a reused connection: mode/ownership changes are
		// unsafe state, not permission to keep sending on an old pool entry.
		if err := CheckDir(t.dir, false); err != nil {
			return nil, refuse(err)
		}
		if err := checkSocket(Path(t.dir)); err != nil {
			return nil, err
		}
		clone := req.Clone(req.Context())
		clone.Header.Set("X-Dibs-IPC-Scheme", req.URL.Scheme)
		clone.URL.Scheme = "http"
		resp, err := t.next.RoundTrip(clone)
		if resp != nil {
			resp.Request = req
		}
		return resp, err
	default:
		return nil, errors.New("no client transport selected; choose local IPC or verified HTTPS")
	}
}

// CloseIdleConnections releases this profile's pool when a bridge changes boards.
func (t *roundTripper) CloseIdleConnections() { t.next.CloseIdleConnections() }

// RefuseRedirect prevents a successful request from forwarding custom board or
// admin credentials to another HTTPS authority. Native clients never need it.
func RefuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// PrivateFile refuses symlinks and permissive credential files before reading.
// A missing file is allowed only when the caller will create it itself.
func PrivateFile(path string, missing bool) error {
	info, err := os.Lstat(path)
	if missing && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := privateFileInfo(path, info); err != nil {
		return err
	}
	return checkCredentialACL(path, info)
}

func privateFileInfo(path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() || !owned(info) || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("credential file %s must be a regular file owned by this user with mode 0600; "+
			"inspect its provenance before using it; %s", path, credentialRepair(path))
	}
	return nil
}

func socketError(path string, err error) error {
	return refuse(fmt.Errorf("local IPC at %s is unavailable or unsafe (%w): %s", path, err, MissingHint))
}

type refusal struct{ error }

func (e *refusal) Unwrap() error { return e.error }

func refuse(err error) error { return &refusal{err} }

// RefusalHint returns only a transport's pre-send corrective explanation,
// excluding net/http's request URL wrapper and arbitrary network error text.
func RefusalHint(err error) (string, bool) {
	var r *refusal
	if errors.As(err, &r) {
		return r.Error(), true
	}
	return "", false
}

type notListening struct{ error }

func (e *notListening) Unwrap() error { return e.error }

// NotListening identifies a verified private profile with no socket to dial.
// It is a pre-send condition suitable for the existing restart grace, never
// authority to fall back to a TCP listener. Unsafe ownership is not this error.
func NotListening(err error) bool {
	var missing *notListening
	return errors.As(err, &missing)
}

// IsLegacyHTTP reports the address shape for migration diagnostics only.
func IsLegacyHTTP(raw string) bool { return strings.HasPrefix(strings.ToLower(raw), "http://") }
