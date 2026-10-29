// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

const codexMarketplaceRepair = "codex plugin marketplace remove dibs, then " + codexPluginRepair

// The persisted marketplace is the person's choice, not the installer's.
// A per-command -c override cannot survive Codex's next local cache sync.
// Measured with the real installer and plugin/list(forceRefetch:true) on
// 0.165.0-alpha.1.2: an explicit checkout source replaces the verified root.
type codexMarketplaceConflict struct{ kind, source, expected string }

func (e *codexMarketplaceConflict) Error() string {
	return fmt.Sprintf("Codex's Dibs marketplace source is %s %q, not this build's verified root %q; "+
		"Codex sync can replace the installed plugin. The configured source is unchanged. Run %s",
		e.kind, e.source, e.expected, codexMarketplaceRepair)
}

func (p *codexPluginPlan) checkMarketplaceSource() error {
	// #nosec G304 G703 -- read-only inspection of the operator's Codex config
	b, err := os.ReadFile(filepath.Join(p.home, "config.toml"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var config struct {
		Marketplaces map[string]struct {
			SourceType string `toml:"source_type"`
			Source     string
		}
	}
	if err := toml.Unmarshal(b, &config); err != nil {
		return fmt.Errorf("cannot inspect Codex marketplace source: %w", err)
	}
	source, exists := config.Marketplaces["dibs"]
	if !exists {
		return nil // Codex preserves a cached non-curated plugin with no source.
	}
	if source.SourceType == "local" && samePluginDirectory(source.Source, p.source) {
		return nil
	}
	return &codexMarketplaceConflict{source.SourceType, source.Source, p.source}
}

func samePluginDirectory(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	// #nosec G703 -- comparing two local directory identities, not reading files
	aInfo, aErr := os.Stat(a)
	// #nosec G703 -- same directory-identity comparison; no content access or mutation
	bInfo, bErr := os.Stat(b)
	return aErr == nil && bErr == nil && aInfo.IsDir() && bInfo.IsDir() && os.SameFile(aInfo, bInfo)
}

func marketplaceConflict(err error) bool {
	var conflict *codexMarketplaceConflict
	return errors.As(err, &conflict)
}
