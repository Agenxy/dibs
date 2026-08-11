package core

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// A coordination key is Dibs' record that two agents DECIDED to work together,
// as opposed to Dibs having guessed that they might be.
//
// Everything else the classifier weighs is inference. Shared paths, similar
// prose, co-changing files: all of it says two declarations resemble each other,
// and resemblance is exactly what produces the failure this system is judged on
// two agents told they are duplicating work when they are not. Only two things
// escape that. One is a canonical id both agents copied from the world (pr:1231);
// the other is this.
//
// The key is issued when an agent opens and held by its members. Holding it is not
// a claim an agent can make: it is membership, which Dibs granted and recorded,
// and the three ways to get it: opening an agent, being admitted to one, or being
// vouched for by a parent that holds it: are all decisions somebody made on
// purpose. That is why a shared key justifies an exact match when a 0.9 semantic
// score does not.
//
// It is opaque BECAUSE it must not be guessable. A readable agent id can be
// invented by an agent that merely believes it belongs somewhere, and the whole
// point of the key is to be evidence that Dibs issued rather than a string an
// agent wrote. Reviewers made this the condition for taking the mechanism
// seriously: do not treat any self-authored `agent:*` string as issued.
//
// It is DERIVED, not random, because it is created inside Apply. A random key
// would differ on every replay of the same ledger and void the hash chain: the
// same rule that keeps scoring out of the fold (SPEC-CHANNELS.md §4.3).
const coordKeyNS = "key"

// coordKey derives an agent's key from the board and the serial that created it.
//
// Serials are unique per board and the node id makes it unique between boards,
// which is what "globally unambiguous on that board" needs. Hashed rather than
// concatenated so it carries no readable structure to pattern-match against, and
// truncated to 20 hex characters: enough that guessing is hopeless, short enough
// that an agent can copy it into a ref without the line becoming unreadable.
func coordKey(nodeID string, serial uint64) string {
	sum := sha256.Sum256([]byte(nodeID + "\x00" + strconv.FormatUint(serial, 10)))
	return coordKeyNS + ":" + hex.EncodeToString(sum[:])[:20]
}

// isCoordKey reports the shape only. Shape proves nothing: validation is
// holdsCoordKey, and every path that treats a key as identity must go through it.
func isCoordKey(ref string) bool {
	ns, rest, ok := strings.Cut(ref, ":")
	return ok && strings.EqualFold(ns, coordKeyNS) && rest != ""
}

// holdsCoordKey reports whether this agent is entitled to the key it is
// claiming: it is in the agent the key names, or it descends from somebody who
// is, through a lineage the parent actually vouched for.
//
// This is the entire security of the mechanism, and it is deliberately not a
// lookup of "has this key ever existed". An agent that learns another agent's
// key from a message, from a log, or from a panel must not be able to declare
// it and be treated as coordinating. Issued AND held, or it is just a string.
//
// Inherited holding is not a loosening; it is the case that makes the key worth
// having. Membership alone would confine the key to agents already in one agent,
// and matching deliberately never proposes an agent you are in, so a key that
// only members could hold would fire exactly where it changed nothing. Live
// probing is what showed this: two agents sharing a key matched nothing, because
// each was already where the key would have sent it.
//
// The lineage a parent vouched for is the one space that genuinely carries
// shared intent. A parent that opens an agent and fans out subagents has made one
// coordination decision covering all of them, and each child can then declare
// its own work and be matched to the parent's agent exactly: while holding no
// membership of its own, which is what keeps a helper from being counted as a
// second occupant of its parent's work. speaksFor is the same rule the rest of
// the agent machinery already applies, and it walks only PROVEN links: an
// unvouched parent is a claim anybody could make, and it inherits nothing.
func (s *State) holdsCoordKey(agent, key string) bool {
	if agent == "" || !isCoordKey(key) {
		return false
	}
	for _, ch := range s.Spaces {
		if ch.Key == key {
			return s.speaksFor(ch, agent) != ""
		}
	}
	return false
}

// validatedRefs drops the identity claims an agent cannot back up.
//
// Ordinary refs pass through untouched: `pr:1231` is a claim about the world
// that Dibs cannot verify and does not pretend to. A coordination key is
// different in kind. Dibs issued it, so Dibs can check it, and a key that
// does not survive this is removed rather than downgraded. Leaving it in as a
// label would let an invented key still contribute shared-vocabulary evidence,
// which is a smaller version of the same laundering.
func (s *State) validatedRefs(agent string, refs []string) []string {
	var out []string
	for _, r := range refs {
		if isCoordKey(r) && !s.holdsCoordKey(agent, r) {
			continue
		}
		out = append(out, r)
	}
	return out
}
