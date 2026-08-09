package ledger

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/agenxy/lanes/internal/core"
)

// Box performs daemon-side encryption of private op fields (message bodies,
// responses, lane tokens) so ledger readers see ciphertext. Lanes never
// touch crypto; the human CLI decrypts via the same-user key file.
type Box struct{ aead cipher.AEAD }

const encPrefix = "enc:"

// LoadOrCreateKey reads the 32-byte daemon key, creating it (0600) if absent.
func LoadOrCreateKey(path string) (*Box, error) {
	// #nosec G304 -- a path inside the daemon's own data directory, or one the
	// operator pointed the CLI at. Same-user access only; refusing it would mean
	// refusing to run.
	key, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		key = make([]byte, 32)
		if _, rerr := rand.Read(key); rerr != nil {
			return nil, rerr
		}
		if werr := os.WriteFile(path, key, 0o600); werr != nil {
			return nil, werr
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("daemon key %s: want 32 bytes, have %d", path, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func (b *Box) seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

func (b *Box) open(sealed string) (string, error) {
	if sealed == "" || !strings.HasPrefix(sealed, encPrefix) {
		return sealed, nil // plaintext passthrough (public fields, legacy)
	}
	ct, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, encPrefix))
	if err != nil {
		return "", err
	}
	ns := b.aead.NonceSize()
	if len(ct) < ns {
		return "", fmt.Errorf("ciphertext too short")
	}
	plain, err := b.aead.Open(nil, ct[:ns], ct[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plain), nil
}

// SealBytes AES-256-GCM-seals raw bytes under the daemon key (nonce prepended).
// Used by the blob store so at-rest blob files are ciphertext (A3), the same
// guarantee message bodies get. Empty in → empty out.
func (b *Box) SealBytes(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, nil), nil
}

// OpenBytes reverses SealBytes.
func (b *Box) OpenBytes(sealed []byte) ([]byte, error) {
	if len(sealed) == 0 {
		return nil, nil
	}
	ns := b.aead.NonceSize()
	if len(sealed) < ns {
		return nil, fmt.Errorf("blob ciphertext too short")
	}
	return b.aead.Open(nil, sealed[:ns], sealed[ns:], nil)
}

// EncryptOp seals the private fields of op in place.
func (b *Box) EncryptOp(op *core.Op) error {
	var err error
	// Every op whose Body is CONTENT rather than a public label.
	//
	// Mail was sealed and lane traffic was not, though both carry exactly the
	// same promise: lane_read is membership-gated, revoked on leave or eviction,
	// and SECURITY.md states announcement bodies are unreachable on the
	// token-less path. All of that is true of the running daemon and none of it
	// survives a copied ledger — a backup, a support bundle, a pasted repro —
	// where an announcement body sat in plaintext next to a sealed message body.
	//
	// Two content surfaces with one confidentiality contract and only one of
	// them sealed. Found by an agent reading the ledger of a candidate build
	// rather than reading the code.
	switch op.Kind {
	case core.OpSendMessage, core.OpRespond, core.OpLaneAnnounce, core.OpLanePost:
		if op.Body, err = b.seal(op.Body); err != nil {
			return err
		}
	}
	if op.NewToken != "" {
		if op.NewToken, err = b.seal(op.NewToken); err != nil {
			return err
		}
	}
	// The nonce is a recovery credential (SPEC §5): sealed like a token.
	if op.Nonce != "" {
		if op.Nonce, err = b.seal(op.Nonce); err != nil {
			return err
		}
	}
	return nil
}

// DecryptOp opens the private fields of op in place.
func (b *Box) DecryptOp(op *core.Op) error {
	var err error
	if op.Body, err = b.open(op.Body); err != nil {
		return err
	}
	if op.NewToken, err = b.open(op.NewToken); err != nil {
		return err
	}
	if op.Nonce, err = b.open(op.Nonce); err != nil {
		return err
	}
	return nil
}
