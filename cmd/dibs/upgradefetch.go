package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/agenxy/dibs/internal/build"
	"github.com/agenxy/dibs/internal/selfupdate"
	"github.com/agenxy/dibs/internal/ui"
)

// The other half of R12, and the one that was missing.
//
// `dibs upgrade` moves a running fleet onto the dibd you have installed, and
// said so: "It does NOT fetch anything." That is a defensible split between
// getting a build and cutting over to it, and it left a hole with nothing in
// it, because nothing anywhere told an operator a newer version existed. The
// answer is not to make `upgrade` always fetch, which would turn a command
// that is safe to run under a live fleet into one that reaches the network;
// it is to make asking cheap and fetching explicit.
//
// So: `--check` asks and changes nothing, `--fetch` asks, gets it, proves it,
// installs it and then performs exactly the cutover that already exists.
// Nothing here runs on a timer and the daemon never calls it: the only
// requests Dibs makes to the network are ones a person asked for.

// checkForUpdate prints where this build stands against the published one.
func checkForUpdate() error {
	rel, standing, err := currentStanding()
	if err != nil {
		return err
	}
	switch standing {
	case selfupdate.Behind:
		fmt.Printf("%s\n  you have %s, %s is published\n  %s\n",
			ui.Attn("a newer Dibs is out"), build.Version, rel.Tag, fetchAdvice())
	case selfupdate.Current:
		fmt.Printf("%s (%s)\n", ui.Good("up to date"), build.Version)
	case selfupdate.Ahead:
		fmt.Printf("%s: this build is %s and the newest release is %s. That is what a "+
			"stamped but untagged commit looks like\n", ui.Good("ahead of the release"),
			build.Version, rel.Tag)
	case selfupdate.Unknowable:
		fmt.Printf("this build reports %q, which is not a release version, so there is "+
			"nothing to compare it against. The newest release is %s\n",
			build.Version, rel.Tag)
	}
	return nil
}

// fetchAdvice is the one line that names the right command for THIS install,
// which is not the same command on every machine: a Homebrew install must be
// upgraded by Homebrew, because replacing a file it owns leaves its records
// and the disk disagreeing and the next `brew upgrade` puts the old build
// back with nothing explaining why.
func fetchAdvice() string {
	if by := managedInstall(); by != "" {
		return "this install belongs to " + by + ": `" + selfupdate.ManagedFix(by) + "`"
	}
	return "`dibs upgrade --fetch` gets it, checks its signature, and moves the fleet onto it"
}

// managedInstall reports the package manager that owns the daemon this
// machine runs, or "" when nothing does.
func managedInstall() string {
	daemon, err := daemonPath()
	if err != nil {
		return ""
	}
	return selfupdate.Managed(daemon)
}

func currentStanding() (selfupdate.Release, selfupdate.Standing, error) {
	ctx, cancel := context.WithTimeout(context.Background(), selfupdate.CheckTimeout)
	defer cancel()
	rel, err := selfupdate.Latest(ctx, daemonClient(selfupdate.CheckTimeout))
	if err != nil {
		return rel, selfupdate.Unknowable, err
	}
	return rel, selfupdate.Compare(build.Version, rel.Version), nil
}

