// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/agenxy/dibs/internal/build"
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/selfupdate"
)

func verifyInviteRelease(tag string) error {
	rel, err := selfupdate.SelectGuestRelease(tag, selfupdate.GuestSupportingMinimum, build.Version)
	if err != nil {
		return err // BEFORE data-directory lookup, authentication or any network call
	}
	dir := paths.DataDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open existing board data directory: %w; this operation never initializes a board", err)
	}
	_ = root.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	proved, err := selfupdate.LoadVerifiedRelease(ctx, dir, build.Version)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "Retained release evidence was refused: %v.\n"+
			"This explicit verification command will try fresh signature-checked evidence for %s; "+
			"the existing record is preserved unless acquisition and atomic publication succeed.\n", err, rel.Tag)
	}
	if err != nil || proved.Tag() != rel.Tag {
		staged, err := os.MkdirTemp("", "dibs-explicit-release-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(staged) }()
		proved, err = selfupdate.AcquireVerifiedRelease(ctx, &http.Client{}, rel, staged)
		if err != nil {
			return err
		}
		if err = proved.Save(dir); err != nil {
			return fmt.Errorf("retaining signed release evidence: %w; no invitation or installation was changed", err)
		}
	}
	fmt.Printf("Signed release evidence verified for %s; actual board build remains %s.\n"+
		"INCOMPLETE: this is not a provisionable guest recipe or runtime acceptance. "+
		"No invitation, installation, harness configuration or system trust was changed.\n", proved.Tag(), build.Version)
	return nil
}
