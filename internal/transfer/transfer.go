// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package transfer owns ephemeral byte-plane capabilities, never ledger state.
package transfer

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/blobstore"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/invites"
	xport "github.com/agenxy/dibs/internal/transport"
)

// Prefix is the narrowly scoped byte-plane resource namespace.
const Prefix = "/files/"

const ticketLifetime = 15 * time.Minute

const ticketMaximum = 24 * time.Hour

type invitationEntryKey struct{}

// WithInvitationEntry carries the verifier already proved by the public gate.
// It is transport-only, never accepted from MCP arguments or stored in the ledger.
func WithInvitationEntry(ctx context.Context, entry invites.Entry) context.Context {
	return context.WithValue(ctx, invitationEntryKey{}, entry)
}

// Descriptor uses SEP-2631's HTTPS wire shape. HTTP is an explicitly local-only
// Dibs extension; these tools do not claim the draft's files/authorize methods.
type Descriptor struct {
	Transport string `json:"transport"`
	Method    string `json:"method"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
}

// Digest follows SEP-2631: SHA-256 encoded as unpadded base64url.
type Digest struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

// FileValue names immutable committed bytes; pending handles are a Dibs extension.
type FileValue struct {
	URI      string  `json:"uri"`
	Name     string  `json:"name,omitempty"`
	MimeType string  `json:"mimeType,omitempty"`
	Size     *int64  `json:"size,omitempty"`
	Digest   *Digest `json:"digest,omitempty"`
}

// Manager has one shared instance for both listeners, one owner per capability,
// and bounded admission independent of engine rate limits or blob eviction.
type Manager struct {
	eng      *engine.Engine
	store    *blobstore.Store
	invites  invites.Store
	mu       sync.Mutex
	tickets  map[string]*ticket
	now      func() time.Time
	stopping bool
}

type ticket struct {
	mu                     sync.Mutex
	id                     engine.TransferIdentity
	entry                  *invites.Entry
	origin                 string
	expires, deadline      time.Time
	upload                 *blobstore.Upload
	blob, hash, mime, name string
	size                   *int64
	result                 core.Result
	failed                 bool
}

// New binds the actual shared encrypted store. Cleanup is owned by this daemon
// context; no dormant ticket can retain a staging file or reservation forever.
func New(ctx context.Context, eng *engine.Engine, store *blobstore.Store, dir string) *Manager {
	m := &Manager{eng: eng, store: store, invites: invites.Store{Dir: dir}, tickets: map[string]*ticket{}, now: time.Now}
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				m.cleanup(true)
				return
			case <-tick.C:
				m.cleanup(false)
			}
		}
	}()
	return m
}

func refusal(code, message, hint string) *core.Error {
	return &core.Error{Code: code, Msg: message, Hint: hint}
}

func gone() error {
	return refusal("E_TRANSFER_EXPIRED", "transfer unavailable or expired",
		"authorize a fresh descriptor; phase one loses incomplete uploads on daemon restart")
}

func (m *Manager) release(t *ticket) {
	if t.upload != nil {
		t.upload.Abort()
		t.upload = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.eng.ReleaseTransfer(ctx, t.id.Reservation)
}

func (m *Manager) cleanup(all bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if all {
		m.stopping = true
	}
	for key, t := range m.tickets {
		if !t.mu.TryLock() {
			continue
		}
		if all || !m.now().Before(t.expires) {
			delete(m.tickets, key)
			if all { // engine is stopping too; its derived reservations disappear with it
				if t.upload != nil {
					t.upload.Abort()
					t.upload = nil
				}
			} else {
				m.release(t)
			}
		}
		t.mu.Unlock()
	}
}

func fileValue(blob, name, mime string, size *int64, hash string) FileValue {
	f := FileValue{URI: "dibs:blob:" + blob, Name: name, MimeType: mime, Size: size}
	if hash != "" {
		decoded, _ := hex.DecodeString(hash)
		f.Digest = &Digest{Algorithm: "sha-256", Value: base64.RawURLEncoding.EncodeToString(decoded)}
	}
	return f
}

// Authorize issues a random single-transfer capability after writer admission.
// Origin is configured by the listener, never supplied by the client.
func (m *Manager) Authorize(
	ctx context.Context, origin, token, blob, hash, mime, name string, size *int64,
) (core.Result, error) {
	if err := validateAuthorization(origin, hash, mime, name); err != nil {
		return nil, err
	}
	m.cleanup(false)
	i, err := m.authorizeIdentity(ctx, token, blob, hash, size)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			m.release(&ticket{id: i})
		}
	}()
	raw := make([]byte, 48)
	if _, err = rand.Read(raw); err != nil {
		return nil, err
	}
	key := hex.EncodeToString(raw[:32])
	pendingID := hex.EncodeToString(raw[32:]) // independently random, NOT a bearer capability
	now := m.now()
	t := &ticket{
		id: i, origin: origin, expires: now.Add(ticketLifetime), deadline: now.Add(ticketMaximum),
		blob: blob, hash: hash, mime: mime, name: name, size: size,
	}
	if entry, ok := ctx.Value(invitationEntryKey{}).(invites.Entry); ok {
		// The registration binding exists by the time this token is admitted.
		if entry.AgentID != i.Agent {
			return nil, core.ErrBadToken
		}
		t.entry = &entry
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping {
		return nil, gone()
	}
	if err := m.ticketCapacity(i.Agent); err != nil {
		return nil, err
	}
	method := http.MethodGet
	var value FileValue
	if blob == "" {
		method = http.MethodPut
		t.upload, err = m.store.BeginUpload(i.MaxSize)
		if err != nil {
			return nil, err
		}
		value = fileValue("sha256:"+hash, name, mime, size, hash)
		if hash == "" {
			value.URI = "dibs:pending:" + pendingID
		}
	} else {
		value = fileValue(blob, "", i.Mime, &i.Size, strings.TrimPrefix(blob, "sha256:"))
	}
	m.tickets[key] = t
	committed = true
	transport := "https"
	if strings.HasPrefix(origin, "http://") {
		transport = "http"
	}
	descriptor := Descriptor{
		Transport: transport, Method: method, URL: origin + Prefix + key, ExpiresAt: t.expires.Format(time.RFC3339),
	}
	if blob == "" {
		descriptor.URL = origin + Prefix + "up/" + key
		return core.Result{"file": value, "pending": true, "upload": descriptor}, nil
	}
	return core.Result{"file": value, "download": descriptor}, nil
}

func (m *Manager) authorizeIdentity(
	ctx context.Context, token, blob, hash string, size *int64,
) (engine.TransferIdentity, error) {
	if blob == "" {
		return m.eng.AuthorizeUpload(ctx, token, hash, size)
	}
	return m.eng.AuthorizeTransfer(ctx, token, blob, size)
}

func (m *Manager) authorizeRequest(ctx context.Context, t *ticket) (context.Context, error) {
	if !m.now().Before(t.expires) || t.failed {
		return ctx, gone()
	}
	if t.entry != nil {
		if err := m.invites.CheckGeneration(*t.entry, m.now()); err != nil {
			return ctx, refusal("E_INVITE_AUTH", "transfer invitation revoked, expired or replaced",
				"authorize a new transfer with a live invitation")
		}
		e := t.entry
		ctx = engine.WithInvitation(ctx, engine.Invitation{
			Name: e.Name, AgentID: e.AgentID, IssuedBy: e.IssuedBy, IssuerCreated: e.IssuerCreated, IssuerClosed: e.IssuerClosed,
		})
	}
	return ctx, m.eng.CheckTransfer(ctx, t.id, t.blob)
}

// touch is serialized by the ticket's lock. Actual accepted bytes extend the
// lease; HEAD probes, empty requests and a stalled reader cannot extend it.
func (m *Manager) touch(t *ticket) { t.expires = minTime(m.now().Add(ticketLifetime), t.deadline) }

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// HTTPStatus preserves corrective domain errors without exposing paths/keys.
func HTTPStatus(err error) int {
	var e *core.Error
	if errors.As(err, &e) {
		switch e.Code {
		case "E_TRANSFER_EXPIRED":
			return http.StatusGone
		case "E_TRANSFER_METHOD":
			return http.StatusMethodNotAllowed
		case "E_BAD_TOKEN", "E_INVITE_AUTH", "E_INVITE_SCOPE", "E_NO_BLOB":
			return http.StatusForbidden
		case "E_QUOTA", "E_STORE_FULL", "E_TRANSFER_LIMIT", "E_STAGING_SPACE":
			return http.StatusTooManyRequests
		case "E_HASH_MISMATCH":
			return http.StatusUnprocessableEntity
		case "E_TOO_LARGE":
			return http.StatusRequestEntityTooLarge
		}
	}
	return http.StatusBadRequest
}

func validateAuthorization(origin, hash, mime, name string) error {
	if err := validateOrigin(origin); err != nil {
		return err
	}
	if hash != "" && !core.ValidBlobID("sha256:"+hash) {
		return core.ErrBadID
	}
	if mime != "" && !core.ValidMime(mime) {
		return core.ErrBadMime
	}
	if len(name) > 256 || strings.ContainsAny(name, "\r\n\x00/\\") {
		return refusal("E_TRANSFER_NAME", "file name is not a bounded basename",
			"supply a basename up to 256 bytes without separators or control bytes")
	}
	return nil
}

func validateOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" ||
		(u.Scheme != "https" && (u.Scheme != "http" || !xport.IsLoopback(u.Host))) {
		return refusal("E_TRANSFER_TRANSPORT", "listener has no transfer origin",
			"use the daemon's HTTPS listener; plaintext transfers are loopback-only")
	}
	return nil
}

// Called under the manager lock, before allocating any ciphertext temp.
func (m *Manager) ticketCapacity(agent string) error {
	count := 0
	for _, old := range m.tickets {
		if old.id.Agent == agent {
			count++
		}
	}
	if len(m.tickets) >= 256 || count >= 16 {
		return refusal("E_TRANSFER_LIMIT", "too many outstanding transfers",
			"cancel unused descriptors with DELETE or wait for their 15-minute expiry")
	}
	return nil
}
