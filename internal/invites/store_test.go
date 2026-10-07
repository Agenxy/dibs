// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package invites

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCredentialIsShownOnceAndRevokedOnNextRead(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	now := time.Now()
	token, err := s.Mint("cloud-worker", 24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, "invites.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), token) || strings.Contains(string(b), Prefix) {
		t.Fatal("raw credential persisted")
	}
	info, err := os.Stat(filepath.Join(s.Dir, "invites.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file permissions: %v", info.Mode())
	}
	e, err := s.Authenticate(token, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Bind(e, "cloud-worker"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Mint("cloud-worker", time.Hour, now); err == nil {
		t.Fatal("overwrote a live invite")
	}
	if err = s.Revoke("cloud-worker"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(token, now); err == nil {
		t.Fatal("revoked credential still accepted")
	}
	if err = s.Bind(e, "cloud-worker"); err == nil {
		t.Fatal("old request undid revocation")
	}
	newToken, err := s.Mint("cloud-worker", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Authenticate(newToken, now)
	if err != nil {
		t.Fatal(err)
	}
	if current.AgentID != "cloud-worker" {
		t.Fatal("reissue lost mailbox binding")
	}
	if err = s.Bind(e, "another"); err == nil {
		t.Fatal("old generation rebound reissued key")
	}
	if err = s.Bind(current, "another"); err == nil {
		t.Fatal("reissued key bound another mailbox")
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Digest != "" {
		t.Fatal("list exposed verifier or lost binding")
	}
	if _, err = s.Authenticate(newToken, now.Add(time.Hour)); err == nil {
		t.Fatal("expired invite accepted")
	}
}

func TestInvitationCapIsAtomicAndGenerationRevocationCannotTouchNewIssuance(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	now := time.Now()
	issuer := Issuance{By: "parent", Created: 10, MaxLive: 4}
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for i := range 20 {
		wg.Go(func() {
			_, err := s.MintIssued("parent-cloud-"+strconv.Itoa(i), time.Hour, now, issuer)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 4 {
		t.Fatalf("concurrent cap admitted %d, want 4", successes)
	}
	if err := s.RevokeOwned("", "parent", issuer); err != nil {
		t.Fatal(err)
	}
	issuer.Closed = 15
	key, err := s.MintIssued("parent-new", time.Hour, now, issuer)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeGeneration("parent", 10, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(key, now); err != nil {
		t.Fatal("old closure revoked later generation:", err)
	}
	if err := s.RevokeOwned("parent-new", "", Issuance{By: "stranger", Created: 10}); err == nil {
		t.Fatal("foreign issuer revoked a child")
	}
}

func TestExpiredConfigurationGCUsesCredentialGeneration(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	now := time.Now()
	key, err := s.Mint("worker", time.Second, now)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := s.Authenticate(key, now)
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := s.Mint("worker", time.Hour, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.forget(observed, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(newKey, now.Add(3*time.Second)); err != nil {
		t.Fatal("GC removed concurrently reissued credential:", err)
	}
	if err := s.Revoke("worker"); err != nil {
		t.Fatal(err)
	}
	entries, err := s.entries()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.forget(entries[0], now); err != nil {
		t.Fatal(err)
	}
	entries, err = s.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("dead configuration retained: %v %v", entries, err)
	}
}

func TestMalformedStoreFailsClosed(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(s.Dir, "invites.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mint("worker", time.Hour, time.Now()); err == nil {
		t.Fatal("silently replaced unreadable access config")
	}
	for _, v := range []string{"", "Bearer key", Prefix, Prefix + strings.Repeat("0", 64)} {
		if _, err := s.Authenticate(v, time.Now()); err == nil {
			t.Fatal("bad credential accepted")
		}
	}
}