// fetchUpgrade gets the published release, proves it, installs it beside the
// daemon this machine runs, and then cuts the fleet over with the ordinary
// upgrade, which is the step that proves the new binary can rebuild this
// board BEFORE stopping anything.
func fetchUpgrade(o upgradeOpts) error {
	if by := managedInstall(); by != "" {
		return fmt.Errorf("this Dibs was installed by %s, and replacing a file it owns "+
			"would leave its records and the disk disagreeing: the next `brew upgrade` "+
			"puts the old build back and nothing says why.\n\n  %s\n\nThen `dibs upgrade` "+
			"moves the fleet onto it", by, selfupdate.ManagedFix(by))
	}
	rel, standing, err := currentStanding()
	if err != nil {
		return err
	}
	if standing == selfupdate.Current || standing == selfupdate.Ahead {
		fmt.Printf("%s (%s); the newest release is %s. Nothing to fetch\n",
			ui.Good("already current"), build.Version, rel.Tag)
		return nil
	}

	daemon, err := daemonPath()
	if err != nil {
		return err
	}
	into := filepath.Dir(daemon)
	if err := writable(into); err != nil {
		return err
	}

	staged, err := os.MkdirTemp(into, ".dibs-upgrade-")
	if err != nil {
		return fmt.Errorf("making room to unpack %s beside %s: %w", rel.Tag, daemon, err)
	}
	defer func() { _ = os.RemoveAll(staged) }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := daemonClient(0)

	if o.allowUnsigned {
		fmt.Println(ui.Attn("--allow-unsigned: the release's signature will NOT be checked. " +
			"The digest below proves the download arrived intact and says nothing about " +
			"who produced it"))
	} else if err := selfupdate.Verify(ctx, c, rel, staged); err != nil {
		return err
	}
	goos, goarch := selfupdate.Platform()
	fmt.Printf("fetching %s for %s/%s\n", rel.Tag, goos, goarch)
	if err := selfupdate.Fetch(ctx, c, rel, goos, goarch, staged); err != nil {
		return err
	}
	if err := placePayload(staged, into); err != nil {
		return err
	}
	fmt.Printf("%s %s into %s\n", ui.Good("installed"), rel.Tag, into)
	if o.dryRun {
		fmt.Println("dry run: the fleet has NOT been moved onto it. `dibs upgrade` does that")
		return nil
	}
	return upgrade(o)
}

// writable refuses early, with the reason, rather than after a download.
func writable(dir string) error {
	probe, err := os.CreateTemp(dir, ".dibs-write-probe-")
	if err != nil {
		return fmt.Errorf("cannot install into %s, which is where the daemon this machine "+
			"runs lives: %w.\n\nInstall Dibs somewhere this account owns (~/.local/bin is "+
			"what `task install` uses), or fetch the release by hand", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	return os.Remove(name)
}

// placePayload moves each unpacked file into place, replacing rather than
// overwriting.
//
// `rm` before `cp`, deliberately, and the reason is recorded in the Taskfile
// for the same operation: macOS caches a binary's code-signature validation
// against its INODE. Writing over an existing executable reuses the inode
// with new content, the kernel sees a CDHash that no longer matches, and
// every later run is SIGKILLed with exit 137 and no message, from a file that
// is byte-identical to a working one. A rename gives a fresh inode and no
// cached verdict.
func placePayload(from, into string) error {
	for _, name := range selfupdate.Payload {
		src := filepath.Join(from, name)
		if _, err := os.Stat(src); err != nil {
			if name == "dibs-presence" || name == "Dibs.app" {
				continue // macOS only; a Linux archive carries neither
			}
			return fmt.Errorf("the release archive did not contain %s", name)
		}
		dst := filepath.Join(into, name)
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("replacing %s: %w", dst, err)
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("installing %s: %w", dst, err)
		}
	}
	return nil
}

// reportUpdate is the `dibs doctor` line.
//
// A warning and never a problem: running a version behind is a choice an
// operator is entitled to make, and an install that works is not broken. It
// is skipped entirely when the operator has said so, because a diagnostic
// that reaches the network without being asked is a thing to be able to turn
// off, and because doctor is what somebody runs when the network may be the
// thing that is wrong. Any failure to ask is silence rather than a line: the
// question "is there a newer Dibs" has no answer worth interrupting a
// diagnosis for.
func reportUpdate(ok reportFn, warn fixFn) {
	if selfupdate.Skipped() {
		return
	}
	rel, standing, err := currentStanding()
	if err != nil {
		return
	}
	switch standing {
	case selfupdate.Behind:
		warn("a newer Dibs is published: "+rel.Tag+", and this is "+build.Version,
			fetchAdvice()+". "+rel.URL)
	case selfupdate.Current:
		ok("running the current release (" + build.Version + ")")
	case selfupdate.Ahead, selfupdate.Unknowable:
		// A development build is not a finding.
	}
}
