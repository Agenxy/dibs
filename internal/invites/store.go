// Package invites owns policy-issued, revocable access configuration, not
// coordination state. The ledger never receives an invitation credential.
package invites

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/paths"
)

// Prefix distinguishes invitation credentials from agent and board tokens.
const Prefix = "dibs_inv_"

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// PolicyError distinguishes corrective issuance refusals from storage faults.
type PolicyError struct{ Why string }

func (e *PolicyError) Error() string { return e.Why }

func policyError(why string) error { return &PolicyError{Why: why} }

// Entry contains only the credential's verifier. AgentID survives revocation
// and reissue: issuing a new key does not issue a new mailbox.
type Entry struct {
	Name          string    `json:"name"`
	Digest        string    `json:"digest,omitempty"`
	Expires       time.Time `json:"expires"`
	AgentID       string    `json:"agent_id,omitempty"`
	Revoked       bool      `json:"revoked,omitempty"`
	IssuedBy      string    `json:"issued_by"`
	IssuerCreated uint64    `json:"issuer_created,omitempty"`
	IssuerClosed  uint64    `json:"issuer_closed,omitempty"`
}

// Issuance is authenticated off the store lock, then enforced atomically for
// ownership and caps. Human authority is passed only from a proved admin route.
type Issuance struct {
	By              string
	Created, Closed uint64
	MaxLive         int
	Human           bool
}

// Store is the addressed board's access configuration, not its ledger.
type Store struct{ Dir string }

// ValidName admits an unambiguous ASCII name and stable address.
func ValidName(name string) bool {
	return len(engine.InvitationHost(name)) <= core.DefaultLimits().MaxNameBytes && namePattern.MatchString(name)
}

func verifier(token string) string {
	x := sha256.Sum256([]byte(token))
	return hex.EncodeToString(x[:])
}

// transaction serializes CLI writers and daemon bindings across processes.
// Every request reads the file anew, so revocation is not a daemon restart.
func (s Store) transaction(write bool, f func(map[string]Entry) error) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(s.Dir, "invites.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	acquire := paths.LockShared
	if write {
		acquire = paths.LockExclusive
	}
	if err = acquire(lock, true); err != nil {
		return err
	}
	defer paths.Unlock(lock)
	file := filepath.Join(s.Dir, "invites.json")
	entries, err := readEntries(file)
	if err != nil {
		return err
	}
	if err = f(entries); err != nil || !write {
		return err
	}
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".invites-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(tmpName) }()
	if _, err = tmp.Write(b); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, file)
}

func readEntries(file string) (map[string]Entry, error) {
	entries := map[string]Entry{}
	// #nosec G304 -- fixed basename in the operator's data directory, never client input
	b, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(b) > 0 {
		if err = json.Unmarshal(b, &entries); err != nil {
			return nil, fmt.Errorf("invites.json cannot be read: %w; restore its backup, do not reset access silently", err)
		}
		if entries == nil {
			return nil, errors.New("invites.json must be an object, not null; restore its backup")
		}
		for name, e := range entries {
			if !ValidName(name) || e.Name != name || len(e.Digest) != 64 || e.Expires.IsZero() || e.IssuedBy == "" {
				return nil, errors.New("invites.json contains an invalid entry; restore its backup")
			}
		}
	}
	return entries, nil
}

// Mint returns a key once. Live keys are never silently overwritten.
func (s Store) Mint(name string, ttl time.Duration, now time.Time) (string, error) {
	return s.MintIssued(name, ttl, now, Issuance{By: core.HumanActor, Human: true})
}

// RevokeGeneration records a derived closure without touching a concurrently
// issued credential belonging to the issuer's later generation.
func (s Store) RevokeGeneration(by string, created, closed uint64) error {
	return s.transaction(true, func(m map[string]Entry) error {
		for name, e := range m {
			if e.IssuedBy == by && e.IssuerCreated == created && e.IssuerClosed == closed {
				e.Revoked = true
				m[name] = e
			}
		}
		return nil
	})
}

