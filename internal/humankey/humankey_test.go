// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package humankey

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"testing"
	"time"
)

// softKey stands in for the Secure Enclave: the same curve, the same DER
// encodings CryptoKit produces, and no finger.
func softKey(t *testing.T, curve elliptic.Curve) (*ecdsa.PrivateKey, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return k, base64.StdEncoding.EncodeToString(der)
}

func sign(t *testing.T, k *ecdsa.PrivateKey, msg []byte) string {
	t.Helper()
	d := sha256.Sum256(msg)
	sig, err := ecdsa.SignASN1(rand.Reader, k, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// A signature proves the person only for the message it covers, on the
// board it names: change any field and it stops verifying.
func TestASignatureCoversExactlyWhatItSays(t *testing.T) {
	store := NewStore(t.TempDir())
	priv, pub := softKey(t, elliptic.P256())
	k, err := store.Add(pub, "laptop", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	msg := AnswerMessage("node-a", 42, "approve", "n1")
	sig := sign(t, priv, msg)
	if err := Verify(k, msg, sig); err != nil {
		t.Fatalf("a good signature did not verify: %v", err)
	}
	for name, other := range map[string][]byte{
		"another board":       AnswerMessage("node-b", 42, "approve", "n1"),
		"another message":     AnswerMessage("node-a", 43, "approve", "n1"),
		"another disposition": AnswerMessage("node-a", 42, "deny", "n1"),
		"another nonce":       AnswerMessage("node-a", 42, "approve", "n2"),
		"a session":           SessionMessage("node-a", "n1"),
	} {
		if Verify(k, other, sig) == nil {
			t.Errorf("a signature for one answer verified for %s", name)
		}
	}
	// And only the enrolled key's signature counts.
	stranger, _ := softKey(t, elliptic.P256())
	if Verify(k, msg, sign(t, stranger, msg)) == nil {
		t.Error("a stranger's signature verified against the enrolled key")
	}
}

// The Secure Enclave makes P-256 and nothing else, so another curve did not
// come from one.
func TestOnlyP256KeysEnrol(t *testing.T) {
	_, pub := softKey(t, elliptic.P384())
	if _, err := NewStore(t.TempDir()).Add(pub, "x", time.Now()); err == nil {
		t.Error("a P-384 key was enrolled")
	}
	if _, err := NewStore(t.TempDir()).Add("not base64!", "x", time.Now()); err == nil {
		t.Error("garbage was enrolled")
	}
}

// Enrolling the same key twice is one relay, and revoking it ends it.
func TestEnrolmentIsIdempotentAndRevocable(t *testing.T) {
	store := NewStore(t.TempDir())
	_, pub := softKey(t, elliptic.P256())
	a, _ := store.Add(pub, "laptop", time.Now())
	b, _ := store.Add(pub, "again", time.Now())
	if a.ID != b.ID {
		t.Fatalf("one key enrolled as two: %s, %s", a.ID, b.ID)
	}
	keys, _ := store.List()
	if len(keys) != 1 {
		t.Fatalf("%d keys after enrolling one twice", len(keys))
	}
	if gone, _ := store.Remove(a.ID); !gone {
		t.Fatal("remove did not find the key")
	}
	if _, err := store.Find(a.ID); err == nil {
		t.Error("a revoked key is still found")
	}
}

// A challenge is spent once, by the key it was issued to, before it expires.
func TestAChallengeIsSpentOnceByItsOwnKey(t *testing.T) {
	l := NewLedger()
	now := time.Now()
	n, err := l.Challenge("k1", now)
	if err != nil {
		t.Fatal(err)
	}
	if l.Redeem("k2", n, now) {
		t.Error("another key spent this key's challenge")
	}
	// The wrong key's attempt spent it: a guess cannot be retried.
	if l.Redeem("k1", n, now) {
		t.Error("a challenge survived a failed redemption")
	}
	n2, _ := l.Challenge("k1", now)
	if !l.Redeem("k1", n2, now) {
		t.Fatal("a fresh challenge did not redeem")
	}
	if l.Redeem("k1", n2, now) {
		t.Error("a challenge redeemed twice")
	}
	n3, _ := l.Challenge("k1", now)
	if l.Redeem("k1", n3, now.Add(NonceTTL+time.Second)) {
		t.Error("an expired challenge redeemed")
	}
}

// A session names its key until it expires or the key is revoked.
func TestASessionEndsWithItsKey(t *testing.T) {
	l := NewLedger()
	now := time.Now()
	tok, err := l.Open("k1", now)
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := l.Session(tok, now); !ok || k != "k1" {
		t.Fatalf("session = %q %v, want k1", k, ok)
	}
	if _, ok := l.Session(tok, now.Add(SessionTTL+time.Second)); ok {
		t.Error("an expired session still stands")
	}
	tok2, _ := l.Open("k1", now)
	l.Revoke("k1")
	if _, ok := l.Session(tok2, now); ok {
		t.Error("a revoked key's session still stands")
	}
	if _, ok := l.Session("", now); ok {
		t.Error("the empty token is a session")
	}
}
