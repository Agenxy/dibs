// Package adminpw hashes and verifies the human admin password that gates the
// Lanes god-view (decrypted mail, web board). The password is never stored,
// only a salted PBKDF2 hash, so an agent that reads ~/.lanes/admin.hash cannot
// recover it; only a human who knows the password can mint a god-view session.
// Pure stdlib (crypto/pbkdf2, Go 1.24+), no dependencies.
package adminpw

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const (
	iters   = 600_000 // OWASP-2023-class PBKDF2-SHA256 work factor
	keyLen  = 32
	saltLen = 16
)

// Hash derives an encoded verifier: "pbkdf2-sha256$<iters>$<salt_b64>$<hash_b64>".
func Hash(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := pbkdf2.Key(sha256.New, password, salt, iters, keyLen)
	if err != nil {
		return "", err
	}
	b := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iters, b(salt), b(dk)), nil
}

// Verify checks password against an encoded verifier in constant time.
func Verify(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 1 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[2])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, n, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}
