// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Command releasedrift fails when main has held unreleased commits for longer
// than the allowed window.
//
// v0.0.14 was stamped on 2026-10-06 and then held: approval was required,
// nobody asked for it, and nothing anywhere said the hold had become twelve
// days and 49 commits while the production board ran top of tree. The
// operator found the gap themselves and asked what we were waiting for. A
// hold with no clock attached is indefinite by construction, so this is the
// clock.
//
// It measures the published TAG, never the changelog heading: the heading
// for 0.0.14 carried the 6 October date the whole time it was unreleased,
// which is exactly the lie this exists to catch. It runs on a schedule, not in
// the gate, because time passing is not a defect in any one pull request.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func main() {
	window := flag.Duration("window", 7*24*time.Hour, "longest an unreleased commit may wait on main")
	ref := flag.String("ref", "origin/main", "the branch whose head is the release candidate")
	flag.Parse()
	d, err := measure(*ref)
	if err != nil {
		fmt.Fprintln(os.Stderr, "releasedrift:", err)
		os.Exit(2)
	}
	msg, late := verdict(d, *window, time.Now())
	fmt.Println(msg)
	if late {
		os.Exit(1)
	}
}

// drift is what main holds that no release has published.
type drift struct {
	tag     string
	tagged  time.Time
	pending int
	oldest  time.Time // commit time of the oldest unreleased commit
}

// verdict decides; measure only observes. The age that matters is the OLDEST
// unreleased change's, since that is how long a fix has been withheld.
func verdict(d drift, window time.Duration, now time.Time) (string, bool) {
	if d.pending == 0 {
		return fmt.Sprintf("in step: %s is main's head", d.tag), false
	}
	age := now.Sub(d.oldest)
	msg := fmt.Sprintf("%d commit(s) on main since %s (tagged %s); the oldest has waited %s",
		d.pending, d.tag, d.tagged.Format("2006-01-02"), days(age))
	if age <= window {
		return msg + ", inside the " + days(window) + " window", false
	}
	return msg + ", over the " + days(window) + " window. Ship a release, or ask the owner " +
		"for approval today: an unasked hold is how v0.0.14 waited twelve days", true
}

func days(d time.Duration) string {
	n := int(d.Hours() / 24)
	if n == 1 {
		return "1 day"
	}
	return strconv.Itoa(n) + " days"
}

func measure(ref string) (drift, error) {
	tag, err := git("describe", "--tags", "--abbrev=0", "--match", "v[0-9]*", ref)
	if err != nil {
		return drift{}, fmt.Errorf("no release tag reachable from %s (fetch tags with fetch-depth 0): %w", ref, err)
	}
	d := drift{tag: tag}
	if d.tagged, err = commitTime(tag); err != nil {
		return drift{}, err
	}
	log, err := git("log", "--reverse", "--format=%ct", tag+".."+ref)
	if err != nil {
		return drift{}, err
	}
	stamps := strings.Fields(log)
	d.pending = len(stamps)
	if d.pending == 0 {
		return d, nil
	}
	d.oldest, err = unix(stamps[0])
	return d, err
}

func commitTime(rev string) (time.Time, error) {
	out, err := git("log", "-1", "--format=%ct", rev)
	if err != nil {
		return time.Time{}, err
	}
	return unix(out)
}

func unix(s string) (time.Time, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("unreadable commit time %q: %w", s, err)
	}
	return time.Unix(n, 0), nil
}

func git(args ...string) (string, error) {
	// #nosec G204 -- no shell; every argument is a constant or a ref this command names.
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exit.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
