package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/agenxy/dibs/internal/paths"
)

// Only nonce-less legacy recipes need disk state. The issuer-provided nonce
// survives a disposable container; this fallback cannot. Neither path reads
// or writes the local board's harness-nonces.json.
func guestPrepareNonce(line []byte, file string, recipe *guestRecipe) ([]byte, error) {
	if recipe.Nonce != "" {
		return guestRecoveryNonce(line, recipe.Nonce), nil
	}
	if !guestNeedsStoredNonce(line) {
		return line, nil
	}
	nonce, err := guestStoredNonce(file, recipe)
	if err != nil {
		return nil, fmt.Errorf("guest recovery store: %w; restore the private store or obtain an issuer-nonce recipe", err)
	}
	return guestRecoveryNonce(line, nonce), nil
}

// This is a per-call refusal BEFORE HTTP, not an uncertain transport outcome.
// Never render the underlying filesystem error: it may carry a private path.
func guestNonceReply(line []byte, err error) []byte {
	id := idOf(line)
	if string(id) == "null" {
		return nil // a notification gets no synthesized JSON-RPC response
	}
	hint := "restore the private store, or ask the issuer for an export recipe carrying the recovery nonce"
	if paths.LockHeldElsewhere(err) {
		hint = "another bridge for this guest is registering; retry the call in a few seconds"
	}
	reply, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{
			"code": -32000, "message": "guest recovery store refused registration; no request was sent",
			"data": map[string]any{"hint": hint},
		},
	})
	return reply
}

func guestNeedsStoredNonce(line []byte) bool {
	var call struct {
		Method string `json:"method"`
		Params struct {
			Name string                     `json:"name"`
			Args map[string]json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &call) != nil || call.Method != "tools/call" || call.Params.Name != "register" ||
		call.Params.Args == nil {
		return false
	}
	_, supplied := call.Params.Args["nonce"]
	return !supplied
}

func guestStoredNonce(file string, recipe *guestRecipe) (string, error) {
	root, err := os.OpenRoot(filepath.Dir(file))
	if err != nil {
		return "", fmt.Errorf("open private recipe directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	parent, err := root.Stat(".")
	if err != nil || !parent.IsDir() || !privateGuestOwned(parent) {
		return "", errors.New("recipe directory is no longer private and owned")
	}
	sum := sha256.Sum256([]byte(recipe.Endpoint + "\x00" + recipe.Pin + "\x00" + recipe.Name))
	name := "guest-recovery-" + hex.EncodeToString(sum[:]) + ".nonce"
	lock, err := guestNonceLock(root, name+".lock")
	if err != nil {
		return "", err
	}
	defer func() { paths.Unlock(lock); _ = lock.Close() }()
	if b, err := readPrivateGuestRoot(root, name, 256); err == nil {
		nonce := string(bytes.TrimSpace(b))
		decoded, err := hex.DecodeString(nonce)
		if err != nil || len(decoded) != 32 {
			return "", errors.New("retained guest nonce is corrupt, refusing to mint a sibling")
		}
		// A preceding attempt may have renamed successfully but refused on
		// directory Sync. Do not let retry bypass that durability boundary.
		if err := syncGuestNonceDirectory(root); err != nil {
			return "", err
		}
		return nonce, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return writeGuestNonce(root, name)
}

// This narrow filesystem seam lets a test fail the directory operation AFTER
// the production file write and rename, rather than testing a disconnected flag.
type guestNonceRoot interface {
	OpenFile(string, int, os.FileMode) (*os.File, error)
	Open(string) (*os.File, error)
	Remove(string) error
	Rename(string, string) error
}

func writeGuestNonce(root guestNonceRoot, name string) (string, error) {
	nonce := mintNonce()
	if nonce == "" {
		return "", errors.New("secure nonce entropy is unavailable")
	}
	temporaryID := mintNonce()
	if temporaryID == "" {
		return "", errors.New("secure temporary-file entropy is unavailable")
	}
	tmp := name + ".new-" + temporaryID
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create private guest nonce: %w", err)
	}
	defer func() { _ = f.Close() }()
	defer func() { _ = root.Remove(tmp) }()
	if _, err := f.WriteString(nonce); err != nil {
		return "", fmt.Errorf("write retained guest nonce: %w", err)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("sync retained guest nonce: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close retained guest nonce: %w", err)
	}
	if err := root.Rename(tmp, name); err != nil {
		return "", fmt.Errorf("commit retained guest nonce: %w", err)
	}
	if err := syncGuestNonceDirectory(root); err != nil {
		return "", err
	}
	return nonce, nil
}

func syncGuestNonceDirectory(root guestNonceRoot) error {
	dir, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open retained guest nonce directory for durability: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync retained guest nonce directory before registration: %w", err)
	}
	return nil
}

// Every reader holds this OS lock: no half-written nonce can be read, and a
// process exit releases the lock. Never steal a timeout-based lock or silently
// continue unlocked when that would strand an identity.
func guestNonceLock(root *os.Root, name string) (*os.File, error) {
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrExist) {
		info, serr := root.Lstat(name)
		if serr != nil || !info.Mode().IsRegular() || !privateGuestOwned(info) {
			return nil, errors.New("guest nonce lock must be private, owned and regular")
		}
		f, err = root.OpenFile(name, os.O_RDWR, 0)
		if err == nil {
			opened, serr := f.Stat()
			if serr != nil || !os.SameFile(info, opened) {
				_ = f.Close()
				return nil, errors.New("guest nonce lock changed while opening")
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("open guest nonce lock: %w", err)
	}
	if err := paths.LockExclusive(f, false); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock guest recovery credential (another bridge may be registering): %w", err)
	}
	return f, nil
}
