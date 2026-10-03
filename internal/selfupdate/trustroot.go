package selfupdate

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Authenticated through cosign 3.1.3's embedded production TUF root on
// 2026-10-03. Pinning this bounded byte string in the binary keeps an editable
// provenance record from supplying its own verification authority. Historical
// keys/validity windows stay in the root; a rotation is a reviewed source change,
// not a network fallback during invitation admission.
//
//go:embed sigstore-trusted-root.json
var sigstoreTrustedRoot string

const (
	sigstoreTrustedRootSHA256 = "6494e21ea73fa7ee769f85f57d5a3e6a08725eae1e38c755fc3517c9e6bc0b66"
	sigstoreTUFMirror         = "https://tuf-repo-cdn.sigstore.dev"
	maxTrustedRoot            = 64 << 10
)

// CheckCurrentTrustedRoot is the release-time rotation gate. It fetches CURRENT
// material using normal authenticated TUF into a fresh, disposable cache and
// refuses publication unless it matches the reviewed embedded bytes. It never
// runs on the board writer or on an invitation request. Ordinary online release
// fetch verification continues to use cosign's normal TUF path.
func CheckCurrentTrustedRoot(ctx context.Context) error {
	cosign, err := usableCosign(ctx)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "dibs-sigstore-root-check-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	// No shell and no inherited custom TUF trust: the release authority is the
	// production repository rooted in cosign's embedded bootstrap, not whatever
	// mirror/cache/root the developer happened to configure on this machine.
	// #nosec G204 -- usable executable, fixed command and production mirror.
	cmd := exec.CommandContext(ctx, cosign, "initialize", "--mirror", sigstoreTUFMirror)
	cmd.Env = productionTUFEnv(os.Environ(), dir)
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		return fmt.Errorf("authenticated Sigstore initialize failed: %w: %s; "+
			"fix cosign/TUF connectivity and retry; nothing may be published",
			runErr, strings.TrimSpace(string(out)))
	}
	path := filepath.Join(dir, "tuf-repo-cdn.sigstore.dev", "targets", "trusted_root.json")
	// #nosec G304 -- fixed target in this call's freshly created private cache.
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("authenticated TUF did not provide trusted_root.json: %w; "+
			"check the pinned cosign's cache format before releasing", err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxTrustedRoot+1))
	if err != nil {
		return fmt.Errorf("read authenticated Sigstore root: %w", err)
	}
	if len(b) > maxTrustedRoot {
		return fmt.Errorf("authenticated Sigstore root is too large (limit %d); "+
			"review the new root and bound before releasing", maxTrustedRoot)
	}
	return checkTrustedRoot(b)
}

func checkTrustedRoot(b []byte) error {
	embedded := sha256.Sum256([]byte(sigstoreTrustedRoot))
	if hex.EncodeToString(embedded[:]) != sigstoreTrustedRootSHA256 {
		return fmt.Errorf("embedded Sigstore root does not match its frozen pin; " +
			"repair the source/pin together before releasing")
	}
	got := sha256.Sum256(b)
	if got != embedded {
		return fmt.Errorf("authenticated current Sigstore root differs from embedded pin %s (got %x); "+
			"deliberately rotate the reviewed embedded bytes and frozen pin before releasing",
			sigstoreTrustedRootSHA256, got)
	}
	return nil
}

func productionTUFEnv(env []string, cache string) []string {
	out := make([]string, 0, len(env)+1)
	for _, s := range env {
		key, _, _ := strings.Cut(s, "=")
		if !strings.EqualFold(key, "TUF_ROOT") && !strings.EqualFold(key, "TUF_MIRROR") &&
			!strings.EqualFold(key, "TUF_ROOT_JSON") {
			out = append(out, s)
		}
	}
	return append(out, "TUF_ROOT="+cache)
}
