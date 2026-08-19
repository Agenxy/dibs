package ledger

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/agenxy/dibs/internal/core"
)

// Box performs daemon-side encryption of private op fields (message bodies,
// responses, agent tokens) so ledger readers see ciphertext. Dibs never
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
	// Mail was sealed and agent traffic was not, though both carry exactly the
	// same promise: read_space is membership-gated, revoked on leave or eviction,
	// and SECURITY.md states announcement bodies are unreachable on the
	// token-less path. All of that is true of the running daemon and none of it
	// survives a copied ledger: a backup, a support bundle, a pasted repro,
	// where an announcement body sat in plaintext next to a sealed message body.
	//
	// Two content surfaces with one confidentiality contract and only one of
	// them sealed. Found by an agent reading the ledger of a candidate build
	// rather than reading the code.
	switch op.Kind {
	case core.OpSendMessage, core.OpRespond, core.OpSpaceAnnounce, core.OpSpacePost:
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
	// Choices are message CONTENT, and were the one piece of it left in the
	// clear.
	//
	// They become recipient-scoped state on the message, exactly like the body
	// beside them, and they are written by the same sender for the same reader.
	// A question's body was sealed and its answers were not, so a copied ledger
	// showed "Ship it / Hold for the audit / Escalate to legal" next to
	// ciphertext: the alternatives are frequently the sensitive half, because
	// they name what was actually on the table. Found by a pre-release review.
	//
	// Backward compatible in the direction that matters: open() passes
	// plaintext through, so a ledger written before this reads exactly as
	// before, and only new ops are sealed.
	//
	// A NEW slice, never in place. Append takes a shallow copy of the op before
	// calling this (`enc := *op`), and a shallow copy of a struct shares the
	// backing array of every slice in it. Sealing element by element would
	// therefore write ciphertext into the caller's own op, which the engine
	// still holds and core has already stored on the message: the board would
	// start showing sealed strings as the answers to a live question. The
	// scalar fields above are safe from this because assigning a string field
	// on a copy cannot reach the original.
	if len(op.Choices) > 0 {
		sealed := make([]string, len(op.Choices))
		for i, c := range op.Choices {
			if c == "" {
				continue
			}
			if sealed[i], err = b.seal(c); err != nil {
				return err
			}
		}
		op.Choices = sealed
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
	for i, c := range op.Choices {
		if op.Choices[i], err = b.open(c); err != nil {
			return err
		}
	}
	return nil
}
