// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"sort"
	"strconv"
	"strings"
)

type recipientMatch struct {
	id    string
	edits int
}

// Suggestions are advice, never address resolution. A typo must not silently
// deliver to the closest name. Keep the roster bounded and name every omission:
// an alphabetic subset previously convinced senders that reachable agents left.
func nearestAgentsHint(s *State, want string) string {
	var near []recipientMatch
	var live []string
	w := strings.ToLower(want)
	for id, l := range s.Agents {
		if l.Status != StatusActive && !l.Sleeping() {
			continue
		}
		live = append(live, id)
		edits := min(recipientDistance(w, strings.ToLower(id)), recipientDistance(w, strings.ToLower(l.Name)))
		if edits <= min(3, max(1, len([]rune(w))/5)) {
			near = append(near, recipientMatch{id, edits})
		}
	}
	if len(near) > 0 {
		sort.Slice(near, func(i, j int) bool {
			if near[i].edits != near[j].edits {
				return near[i].edits < near[j].edits
			}
			return near[i].id < near[j].id
		})
		var names []string
		for _, match := range near[:min(8, len(near))] {
			names = append(names, match.id)
		}
		return "no agent " + want + ": did you mean " + strings.Join(names, ", ") + "?" + recipientOmissions(len(near))
	}
	if len(live) == 0 {
		return "no agent " + want + ", and no other agent is live either"
	}
	sort.Strings(live)
	return "no agent " + want + ": live agents include: " + strings.Join(live[:min(8, len(live))], ", ") +
		recipientOmissions(len(live)) + operatorFallback(s)
}

func recipientOmissions(total int) string {
	if total <= 8 {
		return ""
	}
	return "; " + strconv.Itoa(total-8) + " more, call board"
}

// Retain substring advice, recognize shortened hyphen-token names, and bounded
// spelling mistakes. A token must match in full: labs is not a match for lab.
// The single-row edit distance is deterministic and linear in memory. Names
// cannot turn this into an unbounded fuzzy scan: three edits is the ceiling.
func recipientDistance(want, name string) int {
	if name == "" || want == "" {
		return 4
	}
	if strings.Contains(want, name) || strings.Contains(name, want) {
		return 0
	}
	if recipientTokenSubset(want, name) {
		return 0
	}
	a, b := []rune(want), []rune(name)
	if len(a) > len(b)+3 || len(b) > len(a)+3 {
		return 4
	}
	row := make([]int, len(b)+1)
	for j := range row {
		row[j] = j
	}
	for i, x := range a {
		previous := row[0]
		row[0] = i + 1
		for j, y := range b {
			cost := 1
			if x == y {
				cost = 0
			}
			old := row[j+1]
			row[j+1] = min(row[j]+1, old+1, previous+cost)
			previous = old
		}
	}
	return row[len(b)]
}

func recipientTokenSubset(want, name string) bool {
	have := map[string]bool{}
	for _, token := range strings.Split(name, "-") {
		have[token] = true
	}
	for _, token := range strings.Split(want, "-") {
		if token == "" || !have[token] {
			return false
		}
	}
	return true
}
