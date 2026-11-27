// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Command k3gate runs the Go half of the gate natively on riscv64 Linux, on
// the shared K3 seat, and exits with that run's real status.
//
// Hosted CI only emulates riscv64, and only for three packages. The first
// native run of the whole suite (2026-11-24) found two Linux bugs that
// emulation and the amd64 runner both missed. One of them only appeared once
// the run had a root-owned sshd above it. So this is the gate for
// "first-class on riscv64 Linux", and it has to be cheap to run.
//
// Laptop side: cross-build this program for linux/riscv64, copy it to the
// seat, push the commit to a bare repo there, start the remote half
// detached so a dropped ssh session cannot kill it, then poll for
// result.json. A dropped poll is "not yet", never a pass: the exit status
// comes from the file the remote half wrote, and nothing here greps output.
//
// Remote side (-remote): one run at a time behind a lock, then vet, build,
// `go test -race` and govulncheck under nice and idle IO priority. The box is
// shared with the K7 lanes.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

func main() {
	var o options
	flag.StringVar(&o.sha, "sha", "HEAD", "commit to gate (any revision git can resolve)")
	flag.StringVar(&o.host, "host", "k3-dibs", "ssh host of the riscv64 seat")
	flag.DurationVar(&o.poll, "poll", defaultPoll, "interval between result polls")
	flag.DurationVar(&o.timeout, "timeout", defaultTimeout, "give up waiting for a result after this long")
	remote := flag.Bool("remote", false, "run the gate here (invoked on the seat by the laptop side)")
	flag.Parse()
	var (
		code int
		err  error
	)
	if *remote {
		code, err = runRemote(o.sha)
	} else {
		code, err = runLocal(o)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "k3gate:", err)
		if code == 0 {
			code = 2
		}
	}
	os.Exit(code)
}

var errNoResult = errors.New("no result yet")
