// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/agenxy/dibs/internal/adminpw"
)

// Only serial request fixtures may replace this seam. They exercise the real
// verifier and authority checks, but not the production KDF's CPU cost.
// Production's factor and real verification are pinned in adminpw's tests.
var requestPasswordHash = adminpw.Hash

func useFastRequestPasswordHash(t *testing.T) {
	t.Helper()
	previous := requestPasswordHash
	t.Cleanup(func() { requestPasswordHash = previous })
	requestPasswordHash = func(password string) (string, error) {
		const rounds = 1000
		salt := []byte("fixture-salt-543")
		key, err := pbkdf2.Key(sha256.New, password, salt, rounds, 32)
		if err != nil {
			return "", err
		}
		encode := base64.RawStdEncoding.EncodeToString
		return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", rounds, encode(salt), encode(key)), nil
	}
}
