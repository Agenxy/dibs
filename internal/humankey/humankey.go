// Package humankey is how a board that runs somewhere else knows a person is
// present: a signature from a key that signs only after Touch ID, checked
// here with the standard library, so the check is the same on a Linux board
// as on a Mac one. docs/NETWORK.md §8 is the argument.
//
// The key lives in the Secure Enclave of the person's Mac (see
// `dibs-presence --key`, internal/humanauth/presence_darwin.swift) and its
// private half never leaves the chip. This package sees only the public half,
// the messages and the signatures.
package humankey

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Key is one enrolled relay: the public half of a Secure Enclave key.
type Key struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	PublicKey string    `json:"public_key"` // base64 DER SubjectPublicKeyInfo
	Enrolled  time.Time `json:"enrolled"`
}

// FileName is where a board keeps its enrolled keys, beside its other
// credentials in the data directory.
const FileName = "human-keys.json"

// ErrUnknownKey is a key id this board never enrolled, or one revoked since.
var ErrUnknownKey = errors.New("no enrolled key has that id")

// Store is the enrolled keys of one board. Read fresh on every lookup, like
// the admin password, so a revocation takes effect without a restart.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore is the store at dir/human-keys.json.
func NewStore(dir string) *Store { return &Store{path: filepath.Join(dir, FileName)} }

// List returns every enrolled key. A missing file is no keys.
func (s *Store) List() ([]Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

func (s *Store) read() ([]Key, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []Key
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, fmt.Errorf("%s does not parse: %w", s.path, err)
	}
	return keys, nil
}

func (s *Store) write(keys []Key) error {
	raw, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Add enrols a public key. Enrolling the same key again returns the existing
// entry, so a retried enrolment is not a second relay.
func (s *Store) Add(publicKey, label string, now time.Time) (Key, error) {
	pub, err := parsePublic(publicKey)
	if err != nil {
		return Key{}, err
	}
	der, _ := x509.MarshalPKIXPublicKey(pub)
	id := IDOf(der)
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.read()
	if err != nil {
		return Key{}, err
	}
	for _, k := range keys {
		if k.ID == id {
			return k, nil
		}
	}
	k := Key{ID: id, Label: label, PublicKey: base64.StdEncoding.EncodeToString(der), Enrolled: now.UTC()}
	if err := s.write(append(keys, k)); err != nil {
		return Key{}, err
	}
	return k, nil
}

// Remove revokes a key. Removing one that is not there is not an error.
func (s *Store) Remove(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.read()
	if err != nil {
		return false, err
	}
	kept := keys[:0]
	found := false
	for _, k := range keys {
		if k.ID == id {
			found = true
			continue
		}
		kept = append(kept, k)
	}
	if !found {
		return false, nil
	}
	return true, s.write(kept)
}

// Find returns an enrolled key by id.
func (s *Store) Find(id string) (Key, error) {
	keys, err := s.List()
	if err != nil {
		return Key{}, err
	}
	for _, k := range keys {
		if k.ID == id {
			return k, nil
		}
	}
	return Key{}, ErrUnknownKey
}

// IDOf names a key by its DER SubjectPublicKeyInfo.
func IDOf(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:8])
}

// parsePublic accepts a base64 DER SubjectPublicKeyInfo for a P-256 key and
// nothing else: the Secure Enclave makes P-256, so any other curve did not
// come from one.
func parsePublic(b64 string) (*ecdsa.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("the public key is not base64: %w", err)
	}
	any, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("the public key is not a DER SubjectPublicKeyInfo: %w", err)
	}
	pub, ok := any.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, errors.New("the public key is not P-256, which is the only curve a Secure Enclave makes")
	}
	return pub, nil
}

// Verify checks an ASN.1 DER ECDSA signature over SHA-256 of msg, which is
// what CryptoKit's derRepresentation produces.
func Verify(k Key, msg []byte, sigB64 string) error {
	pub, err := parsePublic(k.PublicKey)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("the signature is not base64: %w", err)
	}
	digest := sha256.Sum256(msg)
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		return errors.New("the signature does not verify against the enrolled key")
	}
	return nil
}

// SessionMessage is what a relay signs to open a session with one board.
// The board's node id is in it, so a signature for one board is worth
// nothing to another.
func SessionMessage(node, nonce string) []byte {
	return []byte("dibs-human-session/v1\n" + node + "\n" + nonce)
}

// AnswerMessage is what a relay signs to approve a request that grants
// something. Every field that decides the effect is in it.
func AnswerMessage(node string, serial uint64, disposition, nonce string) []byte {
	return []byte("dibs-human-answer/v1\n" + node + "\n" + strconv.FormatUint(serial, 10) +
		"\n" + disposition + "\n" + nonce)
}

// NonceTTL bounds how long a challenge may wait for a finger.
const NonceTTL = 2 * time.Minute

// SessionTTL is how long one Touch ID keeps a relay attached.
const SessionTTL = 7 * 24 * time.Hour

// maxOutstanding bounds the nonces and sessions held at once, so a caller
// asking for challenges in a loop costs memory up to here and no further.
const maxOutstanding = 256

type grant struct {
	key     string
	expires time.Time
}

// Ledger holds the nonces and sessions a board has issued. In memory only:
// a board that restarts asks each relay for one more Touch ID, which is the
// honest price of not writing bearer tokens to disk.
type Ledger struct {
	mu       sync.Mutex
	nonces   map[string]grant
	sessions map[string]grant
}

// NewLedger is an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{nonces: map[string]grant{}, sessions: map[string]grant{}}
}

func random() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func prune(m map[string]grant, now time.Time) {
	for k, g := range m {
		if now.After(g.expires) {
			delete(m, k)
		}
	}
}

// Challenge issues a nonce for one key, usable once.
func (l *Ledger) Challenge(keyID string, now time.Time) (string, error) {
	n, err := random()
	if err != nil {
		return "", err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	prune(l.nonces, now)
	if len(l.nonces) >= maxOutstanding {
		return "", errors.New("too many challenges outstanding: answer one or wait two minutes")
	}
	l.nonces[n] = grant{key: keyID, expires: now.Add(NonceTTL)}
	return n, nil
}

// Redeem spends a nonce. It must have been issued to this key and not yet
// used or expired.
func (l *Ledger) Redeem(keyID, nonce string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	g, ok := l.nonces[nonce]
	delete(l.nonces, nonce)
	return ok && g.key == keyID && !now.After(g.expires)
}

// Open mints a session token for a key whose signature has been checked.
func (l *Ledger) Open(keyID string, now time.Time) (string, error) {
	t, err := random()
	if err != nil {
		return "", err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	prune(l.sessions, now)
	if len(l.sessions) >= maxOutstanding {
		return "", errors.New("too many relay sessions open")
	}
	l.sessions[t] = grant{key: keyID, expires: now.Add(SessionTTL)}
	return t, nil
}

// Session returns the key a token was opened for, if it is still valid.
func (l *Ledger) Session(token string, now time.Time) (string, bool) {
	if token == "" {
		return "", false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for t, g := range l.sessions {
		if subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			if now.After(g.expires) {
				delete(l.sessions, t)
				return "", false
			}
			return g.key, true
		}
	}
	return "", false
}

// Revoke ends every session and nonce held by a key, for when it is removed.
func (l *Ledger) Revoke(keyID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for t, g := range l.sessions {
		if g.key == keyID {
			delete(l.sessions, t)
		}
	}
	for n, g := range l.nonces {
		if g.key == keyID {
			delete(l.nonces, n)
		}
	}
}
