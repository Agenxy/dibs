package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/agenxy/dibs/internal/guesttrust"
	"github.com/agenxy/dibs/internal/invites"
)

// Keep the opened private directory across mint and publication, so a path
// change cannot redirect the issuer's secret disclosure into another directory.
type guestRecipeExport struct {
	root *os.Root
	name string
}

func openGuestRecipeExport(path string) (*guestRecipeExport, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("--out must be a clean absolute file in an existing owned private directory (0700)")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open private export directory: %w; create a private directory first", err)
	}
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() || !privateGuestOwned(info) {
		_ = root.Close()
		return nil, errors.New("export directory must be owned by you and private (0700); no invitation was minted")
	}
	name := filepath.Base(path)
	if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		_ = root.Close()
		return nil, errors.New("export destination exists or cannot be checked; " +
			"choose a new private file, never overwrite a retained recipe")
	}
	return &guestRecipeExport{root: root, name: name}, nil
}

func (e *guestRecipeExport) publish(out map[string]any, requestedName string) error {
	if code, _ := out["code"].(string); code != "" {
		return fmt.Errorf("%s: %v; %v", code, out["message"], out["hint"])
	}
	body, err := guestExportPayload(out, requestedName)
	if err == nil {
		err = writeGuestRecipeExclusive(e.root, e.name, body)
	}
	if err != nil {
		return fmt.Errorf("private guest export failed: %w; the invitation may have been minted. "+
			"List and revoke the unused invitation by name before retrying; preserve any existing file", err)
	}
	// Never print secrets, a runnable MCP entry or a guessed release. The
	// credential file is an explicitly incomplete checkpoint, not provisioning.
	_, err = fmt.Fprintln(os.Stdout, "Private guest JSON exported. INCOMPLETE: not provisionable until "+
		"supporting signed release metadata and guest runtime acceptance exist. Keep this file private; "+
		"back up the board's original at-rest key with its state. Replacing that key is not supported recovery. "+
		"No harness configuration or system trust was changed.")
	return err
}

func guestExportPayload(out map[string]any, requestedName string) ([]byte, error) {
	config, _ := out["config"].(map[string]any)
	r := guestRecipe{Version: 1}
	r.Name, _ = out["name"].(string)
	r.Key, _ = out["key"].(string)
	r.Nonce, _ = out["recovery_nonce"].(string)
	r.Endpoint, _ = config["endpoint"].(string)
	r.PEM, _ = config["ca_pem"].(string)
	r.Pin, _ = config["ca_spki_sha256"].(string)
	expires, _ := out["expires_at"].(string)
	var err error
	r.Expires, err = time.Parse(time.RFC3339Nano, expires)
	r.Expires = r.Expires.UTC()
	if err != nil {
		return nil, errors.New("issuer omitted a valid exact expiry; fix the issuer rather than inventing credentials")
	}
	if err := validateGuestExportIdentity(&r, requestedName); err != nil {
		return nil, err
	}
	u, ip, err := guestEndpoint(r.Endpoint)
	if err != nil {
		return nil, err
	}
	origin, _ := out["url"].(string)
	if origin+"/mcp" != u.String() {
		return nil, errors.New("issuer endpoint and origin disagree; fix the listener before exporting")
	}
	if len(r.PEM) > 64<<10 {
		return nil, errors.New("issuer guest CA exceeds the recipe bound")
	}
	if _, err := guesttrust.ParseRoot([]byte(r.PEM), ip, r.Pin, time.Now()); err != nil {
		return nil, err
	}
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')
	if len(body) > 64<<10 {
		return nil, errors.New("issuer recipe exceeds the 64 KiB bound")
	}
	return body, nil
}

func validateGuestExportIdentity(r *guestRecipe, requestedName string) error {
	if requestedName == "" || r.Name != requestedName {
		return errors.New("issuer recipe identity differs from the requested name; refuse a different mailbox")
	}
	nonce, err := hex.DecodeString(r.Nonce)
	if !time.Now().Before(r.Expires) || !invites.ValidName(r.Name) || !guestInvitationKey(r.Key) ||
		err != nil || len(nonce) != 32 || hex.EncodeToString(nonce) != r.Nonce {
		return errors.New("issuer omitted valid guest identity, live exact expiry or 256-bit recovery material; " +
			"fix the issuer rather than inventing credentials")
	}
	return nil
}

type guestExportRoot interface {
	OpenFile(string, int, os.FileMode) (*os.File, error)
	Open(string) (*os.File, error)
	Link(string, string) error
	Remove(string) error
	Stat(string) (os.FileInfo, error)
}

// A synced private temporary file is linked exclusively into its final name.
// Rename would overwrite a competing export. On an unsupported filesystem we
// fail closed; no non-atomic fallback, automatic revoke, or secret stdout.
func writeGuestRecipeExclusive(root guestExportRoot, name string, body []byte) error {
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() || !privateGuestOwned(info) {
		return errors.New("export directory is no longer private and owned")
	}
	id := mintNonce()
	if id == "" {
		return errors.New("secure export temporary-file entropy unavailable")
	}
	tmp := ".guest-export-" + id
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create private export temporary file: %w", err)
	}
	// Idempotent cleanup of our random staging link on every exit. After the
	// explicit removal below this second Remove normally returns not-exist.
	defer func() { _ = f.Close(); _ = root.Remove(tmp) }()
	if _, err := f.Write(body); err != nil {
		return fmt.Errorf("write private export: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync private export: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close private export: %w", err)
	}
	if err := root.Link(tmp, name); err != nil {
		return fmt.Errorf("publish exclusive private export: %w", err)
	}
	// Never remove the final file on an ambiguous durability failure. It may
	// already be handed to a guest; the caller must decide whether to revoke.
	if err := root.Remove(tmp); err != nil {
		return fmt.Errorf("remove private temporary link: %w", err)
	}
	dir, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open private export directory for durability: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync private export directory: %w", err)
	}
	return nil
}
