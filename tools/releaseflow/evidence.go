package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Evidence is produced AFTER the immutable payload and real read-only retry,
// so it needs a SECOND release. No unverifiable pre-publication outcome flags.
// Existing evidence (even an empty draft) is a collision, never a retry target.
func publishEvidence(ctx context.Context, c config, run runner) error {
	if err := rehearsalContext(c); err != nil {
		return err
	}
	c.publicationRun = c.runID
	d := targetOf(c)
	tag := evidenceTag(c, c.attempt)
	lookup := c
	lookup.destination.tag = tag
	if _, exists, err := listedStatus(ctx, lookup, run); err != nil {
		return err
	} else if exists {
		return errors.New("evidence release already exists; retry collided, NEVER overwrite or reuse")
	}
	dir := os.Getenv("RUNNER_TEMP")
	blob, bundle := filepath.Join(dir, publicationFile), filepath.Join(dir, publicationBundle)
	if _, err := readPublication(blob); err != nil {
		return err
	}
	if _, err := run(ctx, nil, "cosign", "sign-blob", "--yes", "--bundle", bundle, blob); err != nil {
		return err
	}
	if err := verifyScratchSignature(ctx, c, blob, bundle, run); err != nil {
		return err
	}
	// Fixed scratch API, not origin: a rewritten Git remote cannot turn this
	// evidence tag writer into a production tag writer. Existing refs refuse.
	if _, err := run(ctx, nil, "gh", "api", "repos/"+d.repository+"/git/refs",
		"-f", "ref=refs/tags/"+tag, "-f", "sha="+c.sha); err != nil {
		return err
	}
	notes := rehearsalWarning + ". Signed proof only; payload identity: " + d.identity()
	if _, err := run(ctx, nil, "gh", "release", "create", tag, "--repo", d.repository, "--verify-tag", "--draft",
		"--title", rehearsalWarning+" (evidence)", "--notes", notes); err != nil {
		return err
	}
	if _, err := run(ctx, nil, "gh", "release", "upload", tag, "--repo", d.repository, blob, bundle); err != nil {
		return err
	}
	if err := checkEvidence(ctx, c, lookup, dir, false, run); err != nil {
		return err
	}
	if _, err := run(ctx, nil, "gh", "release", "edit", tag, "--repo", d.repository, "--draft=false"); err != nil {
		return err
	}
	return checkEvidence(ctx, c, lookup, dir, true, run)
}

func checkEvidence(ctx context.Context, c, lookup config, stage string, public bool, run runner) error {
	s, exists, err := listedStatus(ctx, lookup, run)
	if err != nil {
		return err
	}
	if err = validEvidenceStatus(s, exists, public); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "dibs-evidence-readback-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	d := targetOf(lookup)
	if _, err = run(ctx, nil, "gh", "release", "download", d.tag, "--repo", d.repository,
		"--dir", dir, "--pattern", publicationFile, "--pattern", publicationBundle); err != nil {
		return err
	}
	if err = verifyScratchSignature(ctx, c,
		filepath.Join(dir, publicationFile), filepath.Join(dir, publicationBundle), run); err != nil {
		return err
	}
	for _, name := range []string{publicationFile, publicationBundle} {
		want, err := digest(filepath.Join(stage, name))
		if err != nil {
			return err
		}
		got, err := digest(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("evidence asset %s differs from signed stage", name)
		}
	}
	return nil
}

func validEvidenceStatus(s releaseStatus, exists, public bool) error {
	if !exists || s.ID == 0 || len(s.Assets) != 2 || s.Draft == public {
		return errors.New("evidence release missing or wrong draft/public state")
	}
	if public && !s.Immutable {
		return errors.New("evidence release is not immutable")
	}
	seen := make(map[string]bool)
	for _, a := range s.Assets {
		if seen[a.Name] {
			return errors.New("evidence release has duplicate assets")
		}
		seen[a.Name] = true
	}
	if !seen[publicationFile] || !seen[publicationBundle] {
		return errors.New("evidence release must have exact receipt and bundle")
	}
	return nil
}
