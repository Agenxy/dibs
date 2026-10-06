package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"sort"
	"strings"
)

// prepareRehearsalRemote is the ONLY preparation path for a scratch run. The
// scratch default branch is a mirror, not a history of releases: GitHub's job
// token cannot create the later proof tag if the candidate changes a workflow
// file relative to that branch, even when it has contents:write. The exact
// production-main candidate must therefore replace the scratch mirror first.
func prepareRehearsalRemote(ctx context.Context, c config, run runner) error {
	if !targetOf(c).rehearsal || targetOf(c).repository != "Agenxy/dibs-release-rehearsal" || c.negativeControl {
		return errors.New("preparing rehearsal requires the bound scratch target without a negative-control flag")
	}
	scratchGitURL := "git@github.com:" + targetOf(c).repository + ".git"
	if err := prepareSourceCandidate(ctx, c, run); err != nil {
		return err
	}
	scratchMain, err := scratchDefaultMain(ctx, scratchGitURL, run)
	if err != nil {
		return err
	}
	tag := "refs/tags/" + targetOf(c).tag
	existing, err := run(ctx, nil, "git", "ls-remote", scratchGitURL, tag)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(existing)) != "" {
		return errors.New("unique scratch rehearsal tag already exists; refuse reuse")
	}
	proofsBefore, err := scratchProofInventory(ctx, scratchGitURL, run)
	if err != nil {
		return err
	}
	if err = mirrorScratchMain(ctx, c.sha, scratchMain, scratchGitURL, run); err != nil {
		return err
	}
	return finishScratchMirror(ctx, c.sha, tag, scratchGitURL, proofsBefore, run)
}

func finishScratchMirror(
	ctx context.Context, candidateSHA, tag, remote string, before scratchProofs, run runner,
) error {
	proofsAfter, err := scratchProofInventory(ctx, remote, run)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, proofsAfter) {
		return errors.New("scratch proof tags or releases changed during main mirror update; unique tag not pushed")
	}
	currentDefault, err := scratchDefaultMain(ctx, remote, run)
	if err != nil {
		return err
	}
	if currentDefault != candidateSHA {
		return errors.New("scratch default branch is no longer the exact candidate; unique tag not pushed")
	}
	if _, err = run(ctx, nil, "git", "push", remote, candidateSHA+":"+tag); err != nil {
		return err
	}
	actual, err := run(ctx, nil, "git", "ls-remote", remote, tag)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(actual)) != candidateSHA+"\t"+tag {
		return errors.New("scratch rehearsal tag does not point at exact production candidate")
	}
	return nil
}

func prepareSourceCandidate(ctx context.Context, c config, run runner) error {
	origin, err := run(ctx, nil, "git", "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	switch strings.TrimSpace(string(origin)) {
	case "git@github.com:Agenxy/dibs.git", "https://github.com/Agenxy/dibs.git":
	default:
		return errors.New("preparing rehearsal requires the production GitHub origin")
	}
	production := c
	production.destination = productionTarget(c)
	if err = candidate(ctx, production, run); err != nil {
		return fmt.Errorf("preparing rehearsal requires exact stamped production main: %w", err)
	}
	return nil
}

func mirrorScratchMain(ctx context.Context, candidateSHA, scratchMain, remote string, run runner) error {
	if _, err := run(ctx, nil, "git", "fetch", "--no-tags", remote, "refs/heads/main"); err != nil {
		return err
	}
	fetched, err := run(ctx, nil, "git", "rev-parse", "FETCH_HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(fetched)) != scratchMain {
		return errors.New("scratch main moved during preparation; retry from a fresh read")
	}
	mode := "fast-forward"
	if _, err = run(ctx, nil, "git", "merge-base", "--is-ancestor", scratchMain, candidateSHA); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return fmt.Errorf("cannot determine scratch main ancestry; refuse mirror: %w", err)
		}
		mode = "resetting mirror"
	}
	fmt.Printf("scratch main %s: %s -> %s\n", mode, scratchMain, candidateSHA)
	if _, err = run(ctx, nil, "git", "push", "--force-with-lease=refs/heads/main:"+scratchMain,
		remote, candidateSHA+":refs/heads/main"); err != nil {
		return fmt.Errorf("scratch main lease update failed; unique tag not pushed: %w", err)
	}
	main, err := run(ctx, nil, "git", "ls-remote", remote, "refs/heads/main")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(main)) != candidateSHA+"\trefs/heads/main" {
		return errors.New("scratch main is not the exact production candidate after push; unique tag not pushed")
	}
	return nil
}

type scratchProofs struct {
	tags, releases []string
}

func scratchProofInventory(ctx context.Context, remote string, run runner) (scratchProofs, error) {
	var proofs scratchProofs
	tags, err := run(ctx, nil, "git", "ls-remote", "--tags", remote, "refs/tags/rehearsal-proof-*")
	if err != nil {
		return proofs, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(tags)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !shaPattern.MatchString(fields[0]) ||
			!strings.HasPrefix(fields[1], "refs/tags/rehearsal-proof-") {
			return proofs, errors.New("invalid scratch proof tag listing; refuse mirror")
		}
		proofs.tags = append(proofs.tags, fields[1]+"@"+fields[0])
	}
	sort.Strings(proofs.tags)
	out, err := run(ctx, nil, "gh", "api", "--paginate", "--slurp",
		"repos/Agenxy/dibs-release-rehearsal/releases?per_page=100")
	if err != nil {
		return proofs, err
	}
	var pages [][]struct {
		ID  int64  `json:"id"`
		Tag string `json:"tag_name"`
	}
	if err = json.Unmarshal(out, &pages); err != nil || len(pages) == 0 {
		return proofs, errors.New("invalid complete scratch release listing; refuse mirror")
	}
	for _, page := range pages {
		for _, release := range page {
			if strings.HasPrefix(release.Tag, "rehearsal-proof-") {
				if release.ID <= 0 {
					return proofs, errors.New("scratch proof release has no identity; refuse mirror")
				}
				proofs.releases = append(proofs.releases, fmt.Sprintf("%s@%d", release.Tag, release.ID))
			}
		}
	}
	sort.Strings(proofs.releases)
	return proofs, nil
}

func scratchDefaultMain(ctx context.Context, remote string, run runner) (string, error) {
	out, err := run(ctx, nil, "git", "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 || lines[0] != "ref: refs/heads/main\tHEAD" {
		return "", errors.New("scratch repository default branch is not main; refuse mirror or tag")
	}
	fields := strings.Fields(lines[1])
	if len(fields) != 2 || fields[1] != "HEAD" || !shaPattern.MatchString(fields[0]) {
		return "", errors.New("scratch repository default branch has no exact commit")
	}
	return fields[0], nil
}
