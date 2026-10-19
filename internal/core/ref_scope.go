// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "strings"

const unconfirmedRepositoryHint = "same ref, repository unconfirmed: check before assuming overlap"

// A bare tracker number has three verdicts. Unknown must remain visible without
// acquiring the authority of a confirmed shared objective (187303, 188961).
func scopeSharedRefs(a, b *Agent, refs []string) (confirmed, unconfirmed []string) {
	same, different := sameProject(a, b), differentProjects(a, b)
	for _, ref := range refs {
		if bareRepositoryRef(ref) {
			switch {
			case same:
				confirmed = append(confirmed, ref)
			case !different:
				unconfirmed = append(unconfirmed, ref)
			}
		} else if globalBoardRef(ref) || !different {
			confirmed = append(confirmed, ref)
		}
	}
	return confirmed, unconfirmed
}

func bareRepositoryRef(ref string) bool {
	kind, id, _ := strings.Cut(normRef(ref), ":")
	if (kind != "pr" && kind != "issue") || id == "" {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func globalBoardRef(ref string) bool {
	kind, _, _ := strings.Cut(normRef(ref), ":")
	return kind == MsgRequest || kind == MsgQuestion || kind == coordKeyNS
}

func scopeSlotRefs(a, b *Agent, mine, theirs Slot) (Slot, Slot, []string) {
	if sameProject(a, b) {
		return mine, theirs, nil
	}
	shared := sharedStrings(mine.Refs, theirs.Refs)
	_, unknown := scopeSharedRefs(a, b, shared)
	filter := func(refs []string) []string {
		var out []string
		for _, ref := range refs {
			if !bareRepositoryRef(ref) {
				out = append(out, ref)
			}
		}
		return out
	}
	mine.Refs, theirs.Refs = filter(mine.Refs), filter(theirs.Refs)
	return mine, theirs, unknown
}