// MintIssued enforces ownership and the per-issuer live cap inside the same
// cross-process transaction that installs the new credential verifier.
func (s Store) MintIssued(name string, ttl time.Duration, now time.Time, issuer Issuance) (string, error) {
	if !ValidName(name) {
		return "", errors.New("invite names must start with a letter and contain lowercase ASCII letters, " +
			"digits or hyphens; name and invite host must fit the core name limit")
	}
	if ttl <= 0 || ttl > 365*24*time.Hour {
		return "", errors.New("invite ttl must be positive and no greater than 365d")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := Prefix + hex.EncodeToString(raw)
	err := s.transaction(true, func(m map[string]Entry) error {
		old, exists := m[name]
		if err := admitMint(m, old, exists, issuer, now); err != nil {
			return err
		}
		m[name] = Entry{
			Name: name, Digest: verifier(token), Expires: now.Add(ttl), AgentID: old.AgentID,
			IssuedBy: issuer.By, IssuerCreated: issuer.Created, IssuerClosed: issuer.Closed,
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

func ownsEntry(e Entry, issuer Issuance) bool {
	return issuer.Human || (e.IssuedBy == issuer.By && e.IssuerCreated == issuer.Created)
}

func admitMint(entries map[string]Entry, old Entry, exists bool, issuer Issuance, now time.Time) error {
	if exists && !ownsEntry(old, issuer) {
		return policyError("that invite belongs to another issuer; the human may revoke or replace it")
	}
	if exists && !old.Revoked && now.Before(old.Expires) {
		return policyError("invite is still live: revoke it first; its credential cannot be shown again")
	}
	if !exists && len(entries) >= 1024 {
		return policyError("invite store is at its 1024-name limit; retained mailboxes still need their bindings")
	}
	if !issuer.Human && liveInvites(entries, issuer, now) >= issuer.MaxLive {
		return policyError("issuer's live-invitation cap reached; revoke a finished child's invitation first")
	}
	return nil
}

func liveInvites(entries map[string]Entry, issuer Issuance, now time.Time) int {
	n := 0
	for _, e := range entries {
		if e.IssuedBy == issuer.By && e.IssuerCreated == issuer.Created && e.IssuerClosed == issuer.Closed &&
			!e.Revoked && now.Before(e.Expires) {
			n++
		}
	}
	return n
}

// RevokeOwned revokes one name or all children of an issuer. A coordinator
// cannot revoke someone else's children; only their issuer or the human may.
func (s Store) RevokeOwned(name, issuedBy string, issuer Issuance) error {
	return s.transaction(true, func(m map[string]Entry) error {
		if issuedBy != "" {
			return revokeIssuer(m, issuedBy, issuer)
		}
		e, ok := m[name]
		if !ok {
			return policyError("no such invite; call invite with action: list")
		}
		if !ownsEntry(e, issuer) {
			return policyError("only the issuer or human may revoke that invitation")
		}
		e.Revoked = true
		m[name] = e
		return nil
	})
}

func revokeIssuer(entries map[string]Entry, issuedBy string, issuer Issuance) error {
	if !issuer.Human && issuedBy != issuer.By {
		return policyError("only the issuer or human may revoke its children")
	}
	for key, e := range entries {
		if e.IssuedBy == issuedBy && ownsEntry(e, issuer) {
			e.Revoked = true
			entries[key] = e
		}
	}
	return nil
}

// List returns access metadata, never the credential or its verifier.
func (s Store) List() ([]Entry, error) {
	out, err := s.entries()
	for i := range out {
		out[i].Digest = ""
	}
	return out, err
}

func (s Store) entries() ([]Entry, error) {
	var out []Entry
	err := s.transaction(false, func(m map[string]Entry) error {
		for _, e := range m {
			out = append(out, e)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
}

// forget drops only the observed expired/revoked generation. Service first
// proves its bound mailbox is gone; a concurrently reissued key is preserved.
func (s Store) forget(e Entry, now time.Time) error {
	return s.transaction(true, func(m map[string]Entry) error {
		current, ok := m[e.Name]
		if ok && current.Digest == e.Digest && (current.Revoked || !now.Before(current.Expires)) {
			delete(m, e.Name)
		}
		return nil
	})
}

// Revoke refuses this credential on the next request while retaining its binding.
func (s Store) Revoke(name string) error {
	return s.transaction(true, func(m map[string]Entry) error {
		e, ok := m[name]
		if !ok {
			return fmt.Errorf("no invite %q: run dibs invite list", name)
		}
		e.Revoked = true
		m[name] = e
		return nil
	})
}

// Authenticate reads current configuration on every request, failing closed.
func (s Store) Authenticate(token string, now time.Time) (Entry, error) {
	var out Entry
	if len(token) != len(Prefix)+64 || token[:len(Prefix)] != Prefix {
		return out, errors.New("invalid invite credential; use the key printed by dibs invite")
	}
	digest := verifier(token)
	err := s.transaction(false, func(m map[string]Entry) error {
		for _, e := range m {
			if subtle.ConstantTimeCompare([]byte(e.Digest), []byte(digest)) == 1 && !e.Revoked && now.Before(e.Expires) {
				out = e
				return nil
			}
		}
		return errors.New("invite expired, revoked or unknown; ask the operator for a new invitation")
	})
	return out, err
}

// CheckGeneration revalidates a transfer's admitted verifier without retaining
// its original invitation key. Revoke/reissue and expiry invalidate old tickets.
func (s Store) CheckGeneration(entry Entry, now time.Time) error {
	return s.transaction(false, func(m map[string]Entry) error {
		current, ok := m[entry.Name]
		if !ok || current.Digest != entry.Digest || current.Revoked || !now.Before(current.Expires) ||
			current.AgentID != entry.AgentID || current.IssuedBy != entry.IssuedBy ||
			current.IssuerCreated != entry.IssuerCreated || current.IssuerClosed != entry.IssuerClosed {
			return errors.New("invitation generation expired or changed; authorize a new transfer with a live invitation")
		}
		return nil
	})
}

// Bind is compare-and-set against the credential generation that admitted the
// request, so a concurrent revoke/reissue cannot be undone by an old request.
func (s Store) Bind(e Entry, id string) error {
	return s.transaction(true, func(m map[string]Entry) error {
		current, ok := m[e.Name]
		if !ok || current.Digest != e.Digest || current.Revoked || !time.Now().Before(current.Expires) {
			return errors.New("invitation changed while registering; request a current invitation")
		}
		if current.AgentID != "" && current.AgentID != id {
			return errors.New("invitation is already bound to another agent; recover it with its original nonce")
		}
		current.AgentID = id
		m[e.Name] = current
		return nil
	})
}
