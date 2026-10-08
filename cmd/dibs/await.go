// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

// await blocks until events for the caller's agent arrive, prints them as
// JSON lines, and exits 0: the universal adapter between Dibs and agent
// harnesses' background-task wake mechanism: an agent runs `dibs await` in
// the background, keeps working, and is woken by its harness the moment the
// command exits with mail. The shell polls; the model sleeps.
//
// THREE OUTCOMES, THREE EXIT STATUSES: 0 events arrived, 1 the timeout passed
// with none, 75 (EX_TEMPFAIL) the daemon stayed unreachable. The last two both
// exited 1, so a harness waking on the watcher could not tell "the board is
// quiet" from "the board is down". And a daemon that only restarted is ridden
// through rather than reported: see callRiding.
func await(args []string) error {
	fs := flag.NewFlagSet("await", flag.ContinueOnError)
	since := fs.Uint64("since", 0, "resume cursor (0 = from now)")
	timeout := fs.Duration("timeout", 30*time.Minute, "give up after this long (exit 1)")
	tokenFlag := fs.String("token", "", "agent token (prefer DIBS_TOKEN env)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	token := *tokenFlag
	if token == "" {
		token = os.Getenv("DIBS_TOKEN")
	}
	if token == "" {
		return fmt.Errorf("no agent token: set DIBS_TOKEN (from register) or pass --token")
	}
	secret, err := localSecret()
	if err != nil {
		return fmt.Errorf("no local secret yet: start dibd once first: %w", err)
	}

	call := func(tool string, callArgs map[string]any) (map[string]any, error) {
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": tool, "arguments": callArgs},
		})
		req, err := http.NewRequest(http.MethodPost, mcpEndpoint(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Dibs-Local", secret)
		client := daemonClient(75 * time.Second)
		resp, err := client.Do(req)
		if err != nil {
			return nil, transportError{err}
		}
		defer func() { _ = resp.Body.Close() }()
		var rpc struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
			return nil, transportError{err} // the body was cut off: same as a dropped call
		}
		var payload map[string]any
		if len(rpc.Result.Content) > 0 {
			_ = json.Unmarshal([]byte(rpc.Result.Content[0].Text), &payload)
		}
		if rpc.Result.IsError {
			return nil, fmt.Errorf("%v: %v (hint: %v)", payload["code"], payload["message"], payload["hint"])
		}
		return payload, nil
	}

	cursor := *since
	deadline := time.Now().Add(*timeout)
	riding := func(tool string, callArgs map[string]any) (map[string]any, error) {
		return callRiding(call, tool, callArgs, deadline, func() uint64 { return cursor })
	}
	if cursor == 0 {
		res, err := riding("events_since", map[string]any{"token": token, "since_serial": ^uint64(0) >> 1})
		if err != nil {
			return err
		}
		if s, ok := res["serial"].(float64); ok {
			cursor = uint64(s)
		}
	}

	for time.Now().Before(deadline) {
		res, err := riding("await_events", map[string]any{
			"token": token, "since_serial": cursor, "timeout_s": 60,
		})
		if err != nil {
			return err
		}
		evs, _ := res["events"].([]any)
		if len(evs) > 0 {
			for _, ev := range evs {
				line, _ := json.Marshal(ev)
				fmt.Println(string(line))
			}
			return nil // wake the harness
		}
		if s, ok := res["serial"].(float64); ok && uint64(s) > cursor {
			cursor = uint64(s)
		}
	}
	return fmt.Errorf("no events within %s", *timeout)
}

// exitTempFail is EX_TEMPFAIL from sysexits: a temporary failure, try again
// later. What `dibs await` exits with when the daemon stayed unreachable, so it
// differs from a timeout (1) and from mail (0).
const exitTempFail = 75

// awaitReconnectFor is how long `dibs await` keeps trying to reach a daemon
// that went away before it reports the board as down. A restart takes seconds;
// two minutes covers a slow upgrade without hiding a daemon that is really
// gone. A variable so a test can shorten it.
var awaitReconnectFor = 2 * time.Minute

// transportError marks a call that never got an answer: the connection was
// refused, dropped, reset, or cut off mid-body. Distinct from the daemon
// answering with an error, which is final.
type transportError struct{ err error }

func (e transportError) Error() string { return e.err.Error() }
func (e transportError) Unwrap() error { return e.err }

// exitStatusError is an error that asks for a particular exit status.
type exitStatusError struct {
	status int
	err    error
}

func (e exitStatusError) Error() string   { return e.err.Error() }
func (e exitStatusError) Unwrap() error   { return e.err }
func (e exitStatusError) exitStatus() int { return e.status }

// callRiding makes one call, riding through a daemon that goes away and comes
// back.
//
// A RESTART IS ROUTINE, so the watcher does not end on one. k7-dev lost a
// background `dibs await -since 469 -timeout 8h` to an EOF when the daemon
// restarted, and restarts are ordinary: upgrades, configuration reloads,
// several on the day it was reported. -since makes resuming EXACT, because the
// cursor names precisely what the caller has seen, so a watcher that gives up
// on the first dropped connection is one an agent has to babysit.
//
// ANY transport error is retried, a reset included, which the bridge
// deliberately does not do for writes (dialFailed in mcpstdio.go): a reset
// cannot prove a write was not applied, but await_events and events_since are
// reads, so asking twice costs nothing. Same ambiguity, opposite resolution,
// because the stake differs.
//
// An answer from the daemon, even an error, is final and returned at once:
// only silence is retried.
func callRiding(
	call func(string, map[string]any) (map[string]any, error),
	tool string, callArgs map[string]any, deadline time.Time, cursor func() uint64,
) (map[string]any, error) {
	var lostAt time.Time
	pause := 250 * time.Millisecond
	for {
		res, err := call(tool, callArgs)
		var te transportError
		if err == nil || !errors.As(err, &te) {
			return res, err
		}
		now := time.Now()
		if lostAt.IsZero() {
			lostAt = now
		}
		// DOWN FOR THE WHOLE WINDOW is the board being down: 75. The deadline
		// arriving mid-retry is not that, it is the timeout the caller asked
		// for, and reporting it as an unreachable board would be the same
		// confusion this exit status exists to remove, pointed the other way.
		// The first cut made exactly that mistake and its own timeout test
		// caught it.
		if now.Sub(lostAt) >= awaitReconnectFor {
			return nil, exitStatusError{exitTempFail, fmt.Errorf(
				"the Dibs daemon has been unreachable for %s: %v. Nothing after serial %d "+
					"was lost: start it, then resume with -since %d",
				now.Sub(lostAt).Round(time.Second), te.err, cursor(), cursor())}
		}
		if !now.Before(deadline) {
			return nil, fmt.Errorf("no events before the timeout (the daemon was not "+
				"answering at the end: %v); resume with -since %d", te.err, cursor())
		}
		// Ask again where the daemon is: a restart can bring it back elsewhere.
		reresolveMCPEndpoint()
		wait := pause
		if left := time.Until(deadline); left < wait {
			wait = left
		}
		time.Sleep(wait)
		if pause < 5*time.Second {
			pause *= 2
		}
	}
}
