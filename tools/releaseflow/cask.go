package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func publishCask(ctx context.Context, c config, run runner) error {
	want, err := signedCask(ctx, c, run)
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp("", "dibs-cask-publish-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	checkout := filepath.Join(stage, "tap")
	if _, err = run(ctx, nil, "git", "clone", "--depth=1",
		"https://github.com/Agenxy/homebrew-tap.git", checkout); err != nil {
		return err
	}
	matched, err := existingCask(ctx, c, checkout, want, run)
	if err != nil || matched {
		return err
	}
	return pushCask(ctx, c, stage, checkout, want, run)
}

func signedCask(ctx context.Context, c config, run runner) ([]byte, error) {
	s, exists, err := status(ctx, c, run)
	if err != nil {
		return nil, err
	}
	if !exists || s.Draft {
		return nil, errors.New("cask requires the verified PUBLIC release, not a draft")
	}
	if err = requireImmutablePublic(s); err != nil {
		return nil, err
	}
	dir, err := download(ctx, c, s, run)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err = validateAssets(ctx, c, dir); err != nil {
		return nil, err
	}
	// #nosec G304 -- fixed signed asset in the private release download stage.
	return os.ReadFile(filepath.Join(dir, "dibs.rb"))
}

func existingCask(ctx context.Context, c config, checkout string, want []byte, run runner) (bool, error) {
	// #nosec G304 -- fixed cask file in the owned clone of the trusted tap.
	current, err := os.ReadFile(filepath.Join(checkout, "Casks", "dibs.rb"))
	if err != nil {
		return false, err
	}
	if bytes.Equal(current, want) {
		fmt.Println("published cask already matches tap main")
		return true, nil
	}
	branch := "cask-" + c.version
	ref := "refs/heads/" + branch
	out, err := run(ctx, nil, "git", "-C", checkout, "ls-remote", "--heads", "origin", ref)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(string(out)) != "" {
		if _, err = run(ctx, nil, "git", "-C", checkout, "fetch", "origin", ref); err != nil {
			return false, err
		}
		got, err := run(ctx, nil, "git", "-C", checkout, "show", "FETCH_HEAD:Casks/dibs.rb")
		if err != nil {
			return false, err
		}
		if !bytes.Equal(got, want) {
			return false, errors.New("existing cask branch differs from signed release; preserve it and investigate")
		}
		fmt.Println("published cask already matches its review branch")
		return true, nil
	}
	return false, nil
}

func pushCask(ctx context.Context, c config, stage, checkout string, want []byte, run runner) error {
	branch := "cask-" + c.version
	ref := "refs/heads/" + branch
	keyPath, err := preparedCaskKey(ctx, stage, run)
	if err != nil {
		return err
	}
	if _, err := run(ctx, nil, "git", "-C", checkout, "switch", "-c", branch); err != nil {
		return err
	}
	// #nosec G703 -- fixed cask path inside the owned private clone.
	if err := os.WriteFile(filepath.Join(checkout, "Casks", "dibs.rb"), want, 0o600); err != nil {
		return err
	}
	if _, err := run(ctx, nil, "git", "-C", checkout, "add", "Casks/dibs.rb"); err != nil {
		return err
	}
	if _, err := run(ctx, nil, "git", "-C", checkout, "-c", "user.name=Dibs release",
		"-c", "user.email=release@users.noreply.github.com",
		"-c", "commit.gpgsign=false", "commit", "-m", "Brew cask update for dibs v"+c.version); err != nil {
		return err
	}
	// No API token or host-key bypass. Only the version review branch is
	// pushed with the existing tap-scoped SSH deploy key, never tap main.
	// Git requires a command string here. Quote the generated path even when
	// the machine's temporary-directory path contains spaces or shell syntax.
	quotedKey := "'" + strings.ReplaceAll(keyPath, "'", "'\"'\"'") + "'"
	env := []string{"GIT_SSH_COMMAND=ssh -i " + quotedKey +
		" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes"}
	_, err = run(ctx, env, "git", "-C", checkout, "push", "git@github.com:Agenxy/homebrew-tap.git", "HEAD:"+ref)
	return err
}

func normalizedCaskKey(key string) string {
	return strings.TrimRight(strings.ReplaceAll(key, "\r\n", "\n"), "\n") + "\n"
}

func preparedCaskKey(ctx context.Context, stage string, run runner) (string, error) {
	key := os.Getenv("HOMEBREW_TAP_DEPLOY_KEY")
	if strings.TrimSpace(key) == "" {
		return "", errors.New("HOMEBREW_TAP_DEPLOY_KEY is required to push the cask review branch")
	}
	keyPath := filepath.Join(stage, "deploy-key")
	// #nosec G703 -- credential path in the owned private temporary directory.
	if err := os.WriteFile(keyPath, []byte(normalizedCaskKey(key)), 0o600); err != nil {
		return "", err
	}
	if _, err := run(ctx, nil, "ssh-keygen", "-y", "-P", "", "-f", keyPath); err != nil {
		return "", errors.New("HOMEBREW_TAP_DEPLOY_KEY is not a valid unencrypted SSH private key " +
			"after newline normalization; check the tap-scoped secret")
	}
	return keyPath, nil
}

// Diagnose the existing secret on the trusted main workflow without logging
// key material, the derived public key, or ssh-keygen's output. This makes
// format correction measurable before a retry can write to the tap.
func diagnoseCaskKey(ctx context.Context, run runner) error {
	key := os.Getenv("HOMEBREW_TAP_DEPLOY_KEY")
	if strings.TrimSpace(key) == "" {
		return errors.New("HOMEBREW_TAP_DEPLOY_KEY is absent")
	}
	stage, err := os.MkdirTemp("", "dibs-cask-key-check-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	rawPath := filepath.Join(stage, "raw-key")
	// #nosec G703 -- fixed filename in our newly created private temporary directory.
	if err := os.WriteFile(rawPath, []byte(key), 0o600); err != nil {
		return err
	}
	_, rawErr := run(ctx, nil, "ssh-keygen", "-y", "-P", "", "-f", rawPath)
	_, normalizedErr := preparedCaskKey(ctx, stage, run)
	fmt.Printf("tap deploy key format: raw_valid=%t normalized_valid=%t\n", rawErr == nil, normalizedErr == nil)
	return normalizedErr
}
