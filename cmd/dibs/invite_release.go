package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/build"
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/release"
	"github.com/agenxy/dibs/internal/selfupdate"
)

// Intentionally unset until release preparation identifies the FIRST build
// carrying the complete guest bridge contract. A fixture/link-time test value
// is not a claim that an existing public tag supports it. No environment or
// command flag can lower this compiled floor.
var guestSupportingMinimum = ""

func guestReleaseSelection(tag, minimum, boardVersion string) (selfupdate.Release, error) {
	if minimum == "" {
		return selfupdate.Release{}, errors.New("INCOMPLETE: supporting guest release minimum is unset; " +
			"no release record or invitation was changed")
	}
	want, err := selfupdate.ReleaseForTag(tag)
	if err != nil {
		return selfupdate.Release{}, err
	}
	floor, err := selfupdate.ReleaseForTag(minimum)
	if err != nil {
		return selfupdate.Release{}, errors.New("compiled guest release minimum is invalid; repair the build, not its cache")
	}
	if cmp, _ := release.Compare(want.Version, floor.Version); cmp < 0 {
		return selfupdate.Release{}, fmt.Errorf("release %s predates the compiled guest minimum %s; "+
			"verify a supporting release instead", tag, minimum)
	}
	// A devel/pseudo-version build is not relabelled as a release. A released
	// board may use its own tag or a newer supporting tag, never an older one.
	if own, err := selfupdate.ReleaseForTag("v" + strings.TrimPrefix(boardVersion, "v")); err == nil {
		if cmp, _ := release.Compare(want.Version, own.Version); cmp < 0 {
			return selfupdate.Release{}, fmt.Errorf("release %s is older than this board build %s; "+
				"verify its own or a newer tag", tag, boardVersion)
		}
	}
	return want, nil
}

func verifyInviteRelease(tag string) error {
	rel, err := guestReleaseSelection(tag, guestSupportingMinimum, build.Version)
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
